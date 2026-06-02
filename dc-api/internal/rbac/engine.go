// Package rbac — engine.go
//
// The RBAC v2 evaluation engine: pure authorization logic with no knowledge of
// HTTP or PostgreSQL. Given a principal's role assignments and a target scope
// chain, it decides whether an action is permitted. See docs/rbac-v2.md §6.
//
// The pure helpers it exposes (Authorize, HasGrantInChain) are the only
// authorization path in dc-api: every handler and the tenant/project context
// middleware decide access through this engine.
package rbac

import (
	"github.com/google/uuid"

	"github.com/wso2/dc-api/internal/models"
)

// ScopeRef identifies a single node in the resource hierarchy by its immutable
// UUID. A request's scope chain is a slice of these, ordered narrowest →
// broadest (e.g. resource → project → tenant).
//
// Scopes are keyed by UUID, never by slug: tenant slugs are globally unique but
// project slugs are only unique within a tenant, so slug-keyed matching would
// leak a project grant across tenants. See docs/rbac-v2.md §5.1.
type ScopeRef struct {
	Type models.ScopeType
	UUID uuid.UUID
}

// Assignment is the minimal shape the engine needs from a role assignment row:
// which role definition is bound, and at which scope.
type Assignment struct {
	RoleDefKey string
	ScopeType  models.ScopeType
	ScopeUUID  uuid.UUID
}

// Resolver maps a role-definition key to its definition. BuiltinResolver covers
// the system catalog; a composed resolver will additionally consult the
// role_definitions table for custom roles. A key that cannot be resolved grants
// nothing (it is skipped, not an error).
type Resolver func(key string) (RoleDefinition, bool)

// BuiltinResolver resolves built-in role keys only.
func BuiltinResolver(key string) (RoleDefinition, bool) {
	return BuiltinRole(key)
}

// Authorize reports whether the principal may perform action against a target
// whose scope chain is `chain`.
//
//   - isAdmin short-circuits to ALLOW (the platform-admin break-glass).
//   - An assignment applies only if its (ScopeType, ScopeUUID) appears in the
//     chain — i.e. the assignment's scope is an ancestor-or-self of the target.
//   - isData selects the data plane (DataActions) vs the control plane (Actions).
//   - Access is allow-wins and additive: if ANY applicable role permits the
//     action, it is allowed. NotActions are per-role subtractions (handled inside
//     RoleDefinition.Permits), not global deny rules — so one role's subtraction
//     never overrides another role's grant.
func Authorize(
	assignments []Assignment,
	resolve Resolver,
	action string,
	isData bool,
	chain []ScopeRef,
	isAdmin bool,
) bool {
	if isAdmin {
		return true
	}
	if resolve == nil {
		resolve = BuiltinResolver
	}

	for _, a := range assignments {
		if !scopeInChain(a.ScopeType, a.ScopeUUID, chain) {
			continue
		}
		def, ok := resolve(a.RoleDefKey)
		if !ok {
			continue // unknown/deleted role definition grants nothing
		}
		if def.Permits(action, isData) {
			return true
		}
	}
	return false
}

// HasGrantInChain reports whether the principal holds ANY role assignment whose
// scope is an ancestor-or-self of the target (i.e. appears in chain). It is the
// coarse "does this principal have standing in this scope at all" gate the
// tenant/project context middleware uses to choose 404-vs-proceed — the v2
// successor to the v1 "at least Viewer" floor.
//
// It deliberately does not resolve roles or check a specific action: holding any
// assignment in the chain is enough to ENTER the scope. Per-action authorization
// is still enforced by each handler via Authorize, so a principal who may enter a
// project but lacks (say) compute/virtualMachines/write is still denied by the VM
// handler.
func HasGrantInChain(assignments []Assignment, chain []ScopeRef) bool {
	for _, a := range assignments {
		if scopeInChain(a.ScopeType, a.ScopeUUID, chain) {
			return true
		}
	}
	return false
}

func scopeInChain(t models.ScopeType, id uuid.UUID, chain []ScopeRef) bool {
	for _, s := range chain {
		if s.Type == t && s.UUID == id {
			return true
		}
	}
	return false
}
