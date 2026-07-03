package kubeovn

import (
	"context"
	"strings"
	"sync"
	"testing"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/wso2/dc-api/internal/models"
	"github.com/wso2/dc-api/internal/providers/clusteraccess"
	"github.com/wso2/dc-api/internal/providers/common"
)

// This file proves the GAP the CRD-lifecycle slice closed: a REMOTE kubeovn
// client (NewRemoteClient, c.dynamic == nil) routes the ONBOARDED CRD lifecycle
// (Vpc/Subnet/NAD create/get/delete) through its agent-only Accessor instead of
// returning a local-only error — while the REMAINING local-only plumbing (NAT,
// per-VPC DNS) still returns localOnlyErr. The peering/route-table/NSG spec
// writes are no longer in the local-only set — network_plumbing_seam_test.go
// proves they route through the seam on a remote client. It also proves a
// remote client built with a NoCreds-only accessor (no agent) fails CLOSED with
// a clear "no agent connected" error rather than nil-dereferencing c.dynamic.
//
// The remote client has c.dynamic == nil by construction, so these tests are the
// regression guard against re-introducing a `c.dynamic == nil` guard on a routed
// CRD method (which would re-open the gap) AND against a stray c.dynamic.Resource
// call on the routed path (which would panic on the nil client).

// ── A GVR-aware fake agent accessor for the remote-client path ───────────────
//
// Unlike netCountingAccessor (which always returns a Subnet shape), this accessor
// returns the object shape the routed CRD lifecycle reads: a Vpc with the
// tenant/project labels for CreateSubnet's parent fetch, a NAD already stamped
// network.harvesterhci.io/ready=true so waitForNADReady returns immediately, and
// a labelled Subnet for the DeleteSubnet leading read. Create/Delete just count.
type remoteAgentAccessor struct {
	mu          sync.Mutex
	getCalls    int
	createCalls int
	deleteCalls int

	// getGVRs records the GVRs Get was called with, so a test can assert the
	// routed reads (e.g. the onboarded vpc/subnet/nad) actually went through here.
	getGVRs []schema.GroupVersionResource

	// deleted records names the agent has deleted, so a subsequent Get on that
	// name returns NotFound — letting DeleteSubnet's "wait for subnet gone" loop
	// exit immediately instead of timing out (the agent honours the webhook
	// finalizer ordering, but for the unit test the delete is synchronous).
	deleted map[string]bool
}

var _ clusteraccess.Accessor = (*remoteAgentAccessor)(nil)

func (a *remoteAgentAccessor) Get(_ context.Context, gvr schema.GroupVersionResource, ns, name string, _ metav1.GetOptions) (*unstructured.Unstructured, error) {
	a.mu.Lock()
	a.getCalls++
	a.getGVRs = append(a.getGVRs, gvr)
	gone := a.deleted[name]
	a.mu.Unlock()

	if gone {
		return nil, k8serrors.NewNotFound(gvr.GroupResource(), name)
	}

	obj := map[string]interface{}{
		"metadata": map[string]interface{}{
			"name": name, "namespace": ns,
			"labels": map[string]interface{}{
				"dc-api/managed": "true",
				"dc-api/tenant":  "tenant",
				"dc-api/project": "proj",
			},
		},
	}
	switch gvr {
	case vpcGVR:
		obj["apiVersion"] = "kubeovn.io/v1"
		obj["kind"] = "Vpc"
	case subnetGVR:
		obj["apiVersion"] = "kubeovn.io/v1"
		obj["kind"] = "Subnet"
	case nadGVR:
		obj["apiVersion"] = "k8s.cni.cncf.io/v1"
		obj["kind"] = "NetworkAttachmentDefinition"
		// waitForNADReady polls for this label — stamp it so the wait returns now.
		labels := obj["metadata"].(map[string]interface{})["labels"].(map[string]interface{})
		labels["network.harvesterhci.io/ready"] = "true"
	}
	return &unstructured.Unstructured{Object: obj}, nil
}

func (a *remoteAgentAccessor) List(_ context.Context, _ schema.GroupVersionResource, _ string, _ metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	return &unstructured.UnstructuredList{}, nil
}

func (a *remoteAgentAccessor) Create(_ context.Context, _ schema.GroupVersionResource, _ string, obj *unstructured.Unstructured, _ metav1.CreateOptions) (*unstructured.Unstructured, error) {
	a.mu.Lock()
	a.createCalls++
	a.mu.Unlock()
	return obj, nil
}

func (a *remoteAgentAccessor) Apply(_ context.Context, _ schema.GroupVersionResource, _ string, obj *unstructured.Unstructured, _ string, _ bool) (*unstructured.Unstructured, error) {
	return obj, nil
}

func (a *remoteAgentAccessor) Update(_ context.Context, _ schema.GroupVersionResource, _ string, obj *unstructured.Unstructured, _ metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	return obj, nil
}

func (a *remoteAgentAccessor) Delete(_ context.Context, _ schema.GroupVersionResource, _, name string, _ metav1.DeleteOptions) error {
	a.mu.Lock()
	a.deleteCalls++
	if a.deleted == nil {
		a.deleted = map[string]bool{}
	}
	a.deleted[name] = true
	a.mu.Unlock()
	return nil
}

func (a *remoteAgentAccessor) getCount() int    { a.mu.Lock(); defer a.mu.Unlock(); return a.getCalls }
func (a *remoteAgentAccessor) createCount() int { a.mu.Lock(); defer a.mu.Unlock(); return a.createCalls }
func (a *remoteAgentAccessor) deleteCount() int { a.mu.Lock(); defer a.mu.Unlock(); return a.deleteCalls }

func (a *remoteAgentAccessor) sawGet(gvr schema.GroupVersionResource) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, g := range a.getGVRs {
		if g == gvr {
			return true
		}
	}
	return false
}

// ── Remote CRD lifecycle routes through the agent accessor ───────────────────

func TestRemoteClient_CreateVNet_RoutesThroughAgent(t *testing.T) {
	agent := &remoteAgentAccessor{}
	c := NewRemoteClient(agent, "lk", "zone-2")
	if c.dynamic != nil {
		t.Fatal("remote client must have a nil dynamic client")
	}

	res, err := c.CreateVNet(context.Background(), "tenant", "proj", sampleVNetSpec())
	if err != nil {
		t.Fatalf("CreateVNet error: %v", err)
	}
	if res.Status != models.StatusPending {
		t.Errorf("status = %q, want PENDING", res.Status)
	}
	if got := agent.createCount(); got != 1 {
		t.Errorf("agent Create called %d times, want 1 (the Vpc create)", got)
	}
}

func TestRemoteClient_GetVNet_RoutesThroughAgent(t *testing.T) {
	agent := &remoteAgentAccessor{}
	c := NewRemoteClient(agent, "lk", "zone-2")

	res, err := c.GetVNet(context.Background(), "vnet-tenant-proj-app")
	if err != nil {
		t.Fatalf("GetVNet error: %v", err)
	}
	if res.BackendUID != "vnet-tenant-proj-app" {
		t.Errorf("BackendUID = %q, want vnet-tenant-proj-app", res.BackendUID)
	}
	if !agent.sawGet(vpcGVR) {
		t.Error("agent Get was not called with vpcGVR — the VNet read did not route")
	}
}

func TestRemoteClient_DeleteVNet_RoutesThroughAgent(t *testing.T) {
	agent := &remoteAgentAccessor{}
	c := NewRemoteClient(agent, "lk", "zone-2")

	if err := c.DeleteVNet(context.Background(), "vnet-tenant-proj-app"); err != nil {
		t.Fatalf("DeleteVNet error: %v", err)
	}
	if got := agent.deleteCount(); got != 1 {
		t.Errorf("agent Delete called %d times, want 1 (the Vpc delete)", got)
	}
}

func TestRemoteClient_CreateSubnet_RoutesThroughAgent(t *testing.T) {
	agent := &remoteAgentAccessor{}
	c := NewRemoteClient(agent, "lk", "zone-2")

	vnetUID := vpcName("app", "tenant", "proj")
	res, err := c.CreateSubnet(context.Background(), vnetUID, sampleSubnetSpec())
	if err != nil {
		t.Fatalf("CreateSubnet error: %v", err)
	}
	if res.Status != models.StatusPending {
		t.Errorf("status = %q, want PENDING", res.Status)
	}
	// Parent-VPC fetch (vpcGVR Get) and NAD-ready poll (nadGVR Get) must route.
	if !agent.sawGet(vpcGVR) {
		t.Error("parent VPC fetch did not route through the agent (vpcGVR Get missing)")
	}
	if !agent.sawGet(nadGVR) {
		t.Error("NAD readiness poll did not route through the agent (nadGVR Get missing)")
	}
	// NAD create + Subnet create both route through the agent.
	if got := agent.createCount(); got != 2 {
		t.Errorf("agent Create called %d times, want 2 (NAD + Subnet)", got)
	}
}

func TestRemoteClient_DeleteSubnet_RoutesThroughAgent(t *testing.T) {
	agent := &remoteAgentAccessor{}
	c := NewRemoteClient(agent, "lk", "zone-2")

	if err := c.DeleteSubnet(context.Background(), "subnet-tenant-proj-app-sub"); err != nil {
		t.Fatalf("DeleteSubnet error: %v", err)
	}
	// Leading Subnet read + the post-delete wait-loop read both route.
	if !agent.sawGet(subnetGVR) {
		t.Error("Subnet read did not route through the agent (subnetGVR Get missing)")
	}
	// Subnet delete + NAD delete both route through the agent; the local-only ACL
	// clear and stuck-finalizer recovery (c.dynamic) are skipped for a remote zone.
	if got := agent.deleteCount(); got != 2 {
		t.Errorf("agent Delete called %d times, want 2 (Subnet + NAD)", got)
	}
}

// ── Remaining remote PLUMBING methods stay local-only ────────────────────────

// TestRemoteClient_PlumbingMethods_ReturnLocalOnlyErr pins the REMAINING
// local-only boundary after the spec-write plumbing slice: the per-VPC DNS
// zone/record ops (ConfigMap-backed — configmaps are not an onboarded family).
// The peering/route-table/NSG ops that used to sit here are now agent-routable;
// their remote-client coverage lives in network_plumbing_seam_test.go.
func TestRemoteClient_PlumbingMethods_ReturnLocalOnlyErr(t *testing.T) {
	agent := &remoteAgentAccessor{}
	c := NewRemoteClient(agent, "lk", "zone-2")
	ctx := context.Background()

	t.Run("CreatePrivateDnsZone", func(t *testing.T) {
		_, err := c.CreatePrivateDnsZone(ctx, "vnet-tenant-proj-app", models.DnsZoneSpec{})
		assertLocalOnlyErr(t, err, "lk", "zone-2")
	})
	t.Run("DeletePrivateDnsZone", func(t *testing.T) {
		err := c.DeletePrivateDnsZone(ctx, "configmap-dc-tenant-proj/zone-uuid")
		assertLocalOnlyErr(t, err, "lk", "zone-2")
	})
	t.Run("UpsertDnsRecord", func(t *testing.T) {
		err := c.UpsertDnsRecord(ctx, "zone-uuid", models.DnsRecord{})
		assertLocalOnlyErr(t, err, "lk", "zone-2")
	})
	t.Run("DeleteDnsRecord", func(t *testing.T) {
		err := c.DeleteDnsRecord(ctx, "zone-uuid", "record-uuid")
		assertLocalOnlyErr(t, err, "lk", "zone-2")
	})

	// None of the local-only plumbing methods should have touched the agent.
	if got := agent.createCount() + agent.deleteCount() + agent.getCount(); got != 0 {
		t.Errorf("plumbing methods touched the agent %d times, want 0", got)
	}
}

// ── Remote client with NO agent fails closed (NoCreds) ───────────────────────

func TestRemoteClient_NoAgent_FailsClosedOnCRDOp(t *testing.T) {
	// Mirror buildRemoteSet with NO live agent: the Routed decision always declines,
	// so the accessor falls back to NoCreds — which must error clearly, never panic.
	noCreds := clusteraccess.NewNoCreds("lk", "zone-2")
	routed := clusteraccess.NewRouted(noCreds, func(clusteraccess.Verb, schema.GroupVersionResource) (clusteraccess.Accessor, bool) {
		return nil, false // no agent connected
	})
	c := NewRemoteClient(routed, "lk", "zone-2")

	_, err := c.CreateVNet(context.Background(), "tenant", "proj", sampleVNetSpec())
	if err == nil {
		t.Fatal("CreateVNet returned nil error with no agent; it must fail closed")
	}
	if !strings.Contains(err.Error(), "no agent connected for zone lk/zone-2") {
		t.Errorf("error = %q, want it to name the zone and the no-agent cause", err.Error())
	}

	if err := c.DeleteVNet(context.Background(), "vnet-tenant-proj-app"); err == nil {
		t.Fatal("DeleteVNet returned nil error with no agent; it must fail closed")
	}

	if _, err := c.GetVNet(context.Background(), "vnet-tenant-proj-app"); err == nil {
		t.Fatal("GetVNet returned nil error with no agent; it must fail closed")
	}
}

func assertLocalOnlyErr(t *testing.T, err error, region, zone string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a local-only error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, region+"/"+zone) {
		t.Errorf("error = %q, want it to name the zone %s/%s", msg, region, zone)
	}
	if !strings.Contains(msg, "remote zone") {
		t.Errorf("error = %q, want it to be a remote-zone local-only error", msg)
	}
}

// Keep common imported (used by sibling seam tests via shared helpers); referenced
// here to document the namespace convention the remote agent shape mirrors.
var _ = common.NamespaceForProject
