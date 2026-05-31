// Package handlers — service_accounts.go
//
// ServiceAccountHandler implements the
// /v1/tenants/{tenant_id}/service-accounts endpoints introduced in M1.5 Chunk 7.
//
// Auth matrix:
//
//	POST   /v1/tenants/{tenant_id}/service-accounts             → RoleOwner required
//	GET    /v1/tenants/{tenant_id}/service-accounts             → any authenticated tenant member
//	GET    /v1/tenants/{tenant_id}/service-accounts/{sa_id}     → any authenticated tenant member
//	DELETE /v1/tenants/{tenant_id}/service-accounts/{sa_id}     → RoleOwner required
//
// Token-shown-once policy:
//
//	On POST (create) the raw token is returned in the response field "token".
//	token_hash is NEVER selected from the DB after creation; the raw token is
//	NEVER echoed on subsequent GET/LIST responses. The only way a caller can
//	hold the raw token is to save it at creation time. This is enforced by:
//	  1. Generating the token here, passing only the hash to the DB.
//	  2. Using separate create-response and get/list-response DTOs — the create
//	     response has a "token" field; the get/list DTOs do not.
//	  3. GET and LIST use GetServiceAccountForTenant, which explicitly omits
//	     token_hash and token_lookup_id from the SELECT list.
//
// Cross-tenant guard: the URL {tenant_id} must match the caller's tenantID
// extracted from the auth token. Mismatches return 404 (not 403) so tenant
// identifiers from other tenants are not enumerable.
package handlers

import (
	"crypto/rand"
	"encoding/hex"
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
	"golang.org/x/crypto/bcrypt"
)

// saLookupIDBytes is the number of random bytes to generate for the lookup_id.
// 6 bytes → 12 hex chars (matches the saLookupIDLen constant in serviceaccount.go).
const saLookupIDBytes = 6

// saSecretBytes is the number of random bytes to generate for the secret.
// 16 bytes → 32 hex chars.
const saSecretBytes = 16

// saTokenPrefix is duplicated from middleware to avoid an import cycle.
// The string value MUST match middleware.ServiceAccountTokenPrefix exactly.
const saTokenPrefix = "dcapi_sa_"

// ServiceAccountHandler handles all /v1/tenants/{tenant_id}/service-accounts endpoints.
type ServiceAccountHandler struct {
	repo *db.Repository
	log  zerolog.Logger
}

// NewServiceAccountHandler creates a ServiceAccountHandler with injected dependencies.
func NewServiceAccountHandler(repo *db.Repository, log zerolog.Logger) *ServiceAccountHandler {
	return &ServiceAccountHandler{repo: repo, log: log}
}

// ── DTOs ──────────────────────────────────────────────────────────────────────

// createServiceAccountRequest is the JSON body for POST .../service-accounts.
type createServiceAccountRequest struct {
	Name        string `json:"name"`
	Role        string `json:"role"`
	Description string `json:"description"`
}

func (req *createServiceAccountRequest) validate() error {
	if err := validateResourceName(req.Name); err != nil {
		return err
	}
	switch models.Role(req.Role) {
	case models.RoleOwner, models.RoleMember, models.RoleViewer:
		// valid
	default:
		return fmt.Errorf("role must be one of: owner, member, viewer")
	}
	if len(req.Description) > 256 {
		return fmt.Errorf("description must be 256 characters or fewer")
	}
	return nil
}

// createServiceAccountResponse is returned from POST on success (201).
// It includes the raw token — this is the ONLY time the token appears in an
// API response. All subsequent GET/LIST responses use serviceAccountResponse,
// which has no token field. Do NOT add a token field to serviceAccountResponse.
type createServiceAccountResponse struct {
	ID          string `json:"id"`
	TenantID    string `json:"tenant_id"`
	Name        string `json:"name"`
	Role        string `json:"role"`
	Description string `json:"description,omitempty"`
	CreatedAt   string `json:"created_at"`
	// Token carries the raw dcapi_sa_<lookup_id>_<secret> token. It is generated
	// server-side and returned exactly once at creation. The caller MUST store it
	// immediately; it cannot be retrieved again.
	Token string `json:"token"`
}

// serviceAccountResponse is the JSON shape for GET / LIST responses.
// token and token_hash are intentionally absent — they must never appear here.
type serviceAccountResponse struct {
	ID          string  `json:"id"`
	TenantID    string  `json:"tenant_id"`
	Name        string  `json:"name"`
	Role        string  `json:"role"`
	Description string  `json:"description,omitempty"`
	CreatedAt   string  `json:"created_at"`
	LastUsed    *string `json:"last_used,omitempty"`
}

func saToResponse(sa *models.ServiceAccount, role models.Role) serviceAccountResponse {
	resp := serviceAccountResponse{
		ID:          sa.ID.String(),
		TenantID:    sa.TenantID,
		Name:        sa.Name,
		Role:        string(role),
		Description: sa.Description,
		CreatedAt:   sa.CreatedAt.Format(time.RFC3339),
	}
	if sa.LastUsed != nil {
		s := sa.LastUsed.Format(time.RFC3339)
		resp.LastUsed = &s
	}
	return resp
}

// ── Token generation ──────────────────────────────────────────────────────────

// generateSAToken creates a fresh raw service-account token.
// Returns (rawToken, lookupID, secret, error).
//
// rawToken = "dcapi_sa_" + lookupID + "_" + secret
// lookupID = 12-char lowercase hex (6 random bytes)
// secret   = 32-char lowercase hex (16 random bytes)
func generateSAToken() (rawToken, lookupID, secret string, err error) {
	lookupBytes := make([]byte, saLookupIDBytes)
	if _, err = rand.Read(lookupBytes); err != nil {
		return "", "", "", fmt.Errorf("generate SA lookup_id: %w", err)
	}
	secretBytes := make([]byte, saSecretBytes)
	if _, err = rand.Read(secretBytes); err != nil {
		return "", "", "", fmt.Errorf("generate SA secret: %w", err)
	}
	lookupID = hex.EncodeToString(lookupBytes)
	secret = hex.EncodeToString(secretBytes)
	rawToken = saTokenPrefix + lookupID + "_" + secret
	return rawToken, lookupID, secret, nil
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// Create handles POST /v1/tenants/{tenant_id}/service-accounts.
// Only owners may create service accounts. Returns 201 with the raw token
// (shown exactly once). Returns 409 on duplicate name.
func (h *ServiceAccountHandler) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant uuid in context")
		return
	}

	// Cross-tenant guard.
	urlTenantID := chi.URLParam(r, "tenant_id")
	if !middleware.IsAdminFromContext(r.Context()) && urlTenantID != tenantID {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	if !requireAction(w, r, h.repo, rbac.ActionServiceAccountWrite) {
		return
	}

	_, callerID, ok := middleware.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no principal in context")
		return
	}

	var req createServiceAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := req.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Generate the token components.
	rawToken, lookupID, secret, err := generateSAToken()
	if err != nil {
		h.log.Error().Err(err).Msg("generate SA token failed")
		writeError(w, http.StatusInternalServerError, "failed to generate service account token")
		return
	}

	// Hash the secret portion with bcrypt at DefaultCost (must match the cost
	// used by ServiceAccountAuth.Validate in middleware/serviceaccount.go).
	tokenHash, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.DefaultCost)
	if err != nil {
		h.log.Error().Err(err).Msg("bcrypt hash SA token failed")
		writeError(w, http.StatusInternalServerError, "failed to hash service account token")
		return
	}

	projectID, projectUUID, _ := lookupProjectUUID(w, r)
	sa, err := h.repo.CreateServiceAccountWithRole(r.Context(), models.ServiceAccount{
		TenantID:      tenantID,
		TenantUUID:    tenantUUID,
		ProjectID:     projectID,
		ProjectUUID:   projectUUID,
		Name:          req.Name,
		TokenLookupID: lookupID,
		TokenHash:     string(tokenHash),
		Description:   req.Description,
	}, models.Role(req.Role), callerID)
	if err != nil {
		if strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "duplicate key") {
			writeError(w, http.StatusConflict,
				fmt.Sprintf("a service account named %q already exists for this tenant", req.Name))
			return
		}
		h.log.Error().Err(err).Str("tenant", tenantID).Str("name", req.Name).
			Msg("create service account failed")
		writeError(w, http.StatusInternalServerError, "failed to create service account")
		return
	}

	_ = h.repo.AppendAuditEvent(r.Context(), &models.AuditEvent{
		ResourceID: sa.ID,
		ActorID:    callerID,
		Action:     "SERVICE_ACCOUNT_CREATE",
		Message: fmt.Sprintf("created service account %s with role %s",
			req.Name, req.Role),
	})

	writeJSON(w, http.StatusCreated, createServiceAccountResponse{
		ID:          sa.ID.String(),
		TenantID:    sa.TenantID,
		Name:        sa.Name,
		Role:        req.Role,
		Description: sa.Description,
		CreatedAt:   sa.CreatedAt.Format(time.RFC3339),
		Token:       rawToken,
	})
}

// List handles GET /v1/tenants/{tenant_id}/service-accounts.
// Any authenticated tenant member can list service accounts (read is open).
// token and token_hash are never included in the response.
func (h *ServiceAccountHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}

	// Cross-tenant guard.
	urlTenantID := chi.URLParam(r, "tenant_id")
	if !middleware.IsAdminFromContext(r.Context()) && urlTenantID != tenantID {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	sas, err := h.repo.ListServiceAccountsForTenant(r.Context(), tenantUUID)
	if err != nil {
		h.log.Error().Err(err).Str("tenant", tenantID).Msg("list service accounts failed")
		writeError(w, http.StatusInternalServerError, "failed to list service accounts")
		return
	}

	resp := make([]serviceAccountResponse, 0, len(sas))
	for i := range sas {
		role, err := h.repo.GetRoleForServiceAccount(r.Context(), sas[i].ID, tenantID)
		if err != nil {
			h.log.Warn().Err(err).Str("sa_id", sas[i].ID.String()).
				Msg("get role for SA failed — omitting from list")
			role = ""
		}
		resp = append(resp, saToResponse(&sas[i], role))
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"service_accounts": resp})
}

// Get handles GET /v1/tenants/{tenant_id}/service-accounts/{sa_id}.
// Any authenticated tenant member can retrieve a service account.
// token and token_hash are never included in the response.
func (h *ServiceAccountHandler) Get(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}

	// Cross-tenant guard.
	urlTenantID := chi.URLParam(r, "tenant_id")
	if !middleware.IsAdminFromContext(r.Context()) && urlTenantID != tenantID {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	saIDStr := chi.URLParam(r, "sa_id")
	saID, err := uuid.Parse(saIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service account ID")
		return
	}

	sa, err := h.repo.GetServiceAccountForTenant(r.Context(), saID, tenantUUID)
	if err != nil {
		h.log.Error().Err(err).Str("sa_id", saIDStr).Msg("get service account failed")
		writeError(w, http.StatusInternalServerError, "failed to get service account")
		return
	}
	if sa == nil {
		writeError(w, http.StatusNotFound, "service account not found")
		return
	}

	role, err := h.repo.GetRoleForServiceAccount(r.Context(), saID, tenantID)
	if err != nil {
		h.log.Warn().Err(err).Str("sa_id", saIDStr).Msg("get role for SA failed")
		role = ""
	}

	writeJSON(w, http.StatusOK, saToResponse(sa, role))
}

// Delete handles DELETE /v1/tenants/{tenant_id}/service-accounts/{sa_id}.
// Only owners may delete service accounts. Deletes the SA row and all its
// role_assignments rows in a single transaction. Returns 204 on success.
func (h *ServiceAccountHandler) Delete(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}

	// Cross-tenant guard.
	urlTenantID := chi.URLParam(r, "tenant_id")
	if !middleware.IsAdminFromContext(r.Context()) && urlTenantID != tenantID {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	if !requireAction(w, r, h.repo, rbac.ActionServiceAccountDelete) {
		return
	}

	_, callerID, ok := middleware.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no principal in context")
		return
	}

	saIDStr := chi.URLParam(r, "sa_id")
	saID, err := uuid.Parse(saIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service account ID")
		return
	}

	// Verify the SA belongs to this tenant before deleting.
	sa, err := h.repo.GetServiceAccountForTenant(r.Context(), saID, tenantUUID)
	if err != nil {
		h.log.Error().Err(err).Str("sa_id", saIDStr).Msg("get service account for delete failed")
		writeError(w, http.StatusInternalServerError, "failed to validate service account")
		return
	}
	if sa == nil {
		writeError(w, http.StatusNotFound, "service account not found")
		return
	}

	if err := h.repo.DeleteServiceAccountWithRole(r.Context(), saID); err != nil {
		h.log.Error().Err(err).Str("sa_id", saIDStr).Str("tenant", tenantID).
			Msg("delete service account failed")
		writeError(w, http.StatusInternalServerError, "failed to delete service account")
		return
	}

	_ = h.repo.AppendAuditEvent(r.Context(), &models.AuditEvent{
		ResourceID: saID,
		ActorID:    callerID,
		Action:     "SERVICE_ACCOUNT_DELETE",
		Message:    fmt.Sprintf("deleted service account %s", sa.Name),
	})

	w.WriteHeader(http.StatusNoContent)
}
