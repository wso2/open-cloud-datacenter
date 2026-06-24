package clusteraccess

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// NoCreds is the Direct-fallback replacement used for a REMOTE zone, where
// dc-api holds NO kubeconfig and therefore has no dynamic client to dial. It is
// the second half of the no-silent-wrong-cluster guarantee: a Routed accessor
// for a remote zone uses NoCreds as its `direct` fallback, so when the per-call
// AgentDecision declines to route (toggle off, verb not in the allow-set, OR —
// critically — no agent connected for the zone) the op fails with a CLEAR error
// naming the zone instead of silently hitting the LOCAL cluster (which is the
// wrong cluster for a remote-zone resource).
//
// Contrast with Direct (direct.go), whose fallback IS the local master
// kubeconfig — correct for the LOCAL zone because dc-api genuinely holds those
// credentials. NoCreds is correct for a remote zone because dc-api does not.
type NoCreds struct {
	region, zone string
}

// NewNoCreds builds a NoCreds accessor bound to the region/zone it guards, so
// every error names the zone the caller tried to reach.
func NewNoCreds(region, zone string) *NoCreds { return &NoCreds{region: region, zone: zone} }

// Ensure NoCreds satisfies Accessor at compile time.
var _ Accessor = (*NoCreds)(nil)

func (n *NoCreds) err(verb string) error {
	return fmt.Errorf(
		"no agent connected for zone %s/%s: cannot %s — dc-api holds no direct credentials for a remote zone, so the operation cannot fall back to the local cluster; connect a dc-agent for this zone",
		n.region, n.zone, verb)
}

func (n *NoCreds) Get(_ context.Context, _ schema.GroupVersionResource, _, _ string, _ metav1.GetOptions) (*unstructured.Unstructured, error) {
	return nil, n.err("get")
}

func (n *NoCreds) List(_ context.Context, _ schema.GroupVersionResource, _ string, _ metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	return nil, n.err("list")
}

func (n *NoCreds) Create(_ context.Context, _ schema.GroupVersionResource, _ string, _ *unstructured.Unstructured, _ metav1.CreateOptions) (*unstructured.Unstructured, error) {
	return nil, n.err("create")
}

func (n *NoCreds) Apply(_ context.Context, _ schema.GroupVersionResource, _ string, _ *unstructured.Unstructured, _ string, _ bool) (*unstructured.Unstructured, error) {
	return nil, n.err("apply")
}

func (n *NoCreds) Update(_ context.Context, _ schema.GroupVersionResource, _ string, _ *unstructured.Unstructured, _ metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	return nil, n.err("update")
}

func (n *NoCreds) Delete(_ context.Context, _ schema.GroupVersionResource, _, _ string, _ metav1.DeleteOptions) error {
	return n.err("delete")
}
