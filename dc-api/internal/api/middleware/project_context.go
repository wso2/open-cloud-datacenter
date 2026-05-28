// Package middleware — project_context.go
//
// ProjectContext is the per-request middleware mounted on every
// /v1/tenants/{tenant_id}/projects/{project_id}/... route. It reads project_id
// from the URL, resolves it to the immutable project_uuid, validates the caller
// has access, and injects both ContextKeyProjectID and ContextKeyProjectUUID
// into the request context so downstream handlers can read the active project.
//
// Must run AFTER TenantContext (which has already injected ContextKeyTenantID
// and ContextKeyTenantUUID). Relies on those values being present.
//
// Access rules (mirrors TenantContext but at the project scope):
//   - Platform admins: allowed on any project in any registered tenant.
//   - Tenant owners: implicitly project-owner in every project under that tenant.
//     The scope-chain walk (project → tenant → admin) encodes this.
//   - Users with a role_assignment at scope_type='project' for this project_uuid:
//     allowed.
//   - All others: 404 (not 403) to avoid leaking project existence.
//
// HTTP status codes:
//   - 400 — project_id missing/empty in URL
//   - 401 — no principal in context (Auth didn't run)
//   - 404 — project slug not registered, OR caller has no access
//   - 500 — DB lookup failed
package middleware

import (
	"context"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/wso2/dc-api/internal/api/respond"
	"github.com/wso2/dc-api/internal/models"
	"github.com/wso2/dc-api/internal/rbac"
)

// projectIDPattern matches the canonical project slug — mirrors the
// `^[a-z][a-z0-9-]{0,18}[a-z0-9]$` pattern declared in openapi.yaml on the
// `project_id` path parameter. Same defense-in-depth as tenantIDPattern in
// tenant_context.go.
var projectIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,18}[a-z0-9]$`)

// ProjectContext validates the URL project_id within the active tenant and
// injects both the slug and the canonical project_uuid into the request context.
type ProjectContext struct {
	repo AuthRepo
}

// NewProjectContext constructs a ProjectContext middleware.
func NewProjectContext(repo AuthRepo) *ProjectContext {
	return &ProjectContext{repo: repo}
}

// Validate is the Chi middleware function.
// Usage: r.Route("/projects/{project_id}", func(r chi.Router) { r.Use(pc.Validate); ... })
func (p *ProjectContext) Validate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		urlProject := chi.URLParam(r, "project_id")
		if urlProject == "" {
			respond.Error(w, http.StatusBadRequest, "Bad Request: project_id required in URL")
			return
		}
		if !projectIDPattern.MatchString(urlProject) {
			respond.Error(w, http.StatusBadRequest, "Bad Request: project_id must match ^[a-z][a-z0-9-]{0,18}[a-z0-9]$")
			return
		}

		// TenantContext must have run first.
		tenantID, ok := TenantFromContext(r.Context())
		if !ok {
			respond.Error(w, http.StatusInternalServerError, "Internal Server Error: no tenant in context")
			return
		}

		// Resolve slug → project_uuid. Missing row = 404 for everyone.
		projectUUID, err := p.repo.GetProjectUUIDByTenantAndSlug(r.Context(), tenantID, urlProject)
		if err != nil {
			log.Error().Err(err).Str("tenant", tenantID).Str("project", urlProject).
				Msg("project_context: project uuid lookup failed")
			respond.Error(w, http.StatusInternalServerError, "Internal Server Error")
			return
		}
		if projectUUID == uuid.Nil {
			respond.Error(w, http.StatusNotFound, "project not found")
			return
		}

		// Helper to inject both keys and dispatch.
		dispatch := func(ctx context.Context) {
			ctx = context.WithValue(ctx, ContextKeyProjectID, urlProject)
			ctx = context.WithValue(ctx, ContextKeyProjectUUID, projectUUID)
			next.ServeHTTP(w, r.WithContext(ctx))
		}

		// Platform admin bypass.
		if IsAdminFromContext(r.Context()) {
			dispatch(r.Context())
			return
		}

		pType, pID, ok := PrincipalFromContext(r.Context())
		if !ok {
			respond.Error(w, http.StatusUnauthorized, "Unauthorized: no principal in context")
			return
		}

		// Service accounts are bound at project scope. Their role_assignment
		// has scope_type='project', scope_uuid=project_uuid. Fall through to
		// the generic RBAC walk below — the SA token sets its own principal_type
		// and principal_id; the walk checks the assignments the same way as for users.

		// Build the scope chain: project → tenant (broadest wins).
		// A tenant owner is implicitly a project owner — encoding this via the
		// chain means a single ListRoleAssignmentsForPrincipal call covers both.
		chain := []models.Scope{
			{Type: models.ScopeTypeProject, ID: urlProject},
			{Type: models.ScopeTypeTenant, ID: tenantID},
		}
		if err := rbac.RequireRole(r.Context(), p.repo, pType, pID, false, chain, models.RoleViewer); err != nil {
			// No matching assignment at project or tenant scope → 404 to avoid
			// leaking project existence to callers with no access.
			respond.Error(w, http.StatusNotFound, "project not found")
			return
		}

		dispatch(r.Context())
	})
}
