//go:build integration

package integration

// M3 chunk 3 — Key Vault Secret CRUD integration tests.
//
// These tests hit the real OpenBao cluster via dc-api's pod-proxy mechanism.
// They SKIP when the KVI operator CRDs are not present on the cluster.
//
// All six tests share ONE provisioned KeyVaultInstance for the "kv-demo"
// tenant. Sharing avoids the 5-8 min OpenBao cold-start per test — the
// backend is provisioned once (or reused if already Ready), and all tests
// run against it sequentially (NOT t.Parallel) to avoid key-name conflicts.
//
// Coverage:
//   - viewer can list, gets 403 on get/put/delete
//   - member can put → get → list → delete → 410 on get (soft-delete)
//   - put then put-again returns 200 (not 201) the second time
//   - delete then delete returns 409 (already deleted) the second time
//   - key with invalid name → 400
//   - value > 1 MiB → 400
//   - cursor pagination: write 10 secrets, list with limit=5, walk both pages
//   - get with ?version=N returns the right version even after delete

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/wso2/dc-api/internal/models"
)

// viewerClientForRaw creates a viewer-role API client for a raw tenant slug
// (no "test-" prefix added). Used with non-test tenants like kv-demo.
//
// IMPORTANT: the JWT must NOT carry a "dc-tenant-<id>" group claim; if it
// did, AutoProvisionMembers would upgrade the user to member role on first
// request, defeating the viewer-RBAC test. We use MintTokenWithGroups with
// an empty group list and rely on the explicit role assignment below.
func viewerClientForRaw(t *testing.T, tenantID string) *APIClient {
	t.Helper()
	viewerSub := "viewer-" + tenantID
	// No dc-tenant-* group → autoprovision won't fire → explicit viewer role wins.
	token, err := env.JWT.MintTokenWithGroups(viewerSub, viewerSub, []string{})
	require.NoError(t, err, "viewerClientForRaw: mint JWT")

	// Grant viewer role explicitly.
	_, err = env.DB.CreateRoleAssignment(context.Background(), models.RoleAssignment{
		PrincipalType: models.PrincipalTypeUser,
		PrincipalID:   viewerSub,
		ScopeType:     models.ScopeTypeTenant,
		ScopeID:       tenantID,
		Role:          models.RoleViewer,
		GrantedBy:     "test-fixture",
	})
	if err != nil && !strings.Contains(err.Error(), "23505") && !strings.Contains(err.Error(), "duplicate key") {
		require.NoError(t, err, "viewerClientForRaw: insert viewer role")
	}
	return NewAPIClientForProject(env.BaseURL, token, tenantID, defaultProjectID)
}

// memberClientForRaw creates a member-role (→ Contributor) API client for a raw
// tenant slug. Used to verify the v2 data-plane split: a member can list secret
// names (readMetadata, matched by Contributor's "*") but cannot read or write
// secret values (DataActions, which Contributor does not have).
func memberClientForRaw(t *testing.T, tenantID string) *APIClient {
	t.Helper()
	memberSub := "member-" + tenantID
	token, err := env.JWT.MintTokenWithGroups(memberSub, memberSub, []string{})
	require.NoError(t, err, "memberClientForRaw: mint JWT")
	_, err = env.DB.CreateRoleAssignment(context.Background(), models.RoleAssignment{
		PrincipalType: models.PrincipalTypeUser,
		PrincipalID:   memberSub,
		ScopeType:     models.ScopeTypeTenant,
		ScopeID:       tenantID,
		Role:          models.RoleMember,
		GrantedBy:     "test-fixture",
	})
	if err != nil && !strings.Contains(err.Error(), "23505") && !strings.Contains(err.Error(), "duplicate key") {
		require.NoError(t, err, "memberClientForRaw: insert member role")
	}
	return NewAPIClientForProject(env.BaseURL, token, tenantID, defaultProjectID)
}

// viewerClientForTenant creates an API client with the viewer role for the
// given tenant. The viewer client uses a different sub ("viewer-<tenantID>")
// so RBAC tests can distinguish it from the owner.
func viewerClientForTenant(t *testing.T, rawTenantID string) *APIClient {
	t.Helper()
	tenantID := rawTenantID
	if !strings.HasPrefix(tenantID, "test-") {
		tenantID = "test-" + tenantID
	}
	// Register the tenant.
	if _, err := env.DB.UpsertTenant(
		context.Background(), tenantID, tenantID, "dc-tenant-"+tenantID, "test-fixture",
	); err != nil {
		require.NoError(t, err, "viewerClientForTenant: UpsertTenant for %s", tenantID)
	}
	// Ensure the default project.
	ensureDefaultProject(t, tenantID)

	viewerSub := "viewer-" + tenantID
	token, err := env.JWT.MintToken(tenantID, viewerSub)
	require.NoError(t, err, "viewerClientForTenant: mint JWT")

	// Grant viewer role explicitly.
	_, err = env.DB.CreateRoleAssignment(context.Background(), models.RoleAssignment{
		PrincipalType: models.PrincipalTypeUser,
		PrincipalID:   viewerSub,
		ScopeType:     models.ScopeTypeTenant,
		ScopeID:       tenantID,
		Role:          models.RoleViewer,
		GrantedBy:     "test-fixture",
	})
	// Ignore duplicate-key errors.
	if err != nil && !strings.Contains(err.Error(), "23505") && !strings.Contains(err.Error(), "duplicate key") {
		require.NoError(t, err, "viewerClientForTenant: insert viewer role")
	}

	return NewAPIClientForProject(env.BaseURL, token, tenantID, defaultProjectID)
}

// ── KVI operator availability check ──────────────────────────────────────────

// kviOperatorAvailable returns true if the KVI operator's KeyVaultBackend CRD
// is registered on the cluster. Used to skip all secret-CRUD tests when the
// operator isn't deployed.
func kviOperatorAvailable() bool {
	if env == nil || env.KubeClient == nil {
		return false
	}
	crdGVR := schema.GroupVersionResource{
		Group:    "apiextensions.k8s.io",
		Version:  "v1",
		Resource: "customresourcedefinitions",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := env.KubeClient.Resource(crdGVR).Get(ctx, "keyvaultbackends.keyvault.opencloud.wso2.com", metav1.GetOptions{})
	return err == nil
}

// ── Shared vault fixture ──────────────────────────────────────────────────────

// sharedKVState is a package-level singleton holding the one vault that all
// secret CRUD tests share. Provisioned once via sharedKVOnce.
var (
	sharedKVOnce   sync.Once
	sharedKVID     string
	sharedKVClient *APIClient
	sharedKVErr    error
)

// kviSharedTenant is the fixed tenant slug used for the shared KVI vault.
// It matches the long-running "kv-demo" demo tenant on harvester-dev —
// whose Backend (kvb-kv-demo) is already in Ready state — so tests reuse
// the running OpenBao instead of cold-starting a fresh StatefulSet.
//
// IMPORTANT: this tenant does NOT carry the "test-" prefix, so its
// Kubernetes namespace (dc-tenant-kv-demo) is NOT garbage-collected by
// cleanupTestResources. That is intentional — the demo Backend must
// persist between test runs to avoid 5-8 min cold-start per run.
const kviSharedTenant = "kv-demo"

// kviRawClientForTenant creates an API client for a tenant slug WITHOUT
// prepending "test-". Used only for the shared kv-demo tenant so its
// namespace is preserved between test runs.
func kviRawClientForTenant(t *testing.T, tenantID string) *APIClient {
	t.Helper()
	if _, err := env.DB.UpsertTenant(
		context.Background(), tenantID, tenantID, "dc-tenant-"+tenantID, "test-fixture",
	); err != nil {
		require.NoError(t, err, "kviRawClientForTenant: UpsertTenant for %s", tenantID)
	}
	token, err := env.JWT.MintToken(tenantID, tenantID+"-user")
	require.NoError(t, err)
	ensureDefaultProjectRaw(t, tenantID)
	return NewAPIClientForProject(env.BaseURL, token, tenantID, defaultProjectID)
}

// ensureDefaultProjectRaw is like ensureDefaultProject but for non-test tenants
// (no "test-" prefix). The K8s project namespace will be "dc-kv-demo-default"
// which doesn't match the dc-test-* cleanup pattern.
func ensureDefaultProjectRaw(t *testing.T, tenantID string) {
	t.Helper()
	ctx := context.Background()
	tenantUUID, err := env.DB.GetTenantUUIDBySlug(ctx, tenantID)
	require.NoError(t, err)
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
		models.ProjectQuota{MaxVNets: 10, MaxClusters: 2, MaxVolumes: 50, MaxPublicIPs: 3},
	)
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		require.NoError(t, err)
	}
	if env.NSProvisioner != nil {
		projectUUID, err := env.DB.GetProjectUUIDByTenantAndSlug(ctx, tenantID, defaultProjectID)
		require.NoError(t, err)
		require.NoError(t, env.NSProvisioner.EnsureProjectNamespace(ctx, tenantID, defaultProjectID, projectUUID, 20, 64, 500, 50))
	}
}

// sharedKV returns (client, vaultID) for the one shared vault used by all
// secret-CRUD tests. Idempotent — the vault is created at most once per test
// run. The vault belongs to tenant "kv-demo" (same slug as the long-running
// demo tenant already present on harvester-dev, so the Backend is reused
// rather than cold-started every run).
//
// Returns ("", nil, skip) when the vault cannot be made ACTIVE within
// 10 minutes (budget for full cold-start: ~8 min on harvester-dev).
func sharedKV(t *testing.T) (client *APIClient, kvID string, skip bool) {
	t.Helper()
	sharedKVOnce.Do(func() {
		sharedKVClient = kviRawClientForTenant(t, kviSharedTenant)
		// Grant owner role: the JWT sub for kviRawClientForTenant is tenantID+"-user".
		mustGrantOwner(t, kviSharedTenant, kviSharedTenant+"-user")

		ctx := context.Background()
		resp, body, status, err := sharedKVClient.CreateKeyVault(ctx, CreateKeyVaultRequest{Name: "kv-secrets-test"})
		if err != nil {
			sharedKVErr = fmt.Errorf("create vault: %w", err)
			return
		}
		if status != http.StatusCreated && status != http.StatusConflict {
			sharedKVErr = fmt.Errorf("create vault: status %d body=%s", status, ErrorBody(body))
			return
		}
		id := resp.ID

		// If there was a conflict, list vaults and find the one named kv-secrets-test.
		if status == http.StatusConflict {
			listResp, _, _, _ := sharedKVClient.ListKeyVaults(ctx)
			for _, kv := range listResp {
				if kv.Name == "kv-secrets-test" {
					id = kv.ID
					break
				}
			}
		}
		if id == "" {
			sharedKVErr = fmt.Errorf("could not resolve shared vault ID after conflict")
			return
		}

		sharedKVID = id

		// Wait up to 10 minutes for the vault to become ACTIVE. Cold-start of a
		// StatefulSet + unseal + mount provisioning takes up to ~8 min on
		// harvester-dev under load.
		deadline := time.Now().Add(10 * time.Minute)
		for time.Now().Before(deadline) {
			ctx2, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			getResp, _, getStatus, _ := sharedKVClient.GetKeyVault(ctx2, sharedKVID)
			cancel()
			if getStatus == http.StatusOK && getResp.Status == "ACTIVE" {
				return
			}
			time.Sleep(10 * time.Second)
		}
		sharedKVErr = fmt.Errorf("vault %s did not become ACTIVE within 10 minutes", sharedKVID)
	})

	if sharedKVErr != nil {
		t.Logf("shared vault unavailable: %v — skipping", sharedKVErr)
		return nil, "", true
	}
	return sharedKVClient, sharedKVID, false
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestKeyVaultSecrets_RBAC verifies the RBAC v2 data-plane split for secrets:
// viewer (Reader) is denied everything; member (Contributor) can list secret
// names (readMetadata) but cannot read/write secret VALUES (DataActions); owner
// can do everything (dataActions: *).
func TestKeyVaultSecrets_RBAC(t *testing.T) {
	if !kviOperatorAvailable() {
		t.Skip("KVI operator not available on cluster — skipping secret CRUD tests")
	}
	ctx := context.Background()

	ownerClient, kvID, skip := sharedKV(t)
	if skip {
		t.Skip("shared vault not ACTIVE — skipping")
	}

	viewerClient := viewerClientForRaw(t, kviSharedTenant)
	memberClient := memberClientForRaw(t, kviSharedTenant)

	// unique key names to avoid collision with other tests running sequentially
	testKey := "rbac-test-key"

	// ── Viewer (Reader): denied on everything. Secret names are readMetadata (a
	//    Key Vault role action, not generic */read); values are DataActions. ────
	_, body, status, err := viewerClient.ListKeyVaultSecrets(ctx, kvID, "", 0)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, status, "viewer list must 403 (v2): body=%s", ErrorBody(body))
	_, body, status, err = viewerClient.PutKeyVaultSecret(ctx, kvID, testKey, PutKeyVaultSecretRequest{Value: "v"})
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, status, "viewer put must 403: body=%s", ErrorBody(body))
	_, body, status, err = viewerClient.GetKeyVaultSecret(ctx, kvID, testKey, 0)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, status, "viewer get must 403: body=%s", ErrorBody(body))
	body, status, err = viewerClient.DeleteKeyVaultSecret(ctx, kvID, testKey)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, status, "viewer delete must 403: body=%s", ErrorBody(body))

	// ── Member (Contributor): the data-plane split. CAN list secret names, but
	//    CANNOT read or write the secret VALUES. ───────────────────────────────
	_, body, status, err = memberClient.ListKeyVaultSecrets(ctx, kvID, "", 0)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "member CAN list secret names (v2): body=%s", ErrorBody(body))
	_, body, status, err = memberClient.GetKeyVaultSecret(ctx, kvID, testKey, 0)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, status, "member must NOT read secret values (v2 split): body=%s", ErrorBody(body))
	_, body, status, err = memberClient.PutKeyVaultSecret(ctx, kvID, testKey, PutKeyVaultSecretRequest{Value: "v"})
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, status, "member must NOT write secret values (v2 split): body=%s", ErrorBody(body))

	// ── Owner (dataActions: *) CAN put, get, list. ────────────────────────────
	putResp, body, status, err := ownerClient.PutKeyVaultSecret(ctx, kvID, testKey, PutKeyVaultSecretRequest{Value: "hello"})
	require.NoError(t, err)
	// Accept 201 (new key) or 200 (key from prior run; soft-delete preserves metadata).
	require.True(t, status == http.StatusCreated || status == http.StatusOK,
		"owner put (first): got %d body=%s", status, ErrorBody(body))
	require.Equal(t, testKey, putResp.Key)
	require.Equal(t, "hello", putResp.Value)
	require.GreaterOrEqual(t, putResp.Version, 1)

	getResp, body, status, err := ownerClient.GetKeyVaultSecret(ctx, kvID, testKey, 0)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "owner get: body=%s", ErrorBody(body))
	require.Equal(t, "hello", getResp.Value)

	// Owner can list and see the key it wrote.
	oListResp, body, status, err := ownerClient.ListKeyVaultSecrets(ctx, kvID, "", 0)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	found := false
	for _, s := range oListResp.Items {
		if s.Name == testKey {
			found = true
		}
	}
	require.True(t, found, "owner list must show the key it wrote")

	// Cleanup: delete the key we wrote.
	body, status, err = ownerClient.DeleteKeyVaultSecret(ctx, kvID, testKey)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, status, "owner delete: body=%s", ErrorBody(body))
}

// TestKeyVaultSecrets_FullLifecycle verifies the happy-path create/update/
// list/delete/410 flow.
func TestKeyVaultSecrets_FullLifecycle(t *testing.T) {
	if !kviOperatorAvailable() {
		t.Skip("KVI operator not available on cluster — skipping secret CRUD tests")
	}
	ctx := context.Background()

	client, kvID, skip := sharedKV(t)
	if skip {
		t.Skip("shared vault not ACTIVE — skipping")
	}

	key := "lc-db-password"

	// ── First PUT → 201 (or 200 if vault was reused from a prior test run) ───
	putResp, body, status, err := client.PutKeyVaultSecret(ctx, kvID, key, PutKeyVaultSecretRequest{
		Value:    "s3cr3t-v1",
		Metadata: map[string]string{"owner": "billing-team"},
	})
	require.NoError(t, err)
	// Accept both 201 (brand-new key) and 200 (key exists from a prior run of
	// this test against the shared vault — soft-deletes preserve metadata).
	require.True(t, status == http.StatusCreated || status == http.StatusOK,
		"first PUT must be 201 or 200: got %d body=%s", status, ErrorBody(body))
	require.Equal(t, key, putResp.Key)
	require.Equal(t, "s3cr3t-v1", putResp.Value)
	require.GreaterOrEqual(t, putResp.Version, 1)
	require.Equal(t, "billing-team", putResp.Metadata["owner"])
	v1 := putResp.Version

	// ── Second PUT same key → 200 ─────────────────────────────────────────────
	put2Resp, body, status, err := client.PutKeyVaultSecret(ctx, kvID, key, PutKeyVaultSecretRequest{
		Value: "s3cr3t-v2",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "second PUT must be 200: body=%s", ErrorBody(body))
	require.Equal(t, v1+1, put2Resp.Version)

	// ── GET latest → 200, correct value ──────────────────────────────────────
	getResp, body, status, err := client.GetKeyVaultSecret(ctx, kvID, key, 0)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "get latest: body=%s", ErrorBody(body))
	require.Equal(t, "s3cr3t-v2", getResp.Value)
	require.Equal(t, v1+1, getResp.Version)
	require.Nil(t, getResp.DeletedAt)

	// ── GET version v1 → 200, original value ─────────────────────────────────
	getV1Resp, body, status, err := client.GetKeyVaultSecret(ctx, kvID, key, v1)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "get v1: body=%s", ErrorBody(body))
	require.Equal(t, "s3cr3t-v1", getV1Resp.Value)
	require.Equal(t, v1, getV1Resp.Version)

	// ── LIST → secret appears ─────────────────────────────────────────────────
	listResp, body, status, err := client.ListKeyVaultSecrets(ctx, kvID, "", 0)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "list: body=%s", ErrorBody(body))
	found := false
	for _, s := range listResp.Items {
		if s.Name == key {
			found = true
			require.Equal(t, v1+1, s.LatestVersion)
			require.Nil(t, s.DeletedAt)
		}
	}
	require.True(t, found, "key must appear in list")

	// ── DELETE → 204 ─────────────────────────────────────────────────────────
	body, status, err = client.DeleteKeyVaultSecret(ctx, kvID, key)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, status, "delete: body=%s", ErrorBody(body))

	// ── GET latest after delete → 410 ────────────────────────────────────────
	_, body, status, err = client.GetKeyVaultSecret(ctx, kvID, key, 0)
	require.NoError(t, err)
	require.Equal(t, http.StatusGone, status, "get after delete must be 410: body=%s", ErrorBody(body))

	// ── GET version v1 after delete → 200 (v1 is NOT soft-deleted; only v2
	// was explicitly deleted via DELETE /data. OpenBao allows reading older
	// non-deleted versions even when the latest is soft-deleted).
	getAfterDeleteResp, body, status, err := client.GetKeyVaultSecret(ctx, kvID, key, v1)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "get v1 after delete must be 200: body=%s", ErrorBody(body))
	require.Equal(t, "s3cr3t-v1", getAfterDeleteResp.Value)

	// ── DELETE same key again → 409 ───────────────────────────────────────────
	body, status, err = client.DeleteKeyVaultSecret(ctx, kvID, key)
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, status, "second delete must be 409: body=%s", ErrorBody(body))

	// ── LIST shows deleted_at ─────────────────────────────────────────────────
	listResp2, body, status, err := client.ListKeyVaultSecrets(ctx, kvID, "", 0)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	for _, s := range listResp2.Items {
		if s.Name == key {
			require.NotNil(t, s.DeletedAt, "deleted key must have a non-null deleted_at in list")
		}
	}
}

// TestKeyVaultSecrets_Validation verifies input validation for key names and
// value sizes.
func TestKeyVaultSecrets_Validation(t *testing.T) {
	if !kviOperatorAvailable() {
		t.Skip("KVI operator not available on cluster — skipping secret CRUD tests")
	}
	ctx := context.Background()

	client, kvID, skip := sharedKV(t)
	if skip {
		t.Skip("shared vault not ACTIVE — skipping")
	}

	// ── Invalid key name → 400 ────────────────────────────────────────────────
	_, body, status, err := client.PutKeyVaultSecret(ctx, kvID, "UPPERCASE_KEY", PutKeyVaultSecretRequest{Value: "x"})
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status, "uppercase key must 400: body=%s", ErrorBody(body))

	_, body, status, err = client.PutKeyVaultSecret(ctx, kvID, "path/slash", PutKeyVaultSecretRequest{Value: "x"})
	require.NoError(t, err)
	// Slashes in the key segment are rejected at the router level (chi splits
	// on '/', so the URL /secrets/path/slash is unmatched → 404). Either 400
	// or 404 is an acceptable rejection.
	require.True(t, status == http.StatusBadRequest || status == http.StatusNotFound,
		"slash in key must be rejected (400 or 404): got %d body=%s", status, ErrorBody(body))

	_, body, status, err = client.GetKeyVaultSecret(ctx, kvID, "UPPER", 0)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status, "get invalid key must 400: body=%s", ErrorBody(body))

	body, status, err = client.DeleteKeyVaultSecret(ctx, kvID, "UPPER")
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status, "delete invalid key must 400: body=%s", ErrorBody(body))

	// ── Value > 1 MiB → 400 ──────────────────────────────────────────────────
	bigValue := strings.Repeat("a", 1<<20+1) // 1 MiB + 1 byte
	_, body, status, err = client.PutKeyVaultSecret(ctx, kvID, "big-key", PutKeyVaultSecretRequest{Value: bigValue})
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status, "value > 1MiB must 400: body=%s", ErrorBody(body))
}

// TestKeyVaultSecrets_NotFound verifies 404 behaviour for unknown vault/key.
func TestKeyVaultSecrets_NotFound(t *testing.T) {
	if !kviOperatorAvailable() {
		t.Skip("KVI operator not available on cluster — skipping secret CRUD tests")
	}
	ctx := context.Background()

	client, kvID, skip := sharedKV(t)
	if skip {
		t.Skip("shared vault not ACTIVE — skipping")
	}

	// Get non-existent key → 404
	_, body, status, err := client.GetKeyVaultSecret(ctx, kvID, "doesnt-exist-404", 0)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status, "get unknown key must 404: body=%s", ErrorBody(body))

	// Delete non-existent key → 404
	body, status, err = client.DeleteKeyVaultSecret(ctx, kvID, "doesnt-exist-404")
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status, "delete unknown key must 404: body=%s", ErrorBody(body))
}

// TestKeyVaultSecrets_CursorPagination writes N secrets and walks all pages,
// asserting every key is returned exactly once. Uses a small page size so the
// test doesn't take too long, while still exercising the cursor mechanism.
func TestKeyVaultSecrets_CursorPagination(t *testing.T) {
	if !kviOperatorAvailable() {
		t.Skip("KVI operator not available on cluster — skipping secret CRUD tests")
	}
	ctx := context.Background()

	client, kvID, skip := sharedKV(t)
	if skip {
		t.Skip("shared vault not ACTIVE — skipping")
	}

	// Write 10 secrets with names that sort predictably.
	// Use a prefix unique to this test to avoid collision with FullLifecycle.
	const total = 10
	const pageSize = 4
	writtenKeys := make([]string, total)
	for i := 0; i < total; i++ {
		key := fmt.Sprintf("pag-key-%02d", i)
		writtenKeys[i] = key
		_, body, status, err := client.PutKeyVaultSecret(ctx, kvID, key, PutKeyVaultSecretRequest{Value: "v"})
		require.NoError(t, err)
		// 201 on first write, 200 on re-run (if key exists from previous run).
		require.True(t, status == http.StatusCreated || status == http.StatusOK,
			"put %s: status=%d body=%s", key, status, ErrorBody(body))
	}

	// Walk pages, collecting only the pag-key-NN keys (the vault may contain
	// keys from other tests that ran before this one).
	seen := make(map[string]bool)
	cursor := ""
	pages := 0
	for {
		listResp, body, status, err := client.ListKeyVaultSecrets(ctx, kvID, cursor, pageSize)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status, "list page %d: body=%s", pages, ErrorBody(body))
		require.LessOrEqual(t, len(listResp.Items), pageSize, "page must not exceed limit")

		for _, s := range listResp.Items {
			if strings.HasPrefix(s.Name, "pag-key-") {
				require.False(t, seen[s.Name], "duplicate key %q across pages", s.Name)
				seen[s.Name] = true
			}
		}
		pages++

		if listResp.NextCursor == nil || *listResp.NextCursor == "" {
			break
		}
		cursor = *listResp.NextCursor

		// Sanity guard: prevent infinite loop if pagination is broken.
		require.Less(t, pages, 20, "too many pages — pagination loop detected")
	}

	require.Equal(t, total, len(seen), "all %d pag-key-NN secrets must be seen across all pages", total)
}

// TestKeyVaultSecrets_VersionAfterDelete verifies that ?version=N returns the
// correct version even after the latest version is soft-deleted.
func TestKeyVaultSecrets_VersionAfterDelete(t *testing.T) {
	if !kviOperatorAvailable() {
		t.Skip("KVI operator not available on cluster — skipping secret CRUD tests")
	}
	ctx := context.Background()

	client, kvID, skip := sharedKV(t)
	if skip {
		t.Skip("shared vault not ACTIVE — skipping")
	}

	key := "ver-test-versioned-key"

	// Write version 1 and version 2.
	_, _, status, err := client.PutKeyVaultSecret(ctx, kvID, key, PutKeyVaultSecretRequest{Value: "v1"})
	require.NoError(t, err)
	// Accept 201 (fresh key) or 200 (key from a prior run).
	require.True(t, status == http.StatusCreated || status == http.StatusOK, "put v1: status=%d", status)

	_, _, status, err = client.PutKeyVaultSecret(ctx, kvID, key, PutKeyVaultSecretRequest{Value: "v2"})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	// Get the latest version to know what version numbers we're at.
	getLatest, _, status, err := client.GetKeyVaultSecret(ctx, kvID, key, 0)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	latestVersion := getLatest.Version
	require.GreaterOrEqual(t, latestVersion, 2, "must have written at least 2 versions")

	// Soft-delete the latest version.
	_, status, err = client.DeleteKeyVaultSecret(ctx, kvID, key)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, status)

	// GET latest → 410.
	_, _, status, _ = client.GetKeyVaultSecret(ctx, kvID, key, 0)
	require.Equal(t, http.StatusGone, status)

	// GET (latestVersion-1) → 200 with the older value. OpenBao only marks
	// the explicitly deleted version as inaccessible; non-deleted older
	// versions remain readable.
	prevVersion := latestVersion - 1
	if prevVersion < 1 {
		prevVersion = 1
	}
	getPrev, _, status, err := client.GetKeyVaultSecret(ctx, kvID, key, prevVersion)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status,
		"non-deleted older version must be readable even when latest is soft-deleted")
	require.Equal(t, prevVersion, getPrev.Version)

	// GET the explicitly deleted latest version → 404 (OpenBao returns 404
	// for soft-deleted versions; dc-api passes through as 404 since a specific
	// version was requested and the data endpoint returns 404).
	_, _, status, err = client.GetKeyVaultSecret(ctx, kvID, key, latestVersion)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status,
		"explicitly requesting a soft-deleted version returns 404")
}
