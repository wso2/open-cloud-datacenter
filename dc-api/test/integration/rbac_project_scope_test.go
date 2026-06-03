//go:build integration

package integration

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRBAC_ProjectScopeRoleAssignment proves the M5 project-scope role-assignment
// endpoints: a role granted at PROJECT scope is created, appears in the project's
// own list tagged scope_type=project, and — critically — does NOT appear in the
// TENANT-scope list. That last assertion exercises the scope_uuid keying: project
// slugs are not globally unique, so list/delete MUST filter on the immutable
// project_uuid, never the slug. A slug filter would cross-leak grants between a
// tenant's role-assignments and a same-named project's.
//
// The whole test is owner-driven; the grantee never authenticates, so the env's
// autoprovision (true by default) can't mint a competing tenant grant and mask
// the scope separation.
func TestRBAC_ProjectScopeRoleAssignment(t *testing.T) {
	ctx := context.Background()
	tid := randomTenantID("proj-ra")

	owner := clientForTenant(t, tid)
	mustGrantOwnerForClient(t, tid)

	grantee := "test-proj-grantee-" + tid

	// Grant a role at PROJECT scope via the new endpoint. The owner holds tenant
	// Owner, which inherits authorization/roleAssignments/write into the project.
	_, raw, status, err := owner.CreateProjectRoleAssignment(ctx, tid, defaultProjectID, grantee, "Reader")
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status,
		"owner must create a project-scope role assignment: %s", ErrorBody(raw))

	t.Run("project list includes the grant, tagged project scope", func(t *testing.T) {
		resp, status, err := owner.ListProjectRoleAssignments(ctx, tid, defaultProjectID)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status)

		var found *MemberResponse
		for i := range resp.Members {
			if resp.Members[i].PrincipalID == grantee {
				found = &resp.Members[i]
				break
			}
		}
		require.NotNil(t, found, "project list must include the new grant")
		require.Equal(t, "project", found.ScopeType, "grant must be project-scoped")
		require.Equal(t, defaultProjectID, found.ScopeID)
		require.Equal(t, "Reader", found.RoleDefinition)
	})

	t.Run("tenant list excludes the project grant (scope_uuid keyed)", func(t *testing.T) {
		resp, status, err := owner.ListMembers(ctx, tid) // tenant scope
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status)

		for _, m := range resp.Members {
			require.NotEqual(t, grantee, m.PrincipalID,
				"a project-scope grant must NOT surface in the tenant-scope list")
		}
	})

	t.Run("project grant is removable at project scope", func(t *testing.T) {
		path := "/v1/tenants/" + tid + "/projects/" + defaultProjectID + "/role-assignments/" + grantee
		body, status, err := owner.do(ctx, http.MethodDelete, path, nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusNoContent, status, "delete project grant: %s", ErrorBody(body))

		resp, _, err := owner.ListProjectRoleAssignments(ctx, tid, defaultProjectID)
		require.NoError(t, err)
		for _, m := range resp.Members {
			require.NotEqual(t, grantee, m.PrincipalID, "grant must be gone after delete")
		}
	})
}
