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
// Op coverage: the status-shaped Get maps onto Session.GetStatus (read slice);
// Create/Update/Apply all map onto Session.Apply and Delete onto Session.Delete
// (write slice 1 — VM create/delete); List maps onto Session.List (M-D — the
// generic collection read). All of these verbs are IMPLEMENTED, not stubbed.
//
// NOTE on Create: the agent exposes only server-side apply (Session.Apply) as its
// create mechanism, so AgentBacked.Create is an SSA, NOT a POST. This is the one
// intentional asymmetry with the Direct seam, whose Create is a plain dynamic POST
// (byte-identical to the pre-seam behaviour). It is acceptable because it lives
// solely on the opt-in agent path (DCAPI_AGENT_ROUTE_WRITES); the default
// toggle-OFF create stays a POST.
//
// Activation is NOT decided here — it is gated by the Routed allow-set (which the
// per-family DCAPI_AGENT_ROUTE_{READS,WRITES} toggles drive). A verb the allow-set
// does not include is chosen as Direct BEFORE any method here is called, so no
// agent op runs. CRITICALLY, for a verb the allow-set DOES activate, this
// accessor's return value — success OR error — is TERMINAL in Routed: there is no
// mid-call fallback to Direct (see routed.go). The load-bearing invariant that
// keeps that safe: for a mapped, supported resource an activated write verb must
// never return ErrOpNotRoutable. ErrOpNotRoutable is produced only for an
// unmapped GVR (a programming error — VMs are mapped) or an OP_UNSUPPORTED reply
// (gone once the agent's op handler + RBAC ship in the activating PR). A genuine
// write failure — RBAC 403, timeout, dropped channel — surfaces as a real error
// (403 passes through translateErr unchanged; a dropped channel becomes a
// retryable "agent unavailable"), so a misconfigured routed write fails LOUDLY
// rather than silently double-running on Direct.
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

// List routes a collection read to Session.List and re-hydrates the raw object
// JSON the agent returns into an *unstructured.UnstructuredList — the same shape
// Direct.List returns, so the provider field extraction is byte-identical across
// seams. It mirrors how Get routes via GetStatus: resolve the GVR→GVK, issue the
// agent op under the accessor's own deadline, translate errors. For a mapped GVR
// this never returns ErrOpNotRoutable — only a real list failure or, transiently,
// agent-unavailable. An unmapped GVR is a programming error (ErrOpNotRoutable).
func (a *AgentBacked) List(ctx context.Context, gvr schema.GroupVersionResource, ns string, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	apiVersion, kind, ok := a.mapper.GVK(gvr)
	if !ok {
		a.log.Error().
			Str("group", gvr.Group).Str("version", gvr.Version).Str("resource", gvr.Resource).
			Msg("clusteraccess: no GVR→GVK mapping for agent list — refusing to route (programming error)")
		return nil, fmt.Errorf("clusteraccess: unmapped GVR %s: %w", gvr.String(), agentgw.ErrOpNotRoutable)
	}

	ctx, cancel := a.bound(ctx)
	defer cancel()

	res, err := a.sess.List(ctx, agentgw.ListRef{
		APIVersion:    apiVersion,
		Kind:          kind,
		Namespace:     ns,
		LabelSelector: opts.LabelSelector,
	})
	if err != nil {
		return nil, a.translateErr(err)
	}

	a.log.Info().
		Str("seam", "agent").
		Str("region", a.region).Str("zone", a.zone).
		Str("api_version", apiVersion).Str("kind", kind).
		Str("namespace", ns).Str("label_selector", opts.LabelSelector).
		Int("items", len(res.Items)).
		Msg("provider list routed through agent")

	out := &unstructured.UnstructuredList{}
	out.SetAPIVersion(apiVersion)
	out.SetKind(kind + "List")
	out.Items = make([]unstructured.Unstructured, 0, len(res.Items))
	for i, raw := range res.Items {
		obj := &unstructured.Unstructured{}
		if err := obj.UnmarshalJSON(raw); err != nil {
			return nil, fmt.Errorf("clusteraccess: decode agent-listed %s object [%d]: %w", kind, i, err)
		}
		out.Items = append(out.Items, *obj)
	}
	return out, nil
}

// Create / Update / Apply all map to Session.Apply (SSA is create-or-update) —
// server-side apply is the only mutating mechanism the agent exposes. They are
// LIVE for the VM write slice: the Routed allow-set activates VerbCreate (and
// VerbDelete) behind DCAPI_AGENT_ROUTE_WRITES, so the CreateVM call site's
// c.access.Create(...) reaches Session.Apply here when the toggle is on and a live
// agent serves the zone. The create therefore becomes an SSA on the agent path
// (vs a plain POST on the Direct path) — the intentional asymmetry documented on
// the type above. For a mapped GVR (VMs are mapped) these never return
// ErrOpNotRoutable — only a real apply failure or, transiently, agent-unavailable.
func (a *AgentBacked) Create(ctx context.Context, gvr schema.GroupVersionResource, ns string, obj *unstructured.Unstructured, _ metav1.CreateOptions) (*unstructured.Unstructured, error) {
	return a.apply(ctx, gvr, ns, obj)
}

func (a *AgentBacked) Update(ctx context.Context, gvr schema.GroupVersionResource, ns string, obj *unstructured.Unstructured, _ metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	return a.apply(ctx, gvr, ns, obj)
}

// Apply deliberately IGNORES the caller's fieldManager argument and uses
// a.fieldManager instead: the agent owns the field-manager identity for every op
// it applies (it is fixed at the agent boundary, not chosen per call site), so the
// caller's value is intentionally not forwarded. This is by design, not a bug.
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
