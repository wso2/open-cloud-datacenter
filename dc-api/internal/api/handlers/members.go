// Package handlers — members.go
//
// MemberHandler implements the /v1/tenants/{tenant_id}/members endpoints.
//
// These are the first endpoints under the /v1/tenants prefix. They allow owners
// to invite, list, and remove human users from a tenant scope. Service accounts
// have a separate endpoint (Chunk 7) and are explicitly excluded from LIST here.
//
// Auth matrix:
//
//	POST   /v1/tenants/{tenant_id}/members                → RoleOwner required
//	GET    /v1/tenants/{tenant_id}/members                → any authenticated tenant member
//	DELETE /v1/tenants/{tenant_id}/members/{principal_id} → RoleOwner required
//
// Cross-tenant requests (tenant_id in URL ≠ tenantID from JWT) return 404 so
// tenant identifiers from other tenants are not enumerable.
//
// Audit events are appended synchronously on every successful mutation; the
// resource_id is the role_assignments row UUID on invite, or a zero UUID on
// remove (because removal deletes all rows for the principal atomically).
// actor_id is always the caller's principal_id from context.
//
// TODO: Add PATCH /v1/tenants/{tenant_id}/members/{principal_id} for in-place
// role changes (currently requires remove + re-add).
package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"
	"github.com/wso2/dc-api/internal/api/middleware"
	"github.com/wso2/dc-api/internal/db"
	"github.com/wso2/dc-api/internal/models"
	"github.com/wso2/dc-api/internal/rbac"
)

// MemberHandler handles all /v1/tenants/{tenant_id}/members endpoints.
type MemberHandler struct {
	repo *db.Repository
	log  zerolog.Logger
}

// NewMemberHandler creates a MemberHandler with injected dependencies.
func NewMemberHandler(repo *db.Repository, log zerolog.Logger) *MemberHandler {
	return &MemberHandler{repo: repo, log: log}
}

// ── DTOs ──────────────────────────────────────────────────────────────────────

type inviteMemberRequest struct {
	UserSub      string `json:"user_sub"`
	Role         string `json:"role"`
	DisplayAlias string `json:"display_alias,omitempty"`
}

func (req *inviteMemberRequest) validate() error {
	if req.UserSub == "" {
		return fmt.Errorf("user_sub is required")
	}
	switch models.Role(req.Role) {
	case models.RoleOwner, models.RoleMember, models.RoleViewer:
		// valid
	default:
		return fmt.Errorf("role must be one of: owner, member, viewer")
	}
	return nil
}

// memberResponse is the JSON shape returned by the members endpoints.
// It mirrors all columns of role_assignments that are safe to expose.
type memberResponse struct {
	ID            string `json:"id"`
	PrincipalType string `json:"principal_type"`
	PrincipalID   string `json:"principal_id"`
	ScopeType     string `json:"scope_type"`
	ScopeID       string `json:"scope_id"`
	Role          string `json:"role"`
	GrantedAt     string `json:"granted_at"`
	GrantedBy     string `json:"granted_by"`
	DisplayAlias  string `json:"display_alias,omitempty"`
}

func roleAssignmentToMemberResponse(ra *models.RoleAssignment) memberResponse {
	return memberResponse{
		ID:            ra.ID.String(),
		PrincipalType: string(ra.PrincipalType),
		PrincipalID:   ra.PrincipalID,
		ScopeType:     string(ra.ScopeType),
		ScopeID:       ra.ScopeID,
		Role:          string(ra.Role),
		GrantedAt:     ra.GrantedAt.Format(time.RFC3339),
		GrantedBy:     ra.GrantedBy,
		DisplayAlias:  ra.DisplayAlias,
	}
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// Invite handles POST /v1/tenants/{tenant_id}/members.
// Only owners can invite new members. Returns 201 on success, 409 on duplicate.
func (h *MemberHandler) Invite(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}

	// Cross-tenant guard: URL tenant_id must match caller's tenant context.
	// Admins (is_admin=true) are exempt — they operate across all tenants.
	urlTenantID := chi.URLParam(r, "tenant_id")
	if !middleware.IsAdminFromContext(r.Context()) && urlTenantID != tenantID {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	if !requireAction(w, r, h.repo, rbac.ActionRoleAssignmentWrite) {
		return
	}

	_, callerID, ok := middleware.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no principal in context")
		return
	}

	var req inviteMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := req.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ra, err := h.repo.CreateRoleAssignment(r.Context(), models.RoleAssignment{
		PrincipalType: models.PrincipalTypeUser,
		PrincipalID:   req.UserSub,
		ScopeType:     models.ScopeTypeTenant,
		ScopeID:       tenantID,
		Role:          models.Role(req.Role),
		GrantedBy:     callerID,
		DisplayAlias:  req.DisplayAlias,
	})
	if err != nil {
		if strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "duplicate key") {
			writeError(w, http.StatusConflict,
				"user already has that role in this tenant; remove and re-add to change role")
			return
		}
		h.log.Error().Err(err).Str("tenant", tenantID).Str("user_sub", req.UserSub).
			Msg("create role assignment failed")
		writeError(w, http.StatusInternalServerError, "failed to invite member")
		return
	}

	// UPSERT the tenants registry so the tenant is enumerable via
	// GET /v1/tenants (admin path) immediately. No-op when the row
	// already exists. Fail-open: registry visibility is a quality-of-
	// life feature, not a correctness gate for membership.
	if _, err := h.repo.UpsertTenant(r.Context(), tenantID, tenantID, "", "member-invite:"+callerID); err != nil {
		h.log.Warn().Err(err).Str("tenant", tenantID).
			Msg("tenants-registry UPSERT after member invite failed")
	}

	_ = h.repo.AppendAuditEvent(r.Context(), &models.AuditEvent{
		ResourceID: ra.ID,
		ActorID:    callerID,
		Action:     "MEMBER_INVITE",
		Message:    "invited " + req.UserSub + " as " + req.Role + " in tenant " + tenantID,
	})

	writeJSON(w, http.StatusCreated, roleAssignmentToMemberResponse(ra))
}

// List handles GET /v1/tenants/{tenant_id}/members.
// Any authenticated member of the tenant can list members (read is open).
// Service accounts are explicitly excluded; they are covered by the Chunk 7 endpoint.
func (h *MemberHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}

	// Cross-tenant guard.
	urlTenantID := chi.URLParam(r, "tenant_id")
	if !middleware.IsAdminFromContext(r.Context()) && urlTenantID != tenantID {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	all, err := h.repo.ListRoleAssignmentsForScope(r.Context(), models.ScopeTypeTenant, tenantID)
	if err != nil {
		h.log.Error().Err(err).Str("tenant", tenantID).Msg("list role assignments failed")
		writeError(w, http.StatusInternalServerError, "failed to list members")
		return
	}

	resp := make([]memberResponse, 0, len(all))
	for i := range all {
		if all[i].PrincipalType != models.PrincipalTypeUser {
			continue // exclude service accounts (Chunk 7 has /service-accounts)
		}
		resp = append(resp, roleAssignmentToMemberResponse(&all[i]))
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"members": resp})
}

// Remove handles DELETE /v1/tenants/{tenant_id}/members/{principal_id}.
// Only owners can remove members. Returns 409 if caller is the last owner.
func (h *MemberHandler) Remove(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}

	// Cross-tenant guard.
	urlTenantID := chi.URLParam(r, "tenant_id")
	if !middleware.IsAdminFromContext(r.Context()) && urlTenantID != tenantID {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	if !requireAction(w, r, h.repo, rbac.ActionRoleAssignmentDelete) {
		return
	}

	_, callerID, ok := middleware.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no principal in context")
		return
	}

	targetPrincipalID := chi.URLParam(r, "principal_id")
	if targetPrincipalID == "" {
		writeError(w, http.StatusBadRequest, "principal_id is required")
		return
	}

	// Last-owner check: count owners BEFORE deciding to delete, so we can return
	// a clear 409 if the target is the last owner.
	ownerCount, err := h.repo.CountOwnersForTenant(r.Context(), tenantID)
	if err != nil {
		h.log.Error().Err(err).Str("tenant", tenantID).Msg("count owners failed")
		writeError(w, http.StatusInternalServerError, "failed to validate member removal")
		return
	}

	// Determine whether the target holds the owner role in this tenant.
	allForPrincipal, err := h.repo.ListRoleAssignmentsForPrincipal(
		r.Context(), models.PrincipalTypeUser, targetPrincipalID,
	)
	if err != nil {
		h.log.Error().Err(err).Str("principal", targetPrincipalID).
			Msg("list principal role assignments failed")
		writeError(w, http.StatusInternalServerError, "failed to validate member removal")
		return
	}

	targetIsOwner := false
	for _, ra := range allForPrincipal {
		if ra.ScopeType == models.ScopeTypeTenant && ra.ScopeID == tenantID &&
			ra.Role == models.RoleOwner {
			targetIsOwner = true
			break
		}
	}

	// Last-owner guard. Platform admins bypass it for the same reason they
	// bypass the cross-tenant guard above: they can always re-promote any
	// member afterward, so "tenant has no owner" is not the dead-end it
	// would be for a regular caller (who needs an existing owner to invite
	// anyone new). The guard remains for tenant owners protecting themselves
	// from accidentally orphaning their own tenant.
	if targetIsOwner && ownerCount <= 1 && !middleware.IsAdminFromContext(r.Context()) {
		writeError(w, http.StatusConflict,
			"cannot remove the last owner of a tenant; promote another member to owner first")
		return
	}

	if err := h.repo.DeleteRoleAssignmentsForPrincipalAtScope(
		r.Context(),
		models.PrincipalTypeUser,
		targetPrincipalID,
		models.ScopeTypeTenant,
		tenantID,
	); err != nil {
		h.log.Error().Err(err).Str("tenant", tenantID).Str("principal", targetPrincipalID).
			Msg("delete role assignments failed")
		writeError(w, http.StatusInternalServerError, "failed to remove member")
		return
	}

	_ = h.repo.AppendAuditEvent(r.Context(), &models.AuditEvent{
		// ResourceID intentionally zero — removal affects multiple rows atomically;
		// the message field carries the identity detail for audit purposes.
		ActorID: callerID,
		Action:  "MEMBER_REMOVE",
		Message: "removed " + targetPrincipalID + " from tenant " + tenantID,
	})

	w.WriteHeader(http.StatusNoContent)
}
