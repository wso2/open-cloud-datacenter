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
