//go:build integration

// rbac_members_test.go — M1.5 Chunk 6 integration tests.
//
// These tests exercise the /v1/tenants/{tenant_id}/members endpoints:
//
//	POST   — invite a human user to a tenant (owner only)
//	GET    — list tenant members, excluding service accounts
//	DELETE — remove a member (owner only; last-owner guard enforced)
//
// Test strategy:
//   - Each test creates its own subEnv with AutoProvisionMembers=false for
//     deterministic role control. Roles are inserted directly via insertRole.
//   - Each test uses distinct tenant IDs to avoid cross-test interference with
//     the shared database.
//   - The test helpers insertRole, mintTokenForSubEnv, and rbacSubEnv are
//     defined in rbac_role_matrix_test.go and reused here.
package integration

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wso2/dc-api/internal/models"
)

// TestMembers_OwnerCanInviteAndList verifies that an owner can invite a new
// member via POST and that both the owner and the invited member appear in
// the GET response with principal_type="user".
func TestMembers_OwnerCanInviteAndList(t *testing.T) {
	t.Parallel()
	subEnv := rbacSubEnv(t)

	tenantID := randomTenantID("members-invite")
	ownerSub := "sub-owner-invite-" + randomName("u")
	inviteeSub := "sub-invitee-" + randomName("u")

	insertRole(t, subEnv, ownerSub, tenantID, models.RoleOwner)

	ownerToken := mintTokenForSubEnv(t, subEnv, ownerSub, tenantID)
	ownerClient := NewAPIClient(subEnv.BaseURL, ownerToken)
	ctx := context.Background()

	// ── POST → 201 ───────────────────────────────────────────────────────────

	invited, rawBody, status, err := ownerClient.InviteMember(ctx, tenantID, inviteeSub, "member")
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status,
		"owner must receive 201 on POST /v1/tenants/{id}/members; body=%s", rawBody)
	require.Equal(t, "user", invited.PrincipalType)
	require.Equal(t, inviteeSub, invited.PrincipalID)
	require.Equal(t, "member", invited.Role)
	require.NotEmpty(t, invited.ID, "invited member response must include the role_assignment UUID")
	require.Equal(t, ownerSub, invited.GrantedBy)

	// ── GET → 2 members, all principal_type=user ──────────────────────────────

	listResp, listStatus, err := ownerClient.ListMembers(ctx, tenantID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listStatus,
		"owner must receive 200 on GET /v1/tenants/{id}/members")
	require.Len(t, listResp.Members, 2,
		"tenant must have 2 members (owner + invited); got %v", listResp.Members)

	for _, m := range listResp.Members {
		require.Equal(t, "user", m.PrincipalType,
			"all items in member list must have principal_type=user")
	}

	// Confirm both principals are present.
	principals := make(map[string]string) // principal_id → role
	for _, m := range listResp.Members {
		principals[m.PrincipalID] = m.Role
	}
	require.Contains(t, principals, ownerSub,
		"owner must appear in member list")
	require.Contains(t, principals, inviteeSub,
		"invited member must appear in member list")
	require.Equal(t, "owner", principals[ownerSub])
	require.Equal(t, "member", principals[inviteeSub])
}

// TestMembers_MemberCannotInvite verifies that a user with only the member role
// receives HTTP 403 when attempting POST /v1/tenants/{id}/members.
func TestMembers_MemberCannotInvite(t *testing.T) {
	t.Parallel()
	subEnv := rbacSubEnv(t)

	tenantID := randomTenantID("members-noinvite")
	memberSub := "sub-member-noinvite-" + randomName("u")
	someUserSub := "sub-target-" + randomName("u")

	insertRole(t, subEnv, memberSub, tenantID, models.RoleMember)

	memberToken := mintTokenForSubEnv(t, subEnv, memberSub, tenantID)
	memberClient := NewAPIClient(subEnv.BaseURL, memberToken)
	ctx := context.Background()

	_, rawBody, status, err := memberClient.InviteMember(ctx, tenantID, someUserSub, "member")
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, status,
		"member must receive 403 on POST /v1/tenants/{id}/members; body=%s", rawBody)
}

// TestMembers_OwnerCanRemoveOtherMember verifies the full invite → list → remove
// lifecycle. After the owner removes the invited member, the member list must
// contain only the owner.
func TestMembers_OwnerCanRemoveOtherMember(t *testing.T) {
	t.Parallel()
	subEnv := rbacSubEnv(t)

	tenantID := randomTenantID("members-remove")
	ownerSub := "sub-owner-remove-" + randomName("u")
	inviteeSub := "sub-invitee-remove-" + randomName("u")

	insertRole(t, subEnv, ownerSub, tenantID, models.RoleOwner)

	ownerToken := mintTokenForSubEnv(t, subEnv, ownerSub, tenantID)
	ownerClient := NewAPIClient(subEnv.BaseURL, ownerToken)
	ctx := context.Background()

	// Invite the member first.
	_, _, status, err := ownerClient.InviteMember(ctx, tenantID, inviteeSub, "member")
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, "invite must succeed")

	// Remove the invited member.
	_, removeStatus, err := ownerClient.RemoveMember(ctx, tenantID, inviteeSub)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, removeStatus,
		"owner must receive 204 on DELETE /v1/tenants/{id}/members/{principal_id}")

	// Verify only the owner remains.
	listResp, listStatus, err := ownerClient.ListMembers(ctx, tenantID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listStatus)
	require.Len(t, listResp.Members, 1,
		"after removal only the owner should remain; got %v", listResp.Members)
	require.Equal(t, ownerSub, listResp.Members[0].PrincipalID,
		"remaining member must be the owner")
}

// TestMembers_CannotRemoveLastOwner verifies that the last owner of a tenant
// cannot remove themselves. The handler must return 409 with a clear message.
func TestMembers_CannotRemoveLastOwner(t *testing.T) {
	t.Parallel()
	subEnv := rbacSubEnv(t)

	tenantID := randomTenantID("members-lastowner")
	ownerSub := "sub-owner-last-" + randomName("u")

	insertRole(t, subEnv, ownerSub, tenantID, models.RoleOwner)

	ownerToken := mintTokenForSubEnv(t, subEnv, ownerSub, tenantID)
	ownerClient := NewAPIClient(subEnv.BaseURL, ownerToken)
	ctx := context.Background()

	// Try to remove self (the only owner).
	rawBody, removeStatus, err := ownerClient.RemoveMember(ctx, tenantID, ownerSub)
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, removeStatus,
		"removing the last owner must return 409; body=%s", rawBody)

	// Confirm the error message is meaningful.
	errMsg := ErrorBody(rawBody)
	require.True(t,
		strings.Contains(strings.ToLower(errMsg), "last owner") ||
			strings.Contains(strings.ToLower(errMsg), "last"),
		"409 body must mention 'last owner'; got: %s", errMsg)
}

// TestMembers_InviteDuplicateReturnsConflict verifies that inviting the same
// user with the same role a second time returns 409 Conflict.
func TestMembers_InviteDuplicateReturnsConflict(t *testing.T) {
	t.Parallel()
	subEnv := rbacSubEnv(t)

	tenantID := randomTenantID("members-dup")
	ownerSub := "sub-owner-dup-" + randomName("u")
	inviteeSub := "sub-invitee-dup-" + randomName("u")

	insertRole(t, subEnv, ownerSub, tenantID, models.RoleOwner)

	ownerToken := mintTokenForSubEnv(t, subEnv, ownerSub, tenantID)
	ownerClient := NewAPIClient(subEnv.BaseURL, ownerToken)
	ctx := context.Background()

	// First invite → 201.
	_, rawBody, status, err := ownerClient.InviteMember(ctx, tenantID, inviteeSub, "member")
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status,
		"first invite must return 201; body=%s", rawBody)

	// Second identical invite → 409.
	_, rawBody2, status2, err := ownerClient.InviteMember(ctx, tenantID, inviteeSub, "member")
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, status2,
		"duplicate invite must return 409; body=%s", rawBody2)
}

// TestMembers_CrossTenantOpsReturn404 verifies that a caller with a valid JWT
// for tenant A cannot operate on tenant B's member list. The endpoint must
// return 404 to avoid revealing whether tenant B exists.
func TestMembers_CrossTenantOpsReturn404(t *testing.T) {
	t.Parallel()
	subEnv := rbacSubEnv(t)

	tenantA := randomTenantID("xcomp-a")
	tenantB := randomTenantID("xcomp-b")
	ownerSub := "sub-owner-xcomp-" + randomName("u")

	insertRole(t, subEnv, ownerSub, tenantA, models.RoleOwner)

	// Mint a token that belongs to tenantA only.
	ownerToken := mintTokenForSubEnv(t, subEnv, ownerSub, tenantA)
	ownerClient := NewAPIClient(subEnv.BaseURL, ownerToken)
	ctx := context.Background()

	// POST to tenantB → 404.
	_, rawBody, status, err := ownerClient.InviteMember(ctx, tenantB, "someone@example.com", "member")
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status,
		"cross-tenant POST must return 404; body=%s", rawBody)

	// GET to tenantB → 404.
	_, getStatus, err := ownerClient.ListMembers(ctx, tenantB)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, getStatus,
		"cross-tenant GET must return 404")

	// DELETE to tenantB → 404.
	_, delStatus, err := ownerClient.RemoveMember(ctx, tenantB, "someone@example.com")
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, delStatus,
		"cross-tenant DELETE must return 404")
}

// TestMembers_ListExcludesServiceAccounts verifies that GET /members does NOT
// return role_assignments rows with principal_type='service_account'. Service
// accounts are intentionally excluded to keep the member list human-only.
func TestMembers_ListExcludesServiceAccounts(t *testing.T) {
	t.Parallel()
	subEnv := rbacSubEnv(t)

	tenantID := randomTenantID("members-nosa")
	ownerSub := "sub-owner-nosa-" + randomName("u")

	insertRole(t, subEnv, ownerSub, tenantID, models.RoleOwner)

	// Insert a service account role_assignment directly, bypassing the HTTP layer
	// (Chunk 7 doesn't exist yet). The members list endpoint must NOT return it.
	ctx := context.Background()
	fakeSAID := fmt.Sprintf("sa-%s", randomName("sa"))
	_, err := subEnv.DB.Pool().Exec(ctx, `
		INSERT INTO role_assignments
			(principal_type, principal_id, scope_type, scope_id, role_definition, granted_by)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		string(models.PrincipalTypeServiceAccount),
		fakeSAID,
		string(models.ScopeTypeTenant),
		tenantID,
		models.RoleDefinitionForRole(models.RoleMember),
		"test-setup",
	)
	require.NoError(t, err, "insert service_account role_assignment")

	ownerToken := mintTokenForSubEnv(t, subEnv, ownerSub, tenantID)
	ownerClient := NewAPIClient(subEnv.BaseURL, ownerToken)

	listResp, listStatus, err := ownerClient.ListMembers(ctx, tenantID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listStatus)

	for _, m := range listResp.Members {
		require.Equal(t, "user", m.PrincipalType,
			"member list must not include service accounts; found %v", m)
	}

	// Confirm exactly one member (the owner — the SA row is excluded).
	require.Len(t, listResp.Members, 1,
		"member list must contain only the owner (1 user); SA row must be excluded")
	require.Equal(t, ownerSub, listResp.Members[0].PrincipalID)
}
