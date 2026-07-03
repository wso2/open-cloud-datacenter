package clusteraccess

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// TestUnionRouteVerbs pins the registry-derived routing allow-set to the current
// onboarded set EXACTLY: {VerbGet, VerbList, VerbCreate, VerbApply, VerbDelete}.
// VerbList joined the union when ListVMs/ListImages/ListNetworks were routed
// through the agent; VerbApply joined it when the kubeovn network-plumbing spec
// writes (peering/static-route/ACL lists on Vpc/Subnet) were routed. Any further
// drift (e.g. accidentally routing VerbUpdate) fails here.
func TestUnionRouteVerbs(t *testing.T) {
	got := UnionRouteVerbs()
	want := map[Verb]bool{VerbGet: true, VerbList: true, VerbCreate: true, VerbApply: true, VerbDelete: true}

	if len(got) != len(want) {
		t.Fatalf("union route verbs size = %d, want %d (got %v)", len(got), len(want), got)
	}
	for v := range want {
		if !got[v] {
			t.Errorf("union route verbs missing %v", v)
		}
	}
	// Explicitly assert the verbs that must NOT be routable.
	for _, v := range []Verb{VerbUpdate, VerbWatch} {
		if got[v] {
			t.Errorf("verb %v must NOT be routable but is in the union", v)
		}
	}
	// VerbList classifies as a read for the reads/writes toggle split; VerbApply
	// classifies as a write (it is gated by DCAPI_AGENT_ROUTE_WRITES).
	if !IsReadVerb(VerbList) {
		t.Error("VerbList must classify as a read verb")
	}
	if !IsWriteVerb(VerbApply) {
		t.Error("VerbApply must classify as a write verb")
	}
}

// TestRoutableVerbs_VMPinnedSet pins the VM family's routable set to exactly
// {Get, List, Create, Delete}. The VM family deliberately does NOT gain
// VerbApply with the network-plumbing slice — no VM op applies a spec field, and
// a family's RouteVerbs never widen ahead of the verb actually being routed.
// (This replaces the earlier VM-equals-union guard, which held only while no
// family routed a verb the VM family didn't.)
func TestRoutableVerbs_VMPinnedSet(t *testing.T) {
	vmGVR := schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}
	got, ok := RoutableVerbs(vmGVR)
	if !ok {
		t.Fatal("virtualmachines must be an onboarded routable family")
	}
	for _, v := range []Verb{VerbGet, VerbList, VerbCreate, VerbDelete} {
		if !got[v] {
			t.Errorf("RoutableVerbs(vm) missing %v", v)
		}
	}
	for _, v := range []Verb{VerbApply, VerbUpdate, VerbWatch} {
		if got[v] {
			t.Errorf("RoutableVerbs(vm) must NOT include %v", v)
		}
	}
	if len(got) != 4 {
		t.Errorf("RoutableVerbs(vm) = %v, want exactly {Get, List, Create, Delete}", got)
	}
}

// TestRoutableVerbs_NetworkPlumbingApply pins the network families' routable
// sets after the plumbing slice: Vpc and Subnet route exactly
// {Get, Create, Apply, Delete} — Apply carries the peering (spec.vpcPeerings),
// static-route (spec.staticRoutes), and NSG ACL (spec.acls) server-side-apply
// writes — while the NAD family does NOT route Apply (nothing applies a NAD;
// RouteVerbs never widen ahead of the routed verb). Vpc/Subnet must also keep
// StatusFallbackOK=false: their seam Gets feed read-modify-write ops that need
// the full object (.spec/.metadata.labels), so degrading to a status-only read
// on an old agent would silently hand them an empty spec.
func TestRoutableVerbs_NetworkPlumbingApply(t *testing.T) {
	vpcGVR := schema.GroupVersionResource{Group: "kubeovn.io", Version: "v1", Resource: "vpcs"}
	subnetGVR := schema.GroupVersionResource{Group: "kubeovn.io", Version: "v1", Resource: "subnets"}
	nadGVR := schema.GroupVersionResource{Group: "k8s.cni.cncf.io", Version: "v1", Resource: "network-attachment-definitions"}

	for _, gvr := range []schema.GroupVersionResource{vpcGVR, subnetGVR} {
		verbs, ok := RoutableVerbs(gvr)
		if !ok {
			t.Errorf("%s must be an onboarded routable family", gvr.Resource)
			continue
		}
		for _, v := range []Verb{VerbGet, VerbCreate, VerbApply, VerbDelete} {
			if !verbs[v] {
				t.Errorf("%s must route %v, got %v", gvr.Resource, v, verbs)
			}
		}
		for _, v := range []Verb{VerbList, VerbUpdate, VerbWatch} {
			if verbs[v] {
				t.Errorf("%s must NOT route %v", gvr.Resource, v)
			}
		}
		if len(verbs) != 4 {
			t.Errorf("%s routable set = %v, want exactly {Get, Create, Apply, Delete}", gvr.Resource, verbs)
		}
		if StatusFallbackOK(gvr) {
			t.Errorf("%s must keep StatusFallbackOK=false — its Gets are full-object consumers", gvr.Resource)
		}
	}

	nadVerbs, ok := RoutableVerbs(nadGVR)
	if !ok {
		t.Fatal("network-attachment-definitions must be an onboarded routable family")
	}
	if nadVerbs[VerbApply] {
		t.Error("network-attachment-definitions must NOT route VerbApply — nothing routes a NAD apply in this slice")
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

// TestRoutableVerbs_NATFamilies pins the routable sets for the five kubeovn
// families the F15 NAT slice onboards: ProviderNetwork and Vlan route exactly
// {Create} (the idempotent external-network bootstrap creates); VpcNatGateway,
// IptablesEIP, and IptablesSnatRule route exactly {Get, Create, Delete} (the
// per-VPC NAT lifecycle: presence/readiness reads, creates, teardown deletes).
// No NAT family routes Apply/Update/List/Watch — no NAT op applies a spec field
// or lists these CRDs, and RouteVerbs never widen ahead of the routed verb. All
// keep StatusFallbackOK=false (only the VM family sets it): the EIP readiness/
// assigned-IP reads DO consume status, but the documented rule stands and an
// old agent yields a clear error instead of a silent partial.
func TestRoutableVerbs_NATFamilies(t *testing.T) {
	bootstrapOnly := []schema.GroupVersionResource{
		{Group: "kubeovn.io", Version: "v1", Resource: "provider-networks"},
		{Group: "kubeovn.io", Version: "v1", Resource: "vlans"},
	}
	for _, gvr := range bootstrapOnly {
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
		if StatusFallbackOK(gvr) {
			t.Errorf("%s must keep StatusFallbackOK=false", gvr.Resource)
		}
	}

	lifecycle := []schema.GroupVersionResource{
		{Group: "kubeovn.io", Version: "v1", Resource: "vpc-nat-gateways"},
		{Group: "kubeovn.io", Version: "v1", Resource: "iptables-eips"},
		{Group: "kubeovn.io", Version: "v1", Resource: "iptables-snat-rules"},
	}
	for _, gvr := range lifecycle {
		verbs, ok := RoutableVerbs(gvr)
		if !ok {
			t.Errorf("%s must be an onboarded routable family", gvr.Resource)
			continue
		}
		for _, v := range []Verb{VerbGet, VerbCreate, VerbDelete} {
			if !verbs[v] {
				t.Errorf("%s must route %v, got %v", gvr.Resource, v, verbs)
			}
		}
		for _, v := range []Verb{VerbList, VerbApply, VerbUpdate, VerbWatch} {
			if verbs[v] {
				t.Errorf("%s must NOT route %v", gvr.Resource, v)
			}
		}
		if len(verbs) != 3 {
			t.Errorf("%s routable set = %v, want exactly {Get, Create, Delete}", gvr.Resource, verbs)
		}
		if StatusFallbackOK(gvr) {
			t.Errorf("%s must keep StatusFallbackOK=false", gvr.Resource)
		}
	}
}

// TestRoutableVerbs_PodsReadOnly pins the pod family (F15 NAT slice) to exactly
// {Get, List} — the gateway-pod status poll and the pods-gone label-selector
// poll. NO write verb may ever be routable for pods: dc-api never mutates a pod
// through the seam, and this pin is the regression guard against a future slice
// accidentally widening a core-group workload family into the write path.
func TestRoutableVerbs_PodsReadOnly(t *testing.T) {
	podGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	verbs, ok := RoutableVerbs(podGVR)
	if !ok {
		t.Fatal("pods must be an onboarded routable family")
	}
	for _, v := range []Verb{VerbGet, VerbList} {
		if !verbs[v] {
			t.Errorf("pods must route %v, got %v", v, verbs)
		}
	}
	for _, v := range []Verb{VerbCreate, VerbApply, VerbUpdate, VerbDelete, VerbWatch} {
		if verbs[v] {
			t.Errorf("pods must NOT route %v — the family is READ-ONLY", v)
		}
	}
	if len(verbs) != 2 {
		t.Errorf("pods routable set = %v, want exactly {Get, List}", verbs)
	}
	if StatusFallbackOK(podGVR) {
		t.Error("pods must keep StatusFallbackOK=false (only the VM family sets it)")
	}
}

// TestDefaultGVKMapperResolvesKnownGVRs pins the derived GVK table to the
// onboarded entries (and same APIVersion/Kind). The six original entries, the
// three cloud-provider-SA bootstrap families (serviceaccounts, rolebindings,
// secrets — F32), and the six NAT-slice families (provider-networks, vlans,
// vpc-nat-gateways, iptables-eips, iptables-snat-rules, pods — F15).
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
		{schema.GroupVersionResource{Group: "kubeovn.io", Version: "v1", Resource: "provider-networks"}, "kubeovn.io/v1", "ProviderNetwork"},
		{schema.GroupVersionResource{Group: "kubeovn.io", Version: "v1", Resource: "vlans"}, "kubeovn.io/v1", "Vlan"},
		{schema.GroupVersionResource{Group: "kubeovn.io", Version: "v1", Resource: "vpc-nat-gateways"}, "kubeovn.io/v1", "VpcNatGateway"},
		{schema.GroupVersionResource{Group: "kubeovn.io", Version: "v1", Resource: "iptables-eips"}, "kubeovn.io/v1", "IptablesEIP"},
		{schema.GroupVersionResource{Group: "kubeovn.io", Version: "v1", Resource: "iptables-snat-rules"}, "kubeovn.io/v1", "IptablesSnatRule"},
		{schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}, "v1", "Pod"},
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
