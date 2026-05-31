// Package handlers — rbac.go
//
// requireTenantRole is the handler-level shortcut for RBAC enforcement.
//
// Every mutating handler calls this after extracting tenantID from context.
// Read-only (GET/LIST) handlers skip it — the existing tenant_id WHERE clause
// in the SQL queries already provides isolation.
//
// Scope chain length is 1 in M1.5 (just the tenant). When M5 lands, callers
// will pass a longer chain (subscription → resource_group → resource); the
// helper signature is already shaped for that by accepting tenantID as a
// parameter from which it builds the chain internally.
package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/wso2/dc-api/internal/api/middleware"
	"github.com/wso2/dc-api/internal/models"
	"github.com/wso2/dc-api/internal/rbac"
)

// requireTenantRole enforces that the requesting principal holds at least the
// minimum role on the given tenant scope.
//
// It pulls the principal (type + id) and isAdmin flag from the request context
// (set by the auth middleware in Chunk 3), builds a single-element tenant scope
// chain, and delegates to rbac.RequireRole.
//
// Return value: true means "allowed, continue". false means "denied, stop" —
// the helper has already written a 401, 403, or 500 response to w.
//
// Usage pattern in a mutating handler:
//
//	tenantID, ok := middleware.TenantFromContext(r.Context())
//	if !ok {
//	    writeError(w, http.StatusUnauthorized, "no tenant in context")
//	    return
//	}
//	if !requireTenantRole(w, r, h.repo, tenantID, models.RoleMember) {
//	    return
//	}
//	// ... normal handler logic
func requireTenantRole(
	w http.ResponseWriter,
	r *http.Request,
	repo rbac.Repo,
	tenantID string,
	min models.Role,
) bool {
	pType, pID, ok := middleware.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no principal in context")
		return false
	}
	isAdmin := middleware.IsAdminFromContext(r.Context())

	chain := []models.Scope{{Type: models.ScopeTypeTenant, ID: tenantID}}
	if err := rbac.RequireRole(r.Context(), repo, pType, pID, isAdmin, chain, min); err != nil {
		if errors.Is(err, rbac.ErrInsufficientRole) {
			writeError(w, http.StatusForbidden, "insufficient role for this action")
			return false
		}
		// DB error or unexpected — log not available here, return 500.
		writeError(w, http.StatusInternalServerError, "rbac check failed")
		return false
	}
	return true
}

// requireAction is the RBAC v2 enforcement helper: it authorizes the principal
// for a specific action (e.g. rbac.ActionVNetWrite) against the request's scope
// chain (project → tenant, built from context) using the action engine. Built-in
// roles resolve via the in-code registry; platform-admin short-circuits. Returns
// true to continue, false after having written a 401/403/500.
//
// This is the v2 successor to requireTenantRole. Handlers migrate from
// requireTenantRole(min) to requireAction(action) one at a time; both coexist
// until every handler is converted.
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
