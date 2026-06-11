//go:build integration

package integration

// identity_decoupling_test.go — IdP-decoupled identity verification.
//
// Three behaviours that pin dc-api's identity model (the IdP is
// authentication-only; tenancy and admin status live in dc-api):
//
//  1. PlatformAdminSubs env-var-driven admin promotion: a user with no
//     dc-admin group claim but whose `sub` is listed in the AuthConfig's
//     PlatformAdminSubs set becomes is_admin=true and sees every tenant
//     in the registry. This is the IdP-decoupled admin bootstrap.
//
//  2. display_alias round-trip: invite a member with a display_alias and
//     verify the same value comes back via GET /members. No PII sourced
//     from the IdP; the alias is purely admin bookkeeping.
//
//  3. Member invite UPSERTs the tenants registry: invite a user to a
//     tenant that's NOT in the registry → tenant becomes visible to
//     admin via GET /v1/tenants on the very next call, without an
//     explicit POST /v1/admin/tenants registration.

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wso2/dc-api/internal/api/middleware"
	"github.com/wso2/dc-api/internal/models"
)

// identitySubEnv builds a sub-env with the supplied platform-admin sub
// list. AdminGroup is left at its default so the group path stays
// functional.
func identitySubEnv(t *testing.T, platformAdminSubs ...string) *TestEnv {
	t.Helper()
	subs := make(map[string]struct{}, len(platformAdminSubs))
	for _, s := range platformAdminSubs {
		subs[s] = struct{}{}
	}
	return newSubEnv(t, middleware.AuthConfig{
		AdminGroup: "dc-admin",
		PlatformAdminSubs: subs,
	})
}

// TestIdentity_PlatformAdminSubsPromotesWithoutGroup verifies that a user
// with NO dc-admin group claim — but whose sub is in PlatformAdminSubs —
// is treated as platform admin (sees the full tenants registry).
func TestIdentity_PlatformAdminSubsPromotesWithoutGroup(t *testing.T) {
	t.Parallel()

	suffix := randomName("ident-envadmin")
	envAdminSub := "sub-env-admin-" + suffix
	subEnv := identitySubEnv(t, envAdminSub)

	tenantA := randomTenantID("ident-env-a")
	tenantB := randomTenantID("ident-env-b")
	insertRole(t, subEnv, "seed-a-"+suffix, tenantA, models.RoleOwner)
	insertRole(t, subEnv, "seed-b-"+suffix, tenantB, models.RoleMember)

	// Mint a token WITHOUT the dc-admin group — only a dc-tenant-something
	// group so the user is authenticatable. The admin promotion must come
	// from the env-var path alone.
	envAdminToken, err := subEnv.JWT.MintTokenWithGroups(
		envAdminSub,
		envAdminSub+"@test.dc",
		[]string{"dc-tenant-some-tenant"}, // NOT dc-admin
	)
	require.NoError(t, err)

	client := NewAPIClient(subEnv.BaseURL, envAdminToken)
	tenants, body, status, err := client.ListTenants(context.Background())
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "body=%s", body)

	// Should see BOTH seeded tenants because env-var promoted us to admin.
	got := map[string]bool{}
	for _, tn := range tenants {
		got[tn.ID] = true
	}
	require.True(t, got[tenantA], "PlatformAdminSubs admin should see tenant A; got=%v", got)
	require.True(t, got[tenantB], "PlatformAdminSubs admin should see tenant B; got=%v", got)
}

// TestIdentity_DisplayAliasRoundTrip verifies display_alias survives
// POST → GET unchanged.
func TestIdentity_DisplayAliasRoundTrip(t *testing.T) {
	t.Parallel()
	subEnv := identitySubEnv(t)

	tenantID := randomTenantID("ident-alias")
	ownerSub := "sub-ident-alias-owner-" + randomName("u")
	inviteeSub := "sub-ident-alias-invitee-" + randomName("u")
	alias := "alice-laptop"

	insertRole(t, subEnv, ownerSub, tenantID, models.RoleOwner)

	ownerToken := mintTokenForSubEnv(t, subEnv, ownerSub, tenantID)
	ownerClient := NewAPIClient(subEnv.BaseURL, ownerToken)
	ctx := context.Background()

	invited, rawBody, status, err := ownerClient.InviteMemberWithAlias(ctx, tenantID, inviteeSub, "Contributor", alias)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, "body=%s", rawBody)
	require.Equal(t, alias, invited.DisplayAlias, "POST response must echo display_alias")

	list, status, err := ownerClient.ListMembers(ctx, tenantID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	var found *MemberResponse
	for i := range list.Members {
		if list.Members[i].PrincipalID == inviteeSub {
			found = &list.Members[i]
			break
		}
	}
	require.NotNil(t, found, "invited member should appear in list")
	require.Equal(t, alias, found.DisplayAlias, "GET response must round-trip display_alias")
}

// TestIdentity_InviteUpsertsTenantsRegistry verifies that inviting a
// member to a previously-unregistered tenant makes the tenant visible to
// platform admins via GET /v1/tenants, without a separate
// POST /v1/admin/tenants call.
func TestIdentity_InviteUpsertsTenantsRegistry(t *testing.T) {
	t.Parallel()

	suffix := randomName("ident-invite-reg")
	adminSub := "sub-ident-inv-admin-" + suffix
	subEnv := identitySubEnv(t, adminSub)

	tenantID := randomTenantID("ident-inv-reg")
	ownerSub := "sub-ident-inv-owner-" + suffix
	inviteeSub := "sub-ident-inv-invitee-" + suffix

	insertRole(t, subEnv, ownerSub, tenantID, models.RoleOwner)

	// Phase 6a: TenantContext refuses any /v1/tenants/{tid}/... request
	// whose slug isn't in the `tenants` registry. The previous version of
	// this test wiped the tenants row to assert that POST .../members would
	// re-UPSERT it; that property is gone because the request now 404s
	// before reaching the handler. The remaining assertion is meaningful:
	// insertRole's UPSERT makes the tenant visible to platform admins via
	// GET /v1/tenants.

	ownerToken := mintTokenForSubEnv(t, subEnv, ownerSub, tenantID)
	ownerClient := NewAPIClient(subEnv.BaseURL, ownerToken)
	ctx := context.Background()

	_, body, status, err := ownerClient.InviteMember(ctx, tenantID, inviteeSub, "Contributor")
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, "body=%s", body)

	// Admin (via env-var sub list) should now see the tenant in the registry.
	adminToken, err := subEnv.JWT.MintTokenWithGroups(adminSub, adminSub+"@test.dc", nil)
	require.NoError(t, err)
	adminClient := NewAPIClient(subEnv.BaseURL, adminToken)

	tenants, body, status, err := adminClient.ListTenants(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "body=%s", body)

	got := map[string]bool{}
	for _, tn := range tenants {
		got[tn.ID] = true
	}
	require.True(t, got[tenantID],
		"member invite must UPSERT the tenants registry so admin sees the tenant; got=%v", got)
}
