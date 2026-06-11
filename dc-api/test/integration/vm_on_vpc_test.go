//go:build integration

package integration

// VM-on-VPC integration tests for M2 DC-API.
//
// Two scenarios are covered:
//
//  1. TestVM_OnVPC_RequestValidation — fast, no real VMs, exercises the
//     vnet_id/subnet_id mutual-exclusivity and UUID validation logic added
//     to the Create handler in M2.
//
//  2. TestVM_OnVPC_LivePeeringEnablesCrossVNetRoute — slow live test that
//     creates real VMs via the Harvester driver, verifies the KubeVirt CR
//     has the correct KubeOVN annotations (MAC pinning, logical_switch,
//     multus default-network override), creates a VNet peering, and asserts
//     that OVN static routes appear on both VPCs.
//     SKIPPED unless DCAPI_RUN_LIVE_VM_TESTS=true.
//
// Required env vars (same as the rest of the suite):
//
//	KUBECONFIG   — path to kubeconfig that reaches the harvester dev cluster
//	KUBE_CONTEXT — context name, e.g. "harvester-dev"
//
// Additional env vars for the live test:
//
//	DCAPI_VM_IMAGE_NAME      — Harvester VirtualMachineImage display name or
//	                           "namespace/id", e.g. "ubuntu-22.04".
//	                           Defaults to "ubuntu-22.04".
//	DCAPI_RUN_LIVE_VM_TESTS  — set to "true" to run the live test.

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"github.com/wso2/dc-api/internal/api"
	"github.com/wso2/dc-api/internal/api/middleware"
	"github.com/wso2/dc-api/internal/providers/harvester"
	"github.com/wso2/dc-api/internal/providers/kubeovn"
	"github.com/wso2/dc-api/internal/reconciler"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ── Fast validation tests (no real VMs) ──────────────────────────────────────

// TestVM_OnVPC_RequestValidation exercises the new vnet_id/subnet_id handler
// logic.  The nopComputeProvider never creates a VM, so the test is fast.
func TestVM_OnVPC_RequestValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := clientForTenant(t, "tenant-vm-vpc-valid")
	mustGrantOwnerForClient(t, "tenant-vm-vpc-valid")

	// Both network_name and vnet_id+subnet_id set → 400.
	_, body, status, err := client.CreateVM(ctx, CreateVMRequest{
		Name:        "test-vm",
		Size:        "small",
		ImageName:   "ubuntu-22.04",
		NetworkName: "default/vm-net",
		VNetID:      "00000000-0000-0000-0000-000000000001",
		SubnetID:    "00000000-0000-0000-0000-000000000002",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status,
		"both network_name and vnet_id+subnet_id should return 400: %s", ErrorBody(body))
	require.Contains(t, ErrorBody(body), "not both",
		"error message should mention mutual exclusivity")

	// Neither set → 400.
	_, body, status, err = client.CreateVM(ctx, CreateVMRequest{
		Name:      "test-vm",
		Size:      "small",
		ImageName: "ubuntu-22.04",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status,
		"neither network_name nor vnet_id should return 400: %s", ErrorBody(body))

	// vnet_id without subnet_id → 400.
	_, body, status, err = client.CreateVM(ctx, CreateVMRequest{
		Name:      "test-vm",
		Size:      "small",
		ImageName: "ubuntu-22.04",
		VNetID:    "00000000-0000-0000-0000-000000000001",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status,
		"vnet_id without subnet_id should return 400: %s", ErrorBody(body))

	// subnet_id without vnet_id → 400.
	_, body, status, err = client.CreateVM(ctx, CreateVMRequest{
		Name:      "test-vm",
		Size:      "small",
		ImageName: "ubuntu-22.04",
		SubnetID:  "00000000-0000-0000-0000-000000000002",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status,
		"subnet_id without vnet_id should return 400: %s", ErrorBody(body))

	// Non-UUID vnet_id → 400.
	_, body, status, err = client.CreateVM(ctx, CreateVMRequest{
		Name:      "test-vm",
		Size:      "small",
		ImageName: "ubuntu-22.04",
		VNetID:    "not-a-uuid",
		SubnetID:  "00000000-0000-0000-0000-000000000002",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status,
		"non-UUID vnet_id should return 400: %s", ErrorBody(body))

	// Valid UUIDs but VNet doesn't exist → 404.
	_, body, status, err = client.CreateVM(ctx, CreateVMRequest{
		Name:      "test-vm",
		Size:      "small",
		ImageName: "ubuntu-22.04",
		VNetID:    "00000000-0000-0000-0000-000000000001",
		SubnetID:  "00000000-0000-0000-0000-000000000002",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status,
		"nonexistent vnet_id should return 404: %s", ErrorBody(body))
}

// ── Live VM test (skipped unless DCAPI_RUN_LIVE_VM_TESTS=true) ───────────────

// newSubEnvWithHarvester builds a TestEnv wired with the real Harvester
// ComputeProvider.  All other dependencies (DB, JWT, KubeClient) are shared
// from the package-level env singleton so state is consistent.
func newSubEnvWithHarvester(t *testing.T) *TestEnv {
	t.Helper()
	kubeconfigRaw, err := loadKubeconfig()
	if err != nil {
		t.Fatalf("newSubEnvWithHarvester: load kubeconfig: %v", err)
	}
	kubeconfigB64 := base64.StdEncoding.EncodeToString(kubeconfigRaw)

	harvesterProvider, err := harvester.NewClient(kubeconfigB64, "default")
	if err != nil {
		t.Fatalf("newSubEnvWithHarvester: create harvester provider: %v", err)
	}
	netProvider, err := kubeovn.New(kubeconfigB64, "kube-ovn")
	if err != nil {
		t.Fatalf("newSubEnvWithHarvester: create kubeovn provider: %v", err)
	}

	cfg := middleware.AuthConfig{
		AdminGroup: "dc-admin",
	}
	testAuth, err := middleware.NewTestModeAuth(env.JWT.PublicKeyJWKS(), cfg)
	if err != nil {
		t.Fatalf("newSubEnvWithHarvester: create test auth: %v", err)
	}
	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, NoColor: true}).
		With().Timestamp().Logger()
	saAuth := middleware.NewServiceAccountAuth(env.DB, logger)
	composite := middleware.NewCompositeAuth(saAuth, testAuth)

	router := api.NewRouter(api.RouterDeps{
		Repo:            env.DB,
		ComputeProvider: harvesterProvider,
		ClusterProvider: &nopClusterProvider{},
		NetworkProvider: netProvider,
		AuthMiddleware:  composite,
		Log:             logger,
	})
	srv := httptest.NewServer(router)
	t.Cleanup(func() { srv.Close() })

	// Spawn the reconciler so PENDING → ACTIVE transitions actually happen.
	// Without this the test's in-process DB never sees that Harvester
	// finished provisioning the VM, and WaitVMActive times out forever.
	// 3s interval gives fast feedback in tests; production uses 60s.
	reconCtx, reconCancel := context.WithCancel(context.Background())
	t.Cleanup(reconCancel)
	go reconciler.New(env.DB, harvesterProvider, &nopClusterProvider{}, logger).
		WithInterval(3 * time.Second).
		Run(reconCtx)

	return &TestEnv{
		Server:      srv,
		BaseURL:     srv.URL,
		DB:          env.DB,
		KubeClient:  env.KubeClient,
		JWT:         env.JWT,
		pgContainer: nil,
	}
}

// TestVM_OnVPC_LivePeeringEnablesCrossVNetRoute creates real VMs on KubeOVN
// subnets, verifies the KubeVirt CRD annotations, establishes VNet peering,
// and asserts that OVN static routes appear on both VPCs.
func TestVM_OnVPC_LivePeeringEnablesCrossVNetRoute(t *testing.T) {
	if os.Getenv("DCAPI_RUN_LIVE_VM_TESTS") != "true" {
		t.Skip("set DCAPI_RUN_LIVE_VM_TESTS=true to run live VM tests (takes ~10-15 min)")
	}

	imageName := os.Getenv("DCAPI_VM_IMAGE_NAME")
	if imageName == "" {
		imageName = "ubuntu-22.04"
	}

	// Each run gets a fresh server with the real harvester provider.
	// The existing fast tests keep their nopComputeProvider.
	subEnv := newSubEnvWithHarvester(t)

	tenantID := "test-tenant-vm-vpc"
	token, err := env.JWT.MintToken(tenantID, tenantID)
	require.NoError(t, err)
	mustGrantOwner(t, tenantID, tenantID)
	// M2.5: VNet/VM/Subnet endpoints are now project-scoped. Ensure the default
	// project exists in the shared DB (subEnv shares env.DB) before routing calls.
	ensureDefaultProject(t, tenantID)
	// Bind client to the tenant AND default project so resource calls route
	// through /v1/tenants/{tenantID}/projects/default/... correctly.
	client := NewAPIClientForProject(subEnv.BaseURL, token, tenantID, defaultProjectID)

	ctx := context.Background()

	// ── Step 1: two VNets with non-overlapping CIDRs ─────────────────────────
	vnetA := mustCreateActiveVNet(t, client, randomName("vpc-a"), "10.10.0.0/16", "lk")
	vnetB := mustCreateActiveVNet(t, client, randomName("vpc-b"), "10.20.0.0/16", "lk")

	// ── Step 2: one subnet per VNet ───────────────────────────────────────────
	subnetA := mustCreateActiveSubnet(t, client, vnetA, randomName("sub-a"), "10.10.1.0/24")
	subnetB := mustCreateActiveSubnet(t, client, vnetB, randomName("sub-b"), "10.20.1.0/24")

	// ── Step 3: provision one VM per subnet ───────────────────────────────────
	vmAResp, body, status, err := client.CreateVM(ctx, CreateVMRequest{
		Name:      randomName("vma"),
		Size:      "small",
		ImageName: imageName,
		VNetID:    vnetA,
		SubnetID:  subnetA,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status, "CreateVM(vm-a): %s", ErrorBody(body))
	vmAID := vmAResp.Resource.ID
	t.Logf("vm-a provisioned: id=%s name=%s", vmAID, vmAResp.Resource.Name)

	t.Cleanup(func() {
		cCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		_, _ = client.DeleteVM(cCtx, vmAID)
		waitVMGoneWithTimeout(t, client, vmAID, 8*time.Minute)
	})

	vmBResp, body, status, err := client.CreateVM(ctx, CreateVMRequest{
		Name:      randomName("vmb"),
		Size:      "small",
		ImageName: imageName,
		VNetID:    vnetB,
		SubnetID:  subnetB,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status, "CreateVM(vm-b): %s", ErrorBody(body))
	vmBID := vmBResp.Resource.ID
	t.Logf("vm-b provisioned: id=%s name=%s", vmBID, vmBResp.Resource.Name)

	t.Cleanup(func() {
		cCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		_, _ = client.DeleteVM(cCtx, vmBID)
		waitVMGoneWithTimeout(t, client, vmBID, 8*time.Minute)
	})

	// ── Step 4: wait for ACTIVE ───────────────────────────────────────────────
	t.Log("waiting for vm-a to become ACTIVE...")
	WaitVMActive(t, client, vmAID)
	t.Log("waiting for vm-b to become ACTIVE...")
	WaitVMActive(t, client, vmBID)

	// ── Step 5: verify KubeVirt CRD annotations on vm-a ──────────────────────
	vmAResource, dbErr := env.DB.GetInternal(ctx, uuidMust(vmAID))
	require.NoError(t, dbErr, "get vm-a from DB")

	vmAns, vmAName, splitErr := splitColonUID(vmAResource.BackendUID)
	require.NoError(t, splitErr, "parse vm-a BackendUID %q", vmAResource.BackendUID)

	vmACR, crErr := vmGetKubeVirtCR(ctx, vmAns, vmAName)
	require.NoError(t, crErr, "fetch KubeVirt VM CRD for vm-a")

	subnetARow, dbErr := env.DB.GetSubnet(ctx, uuidMust(subnetA))
	require.NoError(t, dbErr, "get subnet-a from DB")
	nadName := subnetARow.BackendUID

	assertVMOnVPCAnnotations(t, vmACR, nadName, vmAns)

	// ── Step 6: negative — no cross-VNet routes before peering ───────────────
	uidA := dbGetVNetBackendUID(t, vnetA)
	uidB := dbGetVNetBackendUID(t, vnetB)

	routesA, _ := GetVpcStaticRoutes(ctx, env.KubeClient, uidA)
	routesB, _ := GetVpcStaticRoutes(ctx, env.KubeClient, uidB)
	require.False(t, staticRoutesContainCIDR(routesA, "10.20.0.0/16"),
		"VPC-A must not have a route to VPC-B before peering")
	require.False(t, staticRoutesContainCIDR(routesB, "10.10.0.0/16"),
		"VPC-B must not have a route to VPC-A before peering")

	// ── Step 7: create peering ────────────────────────────────────────────────
	pResp, body, pStatus, err := client.CreatePeering(ctx, vnetA, map[string]interface{}{
		"name":         randomName("peer-ab"),
		"peer_vnet_id": vnetB,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, pStatus, "CreatePeering: %s", ErrorBody(body))
	peeringID := pResp.Resource.ID

	t.Cleanup(func() {
		cCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_, _ = client.DeletePeering(cCtx, vnetA, peeringID)
		WaitPeeringGone(t, client, vnetA, peeringID)
	})

	t.Log("waiting for peering to become ACTIVE...")
	WaitPeeringActive(t, client, vnetA, peeringID)
	t.Log("peering ACTIVE")

	// ── Step 8: positive — routes appear after peering ────────────────────────
	require.Eventually(t, func() bool {
		routes, _ := GetVpcStaticRoutes(ctx, env.KubeClient, uidA)
		return staticRoutesContainCIDR(routes, "10.20.0.0/16")
	}, 60*time.Second, 3*time.Second,
		"VPC-A must have a static route to VPC-B's CIDR (10.20.0.0/16) after peering")

	require.Eventually(t, func() bool {
		routes, _ := GetVpcStaticRoutes(ctx, env.KubeClient, uidB)
		return staticRoutesContainCIDR(routes, "10.10.0.0/16")
	}, 60*time.Second, 3*time.Second,
		"VPC-B must have a static route to VPC-A's CIDR (10.10.0.0/16) after peering")

	t.Log("PASS: VMs attached to VPC subnets; cross-VNet static routes confirmed after peering")
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// waitVMGoneWithTimeout polls until GET /v1/virtual-machines/{id} returns 404.
// Accepts a custom timeout because Longhorn PVC reclaim after VM deletion can
// take longer than the fixed 10-minute window in WaitVMGone.
func waitVMGoneWithTimeout(t *testing.T, client *APIClient, vmID string, timeout time.Duration) {
	t.Helper()
	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, status, _ := client.GetVM(ctx, vmID)
		return status == http.StatusNotFound
	}, timeout, 15*time.Second, "VM %s still present after %s", vmID, timeout)
}

// vmGetKubeVirtCR fetches a KubeVirt VirtualMachine CRD using env.KubeClient.
var kubevirtVMGVR = schema.GroupVersionResource{
	Group:    "kubevirt.io",
	Version:  "v1",
	Resource: "virtualmachines",
}

func vmGetKubeVirtCR(ctx context.Context, ns, name string) (*unstructured.Unstructured, error) {
	obj, err := env.KubeClient.Resource(kubevirtVMGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get kubevirt VM %s/%s: %w", ns, name, err)
	}
	return obj, nil
}

// splitColonUID splits a "namespace:name" BackendUID into its parts.
func splitColonUID(uid string) (ns, name string, err error) {
	parts := strings.SplitN(uid, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid BackendUID %q — expected \"namespace:name\"", uid)
	}
	return parts[0], parts[1], nil
}

// assertVMOnVPCAnnotations checks that the KubeVirt VM CRD's VMI template
// annotations contain the required KubeOVN entries:
//   - logical_switch → routes IPAM to the correct subnet
//   - ovn MAC annotation → satisfies OVN port-security (gotcha 1)
//   - kubernetes MAC annotation → belt-and-suspenders for older kube-ovn
//   - multus default-network override → gotcha 6
//   - interface macAddress field matches the annotation MAC
func assertVMOnVPCAnnotations(t *testing.T, vm *unstructured.Unstructured, nadName, ns string) {
	t.Helper()

	annotations, _, _ := unstructured.NestedStringMap(
		vm.Object, "spec", "template", "metadata", "annotations")
	require.NotNil(t, annotations,
		"KubeVirt VM VMI template must have annotations for KubeOVN to work")

	// logical_switch annotation.
	lsKey := nadName + "." + ns + ".kubernetes.io/logical_switch"
	require.Equal(t, nadName, annotations[lsKey],
		"VMI template must have logical_switch annotation %q = %q", lsKey, nadName)

	// OVN MAC pinning — primary form.
	macOVNKey := nadName + "." + ns + ".ovn.kubernetes.io/mac_address"
	mac := annotations[macOVNKey]
	require.NotEmpty(t, mac,
		"VMI template must have OVN MAC annotation %q", macOVNKey)
	require.True(t, strings.HasPrefix(mac, "02:"),
		"MAC %q must be locally administered (02: prefix, gotcha 1)", mac)

	// OVN MAC pinning — secondary form (belt-and-suspenders).
	macK8sKey := nadName + "." + ns + ".kubernetes.io/mac_address"
	require.Equal(t, mac, annotations[macK8sKey],
		"Both MAC annotation forms must agree: %q vs %q", macOVNKey, macK8sKey)

	// Multus default-network override (gotcha 6).
	defaultNetKey := "v1.multus-cni.io/default-network"
	require.Equal(t, ns+"/"+nadName, annotations[defaultNetKey],
		"VMI template must override multus default-network to the KubeOVN NAD")

	// Verify the interface macAddress field matches the annotation MAC.
	ifaces, _, _ := unstructured.NestedSlice(
		vm.Object, "spec", "template", "spec", "domain", "devices", "interfaces")
	found := false
	for _, raw := range ifaces {
		iface, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if ifaceMAC, ok := iface["macAddress"].(string); ok {
			require.Equal(t, mac, ifaceMAC,
				"interface macAddress must match the VMI annotation MAC (gotcha 1)")
			found = true
			break
		}
	}
	require.True(t, found,
		"KubeVirt VM must have at least one interface with macAddress set")
}

// staticRoutesContainCIDR checks whether any static route entry has a
// "cidrBlock" field equal to the given CIDR string.
func staticRoutesContainCIDR(routes []interface{}, cidr string) bool {
	for _, r := range routes {
		m, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		if cb, ok := m["cidrBlock"].(string); ok && cb == cidr {
			return true
		}
	}
	return false
}
