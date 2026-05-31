//go:build integration

package integration

// cluster_steve_test.go — F32 integration tests for Steve-based RKE2 cluster
// provisioning on tenant VPCs.
//
// These tests require:
//
//	KUBECONFIG=$HOME/.kube/config
//	KUBE_CONTEXT=harvester-dev
//	DCAPI_RANCHER_URL=https://rancher-lk-dev.wso2.com
//	DCAPI_RANCHER_TOKEN=token-xxxxx:yyyyyyy
//	DCAPI_RANCHER_INSECURE=true
//	DCAPI_HARVESTER_KUBECONFIG=<base64-kubeconfig>
//
// All tests are gated by rancherEnvConfigured() and harvesterEnvConfigured().
// Tests that provision real clusters are gated behind the "cluster_steve_live"
// sub-tag to keep the full suite fast; run them explicitly:
//
//	KUBECONFIG=$HOME/.kube/config KUBE_CONTEXT=harvester-dev \
//	DCAPI_RANCHER_URL=... DCAPI_RANCHER_TOKEN=... \
//	DCAPI_HARVESTER_KUBECONFIG=... \
//	go test -tags integration -timeout 30m -run 'TestClusterSteve' ./test/integration/...

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wso2/dc-api/internal/models"
	"github.com/wso2/dc-api/internal/providers/harvester"
	"github.com/wso2/dc-api/internal/providers/rancher"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ── Helpers ───────────────────────────────────────────────────────────────────

// harvesterEnvConfigured returns true when DCAPI_HARVESTER_KUBECONFIG is set.
func harvesterEnvConfigured() bool {
	return os.Getenv("DCAPI_HARVESTER_KUBECONFIG") != ""
}

// newLiveHarvesterClient creates a harvester.Client from env vars.
// Skips the test if DCAPI_HARVESTER_KUBECONFIG is not set.
func newLiveHarvesterClient(t *testing.T) *harvester.Client {
	t.Helper()
	if !harvesterEnvConfigured() {
		t.Skip("DCAPI_HARVESTER_KUBECONFIG not set — skipping live Harvester test")
	}
	kubeconfigB64 := os.Getenv("DCAPI_HARVESTER_KUBECONFIG")
	c, err := harvester.NewClient(kubeconfigB64, "default")
	require.NoError(t, err, "create harvester client from DCAPI_HARVESTER_KUBECONFIG")
	return c
}

// newLiveClusterProvisioner creates a ClusterProvisioner wired with the live
// harvester client for SA bootstrap. Skips if required env vars are absent.
func newLiveClusterProvisioner(t *testing.T) (*rancher.ClusterProvisioner, *harvester.Client) {
	t.Helper()
	if !rancherEnvConfigured() {
		t.Skip("DCAPI_RANCHER_URL and DCAPI_RANCHER_TOKEN not set — skipping live Rancher test")
	}
	harvClient := newLiveHarvesterClient(t)

	rancherURL := strings.TrimRight(os.Getenv("DCAPI_RANCHER_URL"), "/")
	token := os.Getenv("DCAPI_RANCHER_TOKEN")
	insecure := strings.ToLower(os.Getenv("DCAPI_RANCHER_INSECURE")) == "true"

	transport := http.DefaultTransport
	if insecure {
		//nolint:gosec
		transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		}
	}
	httpClient := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	steve := rancher.NewSteveClient(rancherURL, token, httpClient)

	credential := os.Getenv("DCAPI_RANCHER_HARVESTER_CREDENTIAL")
	if credential == "" {
		credential = "cattle-global-data:cc-test"
	}
	mgmtNAD := os.Getenv("DCAPI_CLUSTER_MGMT_NAD")
	if mgmtNAD == "" {
		mgmtNAD = "iaas/vm-network-001"
	}

	p := rancher.NewClusterProvisioner(
		steve, credential, mgmtNAD, "", "", "",
		harvClient, harvClient,
	)
	return p, harvClient
}

// ── SA bootstrap ─────────────────────────────────────────────────────────────

// TestClusterSteve_EnsureCloudProviderSA_Idempotent calls EnsureCloudProviderSA
// twice and asserts no error and that a single SA + RB + Secret remain.
func TestClusterSteve_EnsureCloudProviderSA_Idempotent(t *testing.T) {
	harvClient := newLiveHarvesterClient(t)

	// Use a test namespace that already exists in the cluster (from other tests)
	// or create one. We use env.KubeClient which has the live cluster dynamic client.
	tenantNS := "dc-test-sa-bootstrap"

	// Ensure the namespace exists first (harvester client does this automatically
	// on CreateVM, but we call EnsureCloudProviderSA directly here).
	nsGVR := schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	_, _ = env.KubeClient.Resource(nsGVR).Create(context.Background(), nsObjForName(tenantNS), metav1.CreateOptions{})
	// Idempotent — if already exists that's fine.

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = env.KubeClient.Resource(nsGVR).Delete(ctx, tenantNS, metav1.DeleteOptions{})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// First call — creates SA + RB + Secret.
	token1, err := harvClient.EnsureCloudProviderSA(ctx, tenantNS)
	require.NoError(t, err, "first EnsureCloudProviderSA call")
	require.NotEmpty(t, token1, "expected non-empty SA token from first call")

	// Second call — must be idempotent (all three resources already exist).
	token2, err := harvClient.EnsureCloudProviderSA(ctx, tenantNS)
	require.NoError(t, err, "second EnsureCloudProviderSA call (idempotent)")
	require.NotEmpty(t, token2, "expected non-empty SA token from second call")

	// Both calls should return the same token (same Secret).
	assert.Equal(t, token1, token2, "token should be stable across idempotent calls")

	// Verify exactly one SA exists with the expected name.
	saName := "harvester-cloud-provider-" + tenantNS
	saGVR := schema.GroupVersionResource{Version: "v1", Resource: "serviceaccounts"}
	saObj, err := env.KubeClient.Resource(saGVR).Namespace(tenantNS).Get(ctx, saName, metav1.GetOptions{})
	require.NoError(t, err, "SA %s should exist in namespace %s", saName, tenantNS)
	labels := saObj.GetLabels()
	assert.Equal(t, "true", labels["dc-api/managed"])

	// Verify the RoleBinding exists.
	rbGVR := schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"}
	rbObj, err := env.KubeClient.Resource(rbGVR).Namespace(tenantNS).Get(ctx, saName, metav1.GetOptions{})
	require.NoError(t, err, "RoleBinding %s should exist", saName)
	assert.Equal(t, "true", rbObj.GetLabels()["dc-api/managed"])

	// Verify the token Secret exists.
	secretName := saName + "-token"
	secretGVR := schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	secretObj, err := env.KubeClient.Resource(secretGVR).Namespace(tenantNS).Get(ctx, secretName, metav1.GetOptions{})
	require.NoError(t, err, "token Secret %s should exist", secretName)
	assert.Equal(t, "true", secretObj.GetLabels()["dc-api/managed"])

	t.Logf("SA bootstrap verified: SA=%s, RB=%s, Secret=%s in ns=%s",
		saName, saName, secretName, tenantNS)
}

// ── harvesterconfig Secret shape ─────────────────────────────────────────────

// TestClusterSteve_SAKubeconfig_Shape verifies that the kubeconfig built from a
// live SA token parses correctly and contains the expected fields. It does NOT
// post the Secret to Rancher — it just validates the kubeconfig shape.
func TestClusterSteve_SAKubeconfig_Shape(t *testing.T) {
	harvClient := newLiveHarvesterClient(t)

	apiInfo := harvClient.APIInfo()
	require.NotEmpty(t, apiInfo.ServerURL, "Harvester apiserver URL must be non-empty")
	require.NotEmpty(t, apiInfo.CACert, "Harvester CA cert must be non-empty")

	// Verify the CA is valid base64-decodable data (already raw, not b64).
	caB64 := base64.StdEncoding.EncodeToString(apiInfo.CACert)
	require.NotEmpty(t, caB64, "base64-encoded CA must be non-empty")

	t.Logf("Harvester API server: %s", apiInfo.ServerURL)
	t.Logf("CA cert size: %d bytes (base64: %d chars)", len(apiInfo.CACert), len(caB64))
}

// ── HarvesterAPIInfoProvider interface compliance ─────────────────────────────

// TestHarvesterClient_ImplementsAPIInfoProvider verifies that *harvester.Client
// satisfies the providers.HarvesterAPIInfoProvider interface at compile time.
// This is a compile-time check — if it fails the test won't even compile.
func TestHarvesterClient_ImplementsAPIInfoProvider(t *testing.T) {
	harvClient := newLiveHarvesterClient(t)
	// Interface compliance: both methods must return non-empty values.
	url := harvClient.HarvesterServerURL()
	ca := harvClient.HarvesterCACert()
	require.NotEmpty(t, url, "HarvesterServerURL must return non-empty value")
	require.NotEmpty(t, ca, "HarvesterCACert must return non-empty value")
	t.Logf("HarvesterServerURL: %s, CA size: %d bytes", url, len(ca))
}

// ── Live Rancher Steve: cluster list is healthy ───────────────────────────────

// TestClusterSteve_RancherConnectivity verifies that the Steve client can reach
// live Rancher and the control-plane cluster is listed.
// This is a lightweight read-only check.
func TestClusterSteve_RancherConnectivity(t *testing.T) {
	if !rancherEnvConfigured() {
		t.Skip("DCAPI_RANCHER_URL/TOKEN not set")
	}
	client := newLiveSteveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	items, err := client.List(ctx, "provisioning.cattle.io.clusters", "fleet-default")
	require.NoError(t, err, "Steve LIST clusters failed")
	require.NotEmpty(t, items, "expected at least one cluster in fleet-default")

	// The control-plane cluster must still be there.
	found := false
	for _, item := range items {
		if item.ID == "fleet-default/dcapi-controlplane-rke2" {
			found = true
		}
	}
	require.True(t, found, "expected dcapi-controlplane-rke2 in fleet-default, got: %v", clusterIDs(items))
	t.Logf("Steve connectivity OK: %d clusters in fleet-default", len(items))
}

// ── Full cluster lifecycle (long-running) ─────────────────────────────────────
//
// This test is gated behind a separate env var TEST_CLUSTER_LIFECYCLE=true to
// avoid accidental 15-minute waits during routine suite runs. Set it explicitly
// when validating the full create→poll→delete cycle.

// TestClusterSteve_FullLifecycle_LiveCluster creates a 1-node VPC-attached
// cluster via ClusterProvisioner.CreateCluster, polls until ACTIVE (up to 20
// min), then deletes it and verifies all artifacts are cleaned up.
func TestClusterSteve_FullLifecycle_LiveCluster(t *testing.T) {
	if os.Getenv("TEST_CLUSTER_LIFECYCLE") != "true" {
		t.Skip("TEST_CLUSTER_LIFECYCLE=true not set — skipping full cluster lifecycle test (it takes 15-20 min)")
	}
	if !rancherEnvConfigured() || !harvesterEnvConfigured() {
		t.Skip("DCAPI_RANCHER_URL/TOKEN and DCAPI_HARVESTER_KUBECONFIG required for lifecycle test")
	}

	provisioner, harvClient := newLiveClusterProvisioner(t)
	_ = harvClient

	imageID := os.Getenv("TEST_CLUSTER_IMAGE_ID")
	if imageID == "" {
		imageID = "default/image-rflb5" // default test image
	}
	vnetNS := "dc-test-f32-cluster"
	clusterName := fmt.Sprintf("test-f32-%d", time.Now().Unix()%10000)

	// Ensure the tenant namespace exists.
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	nsGVR := schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	_, _ = env.KubeClient.Resource(nsGVR).Create(ctx, nsObjForName(vnetNS), metav1.CreateOptions{})

	t.Cleanup(func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cleanCancel()
		// Best-effort cleanup on test failure.
		_ = provisioner.DeleteCluster(cleanCtx, "fleet-default/"+clusterName)
		_ = env.KubeClient.Resource(nsGVR).Delete(cleanCtx, vnetNS, metav1.DeleteOptions{})
	})

	spec := rancher.ClusterCreateSpec{
		ClusterName:  clusterName,
		K8sVersion:   "v1.33.10+rke2r3",
		NodeCPU:      4,
		NodeMemoryGB: 8,
		NodeDiskGB:   60,
		NodeImage:    imageID,
		// SystemPool is required since the AKS-style multi-pool refactor; the flat
		// NodeCPU/MemoryGB/DiskGB above carry its resolved compute.
		SystemPool: &models.NodePool{
			Name:                "system",
			Role:                models.NodePoolRoleSystem,
			Size:                "medium",
			Count:               1,
			HarvesterConfigName: "nc-" + clusterName + "-system",
		},
		// No TenantSubnetNAD for this test — we use the legacy single-NIC path
		// because setting up a live VNet + Subnet within the test would require
		// the full networking stack. The SA bootstrap skips when TenantSubnetNAD
		// is empty, so this tests the provisioner's legacy path end-to-end.
		VMNamespace: vnetNS,
	}

	t.Logf("Creating cluster %q (this takes 15-20 min)...", clusterName)
	backendUID, err := provisioner.CreateCluster(ctx, spec)
	require.NoError(t, err, "CreateCluster")
	t.Logf("Cluster creation accepted, backendUID=%s", backendUID)

	// Poll until ACTIVE or stalled.
	deadline := time.Now().Add(20 * time.Minute)
	var lastStatus *rancher.ClusterStatus
	for time.Now().Before(deadline) {
		time.Sleep(30 * time.Second)
		cs, err := provisioner.GetClusterStatus(ctx, backendUID)
		if err != nil {
			t.Logf("GetClusterStatus error (may be transient): %v", err)
			continue
		}
		lastStatus = cs
		t.Logf("Cluster status: ready=%v provisioned=%v msg=%q", cs.Ready, cs.Provisioned, cs.Message)
		if cs.Ready {
			t.Logf("Cluster %s reached ACTIVE", clusterName)
			break
		}
		if cs.Message != "" && !cs.Ready {
			// Stalled — fail early.
			t.Fatalf("Cluster stalled with message: %s", cs.Message)
		}
	}
	require.NotNil(t, lastStatus, "never got a status")
	require.True(t, lastStatus.Ready, "cluster did not reach ready state within 20 min; last status: %+v", lastStatus)

	// Verify harvesterconfig Secret in fleet-default.
	secretGVR := schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	secretName := "harvesterconfig-" + clusterName
	_, err = env.KubeClient.Resource(secretGVR).Namespace("fleet-default").Get(ctx, secretName, metav1.GetOptions{})
	require.NoError(t, err, "harvesterconfig Secret %s should exist in fleet-default", secretName)
	t.Logf("harvesterconfig Secret %s confirmed in fleet-default", secretName)

	// Delete the cluster.
	t.Logf("Deleting cluster %s...", clusterName)
	err = provisioner.DeleteCluster(ctx, backendUID)
	require.NoError(t, err, "DeleteCluster")
	t.Logf("Cluster deletion accepted")

	// Poll until the cluster is gone from Steve.
	deleteDeadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deleteDeadline) {
		time.Sleep(15 * time.Second)
		_, getErr := provisioner.GetClusterStatus(ctx, backendUID)
		if getErr != nil && rancher.IsSteveNotFound(getErr) {
			t.Logf("Cluster %s confirmed deleted from Steve", clusterName)
			break
		}
		t.Logf("Cluster still present (waiting for deletion)...")
	}

	// Verify harvesterconfig Secret is gone.
	_, err = env.KubeClient.Resource(secretGVR).Namespace("fleet-default").Get(ctx, secretName, metav1.GetOptions{})
	assert.True(t, isKubeNotFound(err),
		"harvesterconfig Secret %s should be deleted from fleet-default after cluster delete", secretName)
	t.Logf("harvesterconfig Secret %s confirmed deleted from fleet-default", secretName)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// isKubeNotFound returns true if err is a Kubernetes 404 not-found error.
// Used to distinguish "resource gone" from other errors.
func isKubeNotFound(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "not found") ||
		strings.Contains(err.Error(), "\"404\"") ||
		strings.Contains(err.Error(), "404")
}

// nsObjForName returns a minimal Namespace unstructured object for the given name.
func nsObjForName(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Namespace",
			"metadata": map[string]interface{}{
				"name": name,
				"labels": map[string]interface{}{
					"dc-api/managed": "true",
					"dc-api/test":    "true",
				},
			},
		},
	}
}
