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

	"github.com/google/uuid"
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
		case models.ScopeTypeResource:
			// A resource-scope grant surfaces the resource's parent tenant with
			// no tenant-level role, so a resource-only user can navigate toward
			// the resource they hold. ProjectContext and the per-resource gate
			// enforce access below.
			tslug, _, found, err := h.repo.GetResourceLocationByUUID(r.Context(), a.ScopeUUID)
			if err != nil {
				h.log.Error().Err(err).Str("resource_uuid", a.ScopeUUID.String()).
					Msg("resolve tenant for resource grant failed")
				continue
			}
			if found {
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

// sharedResource is one resource a caller can reach through a resource-scope
// grant. Kind is the URL path segment the UI uses to deep-link to the detail
// page; roles are the role definitions the caller holds on it.
type sharedResource struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Name      string   `json:"name"`
	ProjectID string   `json:"project_id"`
	Status    string   `json:"status"`
	Roles     []string `json:"roles"`
}

// SharedResources handles GET /v1/tenants/{tenant_id}/shared-resources. It
// returns the resources in this tenant the caller holds a direct resource-scope
// grant on, so a resource-only user (no tenant or project role) can find and
// open the resources shared with them. Self-scoped — it reflects only the
// caller's own grants — so it carries no per-action gate.
func (h *TenantHandler) SharedResources(w http.ResponseWriter, r *http.Request) {
	pType, pID, ok := middleware.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no principal in context")
		return
	}
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "no tenant uuid in context")
		return
	}

	assignments, err := h.repo.ListRoleAssignmentsForPrincipal(r.Context(), pType, pID)
	if err != nil {
		h.log.Error().Err(err).Str("principal", pID).Msg("shared-resources: list role assignments failed")
		writeError(w, http.StatusInternalServerError, "failed to list shared resources")
		return
	}

	// Collect the resource-scope grants: the resource UUIDs to look up, and the
	// role definitions held on each.
	rolesByResource := make(map[uuid.UUID][]string)
	ids := make([]uuid.UUID, 0)
	for _, a := range assignments {
		if a.ScopeType != models.ScopeTypeResource {
			continue
		}
		if _, seen := rolesByResource[a.ScopeUUID]; !seen {
			ids = append(ids, a.ScopeUUID)
		}
		rolesByResource[a.ScopeUUID] = append(rolesByResource[a.ScopeUUID], a.RoleDefinition)
	}

	shared, err := h.repo.ListSharedResources(r.Context(), tenantUUID, ids)
	if err != nil {
		h.log.Error().Err(err).Msg("shared-resources: list shared resources failed")
		writeError(w, http.StatusInternalServerError, "failed to list shared resources")
		return
	}

	resp := make([]sharedResource, 0, len(shared))
	for _, s := range shared {
		resp = append(resp, sharedResource{
			ID:        s.ID.String(),
			Kind:      sharedResourceKind(s.Type),
			Name:      s.Name,
			ProjectID: s.ProjectID,
			Status:    s.Status,
			Roles:     rolesByResource[s.ID],
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// sharedResourceKind maps a stored resource type to the URL path segment the UI
// uses to build a deep link to the resource's detail page.
func sharedResourceKind(stored string) string {
	switch stored {
	case string(models.ResourceTypeVM):
		return "virtual-machines"
	case string(models.ResourceTypeCluster):
		return "clusters"
	case "keyvault":
		return "keyvaults"
	case "database":
		return "databases"
	default:
		return ""
	}
}
