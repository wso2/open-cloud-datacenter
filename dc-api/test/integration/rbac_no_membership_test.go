//go:build integration

// rbac_no_membership_test.go — membership comes from role_assignments only.
//
// IdP groups play no part in tenant membership (the only group dc-api
// interprets is the admin group). This test pins the new-user experience:
// a valid token with no role_assignments rows authenticates fine, sees an
// empty tenant list, cannot reach any tenant-scoped path (404 — tenant
// existence is not leaked), and gains access the moment an owner grants a
// role (the invite flow).
package integration

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wso2/dc-api/internal/api/middleware"
	"github.com/wso2/dc-api/internal/models"
)

func TestRBAC_NoMembership_AuthenticatesButSeesNothing(t *testing.T) {
	t.Parallel()

	tenantID := "test-tenant-rbac-strict"
	userSub := "sub-no-membership-" + randomName("u")

	subEnv := newSubEnv(t, middleware.AuthConfig{
		AdminGroup: "dc-admin",
	})

	// Token with NO groups and NO seeded membership — a brand-new user.
	token, err := subEnv.JWT.MintTokenWithGroups(userSub, userSub+"@test.dc", nil)
	require.NoError(t, err, "mint JWT")
	ctx := context.Background()

	// The tenant exists (registered by an admin) — the new user just has no
	// row in it.
	_, err = subEnv.DB.UpsertTenant(ctx, tenantID, tenantID, "", "test-no-membership")
	require.NoError(t, err, "UpsertTenant")

	client := NewAPIClientForTenant(subEnv.BaseURL, token, tenantID)

	// ── Authenticated, but no memberships: GET /v1/tenants → 200 + empty ────
	rawBody, listStatus, listErr := client.do(ctx, "GET", "/v1/tenants", nil)
	require.NoError(t, listErr)
	require.Equal(t, http.StatusOK, listStatus,
		"a valid token with no memberships must still authenticate")
	require.NotContains(t, string(rawBody), tenantID,
		"tenant list must not include tenants the user has no row in")

	// ── Tenant-scoped path → 404 (no membership; existence not leaked) ──────
	_, firstStatus, firstErr := client.do(ctx, "GET", "/v1/tenants/"+tenantID+"/cap-usage", nil)
	require.NoError(t, firstErr)
	require.Equal(t, http.StatusNotFound, firstStatus,
		"tenant-scoped request without a membership row must return 404")

	// Confirm nothing was auto-created.
	assignments, err := subEnv.DB.ListRoleAssignmentsForPrincipal(ctx, models.PrincipalTypeUser, userSub)
	require.NoError(t, err)
	require.Empty(t, assignments, "no role_assignment row must ever be auto-created")

	// ── Owner invites the user (role grant) → same request now succeeds ─────
	_, err = subEnv.DB.CreateRoleAssignment(ctx, models.RoleAssignment{
		PrincipalType: models.PrincipalTypeUser,
		PrincipalID:   userSub,
		ScopeType:     models.ScopeTypeTenant,
		ScopeID:       tenantID,
		Role:          models.RoleMember,
		GrantedBy:     "test-owner",
	})
	require.NoError(t, err, "role_assignment insert (the invite) must succeed")

	_, _, secondStatus, secondErr := client.GetTenantCapUsage(ctx)
	require.NoError(t, secondErr)
	require.Equal(t, http.StatusOK, secondStatus,
		"request must succeed after the role grant")
}
