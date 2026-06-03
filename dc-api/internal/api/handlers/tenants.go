// Package handlers — tenants.go
//
// TenantHandler implements GET /v1/tenants — the endpoint the cloud-ui
// tenant switcher consumes to know which tenants the signed-in principal
// has access to. Both human users (OIDC JWT) and service accounts
// (dcapi_sa_*) may call it; the result is always the caller's own
// tenants.
//
// Auth:
//
//	GET /v1/tenants → any authenticated principal
//
// Behaviour:
//
//   - Platform admins (JWT contained the configured admin group) implicitly
//     access every tenant; we return all distinct tenants from
//     role_assignments with role=["owner"] for each.
//   - Non-admins receive only the tenants they hold an explicit role
//     assignment in. The roles array is the union of distinct roles held
//     across any scope within the tenant — useful for the UI to decide
//     which actions to enable per tenant.
package handlers

import (
	"net/http"
	"sort"

	"github.com/rs/zerolog"
	"github.com/wso2/dc-api/internal/api/middleware"
	"github.com/wso2/dc-api/internal/db"
	"github.com/wso2/dc-api/internal/models"
)

// TenantHandler handles GET /v1/tenants.
type TenantHandler struct {
	repo *db.Repository
	log  zerolog.Logger
}

// NewTenantHandler builds a TenantHandler with injected dependencies.
func NewTenantHandler(repo *db.Repository, log zerolog.Logger) *TenantHandler {
	return &TenantHandler{repo: repo, log: log}
}

// tenantSummary matches the OpenAPI #/components/schemas/TenantSummary shape.
type tenantSummary struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Roles []string `json:"roles"`
}

// List handles GET /v1/tenants.
func (h *TenantHandler) List(w http.ResponseWriter, r *http.Request) {
	pType, pID, ok := middleware.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no principal in context")
		return
	}

	if middleware.IsAdminFromContext(r.Context()) {
		// Admin sees every tenant in the registry — including empty ones
		// that have no role_assignments rows yet. Before the tenants
		// table existed this path queried role_assignments and made
		// empty tenants invisible.
		tenants, err := h.repo.ListTenants(r.Context())
		if err != nil {
			h.log.Error().Err(err).Msg("list tenants failed")
			writeError(w, http.StatusInternalServerError, "failed to list tenants")
			return
		}
		summaries := make([]tenantSummary, 0, len(tenants))
		for _, t := range tenants {
			summaries = append(summaries, tenantSummary{
				ID:    t.ID,
				Name:  t.Name,
				Roles: []string{string(models.RoleOwner)},
			})
		}
		writeJSON(w, http.StatusOK, summaries)
		return
	}

	assignments, err := h.repo.ListRoleAssignmentsForPrincipal(r.Context(), pType, pID)
	if err != nil {
		h.log.Error().Err(err).Str("principal", pID).Msg("list role assignments failed")
		writeError(w, http.StatusInternalServerError, "failed to list tenants")
		return
	}

	// Group by scope_id (tenant id), aggregate distinct roles. A tenant-scope
	// grant contributes its role. A project-scope grant surfaces the project's
	// parent tenant with NO tenant-level role (empty roles), so a project-only
	// user can navigate to the project they were granted on; per-project access
	// is gated by ProjectContext below.
	byTenant := make(map[string]map[string]struct{})
	ensure := func(tenantSlug string) {
		if _, exists := byTenant[tenantSlug]; !exists {
			byTenant[tenantSlug] = make(map[string]struct{})
		}
	}
	for _, a := range assignments {
		switch a.ScopeType {
		case models.ScopeTypeTenant:
			ensure(a.ScopeID)
			byTenant[a.ScopeID][string(a.Role)] = struct{}{}
		case models.ScopeTypeProject:
			tslug, err := h.repo.GetTenantSlugByProjectUUID(r.Context(), a.ScopeUUID)
			if err != nil {
				h.log.Error().Err(err).Str("project_uuid", a.ScopeUUID.String()).
					Msg("resolve tenant for project grant failed")
				continue
			}
			if tslug != "" {
				ensure(tslug)
			}
		}
	}

	summaries := make([]tenantSummary, 0, len(byTenant))
	for id, roleSet := range byTenant {
		roles := make([]string, 0, len(roleSet))
		for role := range roleSet {
			roles = append(roles, role)
		}
		sort.Strings(roles)
		summaries = append(summaries, tenantSummary{ID: id, Name: id, Roles: roles})
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].ID < summaries[j].ID })

	writeJSON(w, http.StatusOK, summaries)
}
