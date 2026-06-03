//go:build integration

package integration

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wso2/dc-api/internal/models"
)

// TestProjectIsolation_VMDoesNotLeakAcrossProjects proves a VM created in one
// project is invisible and unreachable from another project in the same tenant.
//
// Before the fix the VM list filtered by tenant only (ListByTenant) and Get by
// (id, tenant), so the same VM appeared in every project's list and could be
// fetched/deleted through any project's URL. The owner is used deliberately: a
// tenant owner can enter BOTH projects, so any isolation seen here is the
// project-scoped query enforcing it, not a membership check.
func TestProjectIsolation_VMDoesNotLeakAcrossProjects(t *testing.T) {
	ctx := context.Background()
	tid := randomTenantID("proj-iso")

	owner := clientForTenant(t, tid) // ensures the "default" project exists
	mustGrantOwnerForClient(t, tid)

	tenantUUID, err := env.DB.GetTenantUUIDBySlug(ctx, tid)
	require.NoError(t, err)
	defaultProjUUID, err := env.DB.GetProjectUUIDByTenantAndSlug(ctx, tid, defaultProjectID)
	require.NoError(t, err)

	// A second project the VM does NOT live in.
	const otherProject = "other"
	_, _, _, err = env.DB.CreateProject(ctx,
		models.Project{
			ID: otherProject, TenantID: tid, TenantUUID: tenantUUID,
			Name: "Other", CPUCores: 1, MemoryGB: 1, StorageGB: 1, CreatedBy: "test",
		},
		models.ProjectQuota{MaxVNets: 1, MaxClusters: 1, MaxVolumes: 1, MaxPublicIPs: 1},
	)
	require.NoError(t, err, "create second project")

	// A VM in the DEFAULT project.
	vm, err := env.DB.Create(ctx, &models.Resource{
		TenantID:     tid,
		TenantUUID:   tenantUUID,
		ProjectID:    defaultProjectID,
		ProjectUUID:  defaultProjUUID,
		OwnerID:      "test-owner",
		Name:         "iso-vm",
		Type:         models.ResourceTypeVM,
		Status:       models.StatusActive,
		ProviderType: "harvester",
	})
	require.NoError(t, err, "seed VM row")
	vmID := vm.ID.String()

	defaultBase := "/v1/tenants/" + tid + "/projects/" + defaultProjectID
	otherBase := "/v1/tenants/" + tid + "/projects/" + otherProject

	t.Run("VM is listed in its own project", func(t *testing.T) {
		body, status, err := owner.do(ctx, http.MethodGet, defaultBase+"/virtual-machines", nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status, "body: %s", ErrorBody(body))
		require.Contains(t, string(body), vmID, "the VM must appear in its own project's list")
	})

	t.Run("VM is NOT listed in another project", func(t *testing.T) {
		body, status, err := owner.do(ctx, http.MethodGet, otherBase+"/virtual-machines", nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status, "body: %s", ErrorBody(body))
		require.NotContains(t, string(body), vmID, "a VM in 'default' must not appear in 'other' (this is the bug)")
	})

	t.Run("VM is unreachable by ID through another project", func(t *testing.T) {
		body, status, err := owner.do(ctx, http.MethodGet, otherBase+"/virtual-machines/"+vmID, nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusNotFound, status,
			"a VM in 'default' must 404 when fetched via 'other': %s", ErrorBody(body))
	})

	t.Run("VM is reachable by ID in its own project", func(t *testing.T) {
		body, status, err := owner.do(ctx, http.MethodGet, defaultBase+"/virtual-machines/"+vmID, nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status,
			"the VM must be reachable in its own project: %s", ErrorBody(body))
	})
}
