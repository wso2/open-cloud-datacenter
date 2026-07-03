package clusteraccess

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
	getObject func(ref agentgw.ResourceRef) (agentgw.GetObjectResult, error)
	getStatus func(ref agentgw.ResourceRef) (agentgw.StatusSnapshot, error)
	list      func(ref agentgw.ListRef) (agentgw.ListResult, error)
	apply     func(manifest json.RawMessage, fm string, force bool) (agentgw.ApplyResult, error)
	del       func(ref agentgw.ResourceRef, policy string) (agentgw.DeleteResult, error)

	// block, when true, makes GetObject wait on ctx.Done() and return ctx.Err()
	// — modelling a connected-but-unresponsive agent (the real Session.Call only
	// returns on a reply or ctx cancellation). Used to prove the accessor bounds
	// every call with its own deadline.
	block bool

	lastRef     agentgw.ResourceRef // captured by GetObject/GetStatus/Delete
	lastListRef agentgw.ListRef     // captured by List
	listCalled  bool                // set true the moment List is invoked

	getStatusCalled bool // set by GetStatus, asserts the OP_UNSUPPORTED fallback fired
	getObjectCalled bool // set by GetObject, asserts the routability guard fired first
}

func (f *fakeSession) GetObject(ctx context.Context, ref agentgw.ResourceRef) (agentgw.GetObjectResult, error) {
	f.getObjectCalled = true
	f.lastRef = ref
	if f.block {
		<-ctx.Done()
		return agentgw.GetObjectResult{}, ctx.Err()
	}
	if f.getObject != nil {
		return f.getObject(ref)
	}
	return agentgw.GetObjectResult{}, nil
}

func (f *fakeSession) List(_ context.Context, ref agentgw.ListRef) (agentgw.ListResult, error) {
	f.listCalled = true
	f.lastListRef = ref
	if f.list != nil {
		return f.list(ref)
	}
	return agentgw.ListResult{}, nil
}

func (f *fakeSession) GetStatus(_ context.Context, ref agentgw.ResourceRef) (agentgw.StatusSnapshot, error) {
	f.lastRef = ref
	f.getStatusCalled = true
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

// ── AgentBacked.List: collection routing ──────────────────────────────────────

func TestAgentBackedList_RehydratesItems(t *testing.T) {
	item1 := `{"apiVersion":"kubevirt.io/v1","kind":"VirtualMachine","metadata":{"name":"vm-1","namespace":"dc-t-p"},"status":{"printableStatus":"Running"}}`
	item2 := `{"apiVersion":"kubevirt.io/v1","kind":"VirtualMachine","metadata":{"name":"vm-2","namespace":"dc-t-p"},"status":{"printableStatus":"Starting"}}`
	sess := &fakeSession{
		list: func(ref agentgw.ListRef) (agentgw.ListResult, error) {
			return agentgw.ListResult{Items: []json.RawMessage{
				json.RawMessage(item1), json.RawMessage(item2),
			}}, nil
		},
	}
	a := NewAgentBacked(sess, "lk", "zone-1", "dc-api", DefaultGVKMapper(), zerolog.Nop())

	list, err := a.List(context.Background(), vmGVR, "dc-t-p", metav1.ListOptions{LabelSelector: "dc-api/managed=true"})
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}

	// The agent saw the correct GVK + namespace + label selector.
	if sess.lastListRef.APIVersion != "kubevirt.io/v1" || sess.lastListRef.Kind != "VirtualMachine" {
		t.Errorf("list ref GVK = %s/%s, want kubevirt.io/v1/VirtualMachine", sess.lastListRef.APIVersion, sess.lastListRef.Kind)
	}
	if sess.lastListRef.Namespace != "dc-t-p" {
		t.Errorf("list ref namespace = %q, want dc-t-p", sess.lastListRef.Namespace)
	}
	if sess.lastListRef.LabelSelector != "dc-api/managed=true" {
		t.Errorf("list ref label selector = %q, want dc-api/managed=true", sess.lastListRef.LabelSelector)
	}

	// The re-hydrated list carries both objects, with the list-kind GVK set.
	if list.GetAPIVersion() != "kubevirt.io/v1" || list.GetKind() != "VirtualMachineList" {
		t.Errorf("list GVK = %s/%s, want kubevirt.io/v1/VirtualMachineList", list.GetAPIVersion(), list.GetKind())
	}
	if len(list.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(list.Items))
	}
	if list.Items[0].GetName() != "vm-1" || list.Items[1].GetName() != "vm-2" {
		t.Errorf("item names = %q,%q, want vm-1,vm-2", list.Items[0].GetName(), list.Items[1].GetName())
	}
	ps, _, _ := unstructured.NestedString(list.Items[0].Object, "status", "printableStatus")
	if ps != "Running" {
		t.Errorf("item[0] status.printableStatus = %q, want Running", ps)
	}
}

func TestAgentBackedList_Empty(t *testing.T) {
	sess := &fakeSession{
		list: func(ref agentgw.ListRef) (agentgw.ListResult, error) {
			return agentgw.ListResult{}, nil
		},
	}
	a := NewAgentBacked(sess, "lk", "zone-1", "dc-api", DefaultGVKMapper(), zerolog.Nop())

	list, err := a.List(context.Background(), vmGVR, "dc-t-p", metav1.ListOptions{})
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(list.Items) != 0 {
		t.Errorf("items = %d, want 0", len(list.Items))
	}
}

func TestAgentBackedList_UnmappedGVRNotRoutable(t *testing.T) {
	sess := &fakeSession{}
	a := NewAgentBacked(sess, "lk", "zone-1", "dc-api", DefaultGVKMapper(), zerolog.Nop())

	unknown := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	_, err := a.List(context.Background(), unknown, "ns", metav1.ListOptions{})
	if !errors.Is(err, agentgw.ErrOpNotRoutable) {
		t.Errorf("unmapped GVR must yield ErrOpNotRoutable, got %v", err)
	}
}

// TestAgentBackedList_PreSeededGVKNotRoutable proves the routability guard: a GVR
// that IS GVK-mapped (so it survives the mapper lookup) but is a pre-seeded entry
// with no RouteVerbs — virtualmachineinstances — must be REFUSED for List with
// ErrOpNotRoutable, and the agent's List must never be invoked. Without the guard
// the mapped-but-direct-only VMI family would be sent to the agent, which has no
// list grant for it (least-privilege).
func TestAgentBackedList_PreSeededGVKNotRoutable(t *testing.T) {
	// The pre-seeded VMI GVR from vmiCapability (kubevirt.io/v1, GVK-mapped, no
	// RouteVerbs). Confirm it IS mapped so this test exercises the routability
	// guard and not the unmapped-GVR path.
	vmiGVR := schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachineinstances"}
	if _, _, ok := DefaultGVKMapper().GVK(vmiGVR); !ok {
		t.Fatalf("precondition: VMI GVR %s must be GVK-mapped for this test to exercise the routability guard", vmiGVR)
	}

	sess := &fakeSession{}
	a := NewAgentBacked(sess, "lk", "zone-1", "dc-api", DefaultGVKMapper(), zerolog.Nop())

	_, err := a.List(context.Background(), vmiGVR, "dc-t-p", metav1.ListOptions{})
	if !errors.Is(err, agentgw.ErrOpNotRoutable) {
		t.Errorf("mapped-but-not-routable GVR must yield ErrOpNotRoutable, got %v", err)
	}
	if sess.listCalled {
		t.Error("agent List must NOT be invoked for a non-routable family")
	}
}

// TestAgentBackedGet_PreSeededGVKNotRoutable mirrors the List guard for Get: the
// pre-seeded virtualmachineinstances GVR is GVK-mapped but has no RouteVerbs, so
// Get must be REFUSED with ErrOpNotRoutable and the agent's GetObject never invoked.
func TestAgentBackedGet_PreSeededGVKNotRoutable(t *testing.T) {
	vmiGVR := schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachineinstances"}
	if _, _, ok := DefaultGVKMapper().GVK(vmiGVR); !ok {
		t.Fatalf("precondition: VMI GVR %s must be GVK-mapped", vmiGVR)
	}

	sess := &fakeSession{}
	a := NewAgentBacked(sess, "lk", "zone-1", "dc-api", DefaultGVKMapper(), zerolog.Nop())

	_, err := a.Get(context.Background(), vmiGVR, "dc-t-p", "vmi-1", metav1.GetOptions{})
	if !errors.Is(err, agentgw.ErrOpNotRoutable) {
		t.Errorf("mapped-but-not-routable GVR must yield ErrOpNotRoutable for Get, got %v", err)
	}
	if sess.getObjectCalled {
		t.Error("agent GetObject must NOT be invoked for a non-routable family")
	}
}

func TestAgentBackedList_AgentUnavailableIsRetryable(t *testing.T) {
	sess := &fakeSession{
		list: func(ref agentgw.ListRef) (agentgw.ListResult, error) {
			return agentgw.ListResult{}, agentgw.ErrAgentUnavailable
		},
	}
	a := NewAgentBacked(sess, "lk", "zone-1", "dc-api", DefaultGVKMapper(), zerolog.Nop())

	_, err := a.List(context.Background(), vmGVR, "dc-t-p", metav1.ListOptions{})
	if !errors.Is(err, agentgw.ErrAgentUnavailable) {
		t.Errorf("error does not wrap ErrAgentUnavailable: %v", err)
	}
}

// ── AgentBacked.Get: full-object read (get_object) ────────────────────────────

// fullVMObject is a complete VM object JSON (spec + status + metadata) — the
// shape get_object returns and the read-modify-patch write ops need.
const fullVMObject = `{"apiVersion":"kubevirt.io/v1","kind":"VirtualMachine",` +
	`"metadata":{"name":"vm-1","namespace":"dc-t-p","resourceVersion":"12345","generation":7,"uid":"u-1"},` +
	`"spec":{"running":true,"template":{"spec":{"domain":{"cpu":{"cores":2}}}}},` +
	`"status":{"printableStatus":"Running"}}`

func TestAgentBackedGet_ReturnsFullObject(t *testing.T) {
	sess := &fakeSession{
		getObject: func(ref agentgw.ResourceRef) (agentgw.GetObjectResult, error) {
			return agentgw.GetObjectResult{Found: true, Object: json.RawMessage(fullVMObject)}, nil
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
	// The full object path must NOT have called GetStatus (no fallback).
	if sess.getStatusCalled {
		t.Error("Get fell back to GetStatus when get_object succeeded; it must use the full object")
	}

	// The full object round-trips: status (as before) AND spec (the new capability
	// the read-modify-patch ops need).
	printable, found, _ := unstructured.NestedString(obj.Object, "status", "printableStatus")
	if !found || printable != "Running" {
		t.Errorf("status.printableStatus = %q (found=%v), want Running", printable, found)
	}
	cores, found, _ := unstructured.NestedInt64(obj.Object, "spec", "template", "spec", "domain", "cpu", "cores")
	if !found || cores != 2 {
		t.Errorf("spec.template.spec.domain.cpu.cores = %d (found=%v), want 2 — full object must carry .spec", cores, found)
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

// TestAgentBackedGet_FallsBackToStatusOnUnsupported proves the rolling-deploy
// compat path: an OLDER agent that replies OP_UNSUPPORTED to get_object must make
// Get fall back to GetStatus and return the prior status-only partial object —
// not error.
func TestAgentBackedGet_FallsBackToStatusOnUnsupported(t *testing.T) {
	sess := &fakeSession{
		getObject: func(ref agentgw.ResourceRef) (agentgw.GetObjectResult, error) {
			return agentgw.GetObjectResult{}, &agentgw.AgentError{Code: agentgw.CodeOpUnsupported, Message: "unknown op"}
		},
		getStatus: func(ref agentgw.ResourceRef) (agentgw.StatusSnapshot, error) {
			return agentgw.StatusSnapshot{
				Found:           true,
				ResourceVersion: "999",
				Generation:      3,
				Status:          json.RawMessage(`{"printableStatus":"Running"}`),
			}, nil
		},
	}
	a := NewAgentBacked(sess, "lk", "zone-1", "dc-api", DefaultGVKMapper(), zerolog.Nop())

	obj, err := a.Get(context.Background(), vmGVR, "dc-t-p", "vm-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get must fall back to status-only on OP_UNSUPPORTED, got error: %v", err)
	}
	if !sess.getStatusCalled {
		t.Error("Get did not fall back to GetStatus on OP_UNSUPPORTED")
	}
	// The status-only partial object still serves a status-reading caller.
	printable, found, _ := unstructured.NestedString(obj.Object, "status", "printableStatus")
	if !found || printable != "Running" {
		t.Errorf("fallback status.printableStatus = %q (found=%v), want Running", printable, found)
	}
	if rv := obj.GetResourceVersion(); rv != "999" {
		t.Errorf("fallback resourceVersion = %q, want 999", rv)
	}
	// The synthesized partial carries no .spec (status-only) — that is expected on
	// the degraded path.
	if _, found, _ := unstructured.NestedMap(obj.Object, "spec"); found {
		t.Error("status-only fallback object unexpectedly carries .spec")
	}
}

// TestAgentBackedGet_FullObjectFamilyErrorsOnUnsupported proves the other half of
// the compat gate: a FULL-object family (Secrets — StatusFallbackOK=false) whose
// caller needs .data must NOT degrade to the status-only partial on an older
// agent. Get returns a clear error (still wrapping ErrOpNotRoutable) and never
// calls GetStatus — so the cloud-provider SA bootstrap fails fast instead of
// polling a token-less Secret until timeout.
func TestAgentBackedGet_FullObjectFamilyErrorsOnUnsupported(t *testing.T) {
	secretsGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}
	if verbs, ok := RoutableVerbs(secretsGVR); !ok || !verbs[VerbGet] {
		t.Fatal("precondition: secrets Get must be a routable family")
	}
	if StatusFallbackOK(secretsGVR) {
		t.Fatal("precondition: secrets must NOT permit the status-only fallback")
	}
	sess := &fakeSession{
		getObject: func(ref agentgw.ResourceRef) (agentgw.GetObjectResult, error) {
			return agentgw.GetObjectResult{}, &agentgw.AgentError{Code: agentgw.CodeOpUnsupported, Message: "unknown op"}
		},
		getStatus: func(ref agentgw.ResourceRef) (agentgw.StatusSnapshot, error) {
			t.Error("full-object family must NOT fall back to GetStatus")
			return agentgw.StatusSnapshot{}, nil
		},
	}
	a := NewAgentBacked(sess, "lk", "zone-1", "dc-api", DefaultGVKMapper(), zerolog.Nop())

	_, err := a.Get(context.Background(), secretsGVR, "dc-t-p", "sa-token", metav1.GetOptions{})
	if err == nil {
		t.Fatal("Get must return an error for a full-object family on OP_UNSUPPORTED, got nil")
	}
	if !errors.Is(err, agentgw.ErrOpNotRoutable) {
		t.Errorf("error must wrap ErrOpNotRoutable for callers' errors.Is checks, got %v", err)
	}
	if sess.getStatusCalled {
		t.Error("Get fell back to GetStatus for a full-object family; it must error instead")
	}
	if !strings.Contains(err.Error(), "get_object") {
		t.Errorf("error should mention get_object upgrade guidance, got %q", err.Error())
	}
}

func TestAgentBackedGet_NotFoundIsK8sNotFound(t *testing.T) {
	sess := &fakeSession{
		getObject: func(ref agentgw.ResourceRef) (agentgw.GetObjectResult, error) {
			return agentgw.GetObjectResult{Found: false}, nil
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
		getObject: func(ref agentgw.ResourceRef) (agentgw.GetObjectResult, error) {
			return agentgw.GetObjectResult{}, agentgw.ErrAgentUnavailable
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

// ── AgentBacked.Apply: fieldManager forwarding + routability guard ────────────

// TestAgentBackedApply_ForwardsCallerFieldManager proves Apply sends the
// CALLER's fieldManager (and force flag) on the wire — the load-bearing
// property for the kubeovn network-plumbing writes, whose per-spec-list
// managers must not be collapsed to the accessor default (SSA would then prune
// sibling lists on the same object). An empty caller manager falls back to the
// accessor's own default, matching the dc-agent executor's empty-default.
func TestAgentBackedApply_ForwardsCallerFieldManager(t *testing.T) {
	vpcGVR := schema.GroupVersionResource{Group: "kubeovn.io", Version: "v1", Resource: "vpcs"}

	var gotFM string
	var gotForce bool
	sess := &fakeSession{apply: func(_ json.RawMessage, fm string, force bool) (agentgw.ApplyResult, error) {
		gotFM, gotForce = fm, force
		return agentgw.ApplyResult{APIVersion: "kubeovn.io/v1", Kind: "Vpc", Name: "vnet-a"}, nil
	}}
	a := NewAgentBacked(sess, "lk", "zone-1", "dc-api", DefaultGVKMapper(), zerolog.Nop())

	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kubeovn.io/v1", "kind": "Vpc",
		"metadata": map[string]interface{}{"name": "vnet-a"},
		"spec":     map[string]interface{}{"staticRoutes": []interface{}{}},
	}}

	if _, err := a.Apply(context.Background(), vpcGVR, "", obj, "dc-api-kubeovn-staticroutes", true); err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if gotFM != "dc-api-kubeovn-staticroutes" {
		t.Errorf("wire fieldManager = %q, want the caller's %q", gotFM, "dc-api-kubeovn-staticroutes")
	}
	if !gotForce {
		t.Error("wire force = false, want the caller's true")
	}

	// Empty caller manager → the accessor default; force=false forwards too.
	if _, err := a.Apply(context.Background(), vpcGVR, "", obj, "", false); err != nil {
		t.Fatalf("Apply (empty manager) error: %v", err)
	}
	if gotFM != "dc-api" {
		t.Errorf("wire fieldManager = %q for an empty caller value, want the accessor default %q", gotFM, "dc-api")
	}
	if gotForce {
		t.Error("wire force = true for the second call, want the caller's false")
	}
}

// TestAgentBackedApply_SessionErrorTranslated mirrors the Get/List
// agent-unavailable tests: a wire failure from Session.Apply must surface
// through translateErr so callers' errors.Is(ErrAgentUnavailable) checks fire.
func TestAgentBackedApply_SessionErrorTranslated(t *testing.T) {
	vpcGVR := schema.GroupVersionResource{Group: "kubeovn.io", Version: "v1", Resource: "vpcs"}
	sess := &fakeSession{apply: func(json.RawMessage, string, bool) (agentgw.ApplyResult, error) {
		return agentgw.ApplyResult{}, agentgw.ErrAgentUnavailable
	}}
	a := NewAgentBacked(sess, "lk", "zone-1", "dc-api", DefaultGVKMapper(), zerolog.Nop())

	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kubeovn.io/v1", "kind": "Vpc",
		"metadata": map[string]interface{}{"name": "vnet-a"},
	}}
	_, err := a.Apply(context.Background(), vpcGVR, "", obj, "dc-api-kubeovn-staticroutes", true)
	if !errors.Is(err, agentgw.ErrAgentUnavailable) {
		t.Errorf("error does not wrap ErrAgentUnavailable: %v", err)
	}
}

// TestAgentBackedApply_UnmappedGVRNotRoutable mirrors the Get/List unmapped-GVR
// tests: an apply for a GVR outside the capability registry is refused with
// ErrOpNotRoutable before any agent op is issued (the routability guard fires
// first — an unmapped GVR is by definition not Apply-routable).
func TestAgentBackedApply_UnmappedGVRNotRoutable(t *testing.T) {
	bogusGVR := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	applyCalled := false
	sess := &fakeSession{apply: func(json.RawMessage, string, bool) (agentgw.ApplyResult, error) {
		applyCalled = true
		return agentgw.ApplyResult{}, nil
	}}
	a := NewAgentBacked(sess, "lk", "zone-1", "dc-api", DefaultGVKMapper(), zerolog.Nop())

	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "example.com/v1", "kind": "Widget",
		"metadata": map[string]interface{}{"name": "w"},
	}}
	_, err := a.Apply(context.Background(), bogusGVR, "", obj, "dc-api", true)
	if !errors.Is(err, agentgw.ErrOpNotRoutable) {
		t.Errorf("error = %v, want ErrOpNotRoutable", err)
	}
	if applyCalled {
		t.Error("agent Apply was issued for an unmapped GVR")
	}
}

// TestAgentBackedApply_NotRoutableFamilyRefused mirrors the Get/List guards: an
// apply for a family that does not declare VerbApply (the NAD family — mapped,
// routable for other verbs, but nothing applies a NAD) is refused with
// ErrOpNotRoutable BEFORE any agent op is issued.
func TestAgentBackedApply_NotRoutableFamilyRefused(t *testing.T) {
	nadGVR := schema.GroupVersionResource{Group: "k8s.cni.cncf.io", Version: "v1", Resource: "network-attachment-definitions"}

	applyCalled := false
	sess := &fakeSession{apply: func(json.RawMessage, string, bool) (agentgw.ApplyResult, error) {
		applyCalled = true
		return agentgw.ApplyResult{}, nil
	}}
	a := NewAgentBacked(sess, "lk", "zone-1", "dc-api", DefaultGVKMapper(), zerolog.Nop())

	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "k8s.cni.cncf.io/v1", "kind": "NetworkAttachmentDefinition",
		"metadata": map[string]interface{}{"name": "n", "namespace": "ns"},
	}}
	_, err := a.Apply(context.Background(), nadGVR, "ns", obj, "dc-api", true)
	if err == nil {
		t.Fatal("Apply on a non-Apply-routable family must error")
	}
	if !errors.Is(err, agentgw.ErrOpNotRoutable) {
		t.Errorf("error = %v, want ErrOpNotRoutable", err)
	}
	if applyCalled {
		t.Error("agent Apply was issued despite the routability guard")
	}
}

// Compile-time assertion that the fake satisfies the neutral Session interface.
var _ agentgw.Session = (*fakeSession)(nil)
