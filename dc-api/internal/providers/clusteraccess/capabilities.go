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
}

// The network families (NAD, Vpc, Subnet) are onboarded for the kubeovn CRD
// CRUD slice (RouteVerbs/AgentVerbs filled below). vmImageCapability is onboarded
// for the read/list path (ListImages routed via the agent — RouteVerbs={Get,List},
// AgentVerbs={get,list}). vmiCapability remains a GVK-only pre-seeded entry:
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
		// Onboarded for the read path only: ListImages routes the cross-namespace
		// image catalog read through the agent (VerbList), and VerbGet rounds out
		// the read family. No write verbs — image create/import stays local-only.
		RouteVerbs: []Verb{VerbGet, VerbList},
		// SA grant: get/list on virtualmachineimages so the agent can serve the
		// image catalog list (and a future per-image get). No watch — there is no
		// routed watch path, so granting it would be unused RBAC (least-privilege).
		AgentVerbs: []Verb{VerbGet, VerbList},
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
)

// AgentCapabilities is THE registry: one declaration per resource family.
//
// Onboarded (RouteVerbs + AgentVerbs set): vmCapability, the three network
// families nadCapability/vpcCapability/subnetCapability (the kubeovn CRD CRUD
// slice), and vmImageCapability (read/list path). vmiCapability stays a GVK-only
// pre-seeded entry that keeps the wire mapper a superset without granting any
// routing or RBAC. New families are added here (one struct each) in later
// phases — never by editing the mapper, the allow-set switch, or the RBAC YAML
// by hand.
var AgentCapabilities = []AgentCapability{
	vmCapability,
	vmiCapability,
	vmImageCapability,
	nadCapability,
	vpcCapability,
	subnetCapability,
}

// Registry returns the declared capabilities.
func Registry() []AgentCapability { return AgentCapabilities }

// Derived lookups, built once.
var (
	derivedOnce       sync.Once
	derivedGVKTable   map[schema.GroupVersionResource]gvk
	derivedRouteSet   map[schema.GroupVersionResource]map[Verb]bool
	derivedUnionRoute map[Verb]bool
)

func buildDerived() {
	derivedOnce.Do(func() {
		derivedGVKTable = make(map[schema.GroupVersionResource]gvk, len(AgentCapabilities))
		derivedRouteSet = make(map[schema.GroupVersionResource]map[Verb]bool, len(AgentCapabilities))
		derivedUnionRoute = make(map[Verb]bool)
		for _, c := range AgentCapabilities {
			derivedGVKTable[c.GVR] = gvk{APIVersion: c.APIVersion, Kind: c.Kind}
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
// network, and image families onboarded this equals
// {VerbGet, VerbList, VerbCreate, VerbDelete} (List added when ListVMs/Images/
// Networks were routed through the agent). agentDecision consults this for
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
