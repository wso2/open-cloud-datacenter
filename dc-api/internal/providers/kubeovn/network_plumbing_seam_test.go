package kubeovn

// network_plumbing_seam_test.go proves the VPC/Subnet spec-write plumbing slice
// of the credential-locality work: the peering (Vpc.spec.vpcPeerings),
// route-table (Vpc.spec.staticRoutes), and NSG ACL (Subnet.spec.acls)
// read-modify-write ops run through the cluster-access seam — reads via
// c.access.Get, writes via c.access.Apply of a MINIMAL single-field object —
// so they work for a REMOTE (agent-only) zone while the local Direct path keeps
// the exact list semantics of the old JSON MergePatch.
//
// Coverage:
//   - Each of the four converted write sites (appendVpcPeering, removeVpcPeering,
//     patchVPCStaticRoutes, patchSubnetACLs) issues ONE seam Apply with the right
//     GVR/name, a minimal apply object (apiVersion, kind, metadata.name, exactly
//     the one spec list — no status, no other spec fields, no namespace: both
//     CRDs are cluster-scoped), the dedicated per-list field manager, force=true,
//     and list CONTENT equal to what the old merge patch would have written.
//   - The public read-modify-write ops (UpdateRouteTableRoutes, UpdateNSGRules,
//     DetachNSGFromSubnet) read through the seam Get and write the filter+build
//     result of the SAME helpers the merge-patch path used.
//   - Routed toggle: with the decision allowing {Get, Apply} the ops hit the
//     agent accessor and never Direct; with the decision declining they hit
//     Direct and never the agent.
//   - Remote client (NewRemoteClient, c.dynamic == nil): the peering/route/ACL
//     ops work purely through the seam — the regression guard against
//     re-introducing a c.dynamic dereference (which would panic) or a
//     localOnlyErr guard (which would re-open the remote gap). The legacy
//     subnet-CIDR fallback List stays fail-closed (NoCreds) on a remote zone.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/wso2/dc-api/internal/models"
	"github.com/wso2/dc-api/internal/providers/clusteraccess"
)

// ── A recording accessor serving canned objects and capturing applies ─────────

type recordedApply struct {
	gvr          schema.GroupVersionResource
	ns           string
	obj          *unstructured.Unstructured
	fieldManager string
	force        bool
}

// plumbRecordingAccessor serves programmable objects from Get (keyed by name)
// and records every Apply with its full argument set. List returns an empty
// list unless listErr is set. Create/Update/Delete satisfy the interface inertly
// — the plumbing ops under test never call them.
type plumbRecordingAccessor struct {
	mu sync.Mutex

	// objects maps name → the object Get returns. A missing name returns a
	// k8s NotFound, matching the seam contract on both Direct and AgentBacked.
	objects map[string]*unstructured.Unstructured

	getCalls []schema.GroupVersionResource
	applies  []recordedApply

	listCalls int
	listErr   error
}

var _ clusteraccess.Accessor = (*plumbRecordingAccessor)(nil)

func (a *plumbRecordingAccessor) Get(_ context.Context, gvr schema.GroupVersionResource, _, name string, _ metav1.GetOptions) (*unstructured.Unstructured, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.getCalls = append(a.getCalls, gvr)
	obj, ok := a.objects[name]
	if !ok {
		return nil, k8serrors.NewNotFound(gvr.GroupResource(), name)
	}
	return obj.DeepCopy(), nil
}

func (a *plumbRecordingAccessor) List(_ context.Context, _ schema.GroupVersionResource, _ string, _ metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.listCalls++
	if a.listErr != nil {
		return nil, a.listErr
	}
	return &unstructured.UnstructuredList{}, nil
}

func (a *plumbRecordingAccessor) Create(_ context.Context, _ schema.GroupVersionResource, _ string, obj *unstructured.Unstructured, _ metav1.CreateOptions) (*unstructured.Unstructured, error) {
	return obj, nil
}

func (a *plumbRecordingAccessor) Apply(_ context.Context, gvr schema.GroupVersionResource, ns string, obj *unstructured.Unstructured, fieldManager string, force bool) (*unstructured.Unstructured, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Store the object as-is (no DeepCopy): the driver builds list entries from
	// model structs whose ints are plain `int`, which the unstructured JSON
	// deep-copier rejects — json.Marshal (what both real seams do) handles them
	// fine, and the driver never mutates the object after the apply returns.
	a.applies = append(a.applies, recordedApply{gvr: gvr, ns: ns, obj: obj, fieldManager: fieldManager, force: force})
	return obj, nil
}

func (a *plumbRecordingAccessor) Update(_ context.Context, _ schema.GroupVersionResource, _ string, obj *unstructured.Unstructured, _ metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	return obj, nil
}

func (a *plumbRecordingAccessor) Delete(context.Context, schema.GroupVersionResource, string, string, metav1.DeleteOptions) error {
	return nil
}

func (a *plumbRecordingAccessor) applyCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.applies)
}

func (a *plumbRecordingAccessor) applyAt(t *testing.T, i int) recordedApply {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if i >= len(a.applies) {
		t.Fatalf("apply[%d] requested but only %d applies recorded", i, len(a.applies))
	}
	return a.applies[i]
}

// seededVpc returns a Vpc carrying MORE than the plumbing lists — labels,
// annotations, spec.namespaces, and a status — so the minimal-apply assertions
// prove those fields were NOT copied into the apply object.
func seededVpc(name string, peerings, routes []interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kubeovn.io/v1", "kind": "Vpc",
		"metadata": map[string]interface{}{
			"name":   name,
			"labels": map[string]interface{}{"dc-api/managed": "true", "dc-api/tenant": "tenant"},
		},
		"spec": map[string]interface{}{
			"namespaces":   []interface{}{"dc-tenant-proj"},
			"vpcPeerings":  peerings,
			"staticRoutes": routes,
		},
		"status": map[string]interface{}{"standby": true},
	}}
}

func seededSubnet(name string, acls []interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kubeovn.io/v1", "kind": "Subnet",
		"metadata": map[string]interface{}{
			"name":   name,
			"labels": map[string]interface{}{"dc-api/managed": "true"},
		},
		"spec": map[string]interface{}{
			"cidrBlock": "10.0.1.0/24",
			"vpc":       "vnet-a",
			"acls":      acls,
		},
		"status": map[string]interface{}{"v4availableIPs": float64(200)},
	}}
}

// assertMinimalApply asserts the captured apply object is EXACTLY the minimal
// single-field shape: top-level {apiVersion, kind, metadata, spec}, metadata
// exactly {name}, spec exactly {specField}, and no status. It returns the spec
// list for content assertions.
func assertMinimalApply(t *testing.T, rec recordedApply, wantGVR schema.GroupVersionResource, wantKind, wantName, specField, wantManager string) []interface{} {
	t.Helper()

	if rec.gvr != wantGVR {
		t.Errorf("apply GVR = %v, want %v", rec.gvr, wantGVR)
	}
	// Vpc and Subnet are cluster-scoped — the seam namespace must be empty.
	if rec.ns != "" {
		t.Errorf("apply namespace = %q, want \"\" (cluster-scoped)", rec.ns)
	}
	if rec.fieldManager != wantManager {
		t.Errorf("apply fieldManager = %q, want %q", rec.fieldManager, wantManager)
	}
	if !rec.force {
		t.Error("apply force = false, want true (dc-api is the sole intended owner of these spec lists)")
	}

	obj := rec.obj.Object
	if av, _ := obj["apiVersion"].(string); av != "kubeovn.io/v1" {
		t.Errorf("apply apiVersion = %q, want kubeovn.io/v1", av)
	}
	if k, _ := obj["kind"].(string); k != wantKind {
		t.Errorf("apply kind = %q, want %q", k, wantKind)
	}

	wantTop := map[string]bool{"apiVersion": true, "kind": true, "metadata": true, "spec": true}
	for k := range obj {
		if !wantTop[k] {
			t.Errorf("apply object carries unexpected top-level field %q — the object must be minimal (in particular: no status)", k)
		}
	}
	meta, _ := obj["metadata"].(map[string]interface{})
	if name, _ := meta["name"].(string); name != wantName {
		t.Errorf("apply metadata.name = %q, want %q", name, wantName)
	}
	for k := range meta {
		if k != "name" {
			t.Errorf("apply metadata carries unexpected field %q — labels/annotations/namespace must not be claimed", k)
		}
	}
	spec, _ := obj["spec"].(map[string]interface{})
	if spec == nil {
		t.Fatal("apply object has no spec")
	}
	for k := range spec {
		if k != specField {
			t.Errorf("apply spec carries unexpected field %q — only %q may be written (claiming a sibling list would let SSA prune it)", k, specField)
		}
	}
	list, ok := spec[specField].([]interface{})
	if !ok {
		t.Fatalf("apply spec.%s is %T, want []interface{}", specField, spec[specField])
	}
	return list
}

// assertJSONEqual asserts two values marshal to identical JSON — the
// captured-apply-list vs old-merge-patch-list equivalence check.
func assertJSONEqual(t *testing.T, got, want interface{}, what string) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal got %s: %v", what, err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal want %s: %v", what, err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("%s mismatch:\n  applied:      %s\n  merge-patch would have written: %s", what, gotJSON, wantJSON)
	}
}

// ── The four write sites: minimal apply + merge-patch-equivalent content ──────

func TestAppendVpcPeering_SeamApply_MinimalObjectAndUpsert(t *testing.T) {
	keepEntry := map[string]interface{}{"remoteVpc": "vnet-old", "localConnectIP": "100.64.5.1/24"}
	acc := &plumbRecordingAccessor{objects: map[string]*unstructured.Unstructured{
		"vnet-a": seededVpc("vnet-a", []interface{}{keepEntry}, []interface{}{}),
	}}
	c := &Client{access: acc}

	if err := c.appendVpcPeering(context.Background(), "vnet-a", "vnet-b", "100.64.10.0/24"); err != nil {
		t.Fatalf("appendVpcPeering: %v", err)
	}

	// The read went through the seam on the vpc family.
	if len(acc.getCalls) != 1 || acc.getCalls[0] != vpcGVR {
		t.Errorf("seam Get calls = %v, want exactly one vpcGVR Get", acc.getCalls)
	}
	if got := acc.applyCount(); got != 1 {
		t.Fatalf("seam Apply called %d times, want 1", got)
	}

	list := assertMinimalApply(t, acc.applyAt(t, 0), vpcGVR, "Vpc", "vnet-a", "vpcPeerings", fieldManagerVpcPeerings)

	// Content equivalence: exactly what the old merge patch wrote — the existing
	// entry preserved, the new upserted entry appended with the deterministic
	// localConnectIP ("vnet-a" sorts before "vnet-b" → host octet .1).
	want := []interface{}{
		keepEntry,
		map[string]interface{}{"remoteVpc": "vnet-b", "localConnectIP": "100.64.10.1/24"},
	}
	assertJSONEqual(t, list, want, "spec.vpcPeerings")
}

func TestAppendVpcPeering_SeamApply_ReplacesExistingEntry(t *testing.T) {
	// An entry for the same peer already exists (retry) — it is REPLACED, not
	// duplicated, exactly as the merge-patch path behaved.
	stale := map[string]interface{}{"remoteVpc": "vnet-b", "localConnectIP": "100.64.99.1/24"}
	acc := &plumbRecordingAccessor{objects: map[string]*unstructured.Unstructured{
		"vnet-a": seededVpc("vnet-a", []interface{}{stale}, []interface{}{}),
	}}
	c := &Client{access: acc}

	if err := c.appendVpcPeering(context.Background(), "vnet-a", "vnet-b", "100.64.10.0/24"); err != nil {
		t.Fatalf("appendVpcPeering: %v", err)
	}
	list := assertMinimalApply(t, acc.applyAt(t, 0), vpcGVR, "Vpc", "vnet-a", "vpcPeerings", fieldManagerVpcPeerings)
	want := []interface{}{
		map[string]interface{}{"remoteVpc": "vnet-b", "localConnectIP": "100.64.10.1/24"},
	}
	assertJSONEqual(t, list, want, "spec.vpcPeerings")
}

func TestRemoveVpcPeering_SeamApply_FiltersEntry(t *testing.T) {
	keep := map[string]interface{}{"remoteVpc": "vnet-c", "localConnectIP": "100.64.7.1/24"}
	drop := map[string]interface{}{"remoteVpc": "vnet-b", "localConnectIP": "100.64.10.1/24"}
	acc := &plumbRecordingAccessor{objects: map[string]*unstructured.Unstructured{
		"vnet-a": seededVpc("vnet-a", []interface{}{keep, drop}, []interface{}{}),
	}}
	c := &Client{access: acc}

	if err := c.removeVpcPeering(context.Background(), "vnet-a", "vnet-b"); err != nil {
		t.Fatalf("removeVpcPeering: %v", err)
	}
	list := assertMinimalApply(t, acc.applyAt(t, 0), vpcGVR, "Vpc", "vnet-a", "vpcPeerings", fieldManagerVpcPeerings)
	assertJSONEqual(t, list, []interface{}{keep}, "spec.vpcPeerings")
}

func TestRemoveVpcPeering_NotFoundIsIdempotent(t *testing.T) {
	acc := &plumbRecordingAccessor{objects: map[string]*unstructured.Unstructured{}}
	c := &Client{access: acc}

	// The VPC is gone — the op must be a silent no-op (IsNotFound tolerance
	// preserved) with no apply issued.
	if err := c.removeVpcPeering(context.Background(), "vnet-gone", "vnet-b"); err != nil {
		t.Fatalf("removeVpcPeering on a missing VPC must be nil, got %v", err)
	}
	if got := acc.applyCount(); got != 0 {
		t.Errorf("seam Apply called %d times for a missing VPC, want 0", got)
	}
}

func TestPatchVPCStaticRoutes_SeamApply_MinimalObject(t *testing.T) {
	acc := &plumbRecordingAccessor{}
	c := &Client{access: acc}

	routes := []interface{}{
		map[string]interface{}{"cidr": "10.9.0.0/16", "nextHopIP": "10.0.0.1", "policy": "policyDst", "routeTable": "routetable-rt-1"},
	}
	if err := c.patchVPCStaticRoutes(context.Background(), "vnet-a", routes); err != nil {
		t.Fatalf("patchVPCStaticRoutes: %v", err)
	}
	list := assertMinimalApply(t, acc.applyAt(t, 0), vpcGVR, "Vpc", "vnet-a", "staticRoutes", fieldManagerStaticRoutes)
	assertJSONEqual(t, list, routes, "spec.staticRoutes")
}

func TestPatchVPCStaticRoutes_NilBecomesEmptyList(t *testing.T) {
	acc := &plumbRecordingAccessor{}
	c := &Client{access: acc}

	if err := c.patchVPCStaticRoutes(context.Background(), "vnet-a", nil); err != nil {
		t.Fatalf("patchVPCStaticRoutes(nil): %v", err)
	}
	list := assertMinimalApply(t, acc.applyAt(t, 0), vpcGVR, "Vpc", "vnet-a", "staticRoutes", fieldManagerStaticRoutes)
	// The merge patch wrote an explicit empty JSON array; the apply must too
	// (a JSON null would not clear the list).
	if b, _ := json.Marshal(list); string(b) != "[]" {
		t.Errorf("spec.staticRoutes marshals to %s, want [] (explicit empty list)", b)
	}
}

func TestPatchSubnetACLs_SeamApply_MinimalObject(t *testing.T) {
	acc := &plumbRecordingAccessor{}
	c := &Client{access: acc}

	acls := []interface{}{
		map[string]interface{}{"direction": "to-lport", "priority": int64(2001), "match": `inport == "nsg-x" || (tcp)`, "action": "allow-related"},
	}
	if err := c.patchSubnetACLs(context.Background(), "subnet-1", acls); err != nil {
		t.Fatalf("patchSubnetACLs: %v", err)
	}
	list := assertMinimalApply(t, acc.applyAt(t, 0), subnetGVR, "Subnet", "subnet-1", "acls", fieldManagerSubnetACLs)
	assertJSONEqual(t, list, acls, "spec.acls")
}

// ── Public read-modify-write ops: seam Get + filter/build + seam Apply ────────

func TestUpdateRouteTableRoutes_SeamReadModifyApply(t *testing.T) {
	ownedByRT := map[string]interface{}{"cidr": "10.1.0.0/16", "nextHopIP": "10.0.0.9", "policy": "policyDst", "routeTable": "routetable-rt-1"}
	otherRT := map[string]interface{}{"cidr": "10.2.0.0/16", "nextHopIP": "10.0.0.8", "policy": "policyDst", "routeTable": "routetable-rt-2"}
	peering := map[string]interface{}{"cidr": "10.3.0.0/16", "nextHopIP": "100.64.9.2", "policy": "policyDst", "routeTable": ""}
	acc := &plumbRecordingAccessor{objects: map[string]*unstructured.Unstructured{
		"vnet-a": seededVpc("vnet-a", []interface{}{}, []interface{}{ownedByRT, otherRT, peering}),
	}}
	c := &Client{access: acc}

	rules := []models.RouteRule{{Name: "to-fw", DestinationCIDR: "10.1.0.0/16", NextHopType: "virtual_appliance", NextHopIP: "10.0.0.10"}}
	if err := c.UpdateRouteTableRoutes(context.Background(), "vnet-a/rt-1", rules); err != nil {
		t.Fatalf("UpdateRouteTableRoutes: %v", err)
	}

	if len(acc.getCalls) != 1 || acc.getCalls[0] != vpcGVR {
		t.Errorf("seam Get calls = %v, want exactly one vpcGVR Get", acc.getCalls)
	}
	list := assertMinimalApply(t, acc.applyAt(t, 0), vpcGVR, "Vpc", "vnet-a", "staticRoutes", fieldManagerStaticRoutes)

	// Old merge-patch content: everything NOT owned by rt-1, then the freshly
	// built rt-1 entries — computed with the same helpers the driver uses.
	want := append(
		filterSliceByTag([]interface{}{ownedByRT, otherRT, peering}, "routeTable", routeTableTag("rt-1")),
		buildStaticRouteEntries(rules, routeTableTag("rt-1"))...,
	)
	assertJSONEqual(t, list, want, "spec.staticRoutes")
}

func TestDeleteRouteTable_SeamReadModifyApply(t *testing.T) {
	ownedByRT := map[string]interface{}{"cidr": "10.1.0.0/16", "nextHopIP": "10.0.0.9", "policy": "policyDst", "routeTable": "routetable-rt-1"}
	otherRT := map[string]interface{}{"cidr": "10.2.0.0/16", "nextHopIP": "10.0.0.8", "policy": "policyDst", "routeTable": "routetable-rt-2"}
	acc := &plumbRecordingAccessor{objects: map[string]*unstructured.Unstructured{
		"vnet-a": seededVpc("vnet-a", []interface{}{}, []interface{}{ownedByRT, otherRT}),
	}}
	c := &Client{access: acc}

	if err := c.DeleteRouteTable(context.Background(), "vnet-a/rt-1"); err != nil {
		t.Fatalf("DeleteRouteTable: %v", err)
	}
	list := assertMinimalApply(t, acc.applyAt(t, 0), vpcGVR, "Vpc", "vnet-a", "staticRoutes", fieldManagerStaticRoutes)
	assertJSONEqual(t, list, []interface{}{otherRT}, "spec.staticRoutes")
}

func TestDeleteRouteTable_VpcGoneIsIdempotent(t *testing.T) {
	acc := &plumbRecordingAccessor{objects: map[string]*unstructured.Unstructured{}}
	c := &Client{access: acc}

	if err := c.DeleteRouteTable(context.Background(), "vnet-gone/rt-1"); err != nil {
		t.Fatalf("DeleteRouteTable on a missing VPC must be nil, got %v", err)
	}
	if got := acc.applyCount(); got != 0 {
		t.Errorf("seam Apply called %d times for a missing VPC, want 0", got)
	}
}

func TestUpdateNSGRules_SeamReadModifyApply(t *testing.T) {
	const nsgUID = "6b6bd53d-0000-0000-0000-000000000001"
	// One ACL owned by this NSG (to be replaced) and one foreign ACL (preserved).
	owned := map[string]interface{}{"direction": "to-lport", "priority": int64(2500), "match": `inport == "` + nsgACLTag(nsgUID) + `" || (udp)`, "action": "drop"}
	foreign := map[string]interface{}{"direction": "to-lport", "priority": int64(2400), "match": `inport == "nsg-other" || (icmp4)`, "action": "allow-related"}
	acc := &plumbRecordingAccessor{objects: map[string]*unstructured.Unstructured{
		"subnet-1": seededSubnet("subnet-1", []interface{}{owned, foreign}),
	}}
	c := &Client{access: acc}

	rules := []models.NSGRule{{
		Name: "allow-https", Priority: 200, Direction: "inbound", Action: "allow",
		Protocol: "tcp", SourceAddressPrefix: "*", DestinationAddressPrefix: "*", DestinationPortRange: "443",
	}}
	if err := c.UpdateNSGRules(context.Background(), nsgUID+"|subnet-1", rules); err != nil {
		t.Fatalf("UpdateNSGRules: %v", err)
	}

	list := assertMinimalApply(t, acc.applyAt(t, 0), subnetGVR, "Subnet", "subnet-1", "acls", fieldManagerSubnetACLs)
	want := append(
		filterACLsByTag([]interface{}{owned, foreign}, nsgACLTag(nsgUID)),
		buildACLEntries(rules, nsgUID)...,
	)
	assertJSONEqual(t, list, want, "spec.acls")
}

func TestDetachNSGFromSubnet_SeamReadModifyApply(t *testing.T) {
	const nsgUID = "6b6bd53d-0000-0000-0000-000000000001"
	owned := map[string]interface{}{"direction": "to-lport", "priority": int64(2500), "match": `inport == "` + nsgACLTag(nsgUID) + `" || (udp)`, "action": "drop"}
	foreign := map[string]interface{}{"direction": "to-lport", "priority": int64(2400), "match": `inport == "nsg-other" || (icmp4)`, "action": "allow-related"}
	acc := &plumbRecordingAccessor{objects: map[string]*unstructured.Unstructured{
		"subnet-1": seededSubnet("subnet-1", []interface{}{owned, foreign}),
	}}
	c := &Client{access: acc}

	if err := c.DetachNSGFromSubnet(context.Background(), nsgUID, "subnet-1"); err != nil {
		t.Fatalf("DetachNSGFromSubnet: %v", err)
	}
	list := assertMinimalApply(t, acc.applyAt(t, 0), subnetGVR, "Subnet", "subnet-1", "acls", fieldManagerSubnetACLs)
	assertJSONEqual(t, list, []interface{}{foreign}, "spec.acls")
}

func TestDetachNSGFromSubnet_SubnetGoneIsIdempotent(t *testing.T) {
	acc := &plumbRecordingAccessor{objects: map[string]*unstructured.Unstructured{}}
	c := &Client{access: acc}

	if err := c.DetachNSGFromSubnet(context.Background(), "nsg-uid", "subnet-gone"); err != nil {
		t.Fatalf("DetachNSGFromSubnet on a missing subnet must be nil, got %v", err)
	}
	if got := acc.applyCount(); got != 0 {
		t.Errorf("seam Apply called %d times for a missing subnet, want 0", got)
	}
}

// ── Routed toggle: agent vs Direct ────────────────────────────────────────────

// plumbingRouted builds a Routed accessor whose decision activates ONLY the
// plumbing allow-set ({VerbGet, VerbApply}) onto the agent accessor when
// allow=true. allow=false models toggle-off / no-connected-agent (always
// Direct). GVR-aware to match the registry's AgentDecision shape.
func plumbingRouted(direct, agent clusteraccess.Accessor, allow bool) *clusteraccess.Routed {
	return clusteraccess.NewRouted(direct, func(v clusteraccess.Verb, _ schema.GroupVersionResource) (clusteraccess.Accessor, bool) {
		if !allow {
			return nil, false
		}
		switch v {
		case clusteraccess.VerbGet, clusteraccess.VerbApply:
			return agent, true
		default:
			return nil, false
		}
	})
}

func TestUpdateRouteTableRoutes_RoutedToggle_AgentVsDirect(t *testing.T) {
	seed := func() map[string]*unstructured.Unstructured {
		return map[string]*unstructured.Unstructured{
			"vnet-a": seededVpc("vnet-a", []interface{}{}, []interface{}{}),
		}
	}
	rules := []models.RouteRule{{Name: "r", DestinationCIDR: "10.1.0.0/16", NextHopType: "virtual_appliance", NextHopIP: "10.0.0.10"}}

	t.Run("toggle on routes to agent", func(t *testing.T) {
		agent := &plumbRecordingAccessor{objects: seed()}
		direct := &plumbRecordingAccessor{objects: seed()}
		c := &Client{access: plumbingRouted(direct, agent, true)}

		if err := c.UpdateRouteTableRoutes(context.Background(), "vnet-a/rt-1", rules); err != nil {
			t.Fatalf("UpdateRouteTableRoutes: %v", err)
		}
		if got := agent.applyCount(); got != 1 {
			t.Errorf("agent Apply called %d times, want 1", got)
		}
		if len(agent.getCalls) != 1 {
			t.Errorf("agent Get called %d times, want 1", len(agent.getCalls))
		}
		if got := direct.applyCount() + len(direct.getCalls); got != 0 {
			t.Errorf("direct seam touched %d times with toggle ON, want 0", got)
		}
	})

	t.Run("toggle off uses direct", func(t *testing.T) {
		agent := &plumbRecordingAccessor{objects: seed()}
		direct := &plumbRecordingAccessor{objects: seed()}
		c := &Client{access: plumbingRouted(direct, agent, false)}

		if err := c.UpdateRouteTableRoutes(context.Background(), "vnet-a/rt-1", rules); err != nil {
			t.Fatalf("UpdateRouteTableRoutes: %v", err)
		}
		if got := direct.applyCount(); got != 1 {
			t.Errorf("direct Apply called %d times, want 1", got)
		}
		if got := agent.applyCount() + len(agent.getCalls); got != 0 {
			t.Errorf("agent seam touched %d times with toggle OFF, want 0", got)
		}
	})
}

// ── Remote client: the plumbing ops work with NO dynamic client ───────────────

func TestRemoteClient_CreatePeering_RoutesThroughSeam(t *testing.T) {
	acc := &plumbRecordingAccessor{objects: map[string]*unstructured.Unstructured{
		"vnet-a": seededVpc("vnet-a", []interface{}{}, []interface{}{}),
		"vnet-b": seededVpc("vnet-b", []interface{}{}, []interface{}{}),
	}}
	c := NewRemoteClient(acc, "lk", "zone-2")
	if c.dynamic != nil {
		t.Fatal("remote client must have a nil dynamic client")
	}

	res, err := c.CreatePeering(context.Background(), "vnet-a", "vnet-b", models.PeeringSpec{
		TransitCIDR:      "100.64.10.0/24",
		AddressSpace:     []string{"10.1.0.0/16"},
		PeerAddressSpace: []string{"10.2.0.0/16"},
	})
	if err != nil {
		t.Fatalf("CreatePeering (remote): %v", err)
	}
	if res.Status != models.StatusActive {
		t.Errorf("status = %q, want ACTIVE", res.Status)
	}
	// Four seam applies: vpcPeerings on both VPCs, then staticRoutes on both.
	if got := acc.applyCount(); got != 4 {
		t.Fatalf("seam Apply called %d times, want 4 (2 vpcPeerings + 2 staticRoutes)", got)
	}
	// The address-space was supplied, so the legacy subnet-CIDR fallback List
	// must not have run.
	if acc.listCalls != 0 {
		t.Errorf("subnet-CIDR fallback List ran %d times, want 0 (address-space supplied)", acc.listCalls)
	}

	// Spot-check the vnet-a staticRoutes apply: destination = peer's CIDR,
	// nextHop = the PEER side's transit IP (.2 — "vnet-b" sorts after "vnet-a").
	var routesApply *recordedApply
	for i := 0; i < acc.applyCount(); i++ {
		rec := acc.applyAt(t, i)
		spec, _ := rec.obj.Object["spec"].(map[string]interface{})
		if _, isRoutes := spec["staticRoutes"]; isRoutes && rec.obj.GetName() == "vnet-a" {
			r := rec
			routesApply = &r
			break
		}
	}
	if routesApply == nil {
		t.Fatal("no staticRoutes apply recorded for vnet-a")
	}
	list := assertMinimalApply(t, *routesApply, vpcGVR, "Vpc", "vnet-a", "staticRoutes", fieldManagerStaticRoutes)
	want := []interface{}{map[string]interface{}{
		"cidr": "10.2.0.0/16", "nextHopIP": "100.64.10.2", "policy": "policyDst", "routeTable": "",
	}}
	assertJSONEqual(t, list, want, "spec.staticRoutes (vnet-a)")
}

func TestRemoteClient_DeletePeering_RoutesThroughSeam(t *testing.T) {
	peerEntryOnA := map[string]interface{}{"remoteVpc": "vnet-b", "localConnectIP": "100.64.10.1/24"}
	peerEntryOnB := map[string]interface{}{"remoteVpc": "vnet-a", "localConnectIP": "100.64.10.2/24"}
	routeOnA := map[string]interface{}{"cidr": "10.2.0.0/16", "nextHopIP": "100.64.10.2", "policy": "policyDst", "routeTable": ""}
	routeOnB := map[string]interface{}{"cidr": "10.1.0.0/16", "nextHopIP": "100.64.10.1", "policy": "policyDst", "routeTable": ""}
	acc := &plumbRecordingAccessor{objects: map[string]*unstructured.Unstructured{
		"vnet-a": seededVpc("vnet-a", []interface{}{peerEntryOnA}, []interface{}{routeOnA}),
		"vnet-b": seededVpc("vnet-b", []interface{}{peerEntryOnB}, []interface{}{routeOnB}),
	}}
	c := NewRemoteClient(acc, "lk", "zone-2")

	if err := c.DeletePeering(context.Background(), "vnet-a/vnet-b", []string{"10.1.0.0/16"}, []string{"10.2.0.0/16"}); err != nil {
		t.Fatalf("DeletePeering (remote): %v", err)
	}
	if got := acc.applyCount(); got != 4 {
		t.Fatalf("seam Apply called %d times, want 4 (2 vpcPeerings removals + 2 staticRoutes removals)", got)
	}
	// Every apply cleared its list (all owned entries removed).
	for i := 0; i < 4; i++ {
		rec := acc.applyAt(t, i)
		spec, _ := rec.obj.Object["spec"].(map[string]interface{})
		for field, v := range spec {
			if list, ok := v.([]interface{}); !ok || len(list) != 0 {
				t.Errorf("apply[%d] spec.%s = %v, want an empty list after peering teardown", i, field, v)
			}
		}
	}
}

func TestRemoteClient_RouteTableAndNSGOps_RouteThroughSeam(t *testing.T) {
	acc := &plumbRecordingAccessor{objects: map[string]*unstructured.Unstructured{
		"vnet-a":   seededVpc("vnet-a", []interface{}{}, []interface{}{}),
		"subnet-1": seededSubnet("subnet-1", []interface{}{}),
	}}
	c := NewRemoteClient(acc, "lk", "zone-2")
	ctx := context.Background()

	// CreateRouteTable does no k8s I/O and must no longer be local-only.
	if _, err := c.CreateRouteTable(ctx, "vnet-a", models.RouteTableSpec{
		Routes: []models.RouteRule{{Name: "r", DestinationCIDR: "0.0.0.0/0", NextHopType: "virtual_appliance", NextHopIP: "10.0.0.1"}},
	}); err != nil {
		t.Errorf("CreateRouteTable (remote): %v", err)
	}
	if err := c.UpdateRouteTableRoutes(ctx, "vnet-a/rt-1", []models.RouteRule{{Name: "r", DestinationCIDR: "10.9.0.0/16", NextHopType: "virtual_appliance", NextHopIP: "10.0.0.1"}}); err != nil {
		t.Errorf("UpdateRouteTableRoutes (remote): %v", err)
	}
	if err := c.DeleteRouteTable(ctx, "vnet-a/rt-1"); err != nil {
		t.Errorf("DeleteRouteTable (remote): %v", err)
	}
	if err := c.AssociateRouteTable(ctx, "vnet-a/rt-1", "subnet-1"); err != nil {
		t.Errorf("AssociateRouteTable (remote): %v", err)
	}
	if _, err := c.CreateNSG(ctx, "tenant", "proj", models.NSGSpec{Name: "web"}); err != nil {
		t.Errorf("CreateNSG (remote): %v", err)
	}
	if err := c.AttachNSGToSubnet(ctx, "nsg-uid", "subnet-1"); err != nil {
		t.Errorf("AttachNSGToSubnet (remote): %v", err)
	}
	if err := c.UpdateNSGRules(ctx, "nsg-uid|subnet-1", []models.NSGRule{{
		Name: "allow-ssh", Priority: 100, Direction: "inbound", Action: "allow", Protocol: "tcp", DestinationPortRange: "22",
	}}); err != nil {
		t.Errorf("UpdateNSGRules (remote): %v", err)
	}
	if err := c.DetachNSGFromSubnet(ctx, "nsg-uid", "subnet-1"); err != nil {
		t.Errorf("DetachNSGFromSubnet (remote): %v", err)
	}
	if err := c.DeleteNSG(ctx, "nsg-uid"); err != nil {
		t.Errorf("DeleteNSG (remote): %v", err)
	}

	// Route-table update + delete (2 staticRoutes applies) and NSG update +
	// detach (2 acls applies) all went through the seam.
	if got := acc.applyCount(); got != 4 {
		t.Errorf("seam Apply called %d times, want 4", got)
	}
}

// TestRemoteClient_LegacyPeeringCIDRFallback_FailsClosed pins the deliberate
// limit of the slice: a remote peering created WITHOUT an address-space (legacy
// callers only) needs the subnet-CIDR fallback List, and VerbList is not in the
// subnet family's RouteVerbs — so on a remote zone the List lands on the
// NoCreds fallback and fails CLOSED with a clear error instead of panicking on
// the nil dynamic client or silently listing the wrong cluster.
func TestRemoteClient_LegacyPeeringCIDRFallback_FailsClosed(t *testing.T) {
	agent := &plumbRecordingAccessor{objects: map[string]*unstructured.Unstructured{
		"vnet-a": seededVpc("vnet-a", []interface{}{}, []interface{}{}),
		"vnet-b": seededVpc("vnet-b", []interface{}{}, []interface{}{}),
	}}
	// Mirror buildRemoteSet: Routed whose Direct fallback is NoCreds, decision
	// routing only the verbs the plumbing families declare (Get/Apply here; List
	// deliberately absent — mirroring subnetCapability.RouteVerbs).
	routed := clusteraccess.NewRouted(
		clusteraccess.NewNoCreds("lk", "zone-2"),
		func(v clusteraccess.Verb, _ schema.GroupVersionResource) (clusteraccess.Accessor, bool) {
			switch v {
			case clusteraccess.VerbGet, clusteraccess.VerbApply:
				return agent, true
			default:
				return nil, false
			}
		})
	c := NewRemoteClient(routed, "lk", "zone-2")

	_, err := c.CreatePeering(context.Background(), "vnet-a", "vnet-b", models.PeeringSpec{
		TransitCIDR: "100.64.10.0/24", // no AddressSpace/PeerAddressSpace → legacy fallback
	})
	if err == nil {
		t.Fatal("legacy CreatePeering (no address-space) on a remote zone must fail closed, got nil")
	}
	if !strings.Contains(err.Error(), "no agent connected for zone lk/zone-2") {
		t.Errorf("error = %q, want the NoCreds fail-closed message naming the zone", err.Error())
	}
}
