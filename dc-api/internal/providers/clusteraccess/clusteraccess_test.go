package clusteraccess

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/wso2/dc-api/internal/agentgw"
)

// vmGVR is the KubeVirt VirtualMachine GVR the read slice routes — the one
// entry the M-C allow-set + GVK table exercise.
var vmGVR = schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}

// fakeSession is a hand-written agentgw.Session that returns canned results.
// Plain interface, no websocket harness needed.
type fakeSession struct {
	getStatus func(ref agentgw.ResourceRef) (agentgw.StatusSnapshot, error)
	apply     func(manifest json.RawMessage, fm string, force bool) (agentgw.ApplyResult, error)
	del       func(ref agentgw.ResourceRef, policy string) (agentgw.DeleteResult, error)

	// block, when true, makes GetStatus wait on ctx.Done() and return ctx.Err()
	// — modelling a connected-but-unresponsive agent (the real Session.Call only
	// returns on a reply or ctx cancellation). Used to prove the accessor bounds
	// every call with its own deadline.
	block bool

	lastRef agentgw.ResourceRef // captured by GetStatus/Delete
}

func (f *fakeSession) GetStatus(ctx context.Context, ref agentgw.ResourceRef) (agentgw.StatusSnapshot, error) {
	f.lastRef = ref
	if f.block {
		<-ctx.Done()
		return agentgw.StatusSnapshot{}, ctx.Err()
	}
	if f.getStatus != nil {
		return f.getStatus(ref)
	}
	return agentgw.StatusSnapshot{}, nil
}

func (f *fakeSession) Apply(_ context.Context, manifest json.RawMessage, fm string, force bool) (agentgw.ApplyResult, error) {
	if f.apply != nil {
		return f.apply(manifest, fm, force)
	}
	return agentgw.ApplyResult{}, nil
}

func (f *fakeSession) Delete(_ context.Context, ref agentgw.ResourceRef, policy string) (agentgw.DeleteResult, error) {
	f.lastRef = ref
	if f.del != nil {
		return f.del(ref, policy)
	}
	return agentgw.DeleteResult{}, nil
}

func (f *fakeSession) WatchStatus(context.Context, agentgw.ResourceRef, int, func(string, agentgw.StatusSnapshot)) (agentgw.WatchResult, error) {
	return agentgw.WatchResult{}, nil
}

// ── AgentBacked.Get: status synthesis ─────────────────────────────────────────

func TestAgentBackedGet_SynthesizesStatus(t *testing.T) {
	sess := &fakeSession{
		getStatus: func(ref agentgw.ResourceRef) (agentgw.StatusSnapshot, error) {
			return agentgw.StatusSnapshot{
				Found:           true,
				ResourceVersion: "12345",
				Generation:      7,
				Status:          json.RawMessage(`{"printableStatus":"Running"}`),
			}, nil
		},
	}
	a := NewAgentBacked(sess, "lk", "zone-1", "dc-api", DefaultGVKMapper(), zerolog.Nop())

	obj, err := a.Get(context.Background(), vmGVR, "dc-t-p", "vm-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}

	// The ResourceRef the agent saw must carry the correct GVK + ns/name.
	if sess.lastRef.APIVersion != "kubevirt.io/v1" || sess.lastRef.Kind != "VirtualMachine" {
		t.Errorf("agent ref GVK = %s/%s, want kubevirt.io/v1/VirtualMachine", sess.lastRef.APIVersion, sess.lastRef.Kind)
	}
	if sess.lastRef.Namespace != "dc-t-p" || sess.lastRef.Name != "vm-1" {
		t.Errorf("agent ref ns/name = %s/%s, want dc-t-p/vm-1", sess.lastRef.Namespace, sess.lastRef.Name)
	}

	// The synthesized object must carry the status the harvester driver reads.
	printable, found, _ := unstructured.NestedString(obj.Object, "status", "printableStatus")
	if !found || printable != "Running" {
		t.Errorf("status.printableStatus = %q (found=%v), want Running", printable, found)
	}
	if rv := obj.GetResourceVersion(); rv != "12345" {
		t.Errorf("resourceVersion = %q, want 12345", rv)
	}
	if gen := obj.GetGeneration(); gen != 7 {
		t.Errorf("generation = %d, want 7", gen)
	}
	if obj.GetName() != "vm-1" || obj.GetNamespace() != "dc-t-p" {
		t.Errorf("name/ns = %s/%s, want vm-1/dc-t-p", obj.GetName(), obj.GetNamespace())
	}
}

func TestAgentBackedGet_NotFoundIsK8sNotFound(t *testing.T) {
	sess := &fakeSession{
		getStatus: func(ref agentgw.ResourceRef) (agentgw.StatusSnapshot, error) {
			return agentgw.StatusSnapshot{Found: false}, nil
		},
	}
	a := NewAgentBacked(sess, "lk", "zone-1", "dc-api", DefaultGVKMapper(), zerolog.Nop())

	_, err := a.Get(context.Background(), vmGVR, "dc-t-p", "gone", metav1.GetOptions{})
	if err == nil {
		t.Fatal("expected an error for Found=false")
	}
	if !k8serrors.IsNotFound(err) {
		t.Errorf("error is not a k8s NotFound: %v", err)
	}
}

func TestAgentBackedGet_AgentUnavailableIsRetryable(t *testing.T) {
	sess := &fakeSession{
		getStatus: func(ref agentgw.ResourceRef) (agentgw.StatusSnapshot, error) {
			return agentgw.StatusSnapshot{}, agentgw.ErrAgentUnavailable
		},
	}
	a := NewAgentBacked(sess, "lk", "zone-1", "dc-api", DefaultGVKMapper(), zerolog.Nop())

	_, err := a.Get(context.Background(), vmGVR, "dc-t-p", "vm-1", metav1.GetOptions{})
	if err == nil {
		t.Fatal("expected an error when agent is unavailable")
	}
	// Must surface as agent-unavailable (reconciler retries / handlers 503) and
	// must NOT be a NotFound (that would wrongly trigger delete/fail branches).
	if !errors.Is(err, agentgw.ErrAgentUnavailable) {
		t.Errorf("error does not wrap ErrAgentUnavailable: %v", err)
	}
	if k8serrors.IsNotFound(err) {
		t.Errorf("agent-unavailable must not look like NotFound: %v", err)
	}
}

func TestAgentBackedGet_UnmappedGVRNotRoutable(t *testing.T) {
	sess := &fakeSession{}
	a := NewAgentBacked(sess, "lk", "zone-1", "dc-api", DefaultGVKMapper(), zerolog.Nop())

	unknown := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	_, err := a.Get(context.Background(), unknown, "ns", "w", metav1.GetOptions{})
	if !errors.Is(err, agentgw.ErrOpNotRoutable) {
		t.Errorf("unmapped GVR must yield ErrOpNotRoutable, got %v", err)
	}
}

// TestAgentBackedGet_BoundsHangingAgent proves the seam timeout: an agent whose
// GetStatus never replies must NOT hang Get even when the caller passes a
// deadline-less context (the reconciler's process-lifetime Run() context). Get
// must return a DeadlineExceeded-class error promptly from the accessor's own
// bound. A tiny callTimeout is injected so the test is fast; a watchdog turns a
// regression (unbounded block) into a test FAIL instead of a hung suite.
func TestAgentBackedGet_BoundsHangingAgent(t *testing.T) {
	sess := &fakeSession{block: true} // GetStatus waits on ctx.Done() forever
	a := NewAgentBacked(sess, "lk", "zone-1", "dc-api", DefaultGVKMapper(), zerolog.Nop())
	a.callTimeout = 50 * time.Millisecond // inject a short deadline (default is 30s)

	type result struct {
		err error
	}
	done := make(chan result, 1)
	go func() {
		// context.Background() has NO deadline — exactly the reconciler case. If
		// the accessor did not bound the call itself, this would block forever.
		_, err := a.Get(context.Background(), vmGVR, "dc-t-p", "vm-1", metav1.GetOptions{})
		done <- result{err: err}
	}()

	select {
	case res := <-done:
		if res.err == nil {
			t.Fatal("expected a deadline error from the bounded agent call, got nil")
		}
		if !errors.Is(res.err, context.DeadlineExceeded) {
			t.Errorf("error is not DeadlineExceeded-class: %v", res.err)
		}
	case <-time.After(5 * time.Second):
		// 100x the injected timeout — a generous watchdog. Reaching it means the
		// call was NOT bounded and the reconciler would have hung.
		t.Fatal("Get did not return within the watchdog window — the agent call was not bounded")
	}
}

// ── Routed: chooses Direct vs Agent per the decision function ─────────────────

// recordingAccessor counts which verb was invoked so we can assert which seam
// Routed selected.
type recordingAccessor struct {
	name    string
	getHits *int
}

func (r recordingAccessor) Get(_ context.Context, _ schema.GroupVersionResource, _, _ string, _ metav1.GetOptions) (*unstructured.Unstructured, error) {
	*r.getHits++
	return &unstructured.Unstructured{Object: map[string]interface{}{"seam": r.name}}, nil
}
func (r recordingAccessor) List(context.Context, schema.GroupVersionResource, string, metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	return nil, nil
}
func (r recordingAccessor) Create(context.Context, schema.GroupVersionResource, string, *unstructured.Unstructured, metav1.CreateOptions) (*unstructured.Unstructured, error) {
	return nil, nil
}
func (r recordingAccessor) Apply(context.Context, schema.GroupVersionResource, string, *unstructured.Unstructured, string, bool) (*unstructured.Unstructured, error) {
	return nil, nil
}
func (r recordingAccessor) Update(context.Context, schema.GroupVersionResource, string, *unstructured.Unstructured, metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	return nil, nil
}
func (r recordingAccessor) Delete(context.Context, schema.GroupVersionResource, string, string, metav1.DeleteOptions) error {
	return nil
}

func TestRouted_FallsBackToDirectWhenAgentDeclines(t *testing.T) {
	var directGets, agentGets int
	direct := recordingAccessor{name: "direct", getHits: &directGets}
	agent := recordingAccessor{name: "agent", getHits: &agentGets}

	// agent() always declines → must use Direct.
	r := NewRouted(direct, func(Verb, schema.GroupVersionResource) (Accessor, bool) { return agent, false })

	obj, err := r.Get(context.Background(), vmGVR, "ns", "n", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if obj.Object["seam"] != "direct" {
		t.Errorf("expected direct seam, got %v", obj.Object["seam"])
	}
	if directGets != 1 || agentGets != 0 {
		t.Errorf("hits: direct=%d agent=%d, want direct=1 agent=0", directGets, agentGets)
	}
}

func TestRouted_UsesAgentWhenDecisionAllows(t *testing.T) {
	var directGets, agentGets int
	direct := recordingAccessor{name: "direct", getHits: &directGets}
	agent := recordingAccessor{name: "agent", getHits: &agentGets}

	// agent() allows ONLY VerbGet — mirrors the M-C read allow-set.
	r := NewRouted(direct, func(v Verb, _ schema.GroupVersionResource) (Accessor, bool) {
		if v == VerbGet {
			return agent, true
		}
		return nil, false
	})

	obj, err := r.Get(context.Background(), vmGVR, "ns", "n", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if obj.Object["seam"] != "agent" {
		t.Errorf("expected agent seam, got %v", obj.Object["seam"])
	}
	if directGets != 0 || agentGets != 1 {
		t.Errorf("hits: direct=%d agent=%d, want direct=0 agent=1", directGets, agentGets)
	}

	// A non-allowed verb (Delete) must still fall back to Direct even though the
	// agent is "available" — the allow-set gates per verb.
	if err := r.Delete(context.Background(), vmGVR, "ns", "n", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("Delete error: %v", err)
	}
}

func TestRouted_NilDecisionAlwaysDirect(t *testing.T) {
	var directGets int
	direct := recordingAccessor{name: "direct", getHits: &directGets}
	r := NewRouted(direct, nil)
	if _, err := r.Get(context.Background(), vmGVR, "ns", "n", metav1.GetOptions{}); err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if directGets != 1 {
		t.Errorf("nil decision should use Direct; direct hits=%d", directGets)
	}
}

// Compile-time assertion that the fake satisfies the neutral Session interface.
var _ agentgw.Session = (*fakeSession)(nil)
