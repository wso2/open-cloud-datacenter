// Package clusteraccess defines the seam between a provider and the Kubernetes
// API of one zone's cluster.
//
// Today every provider method reaches Kubernetes by doing
// c.dynamic.Resource(gvr).Namespace(ns).<verb>(...) directly. This package
// captures exactly that surface in one narrow interface (Accessor) with two
// implementations:
//
//   - Direct      — a dynamic client on the master kubeconfig (today's behaviour,
//     byte-identical; see direct.go).
//   - AgentBacked — each op routed to the zone's dc-agent over the command
//     channel (see agent.go).
//   - Routed      — picks Direct or AgentBacked per call from a live decision
//     function, so a single cached provider can flip seams without
//     reconstruction (see routed.go).
//
// Providers depend ONLY on the Accessor interface, so the Strategy pattern is
// preserved: a handler never learns whether a provider talks to the cluster
// directly or through an agent. The public provider interfaces
// (providers.ComputeProvider, …) are unchanged — no agent concept leaks out.
package clusteraccess

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Accessor is the per-zone Kubernetes access seam. The method set is the subset
// of dynamic.Interface that the providers actually use, expressed in terms
// providers already speak (GVR + ns/name + unstructured). Keeping the seam in
// GVR-in / unstructured-out terms means the Direct implementation is a one-line
// passthrough and the provider diff is purely mechanical; the AgentBacked
// implementation owns the GVR→GVK translation it needs for the wire.
type Accessor interface {
	Get(ctx context.Context, gvr schema.GroupVersionResource, ns, name string, opts metav1.GetOptions) (*unstructured.Unstructured, error)
	List(ctx context.Context, gvr schema.GroupVersionResource, ns string, opts metav1.ListOptions) (*unstructured.UnstructuredList, error)
	Create(ctx context.Context, gvr schema.GroupVersionResource, ns string, obj *unstructured.Unstructured, opts metav1.CreateOptions) (*unstructured.Unstructured, error)
	Apply(ctx context.Context, gvr schema.GroupVersionResource, ns string, obj *unstructured.Unstructured, fieldManager string, force bool) (*unstructured.Unstructured, error)
	Update(ctx context.Context, gvr schema.GroupVersionResource, ns string, obj *unstructured.Unstructured, opts metav1.UpdateOptions) (*unstructured.Unstructured, error)
	Delete(ctx context.Context, gvr schema.GroupVersionResource, ns, name string, opts metav1.DeleteOptions) error
}

// gvk is the (apiVersion, kind) pair the agent addresses an object by.
type gvk struct {
	APIVersion string
	Kind       string
}

// GVRToGVK maps a GroupVersionResource to the (apiVersion, kind) the dc-agent
// wire uses. The AgentBacked accessor has the GVR (providers speak GVR); the
// agent needs api_version + kind. For the fixed set of GVRs the drivers use this
// is a static table — an unmapped GVR is a programming error, surfaced as
// ErrOpNotRoutable with a loud log rather than a silent wrong-kind apply.
type GVRToGVK interface {
	GVK(gvr schema.GroupVersionResource) (apiVersion, kind string, ok bool)
}

// staticGVKMapper is a GVRToGVK backed by a fixed table.
type staticGVKMapper struct {
	table map[schema.GroupVersionResource]gvk
}

func (m staticGVKMapper) GVK(gvr schema.GroupVersionResource) (string, string, bool) {
	v, ok := m.table[gvr]
	return v.APIVersion, v.Kind, ok
}

// DefaultGVKMapper returns the GVR→GVK table seeded from the drivers' own GVR
// vars and their kinds. It is extended (here, in one place) as each write phase
// routes a new resource family — never widened ahead of the routed verb.
//
// For M-C only the KubeVirt VirtualMachine entry is exercised (the read slice);
// the rest are pre-seeded because they are the GVRs the existing drivers use and
// having them present makes the eventual write phases a no-op here. apiVersion is
// "group/version" (or bare "version" for the core group, which is empty).
func DefaultGVKMapper() GVRToGVK {
	return staticGVKMapper{table: map[schema.GroupVersionResource]gvk{
		// ── harvester driver ───────────────────────────────────────────────────
		{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}:                    {APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine"},
		{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachineinstances"}:            {APIVersion: "kubevirt.io/v1", Kind: "VirtualMachineInstance"},
		{Group: "harvesterhci.io", Version: "v1beta1", Resource: "virtualmachineimages"}:      {APIVersion: "harvesterhci.io/v1beta1", Kind: "VirtualMachineImage"},
		{Group: "k8s.cni.cncf.io", Version: "v1", Resource: "network-attachment-definitions"}: {APIVersion: "k8s.cni.cncf.io/v1", Kind: "NetworkAttachmentDefinition"},
		// ── kubeovn driver (reads first in M-E, then writes) ─────────────────────
		{Group: "kubeovn.io", Version: "v1", Resource: "vpcs"}:    {APIVersion: "kubeovn.io/v1", Kind: "Vpc"},
		{Group: "kubeovn.io", Version: "v1", Resource: "subnets"}: {APIVersion: "kubeovn.io/v1", Kind: "Subnet"},
	}}
}
