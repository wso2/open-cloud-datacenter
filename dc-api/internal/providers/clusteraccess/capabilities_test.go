package clusteraccess

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// TestUnionRouteVerbs pins the registry-derived routing allow-set to the current
// onboarded set EXACTLY: {VerbGet, VerbList, VerbCreate, VerbDelete}. VerbList
// joined the union when ListVMs/ListImages/ListNetworks were routed through the
// agent. Any further drift (e.g. accidentally routing VerbApply/VerbUpdate) fails
// here.
func TestUnionRouteVerbs(t *testing.T) {
	got := UnionRouteVerbs()
	want := map[Verb]bool{VerbGet: true, VerbList: true, VerbCreate: true, VerbDelete: true}

	if len(got) != len(want) {
		t.Fatalf("union route verbs size = %d, want %d (got %v)", len(got), len(want), got)
	}
	for v := range want {
		if !got[v] {
			t.Errorf("union route verbs missing %v", v)
		}
	}
	// Explicitly assert the verbs that must NOT be routable.
	for _, v := range []Verb{VerbApply, VerbUpdate, VerbWatch} {
		if got[v] {
			t.Errorf("verb %v must NOT be routable but is in the union", v)
		}
	}
	// VerbList classifies as a read for the reads/writes toggle split.
	if !IsReadVerb(VerbList) {
		t.Error("VerbList must classify as a read verb")
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

// TestRoutableVerbs_UnknownGVRNotRoutable proves a GVR with no RouteVerbs (a
// pre-seeded GVK-only capability, or an unknown GVR) is NOT routable — the
// per-family gate returns ok=false, so the decision closure falls to Direct.
func TestRoutableVerbs_UnknownGVRNotRoutable(t *testing.T) {
	// virtualmachineinstances is GVK-mapped but has no RouteVerbs (pre-seeded).
	vmiGVR := schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachineinstances"}
	if _, ok := RoutableVerbs(vmiGVR); ok {
		t.Error("virtualmachineinstances has no RouteVerbs and must NOT be routable")
	}
	// A wholly unknown GVR is never routable.
	if _, ok := RoutableVerbs(schema.GroupVersionResource{Group: "x", Version: "v1", Resource: "widgets"}); ok {
		t.Error("unknown GVR must NOT be routable")
	}
}

// TestRoutableVerbs_ListRoutedForListFamilies asserts VerbList is routable for
// exactly the families whose harvester list ops now route through the agent: the
// VM, NAD, and image families.
func TestRoutableVerbs_ListRoutedForListFamilies(t *testing.T) {
	listFamilies := []schema.GroupVersionResource{
		{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"},
		{Group: "k8s.cni.cncf.io", Version: "v1", Resource: "network-attachment-definitions"},
		{Group: "harvesterhci.io", Version: "v1beta1", Resource: "virtualmachineimages"},
	}
	for _, gvr := range listFamilies {
		verbs, ok := RoutableVerbs(gvr)
		if !ok {
			t.Errorf("%s must be an onboarded routable family", gvr.Resource)
			continue
		}
		if !verbs[VerbList] {
			t.Errorf("%s must route VerbList", gvr.Resource)
		}
	}
}

// TestRoutableVerbs_ImageRoutesGetListCreate pins the image family's routable set
// to exactly {Get, List, Create}: the read/list path (resolveImage/ListImages)
// plus CreateImage's create (the image slice). Delete/Apply/Update must NOT be
// routable — image deletion and generic apply stay local-only.
func TestRoutableVerbs_ImageRoutesGetListCreate(t *testing.T) {
	imgGVR := schema.GroupVersionResource{Group: "harvesterhci.io", Version: "v1beta1", Resource: "virtualmachineimages"}
	imgVerbs, ok := RoutableVerbs(imgGVR)
	if !ok {
		t.Fatal("virtualmachineimages must be an onboarded routable family")
	}
	for _, want := range []Verb{VerbGet, VerbList, VerbCreate} {
		if !imgVerbs[want] {
			t.Errorf("virtualmachineimages must route %v, got %v", want, imgVerbs)
		}
	}
	// CreateImage routes VerbCreate but NOT the other write verbs.
	for _, w := range []Verb{VerbApply, VerbUpdate, VerbDelete} {
		if imgVerbs[w] {
			t.Errorf("virtualmachineimages must NOT route %v", w)
		}
	}
	if len(imgVerbs) != 3 {
		t.Errorf("virtualmachineimages routable set = %v, want exactly {Get, List, Create}", imgVerbs)
	}
}

// TestRoutableVerbs_CloudProviderSAFamilies pins the routable sets for the three
// cloud-provider-SA bootstrap families (F32): ServiceAccount and RoleBinding
// route exactly {Create}; Secret routes exactly {Get, Create} (Get to read the
// populated token, Create for the token Secret). No other verb is routable for
// these families — in particular Delete/Apply/Update/List/Watch must NOT be.
func TestRoutableVerbs_CloudProviderSAFamilies(t *testing.T) {
	saGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "serviceaccounts"}
	rbGVR := schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"}
	secGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}

	// SA and RoleBinding: exactly {Create}.
	for _, gvr := range []schema.GroupVersionResource{saGVR, rbGVR} {
		verbs, ok := RoutableVerbs(gvr)
		if !ok {
			t.Errorf("%s must be an onboarded routable family", gvr.Resource)
			continue
		}
		if !verbs[VerbCreate] {
			t.Errorf("%s must route VerbCreate, got %v", gvr.Resource, verbs)
		}
		if len(verbs) != 1 {
			t.Errorf("%s routable set = %v, want exactly {Create}", gvr.Resource, verbs)
		}
	}

	// Secret: exactly {Get, Create}.
	secVerbs, ok := RoutableVerbs(secGVR)
	if !ok {
		t.Fatal("secrets must be an onboarded routable family")
	}
	for _, want := range []Verb{VerbGet, VerbCreate} {
		if !secVerbs[want] {
			t.Errorf("secrets must route %v, got %v", want, secVerbs)
		}
	}
	for _, w := range []Verb{VerbList, VerbApply, VerbUpdate, VerbDelete, VerbWatch} {
		if secVerbs[w] {
			t.Errorf("secrets must NOT route %v", w)
		}
	}
	if len(secVerbs) != 2 {
		t.Errorf("secrets routable set = %v, want exactly {Get, Create}", secVerbs)
	}
}

// TestDefaultGVKMapperResolvesKnownGVRs pins the derived GVK table to the
// onboarded entries (and same APIVersion/Kind). The six original entries plus the
// three cloud-provider-SA bootstrap families (serviceaccounts, rolebindings,
// secrets — F32).
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
		{schema.GroupVersionResource{Group: "", Version: "v1", Resource: "serviceaccounts"}, "v1", "ServiceAccount"},
		{schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"}, "rbac.authorization.k8s.io/v1", "RoleBinding"},
		{schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}, "v1", "Secret"},
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
