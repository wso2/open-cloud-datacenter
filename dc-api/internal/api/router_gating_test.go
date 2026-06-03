package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"
	"github.com/wso2/dc-api/internal/api/handlers"
)

// allowlistedV1Routes are the /v1 endpoints intentionally NOT behind a resource
// permission gate:
//   - the BFF OIDC handshake (/v1/auth/*) — pre-session, can't be RBAC-gated;
//   - self-scoped discovery — the caller's own tenants, their own permissions;
//   - the public role catalog (non-sensitive metadata);
//   - the admin-tenant registry, which self-enforces platform-admin in the handler.
//
// Everything else under /v1 must go through handlers.Gate. Adding a route here is
// a deliberate, reviewed decision — the default is "gated".
var allowlistedV1Routes = map[string]bool{
	"GET /v1/auth/login":                             true,
	"GET /v1/auth/callback":                          true,
	"POST /v1/auth/logout":                           true,
	"GET /v1/auth/me":                                true,
	"GET /v1/tenants":                                true,
	"GET /v1/role-definitions":                       true,
	"GET /v1/role-definitions/{key}":                 true,
	"POST /v1/admin/tenants":                         true,
	"PATCH /v1/admin/tenants/{tenant_id}/":           true,
	"POST /v1/tenants/{tenant_id}/permissions:check":                     true,
	"POST /v1/tenants/{tenant_id}/projects/{project_id}/permissions:check": true,
	"POST /v1/tenants/{tenant_id}/projects/{project_id}/virtual-machines/{id}/permissions:check": true,
	"POST /v1/tenants/{tenant_id}/projects/{project_id}/clusters/{id}/permissions:check":         true,
	"POST /v1/tenants/{tenant_id}/projects/{project_id}/keyvaults/{id}/permissions:check":        true,
	"POST /v1/tenants/{tenant_id}/projects/{project_id}/databases/{id}/permissions:check":        true,
	// Tenant-shared catalog / metadata reads: any tenant member (including a
	// project-scoped service account) reads these to provision. Membership is
	// enforced by TenantContext, so there's no per-action gate. The matching
	// write (POST /images) IS gated.
	"GET /v1/tenants/{tenant_id}/images":    true,
	"GET /v1/tenants/{tenant_id}/networks":  true,
	"GET /v1/tenants/{tenant_id}/cap-usage": true,
	// Self-scoped discovery: the resources the caller holds a resource-scope
	// grant on, so a resource-only user can reach what's shared with them.
	"GET /v1/tenants/{tenant_id}/shared-resources": true,
	// Project navigation reads: listing/reading projects is how a member finds
	// where their resources live. Access is already enforced by TenantContext
	// (list) and ProjectContext (detail), so no per-action gate — otherwise a
	// per-resource-type role couldn't navigate into a project at all. Project
	// writes (create/patch/delete) ARE gated.
	"GET /v1/tenants/{tenant_id}/projects/":               true,
	"GET /v1/tenants/{tenant_id}/projects/{project_id}/":  true,
}

// TestRouter_EveryV1RouteIsGated is the enforcement framework's safety net. It
// walks every registered route and asserts each /v1 route either goes through the
// authorization Gate or is on the explicit allowlist above. Add a resource
// endpoint without a Gate (or an allowlist entry) and this test fails — an ungated
// route cannot ship, which is exactly the "forgot to gate the reads" class of bug
// this framework exists to prevent.
//
// Provider-conditional routes (key-vault secrets, key-vault private endpoints)
// only register when their provider is wired; this build uses nil providers, so
// they are not walked here. They are still declared with Gate in the router.
func TestRouter_EveryV1RouteIsGated(t *testing.T) {
	router := NewRouter(RouterDeps{
		Log:            zerolog.Nop(),
		AuthMiddleware: nopAuth{},
	})
	routes, ok := router.(chi.Routes)
	if !ok {
		t.Fatalf("NewRouter returned %T, want a chi.Routes", router)
	}

	var ungated []string
	walkErr := chi.Walk(routes, func(method, route string, h http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, "/v1/") {
			return nil
		}
		key := method + " " + route
		if allowlistedV1Routes[key] {
			return nil
		}
		if _, gated := handlers.IsGated(h); !gated {
			ungated = append(ungated, key)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk routes: %v", walkErr)
	}
	for _, r := range ungated {
		t.Errorf("ungated /v1 route (no Gate, not allowlisted): %s", r)
	}
}
