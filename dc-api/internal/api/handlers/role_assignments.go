// Package handlers — role_assignments.go
//
// RoleAssignmentsHandler implements the /role-assignments endpoints at every
// scope:
//
//	POST   /v1/tenants/{tenant_id}/role-assignments                → grant a role at tenant scope
//	GET    /v1/tenants/{tenant_id}/role-assignments                → list tenant grants
//	DELETE /v1/tenants/{tenant_id}/role-assignments/{principal_id} → revoke a principal's tenant grants
//	POST   /v1/tenants/{tenant_id}/projects/{project_id}/role-assignments                → grant at project scope
//	GET    /v1/tenants/{tenant_id}/projects/{project_id}/role-assignments                → list project grants
//	DELETE /v1/tenants/{tenant_id}/projects/{project_id}/role-assignments/{principal_id} → revoke at project scope
//
// The handler is scope-agnostic: it reads the active scope from request context
// (ProjectContext wins over TenantContext) and keys list/delete on the immutable
// scope_uuid, so a project grant never matches a same-named project in another
// tenant. Resource scope (…/{resource}/{id}/role-assignments) is added in M5b.
//
// Auth: write/delete require authorization/roleAssignments/{write,delete} and
// read requires authorization/roleAssignments/read — applied by the route Gate.
// requireAction is re-checked here for the mutations as defence in depth.
//
// Cross-tenant requests (tenant_id in URL ≠ tenantID from JWT) return 404 so
// tenant identifiers from other tenants are not enumerable. Service accounts are
// excluded from LIST (they have their own /service-accounts endpoint).
package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/wso2/dc-api/internal/api/middleware"
	"github.com/wso2/dc-api/internal/db"
	"github.com/wso2/dc-api/internal/models"
	"github.com/wso2/dc-api/internal/rbac"
)

// RoleAssignmentsHandler handles the /role-assignments endpoints at tenant and
// project scope.
type RoleAssignmentsHandler struct {
	repo *db.Repository
	log  zerolog.Logger
}

// NewRoleAssignmentsHandler creates a RoleAssignmentsHandler with injected deps.
func NewRoleAssignmentsHandler(repo *db.Repository, log zerolog.Logger) *RoleAssignmentsHandler {
	return &RoleAssignmentsHandler{repo: repo, log: log}
}

// ── DTOs ──────────────────────────────────────────────────────────────────────

type createRoleAssignmentRequest struct {
	UserSub string `json:"user_sub"`
	// RoleDefinition is the v2 role-definition key to grant — any catalog key
	// (e.g. "Owner", "Contributor", "Reader", "VirtualMachineContributor"). This is
	// the only role vocabulary the API accepts; the v1 owner/member/viewer ranks
	// are gone.
	RoleDefinition string `json:"role_definition"`
	DisplayAlias   string `json:"display_alias,omitempty"`
}

func (req *createRoleAssignmentRequest) validate() error {
	if req.UserSub == "" {
		return fmt.Errorf("user_sub is required")
	}
	if req.RoleDefinition == "" {
		return fmt.Errorf("role_definition is required")
	}
	if !rbac.IsBuiltinKey(req.RoleDefinition) {
		return fmt.Errorf("role_definition %q is not a known built-in role", req.RoleDefinition)
	}
	return nil
}

// roleAssignmentResponse is the JSON shape returned by the endpoints. It mirrors
// the columns of role_assignments that are safe to expose.
type roleAssignmentResponse struct {
	ID            string `json:"id"`
	PrincipalType string `json:"principal_type"`
	PrincipalID   string `json:"principal_id"`
	ScopeType     string `json:"scope_type"`
	ScopeID       string `json:"scope_id"`
	// RoleDefinition is the v2 role key (e.g. "VirtualMachineContributor"); the
	// catalog (GET /v1/role-definitions) resolves it to a display name for the UI.
	RoleDefinition string `json:"role_definition"`
	GrantedAt      string `json:"granted_at"`
	GrantedBy      string `json:"granted_by"`
	DisplayAlias   string `json:"display_alias,omitempty"`
}

func toRoleAssignmentResponse(ra *models.RoleAssignment) roleAssignmentResponse {
	return roleAssignmentResponse{
		ID:             ra.ID.String(),
		PrincipalType:  string(ra.PrincipalType),
		PrincipalID:    ra.PrincipalID,
		ScopeType:      string(ra.ScopeType),
		ScopeID:        ra.ScopeID,
		RoleDefinition: ra.RoleDefinition,
		GrantedAt:      ra.GrantedAt.Format(time.RFC3339),
		GrantedBy:      ra.GrantedBy,
		DisplayAlias:   ra.DisplayAlias,
	}
}

// ── Scope resolution ────────────────────────────────────────────────────────

// scopeRef is the active scope for a request: its type, human slug (stored in
// scope_id), and immutable UUID (the list/delete filter and engine key).
type scopeRef struct {
	Type models.ScopeType
	ID   string
	UUID uuid.UUID
}

// activeScope resolves the scope from the request: project wins over tenant,
// because ProjectContext (which injects project_uuid) only runs on project
// routes. The slug comes from the URL param; the UUID from the context the
// middleware injected.
func (h *RoleAssignmentsHandler) activeScope(r *http.Request) (scopeRef, bool) {
	ctx := r.Context()
	if ruuid, ok := middleware.ResourceUUIDFromContext(ctx); ok {
		// Resources have no slug — the UUID is the identity, so scope_id is it too.
		return scopeRef{models.ScopeTypeResource, ruuid.String(), ruuid}, true
	}
	if puuid, ok := middleware.ProjectUUIDFromContext(ctx); ok {
		return scopeRef{models.ScopeTypeProject, chi.URLParam(r, "project_id"), puuid}, true
	}
	if tuuid, ok := middleware.TenantUUIDFromContext(ctx); ok {
		return scopeRef{models.ScopeTypeTenant, chi.URLParam(r, "tenant_id"), tuuid}, true
	}
	return scopeRef{}, false
}

// tenantGuard enforces that the URL tenant_id matches the caller's tenant
// context (admins are exempt — they operate across tenants). Returns false and
// writes a 404 on mismatch so other tenants' identifiers are not enumerable.
func (h *RoleAssignmentsHandler) tenantGuard(w http.ResponseWriter, r *http.Request) (string, bool) {
	tenantID, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return "", false
	}
	if !middleware.IsAdminFromContext(r.Context()) && chi.URLParam(r, "tenant_id") != tenantID {
		writeError(w, http.StatusNotFound, "tenant not found")
		return "", false
	}
	return tenantID, true
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// Create grants a role at the active scope. Requires roleAssignments/write
// (Owner, at this scope or inherited from above). 201 on success, 409 on dup.
func (h *RoleAssignmentsHandler) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.tenantGuard(w, r)
	if !ok {
		return
	}
	scope, ok := h.activeScope(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "no scope in context")
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

	var req createRoleAssignmentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := req.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ra, err := h.repo.CreateRoleAssignment(r.Context(), models.RoleAssignment{
		PrincipalType:  models.PrincipalTypeUser,
		PrincipalID:    req.UserSub,
		ScopeType:      scope.Type,
		ScopeID:        scope.ID,
		ScopeUUID:      scope.UUID,
		RoleDefinition: req.RoleDefinition,
		GrantedBy:      callerID,
		DisplayAlias:   req.DisplayAlias,
	})
	if err != nil {
		if strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "duplicate key") {
			writeError(w, http.StatusConflict,
				"principal already has that role at this scope; remove and re-add to change role")
			return
		}
		h.log.Error().Err(err).Str("scope", string(scope.Type)).Str("scope_id", scope.ID).
			Str("user_sub", req.UserSub).Msg("create role assignment failed")
		writeError(w, http.StatusInternalServerError, "failed to create role assignment")
		return
	}

	// UPSERT the tenants registry so the tenant is enumerable via GET /v1/tenants
	// immediately. Tenant scope only — projects already exist. Fail-open: registry
	// visibility is a quality-of-life feature, not a correctness gate.
	if scope.Type == models.ScopeTypeTenant {
		if _, err := h.repo.UpsertTenant(r.Context(), tenantID, tenantID, "", "role-grant:"+callerID); err != nil {
			h.log.Warn().Err(err).Str("tenant", tenantID).
				Msg("tenants-registry UPSERT after role grant failed")
		}
	}

	_ = h.repo.AppendAuditEvent(r.Context(), &models.AuditEvent{
		ResourceID: ra.ID,
		ActorID:    callerID,
		Action:     "ROLE_ASSIGNMENT_CREATE",
		Message: fmt.Sprintf("granted %s to %s at %s %s",
			req.RoleDefinition, req.UserSub, scope.Type, scope.ID),
	})

	writeJSON(w, http.StatusCreated, toRoleAssignmentResponse(ra))
}

// List returns the user role assignments at the active scope. Read is gated by
// the route (roleAssignments/read). Service accounts are excluded.
func (h *RoleAssignmentsHandler) List(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.tenantGuard(w, r); !ok {
		return
	}
	scope, ok := h.activeScope(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "no scope in context")
		return
	}

	all, err := h.repo.ListRoleAssignmentsForScopeUUID(r.Context(), scope.UUID)
	if err != nil {
		h.log.Error().Err(err).Str("scope", string(scope.Type)).Str("scope_id", scope.ID).
			Msg("list role assignments failed")
		writeError(w, http.StatusInternalServerError, "failed to list role assignments")
		return
	}

	resp := make([]roleAssignmentResponse, 0, len(all))
	for i := range all {
		if all[i].PrincipalType != models.PrincipalTypeUser {
			continue // exclude service accounts (they have /service-accounts)
		}
		resp = append(resp, toRoleAssignmentResponse(&all[i]))
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"role_assignments": resp})
}

// Remove revokes ALL of a principal's grants at the active scope. Requires
// roleAssignments/delete. At tenant scope it refuses to remove the last owner
// (which would orphan the tenant); project scope has no such guard because a
// tenant owner always retains management of the project by inheritance.
func (h *RoleAssignmentsHandler) Remove(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.tenantGuard(w, r)
	if !ok {
		return
	}
	scope, ok := h.activeScope(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "no scope in context")
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

	// Last-owner guard — tenant scope only. Removing the last tenant owner would
	// orphan the tenant (a regular caller needs an existing owner to invite
	// anyone). Platform admins bypass it; they can always re-promote afterward.
	if scope.Type == models.ScopeTypeTenant {
		if blocked, err := h.wouldOrphanTenant(r, tenantID, targetPrincipalID); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to validate removal")
			return
		} else if blocked {
			writeError(w, http.StatusConflict,
				"cannot remove the last owner of a tenant; promote another member to owner first")
			return
		}
	}

	if err := h.repo.DeleteRoleAssignmentsForPrincipalAtScopeUUID(
		r.Context(), models.PrincipalTypeUser, targetPrincipalID, scope.UUID,
	); err != nil {
		h.log.Error().Err(err).Str("scope_id", scope.ID).Str("principal", targetPrincipalID).
			Msg("delete role assignments failed")
		writeError(w, http.StatusInternalServerError, "failed to remove role assignment")
		return
	}

	_ = h.repo.AppendAuditEvent(r.Context(), &models.AuditEvent{
		// ResourceID intentionally zero — removal affects multiple rows atomically.
		ActorID: callerID,
		Action:  "ROLE_ASSIGNMENT_DELETE",
		Message: fmt.Sprintf("revoked %s at %s %s", targetPrincipalID, scope.Type, scope.ID),
	})

	w.WriteHeader(http.StatusNoContent)
}

// wouldOrphanTenant reports whether removing targetPrincipalID would leave the
// tenant with no owner (and the caller is not a platform admin).
func (h *RoleAssignmentsHandler) wouldOrphanTenant(r *http.Request, tenantID, targetPrincipalID string) (bool, error) {
	if middleware.IsAdminFromContext(r.Context()) {
		return false, nil
	}
	ownerCount, err := h.repo.CountOwnersForTenant(r.Context(), tenantID)
	if err != nil {
		h.log.Error().Err(err).Str("tenant", tenantID).Msg("count owners failed")
		return false, err
	}
	if ownerCount > 1 {
		return false, nil
	}
	// One owner remains — block only if the target is that owner.
	allForPrincipal, err := h.repo.ListRoleAssignmentsForPrincipal(
		r.Context(), models.PrincipalTypeUser, targetPrincipalID,
	)
	if err != nil {
		h.log.Error().Err(err).Str("principal", targetPrincipalID).
			Msg("list principal role assignments failed")
		return false, err
	}
	for _, ra := range allForPrincipal {
		if ra.ScopeType == models.ScopeTypeTenant && ra.ScopeID == tenantID && ra.Role == models.RoleOwner {
			return true, nil
		}
	}
	return false, nil
}
