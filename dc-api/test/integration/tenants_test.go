//go:build integration

// tenants_test.go — integration tests for GET /v1/tenants.
//
// The endpoint returns the set of tenants the authenticated principal has
// access to:
//   - Non-admin principals: their explicit role_assignments grouped by tenant,
//     each tenant's roles[] is the union of distinct roles held there.
//   - Admin principals (dc-admin group): every tenant present in the
//     role_assignments table, each with roles=["owner"].
//
// All tests use AutoProvisionMembers=false so role_assignments are inserted
// explicitly via insertRole() and the response is fully predictable.
package integration

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wso2/dc-api/internal/models"
)

// TestTenants_Member_SingleTenant verifies a non-admin user with one role
// assignment sees exactly one tenant in the list, with their role.
func TestTenants_Member_SingleTenant(t *testing.T) {
	t.Parallel()
	subEnv := rbacSubEnv(t)

	tenantID := randomTenantID("list-single")
	userSub := "sub-list-single-" + randomName("u")
	insertRole(t, subEnv, userSub, tenantID, models.RoleMember)

	token := mintTokenForSubEnv(t, subEnv, userSub, tenantID)
	client := NewAPIClient(subEnv.BaseURL, token)

	tenants, body, status, err := client.ListTenants(context.Background())
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "body=%s", body)

	// Filter to just this test's tenant — the shared DB may contain rows from
	// parallel tests under other principals, but the principal isolation in
	// the handler should prevent them leaking. Still, be specific.
	require.Len(t, tenants, 1, "member should see exactly the one tenant they belong to")
	require.Equal(t, tenantID, tenants[0].ID)
	require.Equal(t, tenantID, tenants[0].Name)
	require.Equal(t, []string{"member"}, tenants[0].Roles)
}

// TestTenants_Member_MultiTenant verifies a user with assignments on two
// distinct tenants sees both, sorted by ID, each with the right role.
func TestTenants_Member_MultiTenant(t *testing.T) {
	t.Parallel()
	subEnv := rbacSubEnv(t)

	suffix := randomName("u")
	userSub := "sub-list-multi-" + suffix
	// Use a sortable prefix so the expected order is deterministic regardless
	// of randomName.
	tenantA := randomTenantID("list-multi-a")
	tenantB := randomTenantID("list-multi-b")

	insertRole(t, subEnv, userSub, tenantA, models.RoleOwner)
	insertRole(t, subEnv, userSub, tenantB, models.RoleViewer)

	// Mint a token containing membership in both tenants. Only the first
	// dc-tenant-* group sticks as the active tenant on the JWT, but the role
	// resolution for /v1/tenants reads from role_assignments — not from the
	// JWT — so this still works.
	token := mintTokenForSubEnv(t, subEnv, userSub, tenantA, "dc-tenant-"+tenantB)
	client := NewAPIClient(subEnv.BaseURL, token)

	tenants, body, status, err := client.ListTenants(context.Background())
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "body=%s", body)

	require.Len(t, tenants, 2, "user with two assignments should see two tenants")
	require.Equal(t, tenantA, tenants[0].ID, "tenants must be sorted by id")
	require.Equal(t, []string{"owner"}, tenants[0].Roles)
	require.Equal(t, tenantB, tenants[1].ID)
	require.Equal(t, []string{"viewer"}, tenants[1].Roles)
}

// TestTenants_Member_MultipleRolesSameTenant verifies that when a principal
// holds more than one role at the same scope, the response aggregates them
// into a sorted set of strings.
func TestTenants_Member_MultipleRolesSameTenant(t *testing.T) {
	t.Parallel()
	subEnv := rbacSubEnv(t)

	tenantID := randomTenantID("list-multirole")
	userSub := "sub-list-multirole-" + randomName("u")
	insertRole(t, subEnv, userSub, tenantID, models.RoleOwner)
	insertRole(t, subEnv, userSub, tenantID, models.RoleMember)

	token := mintTokenForSubEnv(t, subEnv, userSub, tenantID)
	client := NewAPIClient(subEnv.BaseURL, token)

	tenants, body, status, err := client.ListTenants(context.Background())
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "body=%s", body)

	require.Len(t, tenants, 1)
	require.Equal(t, tenantID, tenants[0].ID)
	// Sorted ascending — "member" before "owner".
	require.Equal(t, []string{"member", "owner"}, tenants[0].Roles)
}

// TestTenants_Admin_SeesAllTenantsAsOwner verifies that a platform admin
// (dc-admin group) sees every tenant in role_assignments, each with role
// "owner". The admin path does not consult their explicit assignments.
func TestTenants_Admin_SeesAllTenantsAsOwner(t *testing.T) {
	t.Parallel()
	subEnv := rbacSubEnv(t)

	suffix := randomName("admin")
	tenantA := randomTenantID("admin-list-a")
	tenantB := randomTenantID("admin-list-b")

	// Seed both tenants with assignments under different (non-admin) users.
	// The admin should see both tenants in their list.
	insertRole(t, subEnv, "seed-a-"+suffix, tenantA, models.RoleOwner)
	insertRole(t, subEnv, "seed-b-"+suffix, tenantB, models.RoleMember)

	adminSub := "sub-admin-" + suffix
	adminToken, err := subEnv.JWT.MintTokenWithGroups(
		adminSub,
		adminSub+"@test.dc",
		[]string{"dc-admin"},
	)
	require.NoError(t, err)

	client := NewAPIClient(subEnv.BaseURL, adminToken)

	tenants, body, status, err := client.ListTenants(context.Background())
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "body=%s", body)

	// The admin sees every tenant present in role_assignments — including
	// other tests running in parallel. Filter to the two we seeded.
	got := map[string][]string{}
	for _, t := range tenants {
		got[t.ID] = t.Roles
	}
	require.Contains(t, got, tenantA, "admin should see tenant A")
	require.Contains(t, got, tenantB, "admin should see tenant B")
	require.Equal(t, []string{"owner"}, got[tenantA], "admin's role should always be owner")
	require.Equal(t, []string{"owner"}, got[tenantB])
}

// TestTenants_Unauthenticated returns 401 when no bearer token is provided.
func TestTenants_Unauthenticated(t *testing.T) {
	t.Parallel()
	subEnv := rbacSubEnv(t)

	client := NewAPIClient(subEnv.BaseURL, "")
	_, _, status, err := client.ListTenants(context.Background())
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, status,
		"unauthenticated request to /v1/tenants must return 401")
}
