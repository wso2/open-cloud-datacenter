package clusteraccess

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Verb identifies which Accessor method is being invoked, so the routing
// decision (Routed.agent) can gate the agent path verb-by-verb. This is how the
// design's "never widen ahead of the routed verb" is enforced in code: the
// per-phase allow-set decides which verbs may route.
type Verb int

const (
	VerbGet Verb = iota
	VerbList
	VerbCreate
	VerbApply
	VerbUpdate
	VerbDelete
)

// AgentDecision is the live decision function a Routed accessor consults per
// call. It returns the AgentBacked accessor and true ONLY when the relevant
// family toggle is on AND a live agent session exists for the zone right now AND
// the specific verb is in the routed allow-set for the current phase. Otherwise
// it returns (_, false) and Routed falls through to Direct — so behaviour is
// never worse than today.
type AgentDecision func(verb Verb) (Accessor, bool)

// Routed picks Direct or AgentBacked per call from a live decision function.
// This is what lets a single cached provider flip between seams without
// reconstruction: agent() re-evaluates the toggle + live-session check + verb
// allow-set every call. For ops the agent can't do yet, or when agent() says no,
// it falls through to Direct.
type Routed struct {
	direct Accessor
	agent  AgentDecision
}

// NewRouted builds a Routed accessor over a Direct fallback and a live decision
// function. If decide is nil the accessor is always Direct (agent path disabled).
func NewRouted(direct Accessor, decide AgentDecision) *Routed {
	if decide == nil {
		decide = func(Verb) (Accessor, bool) { return nil, false }
	}
	return &Routed{direct: direct, agent: decide}
}

// Ensure Routed satisfies Accessor at compile time.
var _ Accessor = (*Routed)(nil)

func (r *Routed) Get(ctx context.Context, gvr schema.GroupVersionResource, ns, name string, opts metav1.GetOptions) (*unstructured.Unstructured, error) {
	if a, ok := r.agent(VerbGet); ok {
		return a.Get(ctx, gvr, ns, name, opts)
	}
	return r.direct.Get(ctx, gvr, ns, name, opts)
}

func (r *Routed) List(ctx context.Context, gvr schema.GroupVersionResource, ns string, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	if a, ok := r.agent(VerbList); ok {
		return a.List(ctx, gvr, ns, opts)
	}
	return r.direct.List(ctx, gvr, ns, opts)
}

func (r *Routed) Create(ctx context.Context, gvr schema.GroupVersionResource, ns string, obj *unstructured.Unstructured, opts metav1.CreateOptions) (*unstructured.Unstructured, error) {
	if a, ok := r.agent(VerbCreate); ok {
		return a.Create(ctx, gvr, ns, obj, opts)
	}
	return r.direct.Create(ctx, gvr, ns, obj, opts)
}

func (r *Routed) Apply(ctx context.Context, gvr schema.GroupVersionResource, ns string, obj *unstructured.Unstructured, fieldManager string, force bool) (*unstructured.Unstructured, error) {
	if a, ok := r.agent(VerbApply); ok {
		return a.Apply(ctx, gvr, ns, obj, fieldManager, force)
	}
	return r.direct.Apply(ctx, gvr, ns, obj, fieldManager, force)
}

func (r *Routed) Update(ctx context.Context, gvr schema.GroupVersionResource, ns string, obj *unstructured.Unstructured, opts metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	if a, ok := r.agent(VerbUpdate); ok {
		return a.Update(ctx, gvr, ns, obj, opts)
	}
	return r.direct.Update(ctx, gvr, ns, obj, opts)
}

func (r *Routed) Delete(ctx context.Context, gvr schema.GroupVersionResource, ns, name string, opts metav1.DeleteOptions) error {
	if a, ok := r.agent(VerbDelete); ok {
		return a.Delete(ctx, gvr, ns, name, opts)
	}
	return r.direct.Delete(ctx, gvr, ns, name, opts)
}
