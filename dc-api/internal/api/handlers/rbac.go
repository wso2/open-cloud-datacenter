// Package handlers — rbac.go
//
// requireAction is the handler-level RBAC v2 enforcement helper, and
// scopeChainFromContext builds the request's scope chain from the UUIDs that the
// tenant/project context middleware injected. Every mutating and data-plane
// handler calls requireAction(<action>) after the middleware has run; read-only
// list handlers additionally rely on the tenant_id / project_uuid WHERE clauses
// in SQL for isolation.
package handlers

import (
	"context"
	"net/http"

	"github.com/wso2/dc-api/internal/api/middleware"
	"github.com/wso2/dc-api/internal/models"
	"github.com/wso2/dc-api/internal/rbac"
)

// requireAction is the RBAC v2 enforcement helper: it authorizes the principal
// for a specific action (e.g. rbac.ActionVNetWrite) against the request's scope
// chain (project → tenant, built from context) using the action engine. Built-in
// roles resolve via the in-code registry; platform-admin short-circuits. Returns
// true to continue, false after having written a 401/403/500.
func requireAction(w http.ResponseWriter, r *http.Request, repo rbac.Repo, action string) bool {
	pType, pID, ok := middleware.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no principal in context")
		return false
	}
	isAdmin := middleware.IsAdminFromContext(r.Context())

	assignments, err := repo.ListRoleAssignmentsForPrincipal(r.Context(), pType, pID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "rbac check failed")
		return false
	}
	ras := make([]rbac.Assignment, 0, len(assignments))
	for _, a := range assignments {
		ras = append(ras, rbac.Assignment{
			RoleDefKey: a.RoleDefinition,
			ScopeType:  a.ScopeType,
			ScopeUUID:  a.ScopeUUID,
		})
	}

	if !rbac.Authorize(ras, rbac.BuiltinResolver, action, rbac.IsDataAction(action),
		scopeChainFromContext(r.Context()), isAdmin) {
		writeError(w, http.StatusForbidden, "insufficient permissions for this action")
		return false
	}
	return true
}

// scopeChainFromContext builds the request's scope chain (narrowest → broadest)
// from the UUIDs the TenantContext / ProjectContext middleware injected. A
// resource-scope entry is added later, when handlers target an individual
// resource by UUID.
func scopeChainFromContext(ctx context.Context) []rbac.ScopeRef {
	chain := make([]rbac.ScopeRef, 0, 3)
	if pid, ok := middleware.ProjectUUIDFromContext(ctx); ok {
		chain = append(chain, rbac.ScopeRef{Type: models.ScopeTypeProject, UUID: pid})
	}
	if tid, ok := middleware.TenantUUIDFromContext(ctx); ok {
		chain = append(chain, rbac.ScopeRef{Type: models.ScopeTypeTenant, UUID: tid})
	}
	return chain
}
