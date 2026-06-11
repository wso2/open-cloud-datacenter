// Package handlers — directory.go
//
// DirectoryHandler implements the optional IdP directory proxy endpoints:
//
//	GET /v1/tenants/{tenant_id}/directory/users   → SCIM2 user search (invite picker)
//	GET /v1/tenants/{tenant_id}/directory/groups  → SCIM2 group listing
//
// The path is tenant-scoped because RBAC gating evaluates actions at a scope,
// but the directory itself is organization-global — every tenant queries the
// same IdP. Both routes are gated with authorization/roleAssignments/write
// (a write action gating GETs is intentional: the directory is visible only
// to principals who can perform invitations).
//
// Responses are passed through live from the IdP, minimal
// fields only (sub/email/display_name; id/name), nothing persisted.
//
// Feature detection: 501 when no directory provider is configured (the
// feature is dark until the operator reconfigures dc-api); 502 when a
// provider is configured but the live IdP request failed (transient — retry).
package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"
	"github.com/wso2/dc-api/internal/api/middleware"
	"github.com/wso2/dc-api/internal/directory"
)

const (
	directoryDefaultLimit = 50
	directoryMaxLimit     = 200
	directoryMaxFilterLen = 256

	// directoryUpstreamErrorMsg is the generic 502 body returned when the live
	// IdP request fails. The detailed error (which embeds the internal SCIM URL
	// and IdP host) is logged server-side only — it must never reach API
	// clients. Mirrors openapi.yaml components/responses/DirectoryUpstreamError.
	directoryUpstreamErrorMsg = "directory upstream request failed; try again or invite by user_sub"
)

// DirectoryHandler serves the /directory/* read-only proxy endpoints.
type DirectoryHandler struct {
	// directory may be nil — the feature is dark and every endpoint returns 501.
	directory directory.Provider
	log       zerolog.Logger
}

// NewDirectoryHandler creates a DirectoryHandler. dir may be nil (feature dark).
func NewDirectoryHandler(dir directory.Provider, log zerolog.Logger) *DirectoryHandler {
	return &DirectoryHandler{directory: dir, log: log}
}

// ── DTOs ──────────────────────────────────────────────────────────────────────

type directoryUserResponse struct {
	Sub         string `json:"sub"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name,omitempty"`
}

type directoryGroupResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// ListUsers proxies a live SCIM2 user search. Gated by
// authorization/roleAssignments/write at the route.
func (h *DirectoryHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	if !h.tenantGuard(w, r) {
		return
	}
	if h.directory == nil {
		writeError(w, http.StatusNotImplemented,
			"no directory provider is configured on this deployment")
		return
	}

	filter := r.URL.Query().Get("filter")
	if len(filter) > directoryMaxFilterLen {
		writeError(w, http.StatusBadRequest, "filter must be at most 256 characters")
		return
	}
	if strings.ContainsAny(filter, `"\`) {
		writeError(w, http.StatusBadRequest, "filter contains invalid characters")
		return
	}
	limit, offset, ok := parseDirectoryPaging(w, r)
	if !ok {
		return
	}

	users, total, err := h.directory.SearchUsers(r.Context(), filter, limit, offset)
	if err != nil {
		h.log.Error().Err(err).Str("filter", filter).Msg("directory user search failed")
		writeError(w, http.StatusBadGateway, directoryUpstreamErrorMsg)
		return
	}

	resp := make([]directoryUserResponse, 0, len(users))
	for _, u := range users {
		resp = append(resp, directoryUserResponse{
			Sub:         u.Sub,
			Email:       u.Email,
			DisplayName: u.DisplayName,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"users":         resp,
		"total_results": total,
	})
}

// ListGroups proxies a live SCIM2 group listing. Gated by
// authorization/roleAssignments/write at the route.
func (h *DirectoryHandler) ListGroups(w http.ResponseWriter, r *http.Request) {
	if !h.tenantGuard(w, r) {
		return
	}
	if h.directory == nil {
		writeError(w, http.StatusNotImplemented,
			"no directory provider is configured on this deployment")
		return
	}

	limit, offset, ok := parseDirectoryPaging(w, r)
	if !ok {
		return
	}

	groups, total, err := h.directory.ListGroups(r.Context(), limit, offset)
	if err != nil {
		h.log.Error().Err(err).Msg("directory group listing failed")
		writeError(w, http.StatusBadGateway, directoryUpstreamErrorMsg)
		return
	}

	resp := make([]directoryGroupResponse, 0, len(groups))
	for _, g := range groups {
		resp = append(resp, directoryGroupResponse{ID: g.ID, Name: g.Name})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"groups":        resp,
		"total_results": total,
	})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// tenantGuard enforces that the URL tenant_id matches the caller's tenant
// context (admins exempt — they operate across tenants). Mirrors
// RoleAssignmentsHandler.tenantGuard: 404 on mismatch so other tenants'
// identifiers are not enumerable.
func (h *DirectoryHandler) tenantGuard(w http.ResponseWriter, r *http.Request) bool {
	tenantID, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return false
	}
	if !middleware.IsAdminFromContext(r.Context()) && chi.URLParam(r, "tenant_id") != tenantID {
		writeError(w, http.StatusNotFound, "tenant not found")
		return false
	}
	return true
}

// parseDirectoryPaging reads limit/offset with the spec's defaults and bounds
// (limit default 50, 1..200; offset >= 0, default 0). Writes a 400 and
// returns ok=false on invalid input.
func parseDirectoryPaging(w http.ResponseWriter, r *http.Request) (limit, offset int, ok bool) {
	limit = directoryDefaultLimit
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > directoryMaxLimit {
			writeError(w, http.StatusBadRequest, "limit must be an integer between 1 and 200")
			return 0, 0, false
		}
		limit = n
	}
	if s := r.URL.Query().Get("offset"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "offset must be a non-negative integer")
			return 0, 0, false
		}
		offset = n
	}
	return limit, offset, true
}
