package clusteraccess

import (
	"sync"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

//go:generate go run github.com/wso2/dc-api/cmd/gen-agent-rbac -out ../../../../flux/platform/dc-agent/base/rbac.yaml

// AgentCapability is the ONE declaration per agent-manageable resource family.
//
// From this single struct we derive three things that used to be maintained by
// hand in three separate places:
//
//  1. the GVR→GVK wire mapping (clusteraccess.DefaultGVKMapper) the AgentBacked
//     accessor needs to address an object on the dc-agent wire;
//  2. the dc-api routing allow-set (which seam Verbs MAY route to the agent),
//     consulted by providers.Registry.agentDecision; and
//  3. the agent's Kubernetes RBAC ClusterRole rule, emitted by the
//     cmd/gen-agent-rbac generator.
//
// Adding a new agent-managed resource family is a single new struct in
// AgentCapabilities — never a hand-edit of the mapper, the routing switch, or
// the RBAC YAML.
type AgentCapability struct {
	// GVR is the resource as providers speak it (the seam is GVR-in).
	GVR schema.GroupVersionResource
	// APIVersion is how the dc-agent wire (ResourceRef) addresses the group/
	// version, e.g. "kubevirt.io/v1" (bare "v1" for the core group).
	APIVersion string
	// Kind is how the dc-agent wire addresses the object, e.g. "VirtualMachine".
	Kind string
	// Namespaced records whether the resource is namespaced. It controls nothing
	// in dc-api today (the agent resolves scope via its RESTMapper) but is kept
	// for completeness and future scoping. VirtualMachine is namespaced.
	Namespaced bool
	// RouteVerbs is the set of seam Verbs that MAY route to the agent for this
	// family. Membership here, AND-ed with the per-family env toggle and a live
	// session, is what providers.Registry.agentDecision consults. A capability
	// that is GVK-mapped but not yet routed (pre-seeded) leaves this nil.
	RouteVerbs []Verb
	// AgentVerbs is the set of seam Verbs whose Kubernetes verb the agent's
	// ServiceAccount MAY perform on-cluster for this family. It drives the RBAC
	// generator and is deliberately SEPARATE from RouteVerbs: the VM family
	// routes only {Get,Create,Delete} from dc-api, yet the agent's SA needs
	// get/list/watch/create/patch/delete because get_inventory lists VMs,
	// get_status/watch_status read+watch, and SSA-create needs create+patch. A
	// capability that grants no RBAC (pre-seeded GVK-only) leaves this nil.
	AgentVerbs []Verb
	// StatusFallbackOK allows AgentBacked.Get to degrade to a status-only read
	// (Session.GetStatus) when an OLDER agent predates full-object get_object and
	// replies OP_UNSUPPORTED. Set it ONLY for families whose Get consumers read
	// status alone (the VM read slice's status.printableStatus). Families whose Get
	// consumers need the full object — Secrets (.data.token) and the KubeOVN
	// read-modify-patch ops that read .spec / .metadata.labels — MUST leave this
	// false: the status-only partial the fallback synthesizes drops .spec/.data/
	// .labels, so degrading to it would silently return a useless object (the
	// cloud-provider SA bootstrap would poll a token-less Secret until timeout).
	// With the flag false, an old agent yields a clear error instead — see
	// AgentBacked.Get.
	StatusFallbackOK bool
}

// vmCapability is the one truly onboarded capability in phase 1.
//
// RouteVerbs reproduces today's dc-api allow-set EXACTLY: reads={VerbGet},
// writes={VerbCreate, VerbDelete}. AgentVerbs reproduces today's hand-written
// ClusterRole verb list for kubevirt.io/virtualmachines EXACTLY — it maps to
// the k8s verbs get,list,watch,create,patch,delete (VerbApply→patch via SSA;
// VerbCreate→create are both present, reproducing the old {create,patch} grant).
var vmCapability = AgentCapability{
	GVR:        schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"},
	APIVersion: "kubevirt.io/v1",
	Kind:       "VirtualMachine",
	Namespaced: true,
	// What dc-api MAY route to the agent (drives the allow-set + must be
	// GVK-mapped): the VM read slice (Get), the VM list slice (List — ListVMs
	// routed through the agent), and the VM write slice (Create/Delete).
	RouteVerbs: []Verb{VerbGet, VerbList, VerbCreate, VerbDelete},
	// What the agent's SA MAY do on-cluster (drives RBAC). Superset of RouteVerbs:
	// get_inventory lists VMs, get_status/watch_status read+watch, and SSA-create
	// needs create+patch. Reproduces the old rule verbatim:
	// get,list,watch,create,patch,delete.
	AgentVerbs: []Verb{VerbGet, VerbList, VerbWatch, VerbCreate, VerbApply, VerbDelete},
	// GetVM reads only status.printableStatus, so the status-only fallback is a
	// valid degrade for an older agent without get_object (preserves the
	// rolling-deploy compat added with the full-object Get). Every other
	// routable-Get family reads the full object and leaves this false.
	StatusFallbackOK: true,
}

// The network families (NAD, Vpc, Subnet) are onboarded for the kubeovn CRD
// CRUD slice (RouteVerbs/AgentVerbs filled below). vmImageCapability is onboarded
// for the read/list path AND image create (ListImages/resolveImage/CreateImage
// routed via the agent — RouteVerbs={Get,List,Create}, AgentVerbs={get,list,
// create,patch}; create+patch because SSA is the agent's create mechanism).
// vmiCapability remains a GVK-only pre-seeded entry:
// DefaultGVKMapper still resolves its GVR (the table stays a superset the drivers
// rely on) but RouteVerbs=nil means it contributes nothing to the routing
// allow-set and AgentVerbs=nil means it emits no RBAC rule. Onboarding it is a
// later phase that fills in RouteVerbs/AgentVerbs.
var (
	vmiCapability = AgentCapability{
		GVR:        schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachineinstances"},
		APIVersion: "kubevirt.io/v1",
		Kind:       "VirtualMachineInstance",
		Namespaced: true,
	}
	vmImageCapability = AgentCapability{
		GVR:        schema.GroupVersionResource{Group: "harvesterhci.io", Version: "v1beta1", Resource: "virtualmachineimages"},
		APIVersion: "harvesterhci.io/v1beta1",
		Kind:       "VirtualMachineImage",
		Namespaced: true,
		// Onboarded for the read/list path AND image create: ListImages routes the
		// cross-namespace catalog read (VerbList) and resolveImage the create-time
		// storageClass lookup, VerbGet rounds out the read family, and CreateImage
		// routes the image import (VerbCreate). No delete — image deletion stays
		// local-only for now.
		RouteVerbs: []Verb{VerbGet, VerbList, VerbCreate},
		// SA grant: get/list to serve the catalog read + resolveImage lookup, plus
		// create+patch because the AgentBacked create is a server-side apply
		// (VerbApply→patch). Mirrors vmCapability/nadCapability's write grant. No
		// watch — there is no routed watch path, so granting it would be unused RBAC.
		AgentVerbs: []Verb{VerbGet, VerbList, VerbCreate, VerbApply},
	}
	// nadCapability onboards the NetworkAttachmentDefinition CRUD that
	// CreateSubnet/DeleteSubnet route through the kubeovn seam.
	nadCapability = AgentCapability{
		GVR:        schema.GroupVersionResource{Group: "k8s.cni.cncf.io", Version: "v1", Resource: "network-attachment-definitions"},
		APIVersion: "k8s.cni.cncf.io/v1",
		Kind:       "NetworkAttachmentDefinition",
		Namespaced: true,
		// dc-api routes the NAD Create/Get/Delete the seam re-points, plus List
		// (ListNetworks routed through the agent).
		RouteVerbs: []Verb{VerbGet, VerbList, VerbCreate, VerbDelete},
		// SA superset: list/watch for future inventory; create+patch because the
		// AgentBacked create is a server-side apply (patch). Mirrors vmCapability.
		AgentVerbs: []Verb{VerbGet, VerbList, VerbWatch, VerbCreate, VerbApply, VerbDelete},
	}
	// vpcCapability onboards the Vpc (VNet) CRUD that CreateVNet/GetVNet/DeleteVNet
	// route through the kubeovn seam.
	vpcCapability = AgentCapability{
		GVR:        schema.GroupVersionResource{Group: "kubeovn.io", Version: "v1", Resource: "vpcs"},
		APIVersion: "kubeovn.io/v1",
		Kind:       "Vpc",
		Namespaced: false,
		RouteVerbs: []Verb{VerbGet, VerbCreate, VerbDelete},
		AgentVerbs: []Verb{VerbGet, VerbList, VerbWatch, VerbCreate, VerbApply, VerbDelete},
	}
	// subnetCapability onboards the Subnet CRUD that CreateSubnet/GetSubnet/
	// DeleteSubnet route through the kubeovn seam.
	subnetCapability = AgentCapability{
		GVR:        schema.GroupVersionResource{Group: "kubeovn.io", Version: "v1", Resource: "subnets"},
		APIVersion: "kubeovn.io/v1",
		Kind:       "Subnet",
		Namespaced: false,
		RouteVerbs: []Verb{VerbGet, VerbCreate, VerbDelete},
		AgentVerbs: []Verb{VerbGet, VerbList, VerbWatch, VerbCreate, VerbApply, VerbDelete},
	}
	// serviceAccountCapability onboards the ServiceAccount create that
	// EnsureCloudProviderSA routes through the harvester seam (the cloud-provider
	// SA bootstrap slice, F32). The GVR matches harvester.serviceAccountsGVR
	// exactly (core group, v1, serviceaccounts).
	serviceAccountCapability = AgentCapability{
		GVR:        schema.GroupVersionResource{Group: "", Version: "v1", Resource: "serviceaccounts"},
		APIVersion: "v1",
		Kind:       "ServiceAccount",
		Namespaced: true,
		// dc-api routes only the create — the SA bootstrap creates the SA idempotently
		// and never reads/lists/deletes it through the seam.
		RouteVerbs: []Verb{VerbCreate},
		// SA grant: create + patch, because the AgentBacked create is a server-side
		// apply (VerbApply→patch). Mirrors the write grant on the other families
		// (create for the direct-POST semantics, patch for the agent's SSA create).
		AgentVerbs: []Verb{VerbCreate, VerbApply},
	}
	// roleBindingCapability onboards the RoleBinding create that
	// EnsureCloudProviderSA routes through the harvester seam. The RoleBinding binds
	// the SA to the harvesterhci.io:cloudprovider ClusterRole. The GVR matches
	// harvester.roleBindingsGVR exactly (rbac.authorization.k8s.io, v1, rolebindings).
	roleBindingCapability = AgentCapability{
		GVR:        schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"},
		APIVersion: "rbac.authorization.k8s.io/v1",
		Kind:       "RoleBinding",
		Namespaced: true,
		// dc-api routes only the create — the bootstrap creates the RoleBinding
		// idempotently and never reads/lists/deletes it through the seam.
		RouteVerbs: []Verb{VerbCreate},
		// SA grant: create + patch (SSA is the agent's create mechanism).
		AgentVerbs: []Verb{VerbCreate, VerbApply},
	}
	// secretCapability onboards the token Secret ops that EnsureCloudProviderSA
	// routes through the harvester seam: Create (the kubernetes.io/service-account-
	// token Secret) and Get (the token-population poll reads .data.token off the
	// Secret). The GVR matches harvester.secretsGVR exactly (core group, v1, secrets).
	secretCapability = AgentCapability{
		GVR:        schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"},
		APIVersion: "v1",
		Kind:       "Secret",
		Namespaced: true,
		// dc-api routes Get (read the populated SA token) and Create (the token
		// Secret). No list/delete through the seam.
		RouteVerbs: []Verb{VerbGet, VerbCreate},
		// SA grant: get to read the populated token, create + patch for the token
		// Secret (create for the direct POST, patch because the agent create is SSA).
		AgentVerbs: []Verb{VerbGet, VerbCreate, VerbApply},
	}
)

// AgentCapabilities is THE registry: one declaration per resource family.
//
// Onboarded (RouteVerbs + AgentVerbs set): vmCapability, the three network
// families nadCapability/vpcCapability/subnetCapability (the kubeovn CRD CRUD
// slice), vmImageCapability (read/list path + image create), and the three
// cloud-provider SA bootstrap families serviceAccountCapability/
// roleBindingCapability/secretCapability (F32 — EnsureCloudProviderSA routed
// through the harvester seam). vmiCapability stays a GVK-only pre-seeded entry
// that keeps the wire mapper a superset without granting any routing or RBAC.
// New families are added here (one struct each) in later phases — never by
// editing the mapper, the allow-set switch, or the RBAC YAML by hand.
var AgentCapabilities = []AgentCapability{
	vmCapability,
	vmiCapability,
	vmImageCapability,
	nadCapability,
	vpcCapability,
	subnetCapability,
	serviceAccountCapability,
	roleBindingCapability,
	secretCapability,
}

// Registry returns the declared capabilities.
func Registry() []AgentCapability { return AgentCapabilities }

// Derived lookups, built once.
var (
	derivedOnce       sync.Once
	derivedGVKTable   map[schema.GroupVersionResource]gvk
	derivedRouteSet   map[schema.GroupVersionResource]map[Verb]bool
	derivedUnionRoute map[Verb]bool

	derivedStatusFallback map[schema.GroupVersionResource]bool
)

func buildDerived() {
	derivedOnce.Do(func() {
		derivedGVKTable = make(map[schema.GroupVersionResource]gvk, len(AgentCapabilities))
		derivedRouteSet = make(map[schema.GroupVersionResource]map[Verb]bool, len(AgentCapabilities))
		derivedUnionRoute = make(map[Verb]bool)
		derivedStatusFallback = make(map[schema.GroupVersionResource]bool, len(AgentCapabilities))
		for _, c := range AgentCapabilities {
			derivedGVKTable[c.GVR] = gvk{APIVersion: c.APIVersion, Kind: c.Kind}
			if c.StatusFallbackOK {
				derivedStatusFallback[c.GVR] = true
			}
			if len(c.RouteVerbs) > 0 {
				set := make(map[Verb]bool, len(c.RouteVerbs))
				for _, v := range c.RouteVerbs {
					set[v] = true
					derivedUnionRoute[v] = true
				}
				derivedRouteSet[c.GVR] = set
			}
		}
	})
}

// UnionRouteVerbs returns the union of RouteVerbs across all capabilities — the
// set of seam Verbs that MAY route to the agent for ANY family. With the VM,
// network, image, and cloud-provider-SA families onboarded this equals
// {VerbGet, VerbList, VerbCreate, VerbDelete} (List added when ListVMs/Images/
// Networks were routed through the agent; the image family also routes VerbCreate
// for CreateImage, but VM/NAD already contribute it so the union is unchanged).
// The SA/RoleBinding/Secret families (F32) route only Get/Create, both already
// in the union, so the union is unchanged by them too.
// agentDecision consults this for
// membership, then gates reads vs writes by the per-family env toggles
// (VerbList falls on the read side — see IsReadVerb).
//
// NOTE: agentDecision is now GVR-AWARE (the GVR is threaded through
// AgentDecision) and uses RoutableVerbs(gvr) for its per-family membership test,
// NOT this union — that is what lets a second family with DIFFERENT RouteVerbs
// route only its own declared verbs without inheriting another family's. This
// union is retained only for any non-GVR callers/tests that want the aggregate
// routable set; it is no longer the routing membership test.
func UnionRouteVerbs() map[Verb]bool {
	buildDerived()
	out := make(map[Verb]bool, len(derivedUnionRoute))
	for v := range derivedUnionRoute {
		out[v] = true
	}
	return out
}

// RoutableVerbs returns the per-family routable verb set for a GVR (from its
// RouteVerbs), and whether the GVR is an onboarded routable family at all.
func RoutableVerbs(gvr schema.GroupVersionResource) (map[Verb]bool, bool) {
	buildDerived()
	set, ok := derivedRouteSet[gvr]
	if !ok {
		return nil, false
	}
	out := make(map[Verb]bool, len(set))
	for v := range set {
		out[v] = true
	}
	return out, true
}

// StatusFallbackOK reports whether AgentBacked.Get may degrade a routed read of
// this GVR to a status-only get_status when an older agent lacks get_object. True
// only for families whose Get consumers read status alone (VMs); false (the
// default) for full-object consumers, which then get a clear error on an old
// agent instead of a silently-partial object. See AgentCapability.StatusFallbackOK.
func StatusFallbackOK(gvr schema.GroupVersionResource) bool {
	buildDerived()
	return derivedStatusFallback[gvr]
}

// IsReadVerb classifies a seam Verb as a read for the reads/writes toggle split.
// VerbList is included so it falls on the read side once it becomes routable.
func IsReadVerb(v Verb) bool {
	switch v {
	case VerbGet, VerbList:
		return true
	default:
		return false
	}
}

// IsWriteVerb classifies a seam Verb as a write for the reads/writes toggle split.
func IsWriteVerb(v Verb) bool {
	switch v {
	case VerbCreate, VerbApply, VerbUpdate, VerbDelete:
		return true
	default:
		return false
	}
}

// verbToK8s is the canonical seam-Verb → Kubernetes-RBAC-verb mapping. VerbApply
// maps to "patch" because server-side apply is an HTTP PATCH with
// application/apply-patch+yaml. This table is the RBAC vocabulary the generator
// uses to turn AgentVerbs into a ClusterRole rule's verbs.
var verbToK8s = map[Verb]string{
	VerbGet:    "get",
	VerbList:   "list",
	VerbWatch:  "watch",
	VerbCreate: "create",
	VerbApply:  "patch",
	VerbUpdate: "update",
	VerbDelete: "delete",
}

// k8sVerbOrder is the canonical ordering of Kubernetes RBAC verbs used to make
// generated rule output deterministic (and to match kubectl/API conventions).
var k8sVerbOrder = []string{"get", "list", "watch", "create", "update", "patch", "delete"}

// K8sVerb returns the Kubernetes RBAC verb for a seam Verb.
func K8sVerb(v Verb) (string, bool) {
	s, ok := verbToK8s[v]
	return s, ok
}

// K8sVerbOrder returns the canonical ordering of Kubernetes RBAC verbs, used by
// the RBAC generator to emit deterministic, byte-stable verb lists.
func K8sVerbOrder() []string {
	out := make([]string, len(k8sVerbOrder))
	copy(out, k8sVerbOrder)
	return out
}
