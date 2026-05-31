//go:build integration

package integration

// rbac_role_matrix_test.go — RBAC role × verb matrix integration tests.
//
// VNet's create/delete handlers route through the RBAC v2 action engine
// (requireAction), so these tests verify the v2 matrix on VNet:
//
//   viewer  (→ Reader)      — can GET/LIST, cannot CREATE or DELETE
//   member  (→ Contributor) — can GET/LIST + CREATE + DELETE
//   owner   (→ Owner)       — can do everything
//   admin   — platform-admin JWT bypasses all role checks
//
// NOTE (v2): member (Contributor) CAN now delete resources. The v1 model gated
// delete on owner; v2 grants delete to Contributor (Azure-Contributor parity).
//
// Each test spins up a fresh httptest.Server via newSubEnv with
// AutoProvisionMembers=false so the test controls exactly which role_assignment
// rows exist. Roles are inserted directly into the DB before the request is
// made — no round-trip through the autoprovision path.
//
// The resource under test is VNet (POST /v1/vnets, DELETE /v1/vnets/{id}, GET
// /v1/vnets). VNet is representative — the requireTenantRole helper is shared
// across all handlers, so testing one resource is sufficient.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/wso2/dc-api/internal/api/middleware"
	"github.com/wso2/dc-api/internal/models"
)

// mintTokenForSubEnv mints a JWT for the given sub + tenantID using the
// subEnv's JWT minter. The groups slice controls which dc-tenant-* groups the
// user belongs to.
func mintTokenForSubEnv(t *testing.T, subEnv *TestEnv, sub, tenantID string, extraGroups ...string) string {
	t.Helper()
	groups := append([]string{"dc-tenant-" + tenantID}, extraGroups...)
	token, err := subEnv.JWT.MintTokenWithGroups(sub, sub+"@test.dc", groups)
	require.NoError(t, err, "mintTokenForSubEnv")
	return token
}

// setupTenantProject ensures the tenant has a "default" project in the DB
// (used by RBAC tests that construct a client manually with NewAPIClientForTenant
// and then call resource methods that now require a project context). This is the
// same logic as ensureDefaultProject in fixtures.go but usable with any TestEnv.
func setupTenantProject(t *testing.T, subEnv *TestEnv, tenantID string) {
	t.Helper()
	ctx := context.Background()
	tenantUUID, err := subEnv.DB.GetTenantUUIDBySlug(ctx, tenantID)
	require.NoError(t, err, "setupTenantProject: GetTenantUUIDBySlug(%q)", tenantID)
	require.NotEqual(t, uuid.Nil, tenantUUID, "setupTenantProject: tenantUUID nil for %q (UpsertTenant must run first)", tenantID)

	_, _, _, err = subEnv.DB.CreateProject(ctx,
		models.Project{
			ID:         defaultProjectID,
			TenantID:   tenantID,
			TenantUUID: tenantUUID,
			Name:       "Default project",
			CPUCores:   20,
			MemoryGB:   64,
			StorageGB:  500,
			CreatedBy:  "test-rbac",
		},
		models.ProjectQuota{
			MaxVNets:    10,
			MaxClusters: 2,
			MaxVolumes:  50,
			MaxPublicIPs: 3,
		},
	)
	// Idempotent: a duplicate is fine (parallel tests may race to insert).
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		require.NoError(t, err, "setupTenantProject: CreateProject")
	}

	// Same as ensureDefaultProject: also create the K8s project namespace via
	// the provider so resource provisioner calls don't fail with
	// "namespace dc-<tenant>-<project> not found".
	if subEnv.NSProvisioner != nil {
		projectUUID, err := subEnv.DB.GetProjectUUIDByTenantAndSlug(ctx, tenantID, defaultProjectID)
		require.NoError(t, err, "setupTenantProject: GetProjectUUIDByTenantAndSlug")
		if err := subEnv.NSProvisioner.EnsureProjectNamespace(ctx, tenantID, defaultProjectID, projectUUID, 20, 64, 500, 50); err != nil {
			require.NoError(t, err, "setupTenantProject: EnsureProjectNamespace")
		}
	}
}

// insertRole inserts a role_assignment row directly into the DB.
// The test controls the role exactly — no autoprovision is involved.
// Also UPSERTs the tenants registry row so the admin path of
// GET /v1/tenants (which reads from the registry, not from
// role_assignments) sees the seeded tenant.
func insertRole(t *testing.T, subEnv *TestEnv, sub, tenantID string, role models.Role) {
	t.Helper()
	ctx := context.Background()
	// UPSERT the tenant FIRST so CreateRoleAssignment can resolve its scope_uuid
	// (the v2 action engine matches assignments by scope_uuid). This also mirrors
	// the tenant into the registry so admin-list tests see it.
	if _, err := subEnv.DB.UpsertTenant(ctx, tenantID, tenantID, "dc-tenant-"+tenantID, "test-rbac-matrix"); err != nil {
		require.NoError(t, err, "insertRole: UpsertTenant")
	}
	_, err := subEnv.DB.CreateRoleAssignment(ctx, models.RoleAssignment{
		ID:            uuid.New(),
		PrincipalType: models.PrincipalTypeUser,
		PrincipalID:   sub,
		ScopeType:     models.ScopeTypeTenant,
		ScopeID:       tenantID,
		Role:          role,
		GrantedBy:     "test-rbac-matrix",
	})
	// Ignore duplicate-key errors (idempotent).
	if err != nil && !strings.Contains(err.Error(), "23505") && !strings.Contains(err.Error(), "duplicate key") {
		require.NoError(t, err, "insertRole")
	}
}

// rbacSubEnv creates a sub-environment with AutoProvisionMembers=false so
// tests have full control over which role_assignment rows exist.
func rbacSubEnv(t *testing.T) *TestEnv {
	t.Helper()
	return newSubEnv(t, middleware.AuthConfig{
		TenantGroupPrefix:    "dc-tenant-",
		AdminGroup:           "dc-admin",
		AutoProvisionMembers: false,
	})
}

// TestRBAC_Viewer_CanRead_CannotMutate verifies that a user with only the
// viewer role can list VNets but cannot create or delete them.
func TestRBAC_Viewer_CanRead_CannotMutate(t *testing.T) {
	t.Parallel()
	subEnv := rbacSubEnv(t)

	tenantID := "test-tenant-rbac-viewer"
	userSub := "sub-rbac-viewer-" + randomName("u")

	insertRole(t, subEnv, userSub, tenantID, models.RoleViewer)
	setupTenantProject(t, subEnv, tenantID)

	token := mintTokenForSubEnv(t, subEnv, userSub, tenantID)
	// Bind the client to the tenant + project so resource calls route through
	// /v1/tenants/{tenantID}/projects/default/... (required by the M2.5 URL structure).
	client := NewAPIClientForProject(subEnv.BaseURL, token, tenantID, defaultProjectID)
	ctx := context.Background()

	// ── GET /v1/tenants/{tenantID}/vnets must succeed (read is open to any authenticated member) ──

	_, listStatus, err := client.ListVNets(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listStatus, "viewer must be able to list VNets")

	// ── POST /v1/tenants/{tenantID}/vnets must return 403 ────────────────────

	_, _, createStatus, err := client.CreateVNet(ctx, CreateVNetRequest{
		Name:         randomName("vnet"),
		AddressSpace: []string{"10.100.0.0/16"},
		Region:       "lk",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, createStatus,
		"viewer must receive 403 on POST /v1/tenants/{tenantID}/vnets")

	// ── DELETE /v1/tenants/{tenantID}/vnets/{id} must return 403 ─────────────
	// Use a made-up UUID — the RBAC check fires before the resource lookup,
	// so we never reach the 404 path.

	_, deleteStatus, err := client.DeleteVNet(ctx, uuid.New().String())
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, deleteStatus,
		"viewer must receive 403 on DELETE /v1/tenants/{tenantID}/vnets/{id}")
}

// TestRBAC_Member_CanCreateAndDelete verifies that a user with the member role
// (→ Contributor in v2) can list, create, AND delete VNets. Under v1 member
// could not delete; v2 grants delete to Contributor.
func TestRBAC_Member_CanCreateAndDelete(t *testing.T) {
	t.Parallel()
	subEnv := rbacSubEnv(t)

	tenantID := "test-tenant-rbac-member"
	userSub := "sub-rbac-member-" + randomName("u")

	insertRole(t, subEnv, userSub, tenantID, models.RoleMember)
	setupTenantProject(t, subEnv, tenantID)

	token := mintTokenForSubEnv(t, subEnv, userSub, tenantID)
	client := NewAPIClientForProject(subEnv.BaseURL, token, tenantID, defaultProjectID)
	ctx := context.Background()

	// ── GET /v1/tenants/{tenantID}/vnets must succeed ─────────────────────────

	_, listStatus, err := client.ListVNets(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listStatus, "member must be able to list VNets")

	// ── POST /v1/tenants/{tenantID}/vnets must succeed (202 Accepted) ─────────
	// The nopComputeProvider + nopNetworkProvider make this safe: provisioning
	// fails asynchronously without any real cluster call. We only care that the
	// handler accepts the request and returns 202.

	createResp, _, createStatus, err := client.CreateVNet(ctx, CreateVNetRequest{
		Name:         randomName("vnet"),
		AddressSpace: []string{"10.101.0.0/16"},
		Region:       "lk",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, createStatus,
		"member must receive 202 on POST /v1/tenants/{tenantID}/vnets")

	vnetID := createResp.Resource.ID

	// ── DELETE /v1/tenants/{tenantID}/vnets/{id} must return 202 (v2 change) ──
	// The VNet was just created (status PENDING, empty BackendUID) so the handler
	// removes it directly without waiting for KubeOVN.

	_, deleteStatus, err := client.DeleteVNet(ctx, vnetID)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, deleteStatus,
		"member (Contributor) must receive 202 on DELETE under RBAC v2")
}

// TestRBAC_Owner_CanDoEverything verifies that a user with the owner role can
// list, create, and delete VNets.
func TestRBAC_Owner_CanDoEverything(t *testing.T) {
	t.Parallel()
	subEnv := rbacSubEnv(t)

	tenantID := "test-tenant-rbac-owner"
	userSub := "sub-rbac-owner-" + randomName("u")

	insertRole(t, subEnv, userSub, tenantID, models.RoleOwner)
	setupTenantProject(t, subEnv, tenantID)

	token := mintTokenForSubEnv(t, subEnv, userSub, tenantID)
	client := NewAPIClientForProject(subEnv.BaseURL, token, tenantID, defaultProjectID)
	ctx := context.Background()

	// ── GET /v1/tenants/{tenantID}/vnets must succeed ─────────────────────────

	_, listStatus, err := client.ListVNets(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listStatus, "owner must be able to list VNets")

	// ── POST /v1/tenants/{tenantID}/vnets must return 202 ────────────────────

	createResp, _, createStatus, err := client.CreateVNet(ctx, CreateVNetRequest{
		Name:         randomName("vnet"),
		AddressSpace: []string{"10.102.0.0/16"},
		Region:       "lk",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, createStatus,
		"owner must receive 202 on POST /v1/tenants/{tenantID}/vnets")

	vnetID := createResp.Resource.ID

	// ── DELETE /v1/tenants/{tenantID}/vnets/{id} must return 202 ─────────────
	// The VNet was just created (status PENDING) so the handler removes it
	// without waiting for KubeOVN (BackendUID is empty → direct DB delete).

	_, deleteStatus, err := client.DeleteVNet(ctx, vnetID)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, deleteStatus,
		"owner must receive 202 on DELETE /v1/tenants/{tenantID}/vnets/{id}")
}

// TestRBAC_AdminBypass_NoRoleNeeded verifies that a platform admin (dc-admin
// group) can perform all operations even with no role_assignment row.
//
// Admin tokens have the "dc-admin" group. The middleware maps this to
// tenantID="admin" and sets isAdmin=true. The test also adds a "dc-tenant-<x>"
// group so the handler receives a real tenantID (not "admin") — this mirrors
// how production admins act on behalf of a tenant.
func TestRBAC_AdminBypass_NoRoleNeeded(t *testing.T) {
	t.Parallel()
	subEnv := rbacSubEnv(t)

	// Tenant the admin will operate in. No role_assignment row is inserted —
	// the admin bypass must skip the membership check. Phase 6a still requires
	// the tenant to exist in the registry (admins act *on* registered tenants,
	// not on guess-typed slugs), so we UPSERT the row.
	tenantID := "test-tenant-rbac-admin-bypass"
	adminSub := "sub-rbac-admin-" + randomName("u")
	if _, err := subEnv.DB.UpsertTenant(context.Background(), tenantID, tenantID, "dc-tenant-"+tenantID, "test-admin-bypass"); err != nil {
		require.NoError(t, err, "UpsertTenant")
	}
	setupTenantProject(t, subEnv, tenantID)

	// Mint a token with BOTH dc-admin AND dc-tenant-<x>. With the new URL-driven
	// tenant model, the admin's JWT doesn't need to select a tenant — the URL
	// tenant_id does. isAdmin=true allows access to any tenant in the URL.
	// The dc-tenant group is included here so the test verifies the admin can
	// act on behalf of a specific tenant without a role_assignments row.
	groups := []string{"dc-admin", "dc-tenant-" + tenantID}
	token, err := subEnv.JWT.MintTokenWithGroups(adminSub, adminSub+"@test.dc", groups)
	require.NoError(t, err)

	// Bind the client to the target tenant + project. The TenantContext middleware
	// sees isAdmin=true and grants access without checking role_assignments.
	client := NewAPIClientForProject(subEnv.BaseURL, token, tenantID, defaultProjectID)
	ctx := context.Background()

	// ── All operations must succeed for admin ─────────────────────────────────

	// GET (list) — read operations are always open, but admin must not be blocked.
	_, listStatus, err := client.ListVNets(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listStatus,
		"admin must be able to list VNets without any role_assignment row")

	// POST — admin bypasses member check.
	createResp, _, createStatus, err := client.CreateVNet(ctx, CreateVNetRequest{
		Name:         randomName("vnet"),
		AddressSpace: []string{"10.103.0.0/16"},
		Region:       "lk",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, createStatus,
		"admin must receive 202 on POST /v1/tenants/{tenantID}/vnets without any role_assignment row")

	vnetID := createResp.Resource.ID

	// DELETE — admin bypasses owner check.
	_, deleteStatus, err := client.DeleteVNet(ctx, vnetID)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, deleteStatus,
		"admin must receive 202 on DELETE /v1/tenants/{tenantID}/vnets/{id} without any role_assignment row")
}
