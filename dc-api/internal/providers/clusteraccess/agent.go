package clusteraccess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/wso2/dc-api/internal/agentgw"
)

// agentCallTimeout bounds EVERY agent-channel RPC issued from this accessor,
// regardless of the context the caller passes in. Without it a caller holding a
// deadline-less context — notably the reconciler, which calls GetVM with the
// process-lifetime Run() context — would block forever on a connected-but-slow
// agent (Session.Call only returns on a reply or ctx done). Mirrors the
// inventoryCallTimeout precedent in handlers/inventory.go: a hung agent must
// surface as a prompt timeout error, never an unbounded block.
const agentCallTimeout = 30 * time.Second

// AgentBacked routes each Kubernetes op to one zone's dc-agent Session. It
// depends on the neutral agentgw.Session interface, never on package handlers,
// so there is no import cycle.
//
// M-C maps only the status-shaped Get onto an agent op (Session.GetStatus). The
// mutating verbs (Create/Update/Apply/Delete) and List are stubbed to return
// ErrOpNotRoutable until their write phase lands AND the matching dc-agent RBAC
// is widened in the same PR. Routed treats ErrOpNotRoutable as "fall back to
// Direct", so an un-cut-over verb is never worse than today.
type AgentBacked struct {
	sess         agentgw.Session
	region, zone string
	fieldManager string
	mapper       GVRToGVK
	log          zerolog.Logger
	// callTimeout bounds each agent RPC. Zero means agentCallTimeout. It is a
	// field (not a bare const) so tests can inject a tiny value and assert the
	// deadline fires without waiting the full 30s.
	callTimeout time.Duration
}

// NewAgentBacked builds an agent-backed accessor for one zone's session.
// fieldManager defaults to "dc-api" when empty.
func NewAgentBacked(sess agentgw.Session, region, zone, fieldManager string, mapper GVRToGVK, log zerolog.Logger) *AgentBacked {
	if fieldManager == "" {
		fieldManager = "dc-api"
	}
	if mapper == nil {
		mapper = DefaultGVKMapper()
	}
	return &AgentBacked{sess: sess, region: region, zone: zone, fieldManager: fieldManager, mapper: mapper, log: log}
}

// Ensure AgentBacked satisfies Accessor at compile time.
var _ Accessor = (*AgentBacked)(nil)

// bound wraps the incoming ctx with the accessor's call timeout so a slow or
// unresponsive agent always surfaces as a deadline error promptly, even when the
// caller passed a context with no deadline. The caller MUST defer the returned
// cancel. If ctx already has an earlier deadline, context.WithTimeout keeps it.
func (a *AgentBacked) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	d := a.callTimeout
	if d <= 0 {
		d = agentCallTimeout
	}
	return context.WithTimeout(ctx, d)
}

// ref builds the agent ResourceRef for a GVR/ns/name, returning ErrOpNotRoutable
// (with a loud log) for an unmapped GVR so we never issue a wrong-kind op.
func (a *AgentBacked) ref(gvr schema.GroupVersionResource, ns, name string) (agentgw.ResourceRef, error) {
	apiVersion, kind, ok := a.mapper.GVK(gvr)
	if !ok {
		a.log.Error().
			Str("group", gvr.Group).Str("version", gvr.Version).Str("resource", gvr.Resource).
			Msg("clusteraccess: no GVR→GVK mapping for agent routing — refusing to route (programming error)")
		return agentgw.ResourceRef{}, fmt.Errorf("clusteraccess: unmapped GVR %s: %w", gvr.String(), agentgw.ErrOpNotRoutable)
	}
	return agentgw.ResourceRef{APIVersion: apiVersion, Kind: kind, Namespace: ns, Name: name}, nil
}

// Get routes a STATUS-only read to Session.GetStatus and synthesizes a partial
// *unstructured.Unstructured carrying apiVersion, kind, metadata.name/namespace,
// metadata.resourceVersion, metadata.generation, and status.
//
// IMPORTANT: this is sufficient ONLY for callers that read status (e.g. the VM
// read slice, which reads status.printableStatus). A caller needing spec or
// metadata beyond name/ns/rv/generation must NOT route via this accessor until a
// full-object agent read op exists. Found==false is translated to a
// k8serrors.NewNotFound-shaped error so callers' existing IsNotFound /
// "not found" string checks fire unchanged on both seams.
func (a *AgentBacked) Get(ctx context.Context, gvr schema.GroupVersionResource, ns, name string, _ metav1.GetOptions) (*unstructured.Unstructured, error) {
	ref, err := a.ref(gvr, ns, name)
	if err != nil {
		return nil, err
	}

	ctx, cancel := a.bound(ctx)
	defer cancel()

	snap, err := a.sess.GetStatus(ctx, ref)
	if err != nil {
		return nil, a.translateErr(err)
	}

	// One INFO log per routed read, at the moment the agent path is chosen — the
	// validation hook described in the design.
	a.log.Info().
		Str("seam", "agent").
		Str("region", a.region).Str("zone", a.zone).
		Str("api_version", ref.APIVersion).Str("kind", ref.Kind).
		Str("namespace", ref.Namespace).Str("name", ref.Name).
		Bool("found", snap.Found).
		Msg("provider read routed through agent")

	if !snap.Found {
		// Shape a NotFound so reconciler delete/fail branches and providers'
		// idempotent-delete checks fire exactly as on the direct path.
		return nil, k8serrors.NewNotFound(gvr.GroupResource(), name)
	}

	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": ref.APIVersion,
		"kind":       ref.Kind,
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": ns,
		},
	}}
	if snap.ResourceVersion != "" {
		_ = unstructured.SetNestedField(obj.Object, snap.ResourceVersion, "metadata", "resourceVersion")
	}
	if snap.Generation != 0 {
		_ = unstructured.SetNestedField(obj.Object, snap.Generation, "metadata", "generation")
	}
	if len(snap.Status) > 0 {
		var status map[string]interface{}
		if err := json.Unmarshal(snap.Status, &status); err != nil {
			return nil, fmt.Errorf("clusteraccess: decode agent status for %s/%s: %w", ns, name, err)
		}
		obj.Object["status"] = status
	}
	return obj, nil
}

// List is not in the M-B agent op set — no list_* op exists yet (M-D). Returns
// ErrOpNotRoutable so Routed falls back to Direct.
func (a *AgentBacked) List(ctx context.Context, gvr schema.GroupVersionResource, ns string, _ metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	return nil, fmt.Errorf("clusteraccess: list of %s not routable: %w", gvr.Resource, agentgw.ErrOpNotRoutable)
}

// Create / Update / Apply all map to Session.Apply (SSA is create-or-update)
// once a resource family is cut over in its write phase. Until then they return
// ErrOpNotRoutable so Routed uses the direct path. The implementation below is
// the shape the write phases will enable; it is unreachable in M-C because the
// Routed allow-set does not include any mutating verb.
func (a *AgentBacked) Create(ctx context.Context, gvr schema.GroupVersionResource, ns string, obj *unstructured.Unstructured, _ metav1.CreateOptions) (*unstructured.Unstructured, error) {
	return a.apply(ctx, gvr, ns, obj)
}

func (a *AgentBacked) Update(ctx context.Context, gvr schema.GroupVersionResource, ns string, obj *unstructured.Unstructured, _ metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	return a.apply(ctx, gvr, ns, obj)
}

func (a *AgentBacked) Apply(ctx context.Context, gvr schema.GroupVersionResource, ns string, obj *unstructured.Unstructured, _ string, force bool) (*unstructured.Unstructured, error) {
	return a.applyForce(ctx, gvr, ns, obj, force)
}

func (a *AgentBacked) apply(ctx context.Context, gvr schema.GroupVersionResource, ns string, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	return a.applyForce(ctx, gvr, ns, obj, false)
}

func (a *AgentBacked) applyForce(ctx context.Context, gvr schema.GroupVersionResource, ns string, obj *unstructured.Unstructured, force bool) (*unstructured.Unstructured, error) {
	// Verify the GVR is mappable (loud log on a programming error) even though
	// the manifest carries apiVersion/kind — keeps the not-routable contract
	// uniform across verbs.
	if _, _, ok := a.mapper.GVK(gvr); !ok {
		a.log.Error().
			Str("group", gvr.Group).Str("version", gvr.Version).Str("resource", gvr.Resource).
			Msg("clusteraccess: no GVR→GVK mapping for agent apply — refusing to route")
		return nil, fmt.Errorf("clusteraccess: unmapped GVR %s: %w", gvr.String(), agentgw.ErrOpNotRoutable)
	}
	manifest, err := json.Marshal(obj.Object)
	if err != nil {
		return nil, fmt.Errorf("clusteraccess: marshal manifest for apply: %w", err)
	}
	ctx, cancel := a.bound(ctx)
	defer cancel()
	res, err := a.sess.Apply(ctx, manifest, a.fieldManager, force)
	if err != nil {
		return nil, a.translateErr(err)
	}
	out := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": res.APIVersion,
		"kind":       res.Kind,
		"metadata": map[string]interface{}{
			"name":      res.Name,
			"namespace": res.Namespace,
		},
	}}
	if res.UID != "" {
		_ = unstructured.SetNestedField(out.Object, res.UID, "metadata", "uid")
	}
	if res.ResourceVersion != "" {
		_ = unstructured.SetNestedField(out.Object, res.ResourceVersion, "metadata", "resourceVersion")
	}
	return out, nil
}

// Delete routes to Session.Delete. A missing object (Existed==false) is a
// successful idempotent delete (no error), matching the providers' existing
// IsNotFound-is-success contract.
func (a *AgentBacked) Delete(ctx context.Context, gvr schema.GroupVersionResource, ns, name string, opts metav1.DeleteOptions) error {
	ref, err := a.ref(gvr, ns, name)
	if err != nil {
		return err
	}
	policy := ""
	if opts.PropagationPolicy != nil {
		policy = string(*opts.PropagationPolicy)
	}
	ctx, cancel := a.bound(ctx)
	defer cancel()
	if _, err := a.sess.Delete(ctx, ref, policy); err != nil {
		return a.translateErr(err)
	}
	return nil
}

// translateErr maps agent-channel failures into provider-shaped errors so the
// failure modes stay inside the seam and callers keep their current handling:
//   - ErrAgentUnavailable → a retryable error whose message contains
//     "agent unavailable" (reconciler retries next tick; handlers surface 503).
//   - *AgentError(OP_UNSUPPORTED) → wrapped as ErrOpNotRoutable (non-retryable;
//     Routed would fall back to Direct on a fresh call).
//   - anything else → passed through unchanged.
func (a *AgentBacked) translateErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, agentgw.ErrAgentUnavailable) {
		return fmt.Errorf("agent unavailable for zone %s/%s: %w", a.region, a.zone, err)
	}
	var ae *agentgw.AgentError
	if errors.As(err, &ae) && ae.Code == agentgw.CodeOpUnsupported {
		return fmt.Errorf("agent does not support op (%s): %w", ae.Message, agentgw.ErrOpNotRoutable)
	}
	return err
}
