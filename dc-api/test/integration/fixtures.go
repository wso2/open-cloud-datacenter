//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/wso2/dc-api/internal/db"
	"github.com/wso2/dc-api/internal/models"
)

// randomName returns a unique DNS-safe resource name prefixed with "test-".
func randomName(suffix string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("test-%x-%s", b, suffix)
}

// randomTenantID returns a unique tenant slug for tests that satisfies the
// production tenant-id validation `^[a-z][a-z0-9-]{0,30}[a-z0-9]$` (max 32
// chars). randomName() can't be used for tenants: its `test-<8hex>-<suffix>`
// form, concatenated after a descriptive prefix, overflows 32 chars and is
// rejected with HTTP 400. The "test-" prefix is preserved (cleanup and the
// HasPrefix helpers rely on it); the label is truncated and the random token
// kept to 6 hex chars so the total never exceeds 32.
func randomTenantID(label string) string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	const maxLabel = 32 - len("test-") - len("-") - 6 // 20
	if len(label) > maxLabel {
		label = label[:maxLabel]
	}
	return fmt.Sprintf("test-%s-%x", label, b)
}

// defaultProjectID is the project slug created by clientForTenant for every
// test tenant. Tests that don't care about project granularity use this.
const defaultProjectID = "default"

// clientForTenant returns an API client authenticated as the given tenantID,
// bound to a default project named "default".
//
// Auto-prefixes the tenantID with "test-" if not already so the resulting
// kubeovn-driver-managed namespace ("dc-test-<...>") matches the suite's
// scrub pattern. Without this, namespaces like "dc-tenant-vnet-lc" leak
// past test cleanup.
//
// The returned client is bound to the tenant AND to the default project, so
// all resource calls (vnets, subnets, security-groups, etc.) are routed
// through /v1/tenants/{tenantID}/projects/default/... by the client helpers.
func clientForTenant(t *testing.T, tenantID string) *APIClient {
	t.Helper()
	if !strings.HasPrefix(tenantID, "test-") {
		tenantID = "test-" + tenantID
	}
	// Phase 6a: TenantContext refuses any /v1/tenants/{tid}/... request
	// whose slug isn't registered in the `tenants` table. Tests that mint a
	// JWT used to rely on autoprovision in middleware to seed both
	// `role_assignments` AND the `tenants` registry; the invite-based model removed that
	// path for production, so the test helper UPSERTs explicitly here.
	if _, err := env.DB.UpsertTenant(
		context.Background(), tenantID, tenantID, "dc-tenant-"+tenantID, "test-fixture",
	); err != nil {
		require.NoError(t, err, "UpsertTenant for %s", tenantID)
	}
	token, err := env.JWT.MintToken(tenantID, tenantID+"-user")
	require.NoError(t, err, "mint JWT for tenant %s", tenantID)

	// Ensure the default project exists so resource calls that need a project
	// context succeed. CreateProject is idempotent via ErrProjectAlreadyExists.
	ensureDefaultProject(t, tenantID)

	return NewAPIClientForProject(env.BaseURL, token, tenantID, defaultProjectID)
}

// ensureDefaultProject creates the "default" project for a tenant if it does
// not already exist. Uses the repo directly so there is no HTTP round-trip
// (and no dependency on owner role assignments being present yet). Idempotent:
// ErrProjectAlreadyExists is silently swallowed.
//
// Capacity: 20 cpu / 64 GB RAM / 500 GB storage — well within the schema
// default tenant cap (80/256/2000). Tests that exercise cap enforcement must
// use a dedicated tenant (not reuse the "default" one) so their specific cap
// values don't conflict with this default project's allocation.
func ensureDefaultProject(t *testing.T, tenantID string) {
	t.Helper()
	ctx := context.Background()
	tenantUUID, err := env.DB.GetTenantUUIDBySlug(ctx, tenantID)
	require.NoError(t, err, "ensureDefaultProject: GetTenantUUIDBySlug(%q)", tenantID)
	require.NotEqual(t, uuid.Nil, tenantUUID, "ensureDefaultProject: tenantUUID is nil for %q", tenantID)

	_, _, _, err = env.DB.CreateProject(ctx,
		models.Project{
			ID:         defaultProjectID,
			TenantID:   tenantID,
			TenantUUID: tenantUUID,
			Name:       "Default project",
			CPUCores:   20,
			MemoryGB:   64,
			StorageGB:  500,
			CreatedBy:  "test-fixture",
		},
		models.ProjectQuota{
			MaxVNets:    10,
			MaxClusters: 2,
			MaxVolumes:  50,
			MaxPublicIPs: 3,
		},
	)
	if err != nil && !errors.Is(err, db.ErrProjectAlreadyExists) {
		require.NoError(t, err, "ensureDefaultProject: CreateProject for %q", tenantID)
	}

	// Create the K8s project namespace (+ ResourceQuota) on harvester-dev.
	// CreateProject only writes the DB row; without this call, downstream
	// provider operations (CreateSubnet's NAD create, VM launch) fail with
	// "namespace dc-<tenant>-<project> not found". Idempotent.
	if env.NSProvisioner != nil {
		projectUUID, err := env.DB.GetProjectUUIDByTenantAndSlug(ctx, tenantID, defaultProjectID)
		require.NoError(t, err, "ensureDefaultProject: GetProjectUUIDByTenantAndSlug")
		if err := env.NSProvisioner.EnsureProjectNamespace(ctx, tenantID, defaultProjectID, projectUUID, 20, 64, 500, 50); err != nil {
			require.NoError(t, err, "ensureDefaultProject: EnsureProjectNamespace")
		}
	}
}

// projectUUIDFor resolves a (tenantID, projectSlug) pair to its immutable UUID
// via the DB. The project must already be registered. Fails the test if the
// lookup returns uuid.Nil or an error.
func projectUUIDFor(t *testing.T, tenantID, projectID string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	u, err := env.DB.GetProjectUUIDByTenantAndSlug(ctx, tenantID, projectID)
	require.NoError(t, err, "projectUUIDFor(%q, %q): DB lookup failed", tenantID, projectID)
	require.NotEqual(t, uuid.Nil, u,
		"projectUUIDFor(%q, %q): returned nil UUID — is the project registered?", tenantID, projectID)
	return u
}

// WaitVNetActive polls until the VNet status is ACTIVE.
func WaitVNetActive(t *testing.T, client *APIClient, vnetID string) {
	t.Helper()
	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, status, _ := client.GetVNet(ctx, vnetID)
		return status == http.StatusOK && resp.Status == "ACTIVE"
	}, 90*time.Second, 2*time.Second, "VNet %s did not become ACTIVE within 90s", vnetID)
}

// WaitVNetGone polls until GET /v1/vnets/{id} returns 404.
func WaitVNetGone(t *testing.T, client *APIClient, vnetID string) {
	t.Helper()
	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, status, _ := client.GetVNet(ctx, vnetID)
		return status == http.StatusNotFound
	}, 3*time.Minute, 3*time.Second, "VNet %s still present after 3m", vnetID)
}

// WaitVNetGoneBestEffort is the cleanup-side variant: it polls the same
// 3m budget but only logs on timeout instead of failing the test. F24's
// cluster sweep runs after this, so a slow API DELETE doesn't need to
// fail the test — the sweep guarantees the cluster is clean either way.
func WaitVNetGoneBestEffort(t *testing.T, client *APIClient, vnetID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, status, _ := client.GetVNet(ctx, vnetID)
		cancel()
		if status == http.StatusNotFound {
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Logf("cleanup: VNet %s still present in dc-api after 3m — falling back to direct cluster sweep", vnetID)
}

// WaitSubnetActive polls until a subnet becomes ACTIVE.
func WaitSubnetActive(t *testing.T, client *APIClient, vnetID, subnetID string) {
	t.Helper()
	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, status, _ := client.GetSubnet(ctx, vnetID, subnetID)
		return status == http.StatusOK && resp.Status == "ACTIVE"
	}, 90*time.Second, 2*time.Second, "Subnet %s did not become ACTIVE within 90s", subnetID)
}

// WaitSubnetGone polls until GET subnet returns 404.
func WaitSubnetGone(t *testing.T, client *APIClient, vnetID, subnetID string) {
	t.Helper()
	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, status, _ := client.GetSubnet(ctx, vnetID, subnetID)
		return status == http.StatusNotFound
	}, 3*time.Minute, 3*time.Second, "Subnet %s still present after 3m", subnetID)
}

// WaitPeeringActive polls until a peering becomes ACTIVE.
func WaitPeeringActive(t *testing.T, client *APIClient, vnetID, peeringID string) {
	t.Helper()
	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, status, _ := client.GetPeering(ctx, vnetID, peeringID)
		return status == http.StatusOK && resp.Status == "ACTIVE"
	}, 90*time.Second, 2*time.Second, "Peering %s did not become ACTIVE within 90s", peeringID)
}

// WaitPeeringGone polls until GET peering returns 404.
func WaitPeeringGone(t *testing.T, client *APIClient, vnetID, peeringID string) {
	t.Helper()
	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, status, _ := client.GetPeering(ctx, vnetID, peeringID)
		return status == http.StatusNotFound
	}, 90*time.Second, 2*time.Second, "Peering %s still present after 90s", peeringID)
}

// WaitDNSZoneActive polls until a DNS zone becomes ACTIVE.
func WaitDNSZoneActive(t *testing.T, client *APIClient, vnetID, zoneID string) {
	t.Helper()
	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, status, _ := client.GetDNSZone(ctx, vnetID, zoneID)
		return status == http.StatusOK && resp.Status == "ACTIVE"
	}, 90*time.Second, 2*time.Second, "DNS zone %s did not become ACTIVE within 90s", zoneID)
}

// WaitDNSZoneGone polls until GET dns-zone returns 404.
func WaitDNSZoneGone(t *testing.T, client *APIClient, vnetID, zoneID string) {
	t.Helper()
	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, status, _ := client.GetDNSZone(ctx, vnetID, zoneID)
		return status == http.StatusNotFound
	}, 90*time.Second, 2*time.Second, "DNS zone %s still present after 90s", zoneID)
}

// WaitKeyVaultActive polls until the vault status is ACTIVE (i.e. the KVI
// operator has reconciled the CR to Ready). With KVI wired, vaults start as
// PENDING and flip to ACTIVE when the OpenBao StatefulSet is ready.
//
// Timeout is generous (10 minutes) because a cold-start OpenBao StatefulSet
// takes 5-8 minutes on the dev cluster (image pull + init containers).
func WaitKeyVaultActive(t *testing.T, client *APIClient, kvID string) {
	t.Helper()
	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, _, status, _ := client.GetKeyVault(ctx, kvID)
		return status == http.StatusOK && resp.Status == "ACTIVE"
	}, 10*time.Minute, 10*time.Second, "KeyVault %s did not become ACTIVE within 10 minutes", kvID)
}

// WaitVMActive polls until the VM status is ACTIVE and has an IP address.
// Returns the ACTIVE VMResponse (with IPAddress populated).
func WaitVMActive(t *testing.T, client *APIClient, vmID string) VMResponse {
	t.Helper()
	var last VMResponse
	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, status, _ := client.GetVM(ctx, vmID)
		last = resp
		return status == http.StatusOK && resp.Status == "ACTIVE" && resp.IPAddress != ""
	}, 10*time.Minute, 10*time.Second, "VM %s did not become ACTIVE with an IP within 10m (last status: %s, ip: %q)", vmID, last.Status, last.IPAddress)
	return last
}

// WaitVMGone polls until GET /v1/virtual-machines/{id} returns 404.
func WaitVMGone(t *testing.T, client *APIClient, vmID string) {
	t.Helper()
	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, status, _ := client.GetVM(ctx, vmID)
		return status == http.StatusNotFound
	}, 10*time.Minute, 15*time.Second, "VM %s still present after 10m", vmID)
}

// mustCreateActiveVNet creates a VNet, waits for ACTIVE, and registers cleanup.
//
// Cleanup chain:
//  1. Issue the API DELETE (best-effort).
//  2. Poll for the API to confirm DELETE (best-effort, swallow the assertion
//     so a slow teardown doesn't fail the test on an already-passing path).
//  3. Direct cluster sweep of the kubeovn Vpc + Subnets — this is the
//     belt-and-braces step. Even if step 1/2 timed out, this clears the
//     OVN side so the next test run inherits a clean cluster (see F24).
func mustCreateActiveVNet(t *testing.T, client *APIClient, name, cidr, region string) string {
	t.Helper()
	ctx := context.Background()
	resp, _, status, err := client.CreateVNet(ctx, CreateVNetRequest{
		Name: name, AddressSpace: []string{cidr}, Region: region,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status, "CreateVNet returned %d", status)
	vnetID := resp.Resource.ID
	WaitVNetActive(t, client, vnetID)

	// Capture the kubeovn Vpc CRD name now, while the DB still has the row.
	// After API DELETE finishes, the row is gone and dbGetVNetBackendUID
	// would panic — but the kubeovn Vpc may still be lingering in
	// Terminating state, which is exactly what F24's sweep targets.
	backendUID := dbGetVNetBackendUID(t, vnetID)

	t.Cleanup(func() {
		ctx := context.Background()
		_, _, _ = client.DeleteVNet(ctx, vnetID)
		WaitVNetGoneBestEffort(t, client, vnetID)
		// F24: belt-and-braces cluster sweep, even when the API path succeeded.
		SweepKubeOVNVPC(t, ctx, env.KubeClient, backendUID)
	})
	return vnetID
}

// mustCreateActiveSubnet creates a Subnet, waits for ACTIVE, and registers cleanup.
func mustCreateActiveSubnet(t *testing.T, client *APIClient, vnetID, name, cidr string) string {
	t.Helper()
	ctx := context.Background()
	resp, _, status, err := client.CreateSubnet(ctx, vnetID, CreateSubnetRequest{Name: name, CIDR: cidr})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status)
	subnetID := resp.Resource.ID
	WaitSubnetActive(t, client, vnetID, subnetID)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _, _ = client.DeleteSubnet(ctx, vnetID, subnetID)
		WaitSubnetGone(t, client, vnetID, subnetID)
	})
	return subnetID
}

// dbGetVNetBackendUID retrieves the backend_uid of a VNet directly from the DB.
func dbGetVNetBackendUID(t *testing.T, vnetID string) string {
	t.Helper()
	ctx := context.Background()
	vnet, err := env.DB.GetVNetInternal(ctx, uuid.MustParse(vnetID))
	require.NoError(t, err)
	return vnet.BackendUID
}

// dbGetSubnetBackendUID retrieves the backend_uid of a Subnet from the DB.
func dbGetSubnetBackendUID(t *testing.T, subnetID string) string {
	t.Helper()
	ctx := context.Background()
	subnet, err := env.DB.GetSubnet(ctx, uuid.MustParse(subnetID))
	require.NoError(t, err)
	return subnet.BackendUID
}

// uuidMust parses a UUID string; panics on error.
func uuidMust(s string) uuid.UUID { return uuid.MustParse(s) }

// tenantUUIDFor resolves a tenant slug to its immutable UUID via the DB.
// The slug must already be registered (i.e. at least one API call against that
// tenant has been made so the tenants-registry row exists). Fails the test if
// the lookup returns uuid.Nil or an error.
func tenantUUIDFor(t *testing.T, slug string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	u, err := env.DB.GetTenantUUIDBySlug(ctx, slug)
	require.NoError(t, err, "tenantUUIDFor(%q): DB lookup failed", slug)
	require.NotEqual(t, uuid.Nil, u, "tenantUUIDFor(%q): returned nil UUID — is the tenant registered?", slug)
	return u
}

// mustGrantOwner inserts an owner role_assignment for the given principal into
// the given tenant scope. Idempotent: a duplicate-key error is silently ignored.
//
// Call this in any test that exercises DELETE handlers, which now require the
// owner role (M1.5 Chunk 4). With autoprovision=true the first request already
// creates a member row; this adds an owner row so delete requests succeed.
//
// For tests that use clientForTenant(t, "foo"), the principal_id that lands in
// the DB is the same string that clientForTenant passes as the JWT sub — which
// equals the fully-prefixed tenantID (e.g., "test-tenant-foo"). Pass that same
// string as both tenantID and userSub:
//
//	mustGrantOwner(t, "test-tenant-foo", "test-tenant-foo")
//
// The convenience wrapper mustGrantOwnerForClient below handles this pattern.
func mustGrantOwner(t *testing.T, tenantID, userSub string) {
	t.Helper()
	ctx := context.Background()
	// Phase 6a: TenantContext middleware needs a tenants row to resolve the
	// slug to its tenant_uuid. Ensure it's present before granting roles.
	if _, err := env.DB.UpsertTenant(ctx, tenantID, tenantID, "dc-tenant-"+tenantID, "test-setup"); err != nil {
		require.NoError(t, err, "mustGrantOwner: UpsertTenant failed for %s", tenantID)
	}
	_, err := env.DB.CreateRoleAssignment(ctx, models.RoleAssignment{
		PrincipalType: models.PrincipalTypeUser,
		PrincipalID:   userSub,
		ScopeType:     models.ScopeTypeTenant,
		ScopeID:       tenantID,
		Role:          models.RoleOwner,
		GrantedBy:     "test-setup",
	})
	// Unique-constraint violation is fine — the row already exists.
	if err != nil && !strings.Contains(err.Error(), "23505") && !strings.Contains(err.Error(), "duplicate key") {
		require.NoError(t, err, "mustGrantOwner: insert role_assignment failed")
	}
}

// mustGrantOwnerForClient is the convenience form for tests that use
// clientForTenant. clientForTenant sets sub = tenantID (after adding the
// "test-" prefix), so both the scope and the principal are the same string.
//
// rawTenantID is the UNPREFIXED name you pass to clientForTenant, e.g.
// "tenant-vnet-lc". The function applies the same "test-" prefix that
// clientForTenant applies, then calls mustGrantOwner.
func mustGrantOwnerForClient(t *testing.T, rawTenantID string) {
	t.Helper()
	tenantID := rawTenantID
	if !strings.HasPrefix(tenantID, "test-") {
		tenantID = "test-" + tenantID
	}
	mustGrantOwner(t, tenantID, tenantID)
}
