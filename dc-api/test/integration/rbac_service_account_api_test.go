//go:build integration

// rbac_service_account_api_test.go — M1.5 Chunk 7 integration tests.
//
// These tests exercise the CRUD API layer for service accounts introduced in
// Chunk 7 (/v1/tenants/{tenant_id}/service-accounts). They go through the HTTP
// handler — unlike the Chunk 5 tests (rbac_service_account_test.go) which
// insert rows via raw SQL to test the auth path in isolation.
//
// The existing clientForServiceAccount helper in rbac_service_account_test.go
// is NOT changed by this file; it continues to use raw SQL for auth-only tests.
package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wso2/dc-api/internal/models"
)

// ── Helpers ───────────────────────────────────────────────────────────────────

// ownerClientForSATests returns an *APIClient authenticated as an owner of the
// given tenantID, with an owner role_assignment row already in the DB.
//
// MintToken(tenantID, ...) sets JWT sub = tenantID (first param). The auth
// middleware extracts the tenantID from the "dc-tenant-<id>" group claim.
// So both the tenant scope and the principal_id in role_assignments are
// tenantID. mustGrantOwner(t, tenantID, tenantID) sets exactly that.
func ownerClientForSATests(t *testing.T, tenantID string) *APIClient {
	t.Helper()
	if !strings.HasPrefix(tenantID, "test-") {
		tenantID = "test-" + tenantID
	}
	token, err := env.JWT.MintToken(tenantID, tenantID+"-email")
	require.NoError(t, err, "mint JWT for tenant %s", tenantID)
	// JWT sub == tenantID; the group "dc-tenant-<tenantID>" lets the middleware
	// extract tenantID from the JWT. The principal_id in role_assignments must
	// also be tenantID (the sub).
	mustGrantOwner(t, tenantID, tenantID)
	// Ensure the default project exists — SA CRUD is now under
	// /v1/tenants/{tid}/projects/{pid}/service-accounts (M2.5 project scoping).
	ensureDefaultProject(t, tenantID)
	// Bind to the tenant AND default project so SA calls route to the correct URL.
	return NewAPIClientForProject(env.BaseURL, token, tenantID, defaultProjectID)
}

// memberClientForSATests returns an *APIClient authenticated as a member of
// the given tenantID.
//
// We use a distinct sub (memberSub) so this principal is different from the
// owner. The OIDC group "dc-tenant-<tenantID>" maps to the same tenant scope
// so the middleware correctly resolves the tenantID for both principals.
func memberClientForSATests(t *testing.T, tenantID string) *APIClient {
	t.Helper()
	// memberSub is the JWT sub — a distinct string from the owner's sub.
	memberSub := tenantID + "-member"
	// MintTokenWithGroups: sub=memberSub, groups contains the tenant group so
	// the middleware routes this request to tenantID.
	token, err := env.JWT.MintTokenWithGroups(memberSub, memberSub+"-email",
		[]string{"dc-tenant-" + tenantID})
	require.NoError(t, err, "mint JWT for member %s", memberSub)
	// Phase 6a: TenantContext needs a tenants row to resolve the slug.
	// memberClientForSATests is sometimes called without a prior owner setup,
	// so UPSERT defensively here.
	ctx := context.Background()
	if _, err := env.DB.UpsertTenant(ctx, tenantID, tenantID, "dc-tenant-"+tenantID, "test-setup-sa-member"); err != nil {
		require.NoError(t, err, "UpsertTenant for %s", tenantID)
	}
	// Manually insert the member role — autoprovision would do this on first
	// request, but we need it seeded before the test call.
	_, _ = env.DB.CreateRoleAssignment(ctx, models.RoleAssignment{
		PrincipalType: models.PrincipalTypeUser,
		PrincipalID:   memberSub,
		ScopeType:     models.ScopeTypeTenant,
		ScopeID:       tenantID,
		Role:          models.RoleMember,
		GrantedBy:     "test-setup",
	})
	// Ensure the default project exists — SA CRUD is now under
	// /v1/tenants/{tid}/projects/{pid}/service-accounts (M2.5 project scoping).
	ensureDefaultProject(t, tenantID)
	// Bind to the tenant AND default project so SA calls route to the correct URL.
	return NewAPIClientForProject(env.BaseURL, token, tenantID, defaultProjectID)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestServiceAccountAPI_OwnerCanCreate_GetTokenOnce verifies:
//   - Owner can POST → 201 with a valid raw token in the response.
//   - Subsequent GET on the same SA → response has no "token" field.
//   - The captured token can authenticate a real API call (GET /v1/vnets → 200).
func TestServiceAccountAPI_OwnerCanCreate_GetTokenOnce(t *testing.T) {
	t.Parallel()
	tenantID := randomTenantID("sa-api-create")
	owner := ownerClientForSATests(t, tenantID)
	ctx := context.Background()

	// POST → 201.
	createResp, rawBody, status, err := owner.CreateServiceAccount(ctx, tenantID,
		"ci-deploy", "member", "GitHub Actions deploy")
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status,
		"owner POST /service-accounts must return 201; body: %s", rawBody)

	require.NotEmpty(t, createResp.ID, "created SA must have an ID")
	require.Equal(t, "ci-deploy", createResp.Name)
	require.Equal(t, "member", createResp.Role)
	require.NotEmpty(t, createResp.Token, "token must be present in create response")
	require.True(t, strings.HasPrefix(createResp.Token, "dcapi_sa_"),
		"token must have dcapi_sa_ prefix; got %s", createResp.Token)

	// GET on the same SA must NOT include the token field.
	getResp, rawGetBody, getStatus, err := owner.GetServiceAccount(ctx, tenantID, createResp.ID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, getStatus,
		"GET /service-accounts/{id} must return 200; body: %s", rawGetBody)
	require.Equal(t, createResp.ID, getResp.ID)

	// Verify the raw JSON has no "token" or "token_hash" key.
	var rawMap map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rawGetBody, &rawMap))
	_, hasToken := rawMap["token"]
	require.False(t, hasToken, "GET response must not include 'token' field")
	_, hasHash := rawMap["token_hash"]
	require.False(t, hasHash, "GET response must not include 'token_hash' field")

	// Use the captured token to make an authenticated request.
	// cap-usage is tenant-scoped (no project required) and proves the token works.
	saClient := NewAPIClientForTenant(env.BaseURL, createResp.Token, tenantID)
	_, _, capStatus, capErr := saClient.GetTenantCapUsage(ctx)
	require.NoError(t, capErr)
	require.Equal(t, http.StatusOK, capStatus,
		"SA token from create response must authenticate successfully against GET /v1/tenants/{tenantID}/cap-usage")
}

// TestServiceAccountAPI_MemberCannotCreate verifies that a member (non-owner)
// receives 403 when attempting to create a service account.
func TestServiceAccountAPI_MemberCannotCreate(t *testing.T) {
	t.Parallel()
	tenantID := randomTenantID("sa-api-mbr-create")
	member := memberClientForSATests(t, tenantID)
	ctx := context.Background()

	_, _, status, err := member.CreateServiceAccount(ctx, tenantID,
		"ci-deploy", "member", "")
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, status,
		"member POST /service-accounts must return 403")
}

// TestServiceAccountAPI_OwnerCanList_ExcludesTokenFields verifies:
//   - Owner creates 2 SAs.
//   - GET list → 2 items, no "token" or "token_hash" in any item.
func TestServiceAccountAPI_OwnerCanList_ExcludesTokenFields(t *testing.T) {
	t.Parallel()
	tenantID := randomTenantID("sa-api-list")
	owner := ownerClientForSATests(t, tenantID)
	ctx := context.Background()

	_, _, s1, err := owner.CreateServiceAccount(ctx, tenantID, "sa-one", "member", "first")
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, s1)

	_, _, s2, err := owner.CreateServiceAccount(ctx, tenantID, "sa-two", "viewer", "second")
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, s2)

	listResp, rawBody, listStatus, err := owner.ListServiceAccounts(ctx, tenantID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listStatus,
		"GET /service-accounts must return 200; body: %s", rawBody)
	require.Len(t, listResp.ServiceAccounts, 2,
		"list must contain exactly 2 SAs; body: %s", rawBody)

	// Parse the raw body to inspect per-item fields.
	var envelope struct {
		ServiceAccounts []map[string]json.RawMessage `json:"service_accounts"`
	}
	require.NoError(t, json.Unmarshal(rawBody, &envelope))
	for i, item := range envelope.ServiceAccounts {
		_, hasToken := item["token"]
		require.False(t, hasToken, "item %d must not have 'token' field", i)
		_, hasHash := item["token_hash"]
		require.False(t, hasHash, "item %d must not have 'token_hash' field", i)
	}
}

// TestServiceAccountAPI_DuplicateNameReturnsConflict verifies that creating
// two SAs with the same name in the same tenant returns 409.
func TestServiceAccountAPI_DuplicateNameReturnsConflict(t *testing.T) {
	t.Parallel()
	tenantID := randomTenantID("sa-api-dup")
	owner := ownerClientForSATests(t, tenantID)
	ctx := context.Background()

	_, _, s1, err := owner.CreateServiceAccount(ctx, tenantID, "ci-deploy", "member", "")
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, s1)

	_, rawBody, s2, err := owner.CreateServiceAccount(ctx, tenantID, "ci-deploy", "member", "")
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, s2,
		"duplicate name must return 409; body: %s", rawBody)
}

// TestServiceAccountAPI_OwnerCanDelete_TokenStopsWorking verifies:
//   - Owner creates a SA, captures the token, verifies it works.
//   - Owner deletes the SA via API → 204.
//   - The captured token no longer authenticates → 401.
func TestServiceAccountAPI_OwnerCanDelete_TokenStopsWorking(t *testing.T) {
	t.Parallel()
	tenantID := randomTenantID("sa-api-delete")
	owner := ownerClientForSATests(t, tenantID)
	ctx := context.Background()

	createResp, _, status, err := owner.CreateServiceAccount(ctx, tenantID,
		"ephemeral-ci", "member", "")
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status)

	capturedToken := createResp.Token
	// Use the tenant-scoped cap-usage endpoint as an auth probe — no project
	// context required, so the test doesn't need to seed a project for the SA.
	saClient := NewAPIClientForTenant(env.BaseURL, capturedToken, tenantID)

	// Verify token works.
	_, _, workStatus, workErr := saClient.GetTenantCapUsage(ctx)
	require.NoError(t, workErr)
	require.Equal(t, http.StatusOK, workStatus, "SA token must work before deletion")

	// Delete the SA.
	_, deleteStatus, deleteErr := owner.DeleteServiceAccount(ctx, tenantID, createResp.ID)
	require.NoError(t, deleteErr)
	require.Equal(t, http.StatusNoContent, deleteStatus,
		"DELETE /service-accounts/{id} must return 204")

	// Token must no longer work.
	_, _, revokedStatus, revokedErr := saClient.GetTenantCapUsage(ctx)
	require.NoError(t, revokedErr)
	require.Equal(t, http.StatusUnauthorized, revokedStatus,
		"SA token must be rejected (401) after deletion")
}

// TestServiceAccountAPI_CrossTenantOpsReturn404 verifies that an owner of
// tenant A cannot POST / GET / DELETE service accounts on tenant B's namespace.
func TestServiceAccountAPI_CrossTenantOpsReturn404(t *testing.T) {
	t.Parallel()
	tenantA := randomTenantID("sa-api-cross-a")
	tenantB := randomTenantID("sa-api-cross-b")

	ownerA := ownerClientForSATests(t, tenantA)
	ownerB := ownerClientForSATests(t, tenantB)
	ctx := context.Background()

	// Owner B creates an SA in tenant B.
	createResp, _, createStatus, err := ownerB.CreateServiceAccount(ctx, tenantB,
		"b-sa", "member", "")
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, createStatus)

	// Owner A tries to POST on tenant B → 404.
	_, _, crossPostStatus, _ := ownerA.CreateServiceAccount(ctx, tenantB, "hax", "member", "")
	require.Equal(t, http.StatusNotFound, crossPostStatus,
		"cross-tenant POST must return 404")

	// Owner A tries to GET on tenant B → 404.
	_, _, crossGetStatus, _ := ownerA.GetServiceAccount(ctx, tenantB, createResp.ID)
	require.Equal(t, http.StatusNotFound, crossGetStatus,
		"cross-tenant GET must return 404")

	// Owner A tries to LIST on tenant B → 404.
	_, _, crossListStatus, _ := ownerA.ListServiceAccounts(ctx, tenantB)
	require.Equal(t, http.StatusNotFound, crossListStatus,
		"cross-tenant LIST must return 404")

	// Owner A tries to DELETE on tenant B → 404.
	_, crossDeleteStatus, _ := ownerA.DeleteServiceAccount(ctx, tenantB, createResp.ID)
	require.Equal(t, http.StatusNotFound, crossDeleteStatus,
		"cross-tenant DELETE must return 404")
}

// TestServiceAccountAPI_DeleteRemovesRoleAssignments verifies that deleting an
// SA via the API also removes the corresponding role_assignments rows — i.e.,
// the transactional delete is exercised correctly.
func TestServiceAccountAPI_DeleteRemovesRoleAssignments(t *testing.T) {
	t.Parallel()
	tenantID := randomTenantID("sa-api-del-ra")
	owner := ownerClientForSATests(t, tenantID)
	ctx := context.Background()

	createResp, _, createStatus, err := owner.CreateServiceAccount(ctx, tenantID,
		"ra-test-sa", "viewer", "")
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, createStatus)

	saID := createResp.ID

	// Confirm a role_assignments row exists for this SA. Under M2.5 SAs are
	// project-scoped (created via /v1/tenants/{tid}/projects/{pid}/service-accounts),
	// so the row is at scope_type='project' with scope_id=<project-slug>.
	ras, err := env.DB.ListRoleAssignmentsForPrincipal(ctx,
		models.PrincipalTypeServiceAccount, saID)
	require.NoError(t, err)
	require.NotEmpty(t, ras, "role_assignments must exist after SA creation")
	found := false
	for _, ra := range ras {
		if ra.ScopeType == models.ScopeTypeProject && ra.ScopeID == defaultProjectID {
			found = true
			break
		}
	}
	require.True(t, found, "role_assignments must contain a project-scoped row for the SA (scope_type=project, scope_id=%q)", defaultProjectID)

	// Delete the SA via the API.
	_, deleteStatus, deleteErr := owner.DeleteServiceAccount(ctx, tenantID, saID)
	require.NoError(t, deleteErr)
	require.Equal(t, http.StatusNoContent, deleteStatus)

	// role_assignments rows must be gone.
	rasAfter, err := env.DB.ListRoleAssignmentsForPrincipal(ctx,
		models.PrincipalTypeServiceAccount, saID)
	require.NoError(t, err)
	require.Empty(t, rasAfter,
		"role_assignments for deleted SA must be removed by the transactional delete")
}
