//go:build integration

package integration

// TestPhase6a_SlugRecycle verifies that the UUID-keyed tenant isolation
// introduced in Phase 6a correctly prevents a re-registered slug from
// inheriting orphan rows left by the original (now-deleted) tenant.
//
// Scenario:
//  1. Mint a JWT for slug "test-slug-recycle" (Tenant A).
//  2. Tenant A calls POST /v1/vnets — creates VNet row with tenant_uuid=UA.
//  3. Delete Tenant A's role_assignments + tenants row (simulating a tenant
//     being removed from the platform).
//  4. Re-register the same slug with a NEW uuid (Tenant B):
//     INSERT INTO tenants (slug, tenant_uuid) — different UUID.
//  5. Mint a JWT for Tenant B (same slug, different UUID).
//  6. Tenant B calls GET /v1/vnets — must return an empty list (not Tenant A's VNet).
//  7. Tenant B calls GET /v1/vnets/{A's-vnet-id} — must return 404.
//  8. Cleanup: delete the synthetic tenants row and both VNet rows.
//
// This test does NOT exercise the live KubeOVN VPC lifecycle — it bypasses
// the network provider entirely by seeding rows directly via the DB repo.
// The guard we're testing is in the SQL WHERE clause (tenant_uuid = $X),
// not in the provider layer.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/wso2/dc-api/internal/models"
)

func TestPhase6a_SlugRecycle(t *testing.T) {
	// Not parallel: manipulates the tenants table by slug, which could conflict
	// with other tests using the same slug in a parallel run.
	ctx := context.Background()
	slug := "test-slug-recycle-6a"
	projectSlug := "default"

	// ── Step 1: Register Tenant A and mint a JWT ─────────────────────────────

	// Insert Tenant A's tenants row with a fresh UUID. The tenants table is
	// keyed by `id` (the slug) and carries `tenant_uuid` as the immutable
	// identity Phase 6a relies on.
	uuidA := uuid.New()
	_, err := env.DB.Pool().Exec(ctx,
		`INSERT INTO tenants (id, tenant_uuid, name, created_by)
		 VALUES ($1, $2, $3, 'phase-6a-test')
		 ON CONFLICT (id) DO UPDATE SET tenant_uuid = EXCLUDED.tenant_uuid`,
		slug, uuidA, slug+"-display",
	)
	require.NoError(t, err, "seed Tenant A tenants row")

	// Create Tenant A's default project (M2.5: VNets need project context).
	projectA, _, _, err := env.DB.CreateProject(ctx,
		models.Project{
			ID: projectSlug, TenantID: slug, TenantUUID: uuidA,
			Name: "Default", CPUCores: 20, MemoryGB: 64, StorageGB: 500,
			CreatedBy: "phase-6a-test",
		},
		models.ProjectQuota{MaxVNets: 10, MaxClusters: 2, MaxVolumes: 50, MaxPublicIPs: 3},
	)
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		require.NoError(t, err, "create Tenant A project")
	}
	var projectUUIDForA uuid.UUID
	if projectA != nil {
		projectUUIDForA = projectA.ProjectUUID
	} else {
		// Project already exists from a previous run — look it up.
		projectUUIDForA, err = env.DB.GetProjectUUIDByTenantAndSlug(ctx, slug, projectSlug)
		require.NoError(t, err, "lookup project uuid for Tenant A")
		require.NotEqual(t, uuid.Nil, projectUUIDForA)
	}

	// Grant Tenant A's user an owner role so API calls succeed.
	userSubA := slug + "-user-a"
	_, err = env.DB.CreateRoleAssignment(ctx, models.RoleAssignment{
		PrincipalType: models.PrincipalTypeUser,
		PrincipalID:   userSubA,
		ScopeType:     models.ScopeTypeTenant,
		ScopeID:       slug,
		Role:          models.RoleOwner,
		GrantedBy:     "test-setup",
	})
	// Ignore duplicate-key — tolerate test reruns without full DB reset.
	if err != nil && !strings.Contains(err.Error(), "23505") && !strings.Contains(err.Error(), "duplicate") {
		require.NoError(t, err, "grant Tenant A owner role")
	}

	// ── Step 2: Seed a VNet row for Tenant A ──────────────────────────────────

	tokenA, err := env.JWT.MintToken(slug, userSubA)
	require.NoError(t, err, "mint Tenant A JWT")
	// Bind client to tenant + project so ListVNets/GetVNet route correctly.
	clientA := NewAPIClientForProject(env.BaseURL, tokenA, slug, projectSlug)

	// We avoid calling the live network provider (no kubeovn VPC needed for this
	// test). Instead we seed the VNet row directly via the DB so we control
	// tenant_uuid and project_uuid precisely.
	vnetID := uuid.New()
	_, err = env.DB.Pool().Exec(ctx,
		`INSERT INTO vnets (id, tenant_id, tenant_uuid, project_id, project_uuid, name, region, address_space, status, provider_type)
		 VALUES ($1, $2, $3, $4, $5, $6, 'lk', ARRAY['10.90.0.0/16'], 'ACTIVE', 'kubeovn')`,
		vnetID, slug, uuidA, projectSlug, projectUUIDForA, fmt.Sprintf("vnet-tenant-a-%s", vnetID),
	)
	require.NoError(t, err, "seed Tenant A VNet row")

	// Verify Tenant A can see their own VNet.
	listResp, listStatus, err := clientA.ListVNets(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listStatus, "Tenant A must be able to list VNets")
	found := false
	for _, v := range listResp {
		if v.ID == vnetID.String() {
			found = true
			break
		}
	}
	require.True(t, found, "Tenant A must see their own VNet in the list")

	getResp, getStatus, err := clientA.GetVNet(ctx, vnetID.String())
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, getStatus, "Tenant A must be able to GET their VNet")
	require.Equal(t, vnetID.String(), getResp.ID)

	// ── Step 3: Simulate tenant deletion — remove roles + tenants row ─────────
	// Must remove project (cascade deletes project_quotas) before tenants row.
	// VNet row is intentionally left orphaned with old project_uuid — this is the
	// state the isolation check below must protect against.

	// Remove ALL of Tenant A's tenant-scope role assignments, not just the
	// explicitly-granted user. With AutoProvisionMembers=true the JWT's first
	// request also minted an autoprovision row keyed to the token's sub (which is
	// the slug) at scope_uuid=uuidA. A real tenant deletion clears every
	// assignment for that tenant; deleting by the immutable scope_uuid does the
	// same and stops a stale-uuid orphan from surviving the slug recycle (which
	// the UUID-keyed RBAC gate would otherwise — correctly — reject).
	_, err = env.DB.Pool().Exec(ctx,
		`DELETE FROM role_assignments WHERE scope_type = 'tenant' AND scope_uuid = $1`,
		uuidA,
	)
	require.NoError(t, err, "remove Tenant A role assignments")

	// Remove the VNet FK to the project so the project + tenant rows can be deleted.
	_, err = env.DB.Pool().Exec(ctx,
		`UPDATE vnets SET project_id = NULL, project_uuid = NULL WHERE id = $1`,
		vnetID,
	)
	require.NoError(t, err, "detach VNet from project before project deletion")

	_, err = env.DB.Pool().Exec(ctx, `DELETE FROM projects WHERE tenant_uuid = $1`, uuidA)
	require.NoError(t, err, "remove Tenant A project row")

	_, err = env.DB.Pool().Exec(ctx,
		`DELETE FROM tenants WHERE id = $1 AND tenant_uuid = $2`,
		slug, uuidA,
	)
	require.NoError(t, err, "remove Tenant A tenants row")

	// ── Step 4: Re-register the slug with a NEW UUID (Tenant B) ──────────────

	uuidB := uuid.New()
	require.NotEqual(t, uuidA, uuidB, "sanity: two uuid.New() calls must differ")

	_, err = env.DB.Pool().Exec(ctx,
		`INSERT INTO tenants (id, tenant_uuid, name, created_by)
		 VALUES ($1, $2, $3, 'phase-6a-test')`,
		slug, uuidB, slug+"-display-b",
	)
	require.NoError(t, err, "seed Tenant B tenants row (same slug, new UUID)")

	// Create Tenant B's fresh default project — a new project_uuid, different from A's.
	projectB, _, _, err := env.DB.CreateProject(ctx,
		models.Project{
			ID: projectSlug, TenantID: slug, TenantUUID: uuidB,
			Name: "Default", CPUCores: 20, MemoryGB: 64, StorageGB: 500,
			CreatedBy: "phase-6a-test",
		},
		models.ProjectQuota{MaxVNets: 10, MaxClusters: 2, MaxVolumes: 50, MaxPublicIPs: 3},
	)
	require.NoError(t, err, "create Tenant B project")
	require.NotEqual(t, projectA.ProjectUUID, projectB.ProjectUUID,
		"Tenant B's project_uuid must differ from Tenant A's (slug-recycle isolation)")

	// Grant Tenant B user an owner role.
	userSubB := slug + "-user-b"
	_, err = env.DB.CreateRoleAssignment(ctx, models.RoleAssignment{
		PrincipalType: models.PrincipalTypeUser,
		PrincipalID:   userSubB,
		ScopeType:     models.ScopeTypeTenant,
		ScopeID:       slug,
		Role:          models.RoleOwner,
		GrantedBy:     "test-setup",
	})
	require.NoError(t, err, "grant Tenant B owner role")

	// ── Step 5 & 6: Tenant B sees an empty VNet list ──────────────────────────

	tokenB, err := env.JWT.MintToken(slug, userSubB)
	require.NoError(t, err, "mint Tenant B JWT")
	clientB := NewAPIClientForProject(env.BaseURL, tokenB, slug, projectSlug)

	listBResp, listBStatus, err := clientB.ListVNets(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listBStatus, "Tenant B list must succeed")
	for _, v := range listBResp {
		require.NotEqual(t, vnetID.String(), v.ID,
			"Tenant B must NOT see Tenant A's VNet (slug-recycle isolation failure)")
	}

	// ── Step 7: Tenant B's direct GET also returns 404 ───────────────────────

	_, getStatus2, err := clientB.GetVNet(ctx, vnetID.String())
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, getStatus2,
		"Tenant B must receive 404 for Tenant A's VNet ID (slug-recycle isolation failure)")

	// ── Step 8: Cleanup ───────────────────────────────────────────────────────

	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = env.DB.Pool().Exec(ctx, `DELETE FROM role_assignments WHERE scope_id = $1 AND scope_type = 'tenant'`, slug)
		_, _ = env.DB.Pool().Exec(ctx, `DELETE FROM projects WHERE tenant_id = $1`, slug)
		_, _ = env.DB.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, slug)
		_, _ = env.DB.Pool().Exec(ctx, `DELETE FROM vnets WHERE id = $1`, vnetID)
	})
}
