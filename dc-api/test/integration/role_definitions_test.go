//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRBAC_RoleDefinitionsCatalog exercises GET /v1/role-definitions (and the
// by-key variant) — the catalog the web UI reads to render the role picker and a
// role-detail view. Cluster-free: the endpoint returns the in-code built-in
// catalog, so no Harvester/KubeOVN backend is involved.
func TestRBAC_RoleDefinitionsCatalog(t *testing.T) {
	ctx := context.Background()
	token, err := env.JWT.MintToken("test-roledefs", "test-roledefs-user")
	require.NoError(t, err, "mint token")
	client := NewAPIClient(env.BaseURL, token)

	t.Run("list returns the built-in catalog", func(t *testing.T) {
		body, status, err := client.do(ctx, http.MethodGet, "/v1/role-definitions", nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status, "body: %s", ErrorBody(body))

		var resp struct {
			RoleDefinitions []struct {
				Key         string   `json:"key"`
				DisplayName string   `json:"display_name"`
				Description string   `json:"description"`
				Actions     []string `json:"actions"`
				Builtin     bool     `json:"builtin"`
			} `json:"role_definitions"`
		}
		require.NoError(t, json.Unmarshal(body, &resp))
		require.Len(t, resp.RoleDefinitions, 13, "expected the 13 built-in roles")

		byKey := make(map[string]bool, len(resp.RoleDefinitions))
		for _, d := range resp.RoleDefinitions {
			require.NotEmpty(t, d.DisplayName, "role %q must carry a display name", d.Key)
			require.NotEmpty(t, d.Description, "role %q must carry a description", d.Key)
			require.True(t, d.Builtin, "built-in role %q must be flagged builtin", d.Key)
			byKey[d.Key] = true
		}
		// Spot-check a cross-cutting role and a per-resource-type role.
		require.True(t, byKey["Owner"], "catalog must include Owner")
		require.True(t, byKey["VirtualMachineContributor"], "catalog must include VirtualMachineContributor")
	})

	t.Run("get one by key", func(t *testing.T) {
		body, status, err := client.do(ctx, http.MethodGet, "/v1/role-definitions/VirtualMachineContributor", nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status, "body: %s", ErrorBody(body))

		var d struct {
			Key         string   `json:"key"`
			DisplayName string   `json:"display_name"`
			Actions     []string `json:"actions"`
		}
		require.NoError(t, json.Unmarshal(body, &d))
		require.Equal(t, "VirtualMachineContributor", d.Key)
		require.Equal(t, "Virtual Machine Contributor", d.DisplayName)
		require.Contains(t, d.Actions, "compute/virtualMachines/*")
	})

	t.Run("unknown key is 404", func(t *testing.T) {
		_, status, err := client.do(ctx, http.MethodGet, "/v1/role-definitions/NoSuchRole", nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusNotFound, status)
	})
}
