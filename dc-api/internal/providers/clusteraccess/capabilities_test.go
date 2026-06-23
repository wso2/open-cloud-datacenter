package clusteraccess

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// TestUnionRouteVerbs pins the registry-derived routing allow-set to today's
// hand-maintained set EXACTLY: {VerbGet, VerbCreate, VerbDelete}. Any drift
// (e.g. accidentally giving a pre-seeded capability RouteVerbs) fails here.
func TestUnionRouteVerbs(t *testing.T) {
	got := UnionRouteVerbs()
	want := map[Verb]bool{VerbGet: true, VerbCreate: true, VerbDelete: true}

	if len(got) != len(want) {
		t.Fatalf("union route verbs size = %d, want %d (got %v)", len(got), len(want), got)
	}
	for v := range want {
		if !got[v] {
			t.Errorf("union route verbs missing %v", v)
		}
	}
	// Explicitly assert the verbs that must NOT be routable.
	for _, v := range []Verb{VerbList, VerbApply, VerbUpdate, VerbWatch} {
		if got[v] {
			t.Errorf("verb %v must NOT be routable but is in the union", v)
		}
	}
}

// TestRoutableVerbs_VMEqualsUnionToday is the Part A equivalence guard: with the
// VM family the only onboarded routable family before Part B, the per-GVR
// RoutableVerbs(virtualmachines) must equal the cross-family UnionRouteVerbs()
// EXACTLY — proving the GVR-threading change is bit-for-bit identical for VM
// routing decisions. (After Part B, networks declare the SAME {Get,Create,Delete}
// so the union is unchanged; this guard still holds for the VM GVR.)
func TestRoutableVerbs_VMEqualsUnionToday(t *testing.T) {
	vmGVR := schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}
	got, ok := RoutableVerbs(vmGVR)
	if !ok {
		t.Fatal("virtualmachines must be an onboarded routable family")
	}
	want := UnionRouteVerbs()
	if len(got) != len(want) {
		t.Fatalf("RoutableVerbs(vm) size = %d, want %d (union)", len(got), len(want))
	}
	for v := range want {
		if !got[v] {
			t.Errorf("RoutableVerbs(vm) missing %v that the union has", v)
		}
	}
}

// TestRoutableVerbs_UnknownGVRNotRoutable proves a GVR with no RouteVerbs (e.g. a
// pre-seeded GVK-only capability, or an unknown GVR) is NOT routable — the
// per-family gate returns ok=false, so the decision closure falls to Direct.
func TestRoutableVerbs_UnknownGVRNotRoutable(t *testing.T) {
	// virtualmachineimages is GVK-mapped but has no RouteVerbs (pre-seeded).
	imgGVR := schema.GroupVersionResource{Group: "harvesterhci.io", Version: "v1beta1", Resource: "virtualmachineimages"}
	if _, ok := RoutableVerbs(imgGVR); ok {
		t.Error("virtualmachineimages has no RouteVerbs and must NOT be routable")
	}
	// A wholly unknown GVR is never routable.
	if _, ok := RoutableVerbs(schema.GroupVersionResource{Group: "x", Version: "v1", Resource: "widgets"}); ok {
		t.Error("unknown GVR must NOT be routable")
	}
}

// TestDefaultGVKMapperResolvesKnownGVRs pins the derived GVK table to the same
// six entries (and same APIVersion/Kind) the pre-refactor literal had.
func TestDefaultGVKMapperResolvesKnownGVRs(t *testing.T) {
	m := DefaultGVKMapper()
	cases := []struct {
		gvr        schema.GroupVersionResource
		apiVersion string
		kind       string
	}{
		{schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}, "kubevirt.io/v1", "VirtualMachine"},
		{schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachineinstances"}, "kubevirt.io/v1", "VirtualMachineInstance"},
		{schema.GroupVersionResource{Group: "harvesterhci.io", Version: "v1beta1", Resource: "virtualmachineimages"}, "harvesterhci.io/v1beta1", "VirtualMachineImage"},
		{schema.GroupVersionResource{Group: "k8s.cni.cncf.io", Version: "v1", Resource: "network-attachment-definitions"}, "k8s.cni.cncf.io/v1", "NetworkAttachmentDefinition"},
		{schema.GroupVersionResource{Group: "kubeovn.io", Version: "v1", Resource: "vpcs"}, "kubeovn.io/v1", "Vpc"},
		{schema.GroupVersionResource{Group: "kubeovn.io", Version: "v1", Resource: "subnets"}, "kubeovn.io/v1", "Subnet"},
	}
	for _, c := range cases {
		av, k, ok := m.GVK(c.gvr)
		if !ok {
			t.Errorf("GVR %v not resolved", c.gvr)
			continue
		}
		if av != c.apiVersion || k != c.kind {
			t.Errorf("GVR %v → %s/%s, want %s/%s", c.gvr, av, k, c.apiVersion, c.kind)
		}
	}

	// An unknown GVR must not resolve.
	if _, _, ok := m.GVK(schema.GroupVersionResource{Group: "x", Version: "v1", Resource: "widgets"}); ok {
		t.Error("unknown GVR unexpectedly resolved")
	}
}
