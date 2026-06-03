//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wso2/dc-api/internal/models"
)

// TestRBAC_ResourceOnlyUserCanEnter proves the entry path for a user whose ONLY
// grant is on a single resource — no tenant- or project-scope role at all. Such
// a user must discover the resource's tenant, see (and enter) only the project
// the resource lives in, read that one resource, and be denied a sibling
// resource type, another project, and any tenant-level access.
//
// Before the resource-scope entry hooks, GET /v1/tenants, TenantContext, GET
// /projects, and ProjectContext honoured only tenant- and project-scope grants,
// so this user hit "no tenants" and was locked out. The grantee's token carries
// NO dc-tenant group, so autoprovision can't mint a competing tenant grant — the
// resource grant is the sole thing admitting them, which is what we prove.
func TestRBAC_ResourceOnlyUserCanEnter(t *testing.T) {
	ctx := context.Background()
	tid := randomTenantID("res-only")

	owner := clientForTenant(t, tid) // also ensures the "default" project exists
	mustGrantOwnerForClient(t, tid)

	tenantUUID, err := env.DB.GetTenantUUIDBySlug(ctx, tid)
	require.NoError(t, err)
	projectUUID, err := env.DB.GetProjectUUIDByTenantAndSlug(ctx, tid, defaultProjectID)
	require.NoError(t, err)

	// A second project the grantee's resource does NOT live in — to prove the
	// project list shows only the resource's project and entering another is denied.
	const otherProject = "other"
	_, _, _, err = env.DB.CreateProject(ctx,
		models.Project{
			ID: otherProject, TenantID: tid, TenantUUID: tenantUUID,
			Name: "Other", CPUCores: 1, MemoryGB: 1, StorageGB: 1, CreatedBy: "test",
		},
		models.ProjectQuota{MaxVNets: 1, MaxClusters: 1, MaxVolumes: 1, MaxPublicIPs: 1},
	)
	require.NoError(t, err, "create second project")

	// A real VM row in the default project, so the resolver can map the grant's
	// resource UUID back to its tenant and project.
	vm, err := env.DB.Create(ctx, &models.Resource{
		TenantID:     tid,
		TenantUUID:   tenantUUID,
		ProjectID:    defaultProjectID,
		ProjectUUID:  projectUUID,
		OwnerID:      "test-owner",
		Name:         "entry-fixture-vm",
		Type:         models.ResourceTypeVM,
		Status:       models.StatusActive,
		ProviderType: "harvester",
	})
	require.NoError(t, err, "seed VM row")
	vmID := vm.ID.String()

	// Grant the grantee VirtualMachineContributor on THAT VM only — no project or
	// tenant grant of any kind.
	grantee := "test-res-only-" + tid
	raPath := "/v1/tenants/" + tid + "/projects/" + defaultProjectID + "/virtual-machines/" + vmID + "/role-assignments"
	b, status, err := owner.do(ctx, http.MethodPost, raPath,
		map[string]string{"user_sub": grantee, "role_definition": "VirtualMachineContributor"})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, "grant on the VM at resource scope: %s", ErrorBody(b))

	// Authenticate with NO dc-tenant group → autoprovision can't fire.
	tok, err := env.JWT.MintTokenWithGroups(grantee, grantee, []string{})
	require.NoError(t, err)
	user := NewAPIClientForProject(env.BaseURL, tok, tid, defaultProjectID)
	base := "/v1/tenants/" + tid

	t.Run("appears in GET /v1/tenants with no tenant role", func(t *testing.T) {
		body, status, err := user.do(ctx, http.MethodGet, "/v1/tenants", nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status, "body: %s", ErrorBody(body))
		var tenants []struct {
			ID    string   `json:"id"`
			Roles []string `json:"roles"`
		}
		require.NoError(t, json.Unmarshal(body, &tenants))
		var found bool
		for _, tn := range tenants {
			if tn.ID == tid {
				found = true
				require.Empty(t, tn.Roles, "a resource-only user must carry no tenant-level role")
			}
		}
		require.True(t, found, "resource-only user must discover their tenant")
	})

	t.Run("lists only the project their resource lives in", func(t *testing.T) {
		body, status, err := user.do(ctx, http.MethodGet, base+"/projects", nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status, "body: %s", ErrorBody(body))
		var projects []struct {
			ID string `json:"id"`
		}
		require.NoError(t, json.Unmarshal(body, &projects))
		ids := make([]string, 0, len(projects))
		for _, p := range projects {
			ids = append(ids, p.ID)
		}
		require.Contains(t, ids, defaultProjectID, "must see the project their resource lives in")
		require.NotContains(t, ids, otherProject, "must NOT see a project they have no grant on")
	})

	t.Run("can read the one resource they were granted", func(t *testing.T) {
		body, status, err := user.do(ctx, http.MethodGet, base+"/projects/"+defaultProjectID+"/virtual-machines/"+vmID, nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status, "must read the granted VM: %s", ErrorBody(body))
	})

	t.Run("shared-resources lists the one resource they hold", func(t *testing.T) {
		body, status, err := user.do(ctx, http.MethodGet, base+"/shared-resources", nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status, "body: %s", ErrorBody(body))
		var shared []struct {
			ID        string   `json:"id"`
			Kind      string   `json:"kind"`
			ProjectID string   `json:"project_id"`
			Roles     []string `json:"roles"`
		}
		require.NoError(t, json.Unmarshal(body, &shared))
		require.Len(t, shared, 1, "exactly the one VM they were granted")
		require.Equal(t, vmID, shared[0].ID)
		require.Equal(t, "virtual-machines", shared[0].Kind, "kind must be the URL path segment for deep-linking")
		require.Equal(t, defaultProjectID, shared[0].ProjectID)
		require.Contains(t, shared[0].Roles, "VirtualMachineContributor")
	})

	t.Run("cannot read a sibling resource type in the same project", func(t *testing.T) {
		body, status, err := user.do(ctx, http.MethodGet, base+"/projects/"+defaultProjectID+"/vnets", nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusForbidden, status,
			"a VM-scoped grant must not read VNets in the project: %s", ErrorBody(body))
	})

	t.Run("is denied a project their resource is not in", func(t *testing.T) {
		body, status, err := user.do(ctx, http.MethodGet, base+"/projects/"+otherProject+"/virtual-machines", nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusNotFound, status, "must be denied another project: %s", ErrorBody(body))
	})

	t.Run("has no tenant-level access (not autoprovisioned)", func(t *testing.T) {
		body, status, err := user.do(ctx, http.MethodGet, base+"/role-assignments", nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusForbidden, status,
			"a resource grant must not confer tenant-level access: %s", ErrorBody(body))
	})
}
