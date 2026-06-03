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

// TestRBAC_ProjectOnlyUserCanEnter proves the entry path — the "front door" — for
// project-scoped access. A user with NO tenant-scope grant but a project-scope
// grant on ONE project must be able to discover the tenant, list and enter only
// that project, and be denied other projects and any tenant-level access. Before
// the fix, GET /v1/tenants and TenantContext honoured only tenant-scope grants,
// so this user hit "no tenants" and was locked out entirely.
//
// The grantee's token carries NO dc-tenant group, so autoprovision (true in the
// default env) never mints a competing tenant grant — the project grant is the
// sole thing admitting them, which is exactly what we want to prove.
func TestRBAC_ProjectOnlyUserCanEnter(t *testing.T) {
	ctx := context.Background()
	tid := randomTenantID("proj-only")

	owner := clientForTenant(t, tid) // also ensures the "default" project exists
	mustGrantOwnerForClient(t, tid)

	// A second project the grantee is NOT granted on — to prove the list shows
	// only their project and that entering another is denied.
	tenantUUID, err := env.DB.GetTenantUUIDBySlug(ctx, tid)
	require.NoError(t, err)
	const otherProject = "other"
	_, _, _, err = env.DB.CreateProject(ctx,
		models.Project{
			ID: otherProject, TenantID: tid, TenantUUID: tenantUUID,
			Name: "Other", CPUCores: 1, MemoryGB: 1, StorageGB: 1, CreatedBy: "test",
		},
		models.ProjectQuota{MaxVNets: 1, MaxClusters: 1, MaxVolumes: 1, MaxPublicIPs: 1},
	)
	require.NoError(t, err, "create second project")

	// Grant the grantee Contributor on the DEFAULT project only.
	grantee := "test-proj-only-" + tid
	_, raw, status, err := owner.CreateProjectRoleAssignment(ctx, tid, defaultProjectID, grantee, "Contributor")
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, "grant at project scope: %s", ErrorBody(raw))

	// Grantee authenticates with NO dc-tenant group → autoprovision can't fire.
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
				require.Empty(t, tn.Roles, "a project-only user must carry no tenant-level role")
			}
		}
		require.True(t, found, "project-only user must discover their tenant")
	})

	t.Run("lists only the project they are granted on", func(t *testing.T) {
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
		require.Contains(t, ids, defaultProjectID, "must see the project they're granted on")
		require.NotContains(t, ids, otherProject, "must NOT see a project they have no grant on")
	})

	t.Run("can read networks inside their project", func(t *testing.T) {
		body, status, err := user.do(ctx, http.MethodGet, base+"/projects/"+defaultProjectID+"/vnets", nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status, "must read VNets in their project: %s", ErrorBody(body))
	})

	t.Run("is denied a project they hold no grant on", func(t *testing.T) {
		body, status, err := user.do(ctx, http.MethodGet, base+"/projects/"+otherProject+"/vnets", nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusNotFound, status, "must be denied another project: %s", ErrorBody(body))
	})

	t.Run("has no tenant-level access (not autoprovisioned)", func(t *testing.T) {
		body, status, err := user.do(ctx, http.MethodGet, base+"/role-assignments", nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusForbidden, status,
			"a project grant must not confer tenant-level access: %s", ErrorBody(body))
	})
}
