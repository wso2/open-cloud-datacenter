package kubeovn

// nat_seam_test.go proves the F15 NAT slice of the credential-locality work:
// the whole per-VPC NAT lifecycle in nat.go — the external-network bootstrap
// (ProviderNetwork/Vlan/external Subnet/NAD), EnsureVpcNAT's gateway/EIP/SNAT
// chain, DeleteVpcNAT's teardown, IsVpcNATPresent, and the pod/EIP/SNAT status
// waiters — runs through the cluster-access seam (c.access.Create/Get/Delete/
// List) instead of c.dynamic, so it works for a REMOTE (agent-only) zone.
//
// Coverage:
//   - EnsureVpcNAT creates the gateway, EIP, and SNAT rule via seam Creates with
//     the correct GVRs and FIXED, deterministic names (SSA on the agent path
//     requires fixed names — no generateName anywhere), keeps the IsAlreadyExists
//     tolerance, and writes the VPC default route through the seam Apply the
//     parent slice routed (minimal single-field object, dedicated field manager).
//   - DeleteVpcNAT issues the three seam Deletes in reverse order, strips ONLY
//     the 0.0.0.0/0 route (preserving peering routes), and tolerates NotFound.
//   - IsVpcNATPresent reads both presence Gets through the seam.
//   - The pod-running/EIP-ready/SNAT-ready waiters and the pods-gone poll read
//     status via seam Get/List (pods namespaced, NAT CRDs cluster-scoped).
//   - Remote client (NewRemoteClient, c.dynamic == nil): the full lifecycle works
//     purely through the seam — the regression guard against re-introducing a
//     c.dynamic dereference (which would panic on the nil client) or a
//     localOnlyErr guard (which would re-open the remote gap).

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/wso2/dc-api/internal/providers/clusteraccess"
)

// ── A recording accessor for the NAT lifecycle ────────────────────────────────

type recordedNATGet struct {
	gvr  schema.GroupVersionResource
	ns   string
	name string
}

type recordedNATCreate struct {
	gvr schema.GroupVersionResource
	ns  string
	obj *unstructured.Unstructured
}

type recordedNATDelete struct {
	gvr  schema.GroupVersionResource
	ns   string
	name string
}

type recordedNATList struct {
	gvr      schema.GroupVersionResource
	ns       string
	selector string
}

// natRecordingAccessor serves programmable objects from Get (keyed by name — all
// NAT fixture names are distinct), records every Get/Create/Delete/List/Apply
// with its full argument set, and can inject per-resource create/delete errors
// (the AlreadyExists / NotFound tolerance probes). Objects are pre-seeded in
// their CONVERGED state (pod Running, EIP/SNAT ready) so the waiters return on
// their first seam read.
type natRecordingAccessor struct {
	mu sync.Mutex

	objects map[string]*unstructured.Unstructured

	// createErrs/deleteErrs inject an error per GVR.Resource.
	createErrs map[string]error
	deleteErrs map[string]error

	// podList is what List returns (nil → empty list).
	podList *unstructured.UnstructuredList

	gets    []recordedNATGet
	creates []recordedNATCreate
	deletes []recordedNATDelete
	lists   []recordedNATList
	applies []recordedApply // shared shape with network_plumbing_seam_test.go
}

var _ clusteraccess.Accessor = (*natRecordingAccessor)(nil)

func (a *natRecordingAccessor) Get(_ context.Context, gvr schema.GroupVersionResource, ns, name string, _ metav1.GetOptions) (*unstructured.Unstructured, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.gets = append(a.gets, recordedNATGet{gvr: gvr, ns: ns, name: name})
	obj, ok := a.objects[name]
	if !ok {
		return nil, k8serrors.NewNotFound(gvr.GroupResource(), name)
	}
	return obj.DeepCopy(), nil
}

func (a *natRecordingAccessor) List(_ context.Context, gvr schema.GroupVersionResource, ns string, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lists = append(a.lists, recordedNATList{gvr: gvr, ns: ns, selector: opts.LabelSelector})
	if a.podList != nil {
		return a.podList, nil
	}
	return &unstructured.UnstructuredList{}, nil
}

func (a *natRecordingAccessor) Create(_ context.Context, gvr schema.GroupVersionResource, ns string, obj *unstructured.Unstructured, _ metav1.CreateOptions) (*unstructured.Unstructured, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.creates = append(a.creates, recordedNATCreate{gvr: gvr, ns: ns, obj: obj.DeepCopy()})
	if err := a.createErrs[gvr.Resource]; err != nil {
		return nil, err
	}
	return obj, nil
}

func (a *natRecordingAccessor) Apply(_ context.Context, gvr schema.GroupVersionResource, ns string, obj *unstructured.Unstructured, fieldManager string, force bool) (*unstructured.Unstructured, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.applies = append(a.applies, recordedApply{gvr: gvr, ns: ns, obj: obj, fieldManager: fieldManager, force: force})
	return obj, nil
}

func (a *natRecordingAccessor) Update(_ context.Context, _ schema.GroupVersionResource, _ string, obj *unstructured.Unstructured, _ metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	return obj, nil
}

func (a *natRecordingAccessor) Delete(_ context.Context, gvr schema.GroupVersionResource, ns, name string, _ metav1.DeleteOptions) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.deletes = append(a.deletes, recordedNATDelete{gvr: gvr, ns: ns, name: name})
	if err := a.deleteErrs[gvr.Resource]; err != nil {
		return err
	}
	return nil
}

func (a *natRecordingAccessor) sawGetGVR(gvr schema.GroupVersionResource) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, g := range a.gets {
		if g.gvr == gvr {
			return true
		}
	}
	return false
}

// ── Converged NAT fixture objects ─────────────────────────────────────────────

func runningPod(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]interface{}{"name": name, "namespace": "kube-system"},
		"status":   map[string]interface{}{"phase": "Running"},
	}}
}

func readyEIP(name, ip string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kubeovn.io/v1", "kind": "IptablesEIP",
		"metadata": map[string]interface{}{"name": name},
		"status":   map[string]interface{}{"ready": true, "ip": ip},
	}}
}

func readySnat(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kubeovn.io/v1", "kind": "IptablesSnatRule",
		"metadata": map[string]interface{}{"name": name},
		"status":   map[string]interface{}{"ready": true},
	}}
}

// convergedNATObjects seeds every object EnsureVpcNAT's waiters poll, in its
// converged state, for the VPC "vnet-a".
func convergedNATObjects(eipIP string) map[string]*unstructured.Unstructured {
	return map[string]*unstructured.Unstructured{
		"vpc-nat-gw-natgw-vnet-a-0": runningPod("vpc-nat-gw-natgw-vnet-a-0"),
		"eip-vnet-a":                readyEIP("eip-vnet-a", eipIP),
		"snat-vnet-a":               readySnat("snat-vnet-a"),
		"vnet-a":                    seededVpc("vnet-a", []interface{}{}, []interface{}{}),
	}
}

// assertFixedName asserts a created object carries the expected deterministic
// metadata.name and NO generateName — the invariant that keeps the agent path's
// SSA create idempotent (SSA cannot apply a generateName object).
func assertFixedName(t *testing.T, obj *unstructured.Unstructured, wantName string) {
	t.Helper()
	if got := obj.GetName(); got != wantName {
		t.Errorf("created object name = %q, want the fixed name %q", got, wantName)
	}
	if gn := obj.GetGenerateName(); gn != "" {
		t.Errorf("created object %q uses generateName %q — SSA requires fixed names", wantName, gn)
	}
}

// ── EnsureVpcNAT: seam creates with fixed names + seam route write ────────────

func TestEnsureVpcNAT_SeamLifecycle_CreatesWithFixedNames(t *testing.T) {
	acc := &natRecordingAccessor{objects: convergedNATObjects("192.168.10.100")}
	c := &Client{access: acc}

	ip, err := c.EnsureVpcNAT(context.Background(), "vnet-a", "10.0.1.0/24", "subnet-1", net.ParseIP("10.0.1.254"))
	if err != nil {
		t.Fatalf("EnsureVpcNAT: %v", err)
	}
	if ip.String() != "192.168.10.100" {
		t.Errorf("assigned EIP = %s, want 192.168.10.100 (from .status.ip)", ip)
	}

	// Three seam creates, in order, all cluster-scoped (ns="").
	if len(acc.creates) != 3 {
		t.Fatalf("seam Create called %d times, want 3 (gateway, EIP, SNAT)", len(acc.creates))
	}
	wantCreates := []struct {
		gvr  schema.GroupVersionResource
		name string
	}{
		{vpcNatGatewayGVR, natGWName("vnet-a")},
		{iptablesEIPGVR, natEIPName("vnet-a")},
		{iptablesSnatRuleGVR, natSnatName("vnet-a")},
	}
	for i, want := range wantCreates {
		rec := acc.creates[i]
		if rec.gvr != want.gvr {
			t.Errorf("create[%d] GVR = %v, want %v", i, rec.gvr, want.gvr)
		}
		if rec.ns != "" {
			t.Errorf("create[%d] namespace = %q, want \"\" (cluster-scoped)", i, rec.ns)
		}
		assertFixedName(t, rec.obj, want.name)
	}

	// Gateway spec pins vpc/subnet/lanIp.
	gwSpec, _, _ := unstructured.NestedMap(acc.creates[0].obj.Object, "spec")
	if gwSpec["vpc"] != "vnet-a" || gwSpec["subnet"] != "subnet-1" || gwSpec["lanIp"] != "10.0.1.254" {
		t.Errorf("gateway spec = %v, want vpc=vnet-a subnet=subnet-1 lanIp=10.0.1.254", gwSpec)
	}

	// EIP spec: natGwDp set, v4ip OMITTED (KubeOVN's IPAM picks the address).
	eipSpec, _, _ := unstructured.NestedMap(acc.creates[1].obj.Object, "spec")
	if eipSpec["natGwDp"] != natGWName("vnet-a") {
		t.Errorf("EIP spec.natGwDp = %v, want %q", eipSpec["natGwDp"], natGWName("vnet-a"))
	}
	if _, hasV4IP := eipSpec["v4ip"]; hasV4IP {
		t.Error("EIP spec carries v4ip — it must be omitted so KubeOVN's IPAM allocates")
	}

	// SNAT spec pins eip/internalCIDR.
	snatSpec, _, _ := unstructured.NestedMap(acc.creates[2].obj.Object, "spec")
	if snatSpec["eip"] != natEIPName("vnet-a") || snatSpec["internalCIDR"] != "10.0.1.0/24" {
		t.Errorf("SNAT spec = %v, want eip=%q internalCIDR=10.0.1.0/24", snatSpec, natEIPName("vnet-a"))
	}

	// The default-route write went through the seam Apply the parent slice
	// routed: minimal single-field staticRoutes object, dedicated field manager.
	if len(acc.applies) != 1 {
		t.Fatalf("seam Apply called %d times, want 1 (VPC default route)", len(acc.applies))
	}
	list := assertMinimalApply(t, acc.applies[0], vpcGVR, "Vpc", "vnet-a", "staticRoutes", fieldManagerStaticRoutes)
	want := []interface{}{map[string]interface{}{
		"cidr": "0.0.0.0/0", "nextHopIP": "10.0.1.254", "policy": "policyDst", "routeTable": "",
	}}
	assertJSONEqual(t, list, want, "spec.staticRoutes (default route)")

	// The waiters and the route read all went through the seam Get.
	for _, gvr := range []schema.GroupVersionResource{podGVR, iptablesEIPGVR, iptablesSnatRuleGVR, vpcGVR} {
		if !acc.sawGetGVR(gvr) {
			t.Errorf("no seam Get recorded for %v — a read bypassed the seam", gvr)
		}
	}
	// The gateway-pod poll is the one namespaced read (kube-system).
	for _, g := range acc.gets {
		if g.gvr == podGVR {
			if g.ns != "kube-system" || g.name != "vpc-nat-gw-natgw-vnet-a-0" {
				t.Errorf("pod Get = ns %q name %q, want kube-system/vpc-nat-gw-natgw-vnet-a-0", g.ns, g.name)
			}
		} else if g.ns != "" {
			t.Errorf("%v Get carries namespace %q, want \"\" (cluster-scoped)", g.gvr, g.ns)
		}
	}
}

func TestEnsureVpcNAT_AlreadyExistsTolerated(t *testing.T) {
	acc := &natRecordingAccessor{
		objects: convergedNATObjects("192.168.10.101"),
		createErrs: map[string]error{
			vpcNatGatewayGVR.Resource:    k8serrors.NewAlreadyExists(vpcNatGatewayGVR.GroupResource(), natGWName("vnet-a")),
			iptablesEIPGVR.Resource:      k8serrors.NewAlreadyExists(iptablesEIPGVR.GroupResource(), natEIPName("vnet-a")),
			iptablesSnatRuleGVR.Resource: k8serrors.NewAlreadyExists(iptablesSnatRuleGVR.GroupResource(), natSnatName("vnet-a")),
		},
	}
	c := &Client{access: acc}

	// A re-run against existing resources must converge, not error: the local
	// POST path tolerates AlreadyExists (and the agent path's SSA never raises it).
	ip, err := c.EnsureVpcNAT(context.Background(), "vnet-a", "10.0.1.0/24", "subnet-1", net.ParseIP("10.0.1.254"))
	if err != nil {
		t.Fatalf("EnsureVpcNAT on pre-existing resources must succeed, got %v", err)
	}
	if ip.String() != "192.168.10.101" {
		t.Errorf("assigned EIP = %s, want 192.168.10.101", ip)
	}
	if len(acc.applies) != 1 {
		t.Errorf("seam Apply called %d times, want 1 (default route still ensured)", len(acc.applies))
	}
}

// ── DeleteVpcNAT: seam deletes + route strip + NotFound tolerance ─────────────

func TestDeleteVpcNAT_SeamDeletesAndRouteStrip(t *testing.T) {
	defaultRoute := map[string]interface{}{"cidr": "0.0.0.0/0", "nextHopIP": "10.0.1.254", "policy": "policyDst", "routeTable": ""}
	peeringRoute := map[string]interface{}{"cidr": "10.2.0.0/16", "nextHopIP": "100.64.10.2", "policy": "policyDst", "routeTable": ""}
	acc := &natRecordingAccessor{objects: map[string]*unstructured.Unstructured{
		"vnet-a": seededVpc("vnet-a", []interface{}{}, []interface{}{defaultRoute, peeringRoute}),
	}}
	c := &Client{access: acc}

	if err := c.DeleteVpcNAT(context.Background(), "vnet-a"); err != nil {
		t.Fatalf("DeleteVpcNAT: %v", err)
	}

	// The route strip removed ONLY the 0.0.0.0/0 entry (peering preserved),
	// through the same minimal seam Apply as the ensure path.
	if len(acc.applies) != 1 {
		t.Fatalf("seam Apply called %d times, want 1 (route strip)", len(acc.applies))
	}
	list := assertMinimalApply(t, acc.applies[0], vpcGVR, "Vpc", "vnet-a", "staticRoutes", fieldManagerStaticRoutes)
	assertJSONEqual(t, list, []interface{}{peeringRoute}, "spec.staticRoutes after NAT delete")

	// Three seam deletes in reverse creation order, all cluster-scoped.
	if len(acc.deletes) != 3 {
		t.Fatalf("seam Delete called %d times, want 3 (SNAT, EIP, gateway)", len(acc.deletes))
	}
	wantDeletes := []struct {
		gvr  schema.GroupVersionResource
		name string
	}{
		{iptablesSnatRuleGVR, natSnatName("vnet-a")},
		{iptablesEIPGVR, natEIPName("vnet-a")},
		{vpcNatGatewayGVR, natGWName("vnet-a")},
	}
	for i, want := range wantDeletes {
		rec := acc.deletes[i]
		if rec.gvr != want.gvr || rec.name != want.name {
			t.Errorf("delete[%d] = %v %q, want %v %q", i, rec.gvr, rec.name, want.gvr, want.name)
		}
		if rec.ns != "" {
			t.Errorf("delete[%d] namespace = %q, want \"\" (cluster-scoped)", i, rec.ns)
		}
	}
}

func TestDeleteVpcNAT_NotFoundTolerated(t *testing.T) {
	acc := &natRecordingAccessor{
		// No VPC object: the route-strip Get is NotFound → silent no-op.
		objects: map[string]*unstructured.Unstructured{},
		deleteErrs: map[string]error{
			iptablesSnatRuleGVR.Resource: k8serrors.NewNotFound(iptablesSnatRuleGVR.GroupResource(), natSnatName("vnet-a")),
			iptablesEIPGVR.Resource:      k8serrors.NewNotFound(iptablesEIPGVR.GroupResource(), natEIPName("vnet-a")),
			vpcNatGatewayGVR.Resource:    k8serrors.NewNotFound(vpcNatGatewayGVR.GroupResource(), natGWName("vnet-a")),
		},
	}
	c := &Client{access: acc}

	if err := c.DeleteVpcNAT(context.Background(), "vnet-a"); err != nil {
		t.Fatalf("DeleteVpcNAT on already-deleted resources must be nil, got %v", err)
	}
	if len(acc.applies) != 0 {
		t.Errorf("seam Apply called %d times for a missing VPC, want 0", len(acc.applies))
	}
	if len(acc.deletes) != 3 {
		t.Errorf("seam Delete called %d times, want 3 (all attempted despite NotFound)", len(acc.deletes))
	}
}

// ── IsVpcNATPresent: presence reads through the seam ──────────────────────────

func TestIsVpcNATPresent_SeamGets(t *testing.T) {
	t.Run("both present", func(t *testing.T) {
		acc := &natRecordingAccessor{objects: map[string]*unstructured.Unstructured{
			"natgw-vnet-a": {Object: map[string]interface{}{"apiVersion": "kubeovn.io/v1", "kind": "VpcNatGateway", "metadata": map[string]interface{}{"name": "natgw-vnet-a"}}},
			"eip-vnet-a":   readyEIP("eip-vnet-a", "192.168.10.100"),
		}}
		c := &Client{access: acc}
		present, err := c.IsVpcNATPresent(context.Background(), "vnet-a")
		if err != nil {
			t.Fatalf("IsVpcNATPresent: %v", err)
		}
		if !present {
			t.Error("present = false, want true (gateway + EIP both exist)")
		}
		if !acc.sawGetGVR(vpcNatGatewayGVR) || !acc.sawGetGVR(iptablesEIPGVR) {
			t.Errorf("presence Gets did not go through the seam: %v", acc.gets)
		}
	})

	t.Run("gateway missing", func(t *testing.T) {
		acc := &natRecordingAccessor{objects: map[string]*unstructured.Unstructured{
			"eip-vnet-a": readyEIP("eip-vnet-a", "192.168.10.100"),
		}}
		c := &Client{access: acc}
		present, err := c.IsVpcNATPresent(context.Background(), "vnet-a")
		if err != nil || present {
			t.Errorf("IsVpcNATPresent = (%v, %v), want (false, nil) when the gateway is missing", present, err)
		}
	})

	t.Run("eip missing", func(t *testing.T) {
		acc := &natRecordingAccessor{objects: map[string]*unstructured.Unstructured{
			"natgw-vnet-a": {Object: map[string]interface{}{"apiVersion": "kubeovn.io/v1", "kind": "VpcNatGateway", "metadata": map[string]interface{}{"name": "natgw-vnet-a"}}},
		}}
		c := &Client{access: acc}
		present, err := c.IsVpcNATPresent(context.Background(), "vnet-a")
		if err != nil || present {
			t.Errorf("IsVpcNATPresent = (%v, %v), want (false, nil) when the EIP is missing", present, err)
		}
	})
}

// ── The waiters: status reads via seam Get/List ───────────────────────────────

func TestWaitVpcNATPodsGone_SeamList(t *testing.T) {
	t.Run("no pods returns immediately", func(t *testing.T) {
		acc := &natRecordingAccessor{}
		c := &Client{access: acc}

		if err := c.WaitVpcNATPodsGone(context.Background(), "vnet-a", 5*time.Second); err != nil {
			t.Fatalf("WaitVpcNATPodsGone with no pods: %v", err)
		}
		if len(acc.lists) != 1 {
			t.Fatalf("seam List called %d times, want 1", len(acc.lists))
		}
		rec := acc.lists[0]
		if rec.gvr != podGVR {
			t.Errorf("List GVR = %v, want pods", rec.gvr)
		}
		if rec.ns != "kube-system" {
			t.Errorf("List namespace = %q, want kube-system", rec.ns)
		}
		wantSelector := "app=vpc-nat-gw-" + natGWName("vnet-a")
		if rec.selector != wantSelector {
			t.Errorf("List label selector = %q, want %q", rec.selector, wantSelector)
		}
	})

	t.Run("lingering pod blocks until ctx deadline", func(t *testing.T) {
		lingering := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*runningPod("vpc-nat-gw-natgw-vnet-a-0")}}
		acc := &natRecordingAccessor{podList: lingering}
		c := &Client{access: acc}

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if err := c.WaitVpcNATPodsGone(ctx, "vnet-a", 5*time.Second); err == nil {
			t.Fatal("WaitVpcNATPodsGone must not return nil while pods linger")
		}
		if len(acc.lists) == 0 {
			t.Error("seam List was never called")
		}
	})
}

func TestNATWaiters_ReadStatusViaSeam(t *testing.T) {
	acc := &natRecordingAccessor{objects: convergedNATObjects("192.168.10.100")}
	c := &Client{access: acc}
	ctx := context.Background()

	if err := c.waitForPodRunning(ctx, "kube-system", "vpc-nat-gw-natgw-vnet-a-0", time.Second); err != nil {
		t.Errorf("waitForPodRunning on a Running pod: %v", err)
	}
	if err := c.waitForEIPReady(ctx, "eip-vnet-a", time.Second); err != nil {
		t.Errorf("waitForEIPReady on a ready EIP: %v", err)
	}
	if err := c.waitForSnatReady(ctx, "snat-vnet-a", time.Second); err != nil {
		t.Errorf("waitForSnatReady on a ready SNAT rule: %v", err)
	}
	ip, err := c.readEIPAssignedIP(ctx, "eip-vnet-a")
	if err != nil {
		t.Errorf("readEIPAssignedIP: %v", err)
	} else if ip.String() != "192.168.10.100" {
		t.Errorf("readEIPAssignedIP = %s, want 192.168.10.100", ip)
	}

	// Every read above went through the seam.
	for _, gvr := range []schema.GroupVersionResource{podGVR, iptablesEIPGVR, iptablesSnatRuleGVR} {
		if !acc.sawGetGVR(gvr) {
			t.Errorf("no seam Get recorded for %v", gvr)
		}
	}
}

// TestNATWaiters_TimeoutErrors pins the timeout branch of each private waiter:
// an object that never converges makes the waiter return a clear timeout error
// (not nil, not a hang). The pod is Pending, the EIP not-ready, and the SNAT
// rule not-ready; a ~20ms budget forces waitCtx.Done() on the first select.
func TestNATWaiters_TimeoutErrors(t *testing.T) {
	acc := &natRecordingAccessor{objects: map[string]*unstructured.Unstructured{
		"vpc-nat-gw-natgw-vnet-a-0": {Object: map[string]interface{}{
			"apiVersion": "v1", "kind": "Pod",
			"metadata": map[string]interface{}{"name": "vpc-nat-gw-natgw-vnet-a-0", "namespace": "kube-system"},
			"status":   map[string]interface{}{"phase": "Pending"},
		}},
		"eip-vnet-a": {Object: map[string]interface{}{
			"apiVersion": "kubeovn.io/v1", "kind": "IptablesEIP",
			"metadata": map[string]interface{}{"name": "eip-vnet-a"},
			"status":   map[string]interface{}{"ready": false},
		}},
		"snat-vnet-a": {Object: map[string]interface{}{
			"apiVersion": "kubeovn.io/v1", "kind": "IptablesSnatRule",
			"metadata": map[string]interface{}{"name": "snat-vnet-a"},
			"status":   map[string]interface{}{"ready": false},
		}},
	}}
	c := &Client{access: acc}
	ctx := context.Background()

	if err := c.waitForPodRunning(ctx, "kube-system", "vpc-nat-gw-natgw-vnet-a-0", 20*time.Millisecond); err == nil {
		t.Error("waitForPodRunning on a Pending pod must time out, got nil")
	} else if !strings.Contains(err.Error(), "did not reach Running") {
		t.Errorf("waitForPodRunning timeout error = %q, want the did-not-reach-Running message", err)
	}
	if err := c.waitForEIPReady(ctx, "eip-vnet-a", 20*time.Millisecond); err == nil {
		t.Error("waitForEIPReady on a not-ready EIP must time out, got nil")
	} else if !strings.Contains(err.Error(), "did not become ready") {
		t.Errorf("waitForEIPReady timeout error = %q, want the did-not-become-ready message", err)
	}
	if err := c.waitForSnatReady(ctx, "snat-vnet-a", 20*time.Millisecond); err == nil {
		t.Error("waitForSnatReady on a not-ready SNAT rule must time out, got nil")
	} else if !strings.Contains(err.Error(), "did not become ready") {
		t.Errorf("waitForSnatReady timeout error = %q, want the did-not-become-ready message", err)
	}
}

func TestReadEIPAssignedIP_NoIPYetErrors(t *testing.T) {
	// EIP exists but the controller hasn't allocated yet (no .status.ip).
	acc := &natRecordingAccessor{objects: map[string]*unstructured.Unstructured{
		"eip-vnet-a": {Object: map[string]interface{}{
			"apiVersion": "kubeovn.io/v1", "kind": "IptablesEIP",
			"metadata": map[string]interface{}{"name": "eip-vnet-a"},
			"status":   map[string]interface{}{"ready": false},
		}},
	}}
	c := &Client{access: acc}

	_, err := c.readEIPAssignedIP(context.Background(), "eip-vnet-a")
	if err == nil || !strings.Contains(err.Error(), "no .status.ip") {
		t.Errorf("readEIPAssignedIP without an allocation = %v, want a clear no-.status.ip error", err)
	}
}

// ── External-network bootstrap: seam creates with fixed names ─────────────────

func TestEnsureExternalNetworkBootstrap_SeamCreates(t *testing.T) {
	// Run the bootstrap on a REMOTE client (nil dynamic): every one of its four
	// object creations (ProviderNetwork, Vlan, NAD, external Subnet) plus the
	// Subnet existence pre-check must route through the seam.
	acc := &natRecordingAccessor{objects: map[string]*unstructured.Unstructured{}}
	c := NewRemoteClient(acc, "lk", "zone-2")
	c.WithExternalNetwork(ExternalNetworkConfig{
		Bridge:      "mgmt-br",
		CIDR:        "192.168.10.0/24",
		Gateway:     "192.168.10.254",
		ReservedIPs: []string{"192.168.10.15"},
		VLANID:      100,
	})

	if err := c.EnsureExternalNetworkBootstrap(context.Background()); err != nil {
		t.Fatalf("EnsureExternalNetworkBootstrap (remote): %v", err)
	}

	if len(acc.creates) != 4 {
		t.Fatalf("seam Create called %d times, want 4 (ProviderNetwork, Vlan, NAD, Subnet)", len(acc.creates))
	}
	wantCreates := []struct {
		gvr  schema.GroupVersionResource
		ns   string
		name string
	}{
		{providerNetworkGVR, "", "mgmt-br"},
		{vlanGVR, "", "ext-vlan-100"},
		{nadGVR, "kube-system", "ovn-vpc-external-network"},
		{subnetGVR, "", "ovn-vpc-external-network"},
	}
	for i, want := range wantCreates {
		rec := acc.creates[i]
		if rec.gvr != want.gvr {
			t.Errorf("create[%d] GVR = %v, want %v", i, rec.gvr, want.gvr)
		}
		if rec.ns != want.ns {
			t.Errorf("create[%d] namespace = %q, want %q", i, rec.ns, want.ns)
		}
		assertFixedName(t, rec.obj, want.name)
	}

	// The Vlan's spec.id must be int64-typed (unstructured content is
	// JSON-typed; a plain int would panic in DeepCopyJSON at runtime) and carry
	// the configured VLAN id. The explicit .(int64) assertion pins the cast in
	// ensureVlan — removing it would fail here, not at runtime.
	vlanSpec, _, _ := unstructured.NestedMap(acc.creates[1].obj.Object, "spec")
	if id, ok := vlanSpec["id"].(int64); !ok || id != 100 {
		t.Errorf("Vlan spec.id = %v (%T), want int64(100)", vlanSpec["id"], vlanSpec["id"])
	}

	// The external Subnet excludes the gateway + reserved IPs and carries the
	// 3-dot-segment provider (gotcha 2).
	subnetSpec, _, _ := unstructured.NestedMap(acc.creates[3].obj.Object, "spec")
	assertJSONEqual(t, subnetSpec["excludeIps"], []interface{}{"192.168.10.254", "192.168.10.15"}, "external subnet excludeIps")
	if subnetSpec["provider"] != "ovn-vpc-external-network.kube-system.ovn" {
		t.Errorf("external subnet provider = %v, want ovn-vpc-external-network.kube-system.ovn", subnetSpec["provider"])
	}

	// The existence pre-check Get routed through the seam too.
	if !acc.sawGetGVR(subnetGVR) {
		t.Error("external Subnet existence pre-check did not go through the seam")
	}
}

func TestEnsureExternalNetworkBootstrap_ConfigMissing(t *testing.T) {
	acc := &natRecordingAccessor{}
	c := NewRemoteClient(acc, "lk", "zone-2")

	// Without WithExternalNetwork the bootstrap must fail clearly BEFORE any
	// seam I/O.
	if err := c.EnsureExternalNetworkBootstrap(context.Background()); err == nil {
		t.Fatal("bootstrap without external network config must error")
	}
	if len(acc.creates)+len(acc.gets) != 0 {
		t.Errorf("bootstrap touched the seam %d times despite missing config, want 0", len(acc.creates)+len(acc.gets))
	}
}

// ── Remote client: the FULL NAT lifecycle works with NO dynamic client ────────

// TestRemoteClient_NATLifecycle_RoutesThroughSeam is the regression guard that
// pins the gap this slice closed: a REMOTE kubeovn client (c.dynamic == nil)
// runs the whole per-VPC NAT lifecycle purely through the seam — re-introducing
// a c.dynamic dereference would panic here, and re-introducing a localOnlyErr
// guard would fail the calls.
func TestRemoteClient_NATLifecycle_RoutesThroughSeam(t *testing.T) {
	acc := &natRecordingAccessor{objects: convergedNATObjects("192.168.10.100")}
	c := NewRemoteClient(acc, "lk", "zone-2")
	if c.dynamic != nil {
		t.Fatal("remote client must have a nil dynamic client")
	}
	ctx := context.Background()

	// Ensure: full chain through the seam.
	ip, err := c.EnsureVpcNAT(ctx, "vnet-a", "10.0.1.0/24", "subnet-1", net.ParseIP("10.0.1.254"))
	if err != nil {
		t.Fatalf("EnsureVpcNAT (remote): %v", err)
	}
	if ip.String() != "192.168.10.100" {
		t.Errorf("assigned EIP = %s, want 192.168.10.100", ip)
	}

	// Presence check.
	acc.mu.Lock()
	acc.objects["natgw-vnet-a"] = &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kubeovn.io/v1", "kind": "VpcNatGateway",
		"metadata": map[string]interface{}{"name": "natgw-vnet-a"},
	}}
	acc.mu.Unlock()
	present, err := c.IsVpcNATPresent(ctx, "vnet-a")
	if err != nil {
		t.Fatalf("IsVpcNATPresent (remote): %v", err)
	}
	if !present {
		t.Error("IsVpcNATPresent = false, want true")
	}

	// Teardown + drain.
	if err := c.DeleteVpcNAT(ctx, "vnet-a"); err != nil {
		t.Fatalf("DeleteVpcNAT (remote): %v", err)
	}
	if err := c.WaitVpcNATPodsGone(ctx, "vnet-a", 5*time.Second); err != nil {
		t.Fatalf("WaitVpcNATPodsGone (remote): %v", err)
	}

	if len(acc.creates) != 3 {
		t.Errorf("seam Create called %d times, want 3", len(acc.creates))
	}
	if len(acc.deletes) != 3 {
		t.Errorf("seam Delete called %d times, want 3", len(acc.deletes))
	}
	if len(acc.lists) != 1 {
		t.Errorf("seam List called %d times, want 1", len(acc.lists))
	}
	// Two seam applies: the ensure default route + the delete route strip.
	if len(acc.applies) != 2 {
		t.Errorf("seam Apply called %d times, want 2 (route ensure + route strip)", len(acc.applies))
	}
}
