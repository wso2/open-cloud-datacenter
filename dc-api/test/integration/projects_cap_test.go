//go:build integration

package integration

// projects_cap_test.go — exercises the M2.5 tenant cap + project quota
// enforcement paths:
//
//  1. Cap check on project create: second project request that would push the
//     tenant over its cpu_cores_cap returns 400 quota_exceeded.
//  2. Cap check on project PATCH: PATCH that would push the tenant over its
//     cpu_cores_cap returns 400 quota_exceeded.
//  3. In-use guard on project PATCH: PATCH that would shrink a project below
//     its current resource usage returns 400 quota_below_usage.
//  4. Admin tenant cap shrink-guard: PATCH /v1/admin/tenants/{tid} that would
//     shrink the cap below the sum of existing project allocations returns 400
//     quota_exceeded.
//  5. GET /v1/tenants/{tid}/cap-usage smoke: returns the correct cap/
//     allocated/available triplet after one project exists.
//
// All tests use freshly-named tenants so they don't interfere with each other
// or with the rest of the suite. Each test sets a specific cap on the tenant
// (via direct DB update — no admin token needed to bypass the admin-endpoint
// auth requirement in an isolated test DB).

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/wso2/dc-api/internal/models"
)

// ── helpers specific to this file ────────────────────────────────────────────

// capTestTenant registers a tenant with specific caps (set directly in the DB
// after UPSERT so we don't need to go through the admin API). Returns the
// tenant ID and a token / client for the owner.
func capTestTenant(t *testing.T, suffix string, cpuCap, memCap, storageCap int) (tenantID string, client *APIClient) {
	t.Helper()
	ctx := context.Background()
	tenantID = "test-cap-" + suffix

	if _, err := env.DB.UpsertTenant(ctx, tenantID, tenantID, "dc-tenant-"+tenantID, "cap-test"); err != nil {
		require.NoError(t, err, "UpsertTenant")
	}
	// Set the desired cap directly — CreateTenant uses defaults; UpsertTenant
	// is idempotent. A direct UPDATE is the cleanest way to set a specific cap
	// for the isolated cap test without invoking the admin API (which requires
	// platform-admin claims we don't want to wire in unit-style cap tests).
	_, err := env.DB.Pool().Exec(ctx,
		`UPDATE tenants SET cpu_cores_cap = $2, memory_gb_cap = $3, storage_gb_cap = $4
		 WHERE id = $1`,
		tenantID, cpuCap, memCap, storageCap,
	)
	require.NoError(t, err, "set tenant cap")

	// MintToken(tenantID, …) sets JWT sub == tenantID and emits the
	// "dc-tenant-<tenantID>" group claim. So the principal_id Auth middleware
	// sets in context is tenantID; the role_assignments row must use that
	// same string as principal_id. Mirrors ownerClientForSATests.
	mustGrantOwner(t, tenantID, tenantID)

	token, err := env.JWT.MintToken(tenantID, tenantID+"-email")
	require.NoError(t, err)

	client = NewAPIClientForTenant(env.BaseURL, token, tenantID)
	return tenantID, client
}

// capQuotaError parses the quota_exceeded / quota_below_usage error body.
// Returns the error code string ("quota_exceeded" or "quota_below_usage").
func capQuotaError(t *testing.T, body []byte) string {
	t.Helper()
	var b struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(body, &b), "parse quota error body: %s", body)
	return b.Error
}

// ── Test 1: cap check on project create ──────────────────────────────────────

func TestProjectCap_CreateExceedsTenantCap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Tenant cap: 80 cpu / 256 GB / 2000 GB (schema defaults). Deliberately
	// use values that make the math obvious: p1 takes 40 cpu; p2 requests 50
	// cpu — 40+50=90 > 80 → quota_exceeded.
	tenantID, client := capTestTenant(t, randomName("create"), 80, 256, 2000)

	// Create project p1 (40 cpu — within cap).
	p1, body, status, err := client.CreateProject(ctx, CreateProjectRequest{
		ID: "p1", Name: "Project 1", CPUCores: 40, MemoryGB: 128, StorageGB: 1000,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status,
		"p1 create must succeed (40 cpu ≤ 80 cap): %s", body)
	require.Equal(t, 40, p1.CPUCores)

	// Create project p2 (50 cpu — would push total to 90, over cap).
	_, body2, status2, err := client.CreateProject(ctx, CreateProjectRequest{
		ID: "p2", Name: "Project 2", CPUCores: 50, MemoryGB: 128, StorageGB: 1000,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status2,
		"p2 create must fail with 400 (40+50=90 > 80 cap): %s", body2)
	require.Equal(t, "quota_exceeded", capQuotaError(t, body2),
		"error body must say quota_exceeded: %s", body2)

	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = env.DB.Pool().Exec(ctx, `DELETE FROM projects WHERE tenant_id = $1`, tenantID)
		_, _ = env.DB.Pool().Exec(ctx, `DELETE FROM role_assignments WHERE scope_id = $1 AND scope_type = 'tenant'`, tenantID)
		_, _ = env.DB.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenantID)
	})
}

// ── Test 2: cap check on project PATCH ───────────────────────────────────────

func TestProjectCap_PatchExceedsTenantCap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Tenant cap: 80 cpu. p1 allocated at 40. Patch to 90 → quota_exceeded.
	tenantID, client := capTestTenant(t, randomName("patch"), 80, 256, 2000)

	_, _, status, err := client.CreateProject(ctx, CreateProjectRequest{
		ID: "p1", Name: "P1", CPUCores: 40, MemoryGB: 128, StorageGB: 1000,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, "p1 create")

	// PATCH p1 to cpu_cores=90 → over tenant cap.
	newCPU := 90
	_, body, patchStatus, err := client.PatchProject(ctx, "p1", PatchProjectRequest{
		CPUCores: &newCPU,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, patchStatus,
		"PATCH to 90 cpu must fail with 400 (cap=80): %s", body)
	require.Equal(t, "quota_exceeded", capQuotaError(t, body),
		"error body must say quota_exceeded: %s", body)

	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = env.DB.Pool().Exec(ctx, `DELETE FROM projects WHERE tenant_id = $1`, tenantID)
		_, _ = env.DB.Pool().Exec(ctx, `DELETE FROM role_assignments WHERE scope_id = $1 AND scope_type = 'tenant'`, tenantID)
		_, _ = env.DB.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenantID)
	})
}

// ── Test 3: in-use guard on project PATCH ────────────────────────────────────

func TestProjectCap_PatchBelowInUseRejected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Tenant cap: 80 cpu. p1 at 40 cpu. Insert a fake ACTIVE xlarge VM
	// (16 cpu / 32 GB per models.Sizes) and a fake ACTIVE cluster with two
	// node pools (system small×1 = 2 cpu, worker medium×2 = 8 cpu) → 26 cpu
	// in use. Patch p1 to cpu_cores=10 → below in-use (10 < 26) → 400
	// quota_below_usage.
	tenantID, client := capTestTenant(t, randomName("inuse"), 80, 256, 2000)

	_, _, status, err := client.CreateProject(ctx, CreateProjectRequest{
		ID: "p1", Name: "P1", CPUCores: 40, MemoryGB: 128, StorageGB: 1000,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, "p1 create")

	// Look up project_uuid so we can insert the fake resource.
	tenantUUID, err := env.DB.GetTenantUUIDBySlug(ctx, tenantID)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, tenantUUID)

	projectUUID, err := env.DB.GetProjectUUIDByTenantAndSlug(ctx, tenantID, "p1")
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, projectUUID)

	// Create a fake ACTIVE xlarge VM (16 cpu / 32 GB per models.Sizes) through
	// the production repository path. Repository.Create is what persists
	// resources.size — which the usage sum resolves through models.Sizes — so
	// going through it (instead of a raw INSERT) makes this test fail if the
	// create path ever stops writing the column the guard depends on.
	vm, err := env.DB.Create(ctx, &models.Resource{
		TenantID: tenantID, TenantUUID: tenantUUID,
		ProjectID: "p1", ProjectUUID: projectUUID,
		OwnerID: "test", Name: "fake-vm-cap",
		Type: models.ResourceTypeVM, Size: "xlarge",
		Status: models.StatusActive, ProviderType: "harvester",
	})
	require.NoError(t, err, "create fake VM resource for in-use check")

	// Create a fake CLUSTER the same way; its usage is derived from
	// cluster_node_pools (size × count, all roles), not from the resource row.
	cluster, err := env.DB.Create(ctx, &models.Resource{
		TenantID: tenantID, TenantUUID: tenantUUID,
		ProjectID: "p1", ProjectUUID: projectUUID,
		OwnerID: "test", Name: "fake-cluster-cap",
		Type:   models.ResourceTypeCluster,
		Status: models.StatusActive, ProviderType: "rancher",
	})
	require.NoError(t, err, "create fake cluster resource for in-use check")
	require.NoError(t, env.DB.CreateNodePool(ctx, &models.NodePool{
		ClusterID: cluster.ID, Name: "system", Role: models.NodePoolRoleSystem,
		Size: "small", Count: 1, Status: models.NodePoolStatusReady,
	}), "insert fake system pool")
	require.NoError(t, env.DB.CreateNodePool(ctx, &models.NodePool{
		ClusterID: cluster.ID, Name: "workers", Role: models.NodePoolRoleWorker,
		Size: "medium", Count: 2, Status: models.NodePoolStatusReady,
	}), "insert fake worker pool")

	// PATCH p1 to cpu_cores=10 → below the 26 cpu in use → quota_below_usage.
	newCPU := 10
	_, body, patchStatus, err := client.PatchProject(ctx, "p1", PatchProjectRequest{
		CPUCores: &newCPU,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, patchStatus,
		"PATCH to 10 cpu must fail with 400 (26 cpu in use): %s", body)

	// Parse the error body to check the error code and in_use field.
	var errBody struct {
		Error string           `json:"error"`
		InUse models.TenantCap `json:"in_use"`
	}
	require.NoError(t, json.Unmarshal(body, &errBody), "parse error body: %s", body)
	require.Equal(t, "quota_below_usage", errBody.Error,
		"error body must say quota_below_usage: %s", body)
	require.Equal(t, 26, errBody.InUse.CPUCores,
		"in_use.cpu_cores must be 26 (xlarge VM 16 + small×1 pool 2 + medium×2 pool 8): %s", body)
	require.Equal(t, 52, errBody.InUse.MemoryGB,
		"in_use.memory_gb must be 52 (xlarge VM 32 + small×1 pool 4 + medium×2 pool 16): %s", body)

	t.Cleanup(func() {
		ctx := context.Background()
		// Node pools cascade with the cluster row (ON DELETE CASCADE).
		_, _ = env.DB.Pool().Exec(ctx, `DELETE FROM resources WHERE id IN ($1, $2)`, vm.ID, cluster.ID)
		_, _ = env.DB.Pool().Exec(ctx, `DELETE FROM projects WHERE tenant_id = $1`, tenantID)
		_, _ = env.DB.Pool().Exec(ctx, `DELETE FROM role_assignments WHERE scope_id = $1 AND scope_type = 'tenant'`, tenantID)
		_, _ = env.DB.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenantID)
	})
}

// ── Test 4: admin tenant cap shrink-guard ─────────────────────────────────────

func TestProjectCap_AdminShrinkTenantCapBelowAllocated(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Tenant cap: 80 cpu. p1 allocates 40 cpu. Admin tries to shrink to 30 →
	// 400 quota_exceeded (allocated=40 > proposed cap=30).
	tenantID, ownerClient := capTestTenant(t, randomName("shrink"), 80, 256, 2000)

	// Create p1 to lock in 40 cpu of allocation.
	_, _, status, err := ownerClient.CreateProject(ctx, CreateProjectRequest{
		ID: "p1", Name: "P1", CPUCores: 40, MemoryGB: 128, StorageGB: 1000,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, "p1 create")

	// Build an admin client — we need a JWT with the dc-admin group.
	adminSub := "sub-cap-admin-" + randomName("u")
	adminToken, err := env.JWT.MintTokenWithGroups(adminSub, adminSub+"@test.dc", []string{"dc-admin"})
	require.NoError(t, err)
	adminClient := NewAPIClientForTenant(env.BaseURL, adminToken, tenantID)

	// PATCH /v1/admin/tenants/{tenantID} to shrink cpu_cores_cap to 30.
	newCPUCap := 30
	body, patchStatus, err := adminClient.PatchAdminTenantCap(ctx, PatchTenantCapRequest{
		CPUCoresCap: &newCPUCap,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, patchStatus,
		"admin shrink to 30 must fail (allocated=40): %s", body)

	var errBody struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(body, &errBody), "parse error body: %s", body)
	// The admin handler uses writeQuotaExceeded which sets error="quota_exceeded".
	require.Equal(t, "quota_exceeded", errBody.Error, "error body: %s", body)

	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = env.DB.Pool().Exec(ctx, `DELETE FROM projects WHERE tenant_id = $1`, tenantID)
		_, _ = env.DB.Pool().Exec(ctx, `DELETE FROM role_assignments WHERE scope_id = $1 AND scope_type = 'tenant'`, tenantID)
		_, _ = env.DB.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenantID)
	})
}

// ── Test 5: GET /v1/tenants/{tid}/cap-usage smoke ─────────────────────────────

func TestProjectCap_GetTenantCapUsage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Tenant cap: 80/256/2000. Create p1 at 40/128/1000. Expected response:
	//   cap       = {80, 256, 2000}
	//   allocated = {40, 128, 1000}
	//   available = {40, 128, 1000}
	tenantID, client := capTestTenant(t, randomName("capusage"), 80, 256, 2000)

	_, _, status, err := client.CreateProject(ctx, CreateProjectRequest{
		ID: "p1", Name: "P1", CPUCores: 40, MemoryGB: 128, StorageGB: 1000,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, "p1 create")

	usage, body, usageStatus, err := client.GetTenantCapUsage(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, usageStatus, "cap-usage must return 200: %s", body)

	require.Equal(t, 80, usage.Cap.CPUCores, "cap.cpu_cores: %s", body)
	require.Equal(t, 256, usage.Cap.MemoryGB, "cap.memory_gb: %s", body)
	require.Equal(t, 2000, usage.Cap.StorageGB, "cap.storage_gb: %s", body)

	require.Equal(t, 40, usage.Allocated.CPUCores, "allocated.cpu_cores: %s", body)
	require.Equal(t, 128, usage.Allocated.MemoryGB, "allocated.memory_gb: %s", body)
	require.Equal(t, 1000, usage.Allocated.StorageGB, "allocated.storage_gb: %s", body)

	require.Equal(t, 40, usage.Available.CPUCores, "available.cpu_cores: %s", body)
	require.Equal(t, 128, usage.Available.MemoryGB, "available.memory_gb: %s", body)
	require.Equal(t, 1000, usage.Available.StorageGB, "available.storage_gb: %s", body)

	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = env.DB.Pool().Exec(ctx, `DELETE FROM projects WHERE tenant_id = $1`, tenantID)
		_, _ = env.DB.Pool().Exec(ctx, `DELETE FROM role_assignments WHERE scope_id = $1 AND scope_type = 'tenant'`, tenantID)
		_, _ = env.DB.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenantID)
	})
}
