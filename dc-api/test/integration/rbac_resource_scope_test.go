//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestRBAC_ResourceScopeGrantIsIsolated proves resource-scope grants: a role
// granted on ONE VM authorizes a write on that VM and NOT on another. The user
// also holds a project Reader grant so they can reach the VM routes at all (a
// resource-only user with no project access is the deferred entry-path case);
// the resource grant is the only thing that adds delete on the one VM.
//
// No real VMs are provisioned: the authorization gate runs before the handler,
// so a present grant shows up as the gate PASSING (then the handler 404s because
// the VM row doesn't exist) while a missing grant shows up as 403 from the gate.
// The 404-vs-403 split is the isolation proof.
func TestRBAC_ResourceScopeGrantIsIsolated(t *testing.T) {
	ctx := context.Background()
	tid := randomTenantID("res-scope")

	owner := clientForTenant(t, tid)
	mustGrantOwnerForClient(t, tid)

	vmA := uuid.New().String()
	vmB := uuid.New().String()
	user := "test-res-user-" + tid

	// Project Reader — lets the user reach the project + VM routes; Reader cannot
	// write, so any write that succeeds is attributable to the resource grant.
	_, raw, status, err := owner.CreateProjectRoleAssignment(ctx, tid, defaultProjectID, user, "Reader")
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, "grant project Reader: %s", ErrorBody(raw))

	// VirtualMachineContributor on vmA ONLY (resource scope).
	raPath := "/v1/tenants/" + tid + "/projects/" + defaultProjectID + "/virtual-machines/" + vmA + "/role-assignments"
	b, status, err := owner.do(ctx, http.MethodPost, raPath,
		map[string]string{"user_sub": user, "role_definition": "VirtualMachineContributor"})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, "grant on vmA at resource scope: %s", ErrorBody(b))

	// Authenticate with NO dc-tenant group so autoprovision can't mint a tenant
	// role that would mask the isolation.
	tok, err := env.JWT.MintTokenWithGroups(user, user, []string{})
	require.NoError(t, err)
	uc := NewAPIClientForProject(env.BaseURL, tok, tid, defaultProjectID)
	vmBase := "/v1/tenants/" + tid + "/projects/" + defaultProjectID + "/virtual-machines/"

	t.Run("resource grant authorizes a write on the granted VM", func(t *testing.T) {
		body, status, err := uc.do(ctx, http.MethodDelete, vmBase+vmA, nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusNotFound, status,
			"delete of the granted VM must pass authz (gate lets it through), then 404 — no such VM — not 403: %s", ErrorBody(body))
	})

	t.Run("grant does NOT carry to another VM", func(t *testing.T) {
		body, status, err := uc.do(ctx, http.MethodDelete, vmBase+vmB, nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusForbidden, status,
			"delete of a VM the user holds no grant on must be 403: %s", ErrorBody(body))
	})

	t.Run("the grant lists at resource scope tagged 'resource'", func(t *testing.T) {
		body, status, err := owner.do(ctx, http.MethodGet, raPath, nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status)
		var resp struct {
			RoleAssignments []struct {
				PrincipalID    string `json:"principal_id"`
				ScopeType      string `json:"scope_type"`
				RoleDefinition string `json:"role_definition"`
			} `json:"role_assignments"`
		}
		require.NoError(t, json.Unmarshal(body, &resp))
		var found bool
		for _, ra := range resp.RoleAssignments {
			if ra.PrincipalID == user {
				found = true
				require.Equal(t, "resource", ra.ScopeType, "grant must be resource-scoped")
				require.Equal(t, "VirtualMachineContributor", ra.RoleDefinition)
			}
		}
		require.True(t, found, "the resource-scope grant must list under the VM")
	})
}
