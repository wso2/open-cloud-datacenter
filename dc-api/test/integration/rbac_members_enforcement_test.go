//go:build integration

package integration

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRBAC_MembersAPIGrantIsEnforced proves the full path the Access page uses:
// a role granted through the members API (POST /v1/tenants/{tid}/members with a
// role_definition key) is actually enforced by the resource handlers afterwards.
//
// A Reader can read but is denied a write; a Contributor on the same tenant is
// allowed the write. Cluster-free: the authorization decision happens in the
// handler's requireAction gate, before any provider call.
func TestRBAC_MembersAPIGrantIsEnforced(t *testing.T) {
	ctx := context.Background()
	tid := randomTenantID("mem-enf")

	owner := clientForTenant(t, tid)
	mustGrantOwnerForClient(t, tid)

	// Grant a role through the members API (exactly what the Access page does),
	// then return a client authenticated as that newly-added member.
	invite := func(sub, roleDefinition string) *APIClient {
		t.Helper()
		_, raw, status, err := owner.InviteMember(ctx, tid, sub, roleDefinition)
		require.NoError(t, err)
		require.Equal(t, http.StatusCreated, status,
			"invite %s as %s: %s", sub, roleDefinition, ErrorBody(raw))
		tok, err := env.JWT.MintTokenWithGroups(sub, sub, []string{"dc-tenant-" + tid})
		require.NoError(t, err)
		return NewAPIClientForProject(env.BaseURL, tok, tid, defaultProjectID)
	}

	reader := invite("test-reader-"+tid, "Reader")
	contributor := invite("test-contrib-"+tid, "Contributor")

	t.Run("reader can read", func(t *testing.T) {
		_, status, err := reader.ListVNets(ctx)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status, "Reader must be able to list VNets")
	})

	t.Run("reader is denied a write", func(t *testing.T) {
		_, raw, status, err := reader.CreateVNet(ctx, CreateVNetRequest{
			Name: "rdr-denied", AddressSpace: []string{"10.61.0.0/16"}, Region: "lk",
		})
		require.NoError(t, err)
		require.Equal(t, http.StatusForbidden, status,
			"Reader must be denied VNet create: %s", ErrorBody(raw))
	})

	t.Run("contributor is allowed the same write", func(t *testing.T) {
		// 202 Accepted: the requireAction gate passes; the (nop) provider runs
		// asynchronously, so the synchronous decision is what we assert.
		_, raw, status, err := contributor.CreateVNet(ctx, CreateVNetRequest{
			Name: "ctr-allowed", AddressSpace: []string{"10.62.0.0/16"}, Region: "lk",
		})
		require.NoError(t, err)
		require.Equal(t, http.StatusAccepted, status,
			"Contributor must be allowed VNet create: %s", ErrorBody(raw))
	})
}

// TestRBAC_PerTypeRoleScopesReads locks in the read-leak fix: a per-resource-type
// role grants reads ONLY for the types its actions cover. A Virtual Machine
// Contributor (compute/virtualMachines/*, compute/images/read, network/*/read) can
// list VMs and networks but is denied listing clusters — for which it holds no
// read action. Before the router gated reads, it could list every type in the
// tenant.
func TestRBAC_PerTypeRoleScopesReads(t *testing.T) {
	ctx := context.Background()
	tid := randomTenantID("per-type")

	owner := clientForTenant(t, tid)
	mustGrantOwnerForClient(t, tid)

	sub := "test-vmc-" + tid
	_, raw, status, err := owner.InviteMember(ctx, tid, sub, "VirtualMachineContributor")
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, "invite VMContributor: %s", ErrorBody(raw))
	tok, err := env.JWT.MintTokenWithGroups(sub, sub, []string{"dc-tenant-" + tid})
	require.NoError(t, err)
	vmc := NewAPIClientForProject(env.BaseURL, tok, tid, defaultProjectID)

	base := "/v1/tenants/" + tid + "/projects/" + defaultProjectID

	t.Run("can read VMs (role grants compute/virtualMachines/*)", func(t *testing.T) {
		body, status, err := vmc.do(ctx, http.MethodGet, base+"/virtual-machines", nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status, "body: %s", ErrorBody(body))
	})
	t.Run("can read networks (role grants network/*/read)", func(t *testing.T) {
		body, status, err := vmc.do(ctx, http.MethodGet, base+"/vnets", nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status, "body: %s", ErrorBody(body))
	})
	t.Run("CANNOT read clusters (holds no compute/clusters/read)", func(t *testing.T) {
		body, status, err := vmc.do(ctx, http.MethodGet, base+"/clusters", nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusForbidden, status,
			"VM Contributor must be denied listing clusters: %s", ErrorBody(body))
	})
}
