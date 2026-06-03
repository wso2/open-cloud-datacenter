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

// permissionsCheck POSTs actions to a tenant's permissions:check endpoint and
// returns action → allowed.
func permissionsCheck(t *testing.T, client *APIClient, tenantID string, actions []string) map[string]bool {
	t.Helper()
	body, status, err := client.do(context.Background(), http.MethodPost,
		"/v1/tenants/"+tenantID+"/permissions:check",
		map[string]interface{}{"actions": actions})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "permissions:check body: %s", ErrorBody(body))
	var resp struct {
		Results []struct {
			Action  string `json:"action"`
			Allowed bool   `json:"allowed"`
		} `json:"results"`
	}
	require.NoError(t, json.Unmarshal(body, &resp))
	out := make(map[string]bool, len(resp.Results))
	for _, r := range resp.Results {
		out[r.Action] = r.Allowed
	}
	return out
}

// TestRBAC_PermissionsCheck exercises POST /v1/tenants/{tid}/permissions:check —
// the capability probe the UI uses to enable/disable controls without ever
// running the matcher itself. Cluster-free: it only reads role assignments and
// runs the engine.
func TestRBAC_PermissionsCheck(t *testing.T) {
	ctx := context.Background()
	tid := randomTenantID("perms")

	// Owner principal (sub == tid), granted Owner.
	owner := clientForTenant(t, tid)
	mustGrantOwnerForClient(t, tid)

	const (
		actRoleWrite  = "authorization/roleAssignments/write"
		actVMWrite    = "compute/virtualMachines/write"
		actVMRead     = "compute/virtualMachines/read"
		actSecretRead = "keyvault/vaults/secrets/read" // data-plane
	)
	actions := []string{actRoleWrite, actVMWrite, actVMRead, actSecretRead}

	t.Run("owner is allowed everything", func(t *testing.T) {
		got := permissionsCheck(t, owner, tid, actions)
		for _, a := range actions {
			require.True(t, got[a], "owner should be allowed %q", a)
		}
	})

	t.Run("reader can read but not write", func(t *testing.T) {
		// A Reader in the same tenant, granted BEFORE its first request so
		// autoprovision (which would otherwise add Contributor) is skipped.
		readerSub := "test-reader-" + tid
		_, err := env.DB.CreateRoleAssignment(ctx, models.RoleAssignment{
			PrincipalType:  models.PrincipalTypeUser,
			PrincipalID:    readerSub,
			ScopeType:      models.ScopeTypeTenant,
			ScopeID:        tid,
			RoleDefinition: "Reader",
			GrantedBy:      "test-setup",
		})
		require.NoError(t, err, "grant Reader")
		readerToken, err := env.JWT.MintTokenWithGroups(readerSub, readerSub, []string{"dc-tenant-" + tid})
		require.NoError(t, err)
		reader := NewAPIClientForTenant(env.BaseURL, readerToken, tid)

		got := permissionsCheck(t, reader, tid, actions)
		require.True(t, got[actVMRead], "reader should read VMs")
		require.False(t, got[actVMWrite], "reader must not write VMs")
		require.False(t, got[actRoleWrite], "reader must not manage access")
		require.False(t, got[actSecretRead], "reader must not read secret data")
	})

	t.Run("empty actions is 400", func(t *testing.T) {
		_, status, err := owner.do(ctx, http.MethodPost,
			"/v1/tenants/"+tid+"/permissions:check",
			map[string]interface{}{"actions": []string{}})
		require.NoError(t, err)
		require.Equal(t, http.StatusBadRequest, status)
	})
}
