package harvester

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/rs/zerolog"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/wso2/dc-api/internal/agentgw"
	"github.com/wso2/dc-api/internal/models"
	"github.com/wso2/dc-api/internal/providers/clusteraccess"
)

// This file proves the VM write slice (write slice 1):
//
//   - Toggle OFF  → CreateVM uses the DIRECT create (a plain dynamic POST, NOT
//     Direct.Apply/SSA) and DeleteVM uses the DIRECT delete; the agent is never
//     touched (byte-identical to pre-write-slice behaviour). The explicit
//     direct.applyCount()==0 assertions are the regression guard for the bug this
//     slice fixes — toggle-OFF create must stay a POST, not silently become SSA.
//   - Toggle ON + live session → CreateVM routes to the agent's create (which is
//     Session.Apply — the agent's only create mechanism) and DeleteVM routes to
//     Session.Delete, and the result is mapped correctly.
//   - Toggle ON + agent write ERROR → the error SURFACES and the DIRECT seam is
//     NOT called (the no-double-execution guard). Covered for BOTH a plain
//     transport error AND an *agentgw.AgentError{Code: OP_UNSUPPORTED}.
//   - No connected agent (decision returns false) → DIRECT seam, as the registry
//     does when r.agentGateway.Session(...) returns !ok.
//
// It complements getvm_seam_test.go (the read slice). To avoid redeclaring the
// helpers that file already owns (seamStubSession, newFakeDynamicWithVM, the
// testVM* consts), this file defines its own call-counting stub session and a
// call-counting Direct decorator.

// ── A configurable, call-counting agent Session stub ─────────────────────────

// writeStubSession records Apply/Delete invocations and returns a programmable
// result/error for each. It satisfies agentgw.Session (the read methods are
// inert — this slice exercises writes).
type writeStubSession struct {
	mu sync.Mutex

	applyCalls  int
	deleteCalls int

	applyResult agentgw.ApplyResult
	applyErr    error

	deleteResult agentgw.DeleteResult
	deleteErr    error
}

func (s *writeStubSession) GetStatus(context.Context, agentgw.ResourceRef) (agentgw.StatusSnapshot, error) {
	return agentgw.StatusSnapshot{}, nil
}

func (s *writeStubSession) Apply(_ context.Context, _ json.RawMessage, _ string, _ bool) (agentgw.ApplyResult, error) {
	s.mu.Lock()
	s.applyCalls++
	s.mu.Unlock()
	return s.applyResult, s.applyErr
}

func (s *writeStubSession) Delete(_ context.Context, _ agentgw.ResourceRef, _ string) (agentgw.DeleteResult, error) {
	s.mu.Lock()
	s.deleteCalls++
	s.mu.Unlock()
	return s.deleteResult, s.deleteErr
}

func (s *writeStubSession) WatchStatus(context.Context, agentgw.ResourceRef, int, func(string, agentgw.StatusSnapshot)) (agentgw.WatchResult, error) {
	return agentgw.WatchResult{}, nil
}

func (s *writeStubSession) applyCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyCalls
}

func (s *writeStubSession) deleteCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleteCalls
}

// ── A call-counting Direct decorator ─────────────────────────────────────────

// countingAccessor is a fully synthetic Accessor that counts the write verbs so a
// test can assert the DIRECT path was — or was NOT — taken, and WHICH verb it
// took (createCalls vs applyCalls matters: toggle-OFF create must be a POST →
// Create, never Apply/SSA). This is also how the no-double-execution guard is
// proven: when the agent errors, createCalls / deleteCalls here must stay 0 (a
// fallback would increment them).
//
// It is deliberately synthetic rather than wrapping a real clusteraccess.Direct
// over a fake dynamic client, for two unavoidable client-go fake limitations:
//
//   - The dynamicfake Create reactor deep-copies the stored object as JSON and
//     panics on a plain Go int ("cannot deep copy int"); buildVMManifest puts
//     spec.CPU (an int) into the manifest. A live apiserver serializes over the
//     wire so ints are fine — this is purely a fake-store artifact, so a real
//     Direct.Create over the fake cannot be exercised here.
//   - The dynamicfake SSA reactor does NOT support unstructured objects
//     (apply-on-absent 404s; apply-on-existing errors "unable to find api field
//     in struct Unstructured"), so Direct.Apply over the fake is equally
//     off-limits.
//
// Production Direct.Create/Delete against a real apiserver is exercised by the
// integration suite. Here the contract under test is the ROUTING decision and the
// no-fallback guarantee, both of which are about which accessor (and which verb)
// is invoked — the call counters capture that precisely without persisting.
type countingAccessor struct {
	mu          sync.Mutex
	createCalls int
	applyCalls  int
	deleteCalls int
}

var _ clusteraccess.Accessor = (*countingAccessor)(nil)

func (c *countingAccessor) Get(_ context.Context, _ schema.GroupVersionResource, ns, name string, _ metav1.GetOptions) (*unstructured.Unstructured, error) {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kubevirt.io/v1", "kind": "VirtualMachine",
		"metadata": map[string]interface{}{"name": name, "namespace": ns},
	}}, nil
}

func (c *countingAccessor) List(_ context.Context, _ schema.GroupVersionResource, _ string, _ metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	return &unstructured.UnstructuredList{}, nil
}

func (c *countingAccessor) Create(_ context.Context, _ schema.GroupVersionResource, ns string, obj *unstructured.Unstructured, _ metav1.CreateOptions) (*unstructured.Unstructured, error) {
	c.mu.Lock()
	c.createCalls++
	c.mu.Unlock()
	// Synthetic success mirroring a dynamic POST (the created object echoes back).
	// The toggle-OFF / no-agent CreateVM tests assert routing via this counter; no
	// persistence is needed.
	_ = ns
	return obj, nil
}

func (c *countingAccessor) Apply(_ context.Context, _ schema.GroupVersionResource, ns string, obj *unstructured.Unstructured, _ string, _ bool) (*unstructured.Unstructured, error) {
	c.mu.Lock()
	c.applyCalls++
	c.mu.Unlock()
	// Synthetic success mirroring a server-side apply (the object identity echoes
	// back). Not on the CreateVM path any more — CreateVM uses Create — but kept so
	// the decorator stays a full Accessor and any stray Apply is still counted.
	_ = ns
	return obj, nil
}

func (c *countingAccessor) Update(_ context.Context, _ schema.GroupVersionResource, _ string, obj *unstructured.Unstructured, _ metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	return obj, nil
}

func (c *countingAccessor) Delete(_ context.Context, _ schema.GroupVersionResource, _, _ string, _ metav1.DeleteOptions) error {
	c.mu.Lock()
	c.deleteCalls++
	c.mu.Unlock()
	// Synthetic successful delete — mirrors Direct's IsNotFound-is-success
	// observable. Routing is proven by the counter.
	return nil
}

func (c *countingAccessor) createCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.createCalls
}

func (c *countingAccessor) applyCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.applyCalls
}

func (c *countingAccessor) deleteCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.deleteCalls
}

// ── Fake-dynamic helpers + a representative VMSpec ───────────────────────────

// newEmptyFakeDynamic returns a fake dynamic client with NO objects, so a DeleteVM
// sees no object (and CreateVM, when it reaches a real store, would be a fresh
// create). resolveImage does NOT go through the seam, so CreateVM tests pre-seed
// an image object via newFakeDynamicWithImage instead.
func newEmptyFakeDynamic() *dynamicfake.FakeDynamicClient {
	gvrToListKind := map[schema.GroupVersionResource]string{
		harvesterVMResource:  "VirtualMachineList",
		harvesterVMIResource: "VirtualMachineInstanceList",
		vmImageGVR:           "VirtualMachineImageList",
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gvrToListKind)
}

// newFakeDynamicWithImage seeds a ready VirtualMachineImage (status.storageClassName
// populated) so resolveImage — which always runs on c.dynamic — succeeds. The VM
// store itself stays empty.
func newFakeDynamicWithImage(imageName, storageClass string) *dynamicfake.FakeDynamicClient {
	img := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "harvesterhci.io/v1beta1",
		"kind":       "VirtualMachineImage",
		"metadata": map[string]interface{}{
			"name":      "image-abc123",
			"namespace": "default",
		},
		"spec": map[string]interface{}{
			"displayName": imageName,
		},
		"status": map[string]interface{}{
			"storageClassName": storageClass,
		},
	}}
	gvrToListKind := map[schema.GroupVersionResource]string{
		harvesterVMResource:  "VirtualMachineList",
		harvesterVMIResource: "VirtualMachineInstanceList",
		vmImageGVR:           "VirtualMachineImageList",
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gvrToListKind, img)
}

func sampleVMSpec() models.VMSpec {
	return models.VMSpec{
		Name:       testVMName,
		ResourceID: "11111111-2222-3333-4444-555555555555",
		ImageName:  "ubuntu-22.04",
		CPU:        2,
		MemoryGB:   4,
		DiskGB:     20,
		// Legacy bridge path (no VNet/Subnet) keeps the manifest simple; the seam
		// routing under test is identical regardless of the network shape.
		NetworkName: "default/vm-network",
	}
}

// writeRouted builds a Routed accessor whose decision allows ONLY VerbCreate and
// VerbDelete (mirroring the registry's write allow-set) and returns the supplied
// agent accessor for them. direct is the call-counting Direct decorator so a test
// can assert whether the direct path ran. allow=false models the toggle-off /
// no-connected-agent case (always Direct).
func writeRouted(direct clusteraccess.Accessor, agent clusteraccess.Accessor, allow bool) *clusteraccess.Routed {
	return clusteraccess.NewRouted(direct, func(v clusteraccess.Verb) (clusteraccess.Accessor, bool) {
		if !allow {
			return nil, false
		}
		switch v {
		case clusteraccess.VerbCreate, clusteraccess.VerbDelete:
			return agent, true
		default:
			return nil, false
		}
	})
}

// ── Toggle OFF: CreateVM/DeleteVM use Direct; agent untouched ────────────────

func TestCreateVM_ToggleOff_UsesDirect_AgentUntouched(t *testing.T) {
	dyn := newFakeDynamicWithImage("ubuntu-22.04", "harvester-longhorn-default-image-abc123")
	direct := &countingAccessor{}
	sess := &writeStubSession{}
	agent := clusteraccess.NewAgentBacked(sess, "lk", "zone-1", "dc-api", clusteraccess.DefaultGVKMapper(), zerolog.Nop())

	c := &Client{
		dynamic: dyn,
		access:  writeRouted(direct, agent, false), // toggle OFF → never routes
	}

	res, err := c.CreateVM(context.Background(), "tenant", "proj", sampleVMSpec())
	if err != nil {
		t.Fatalf("CreateVM error: %v", err)
	}
	if res.Status != models.StatusPending {
		t.Errorf("status = %q, want PENDING", res.Status)
	}

	// CreateVM must take the DIRECT create path (a plain POST) — NOT the agent, and
	// NOT Direct.Apply (the bug this slice fixes: toggle-OFF create must stay POST).
	if got := sess.applyCount(); got != 0 {
		t.Errorf("agent Apply called %d times with toggle OFF, want 0", got)
	}
	if got := direct.createCount(); got != 1 {
		t.Errorf("direct Create called %d times, want 1", got)
	}
	if got := direct.applyCount(); got != 0 {
		t.Errorf("direct Apply called %d times with toggle OFF, want 0 (create must be a POST, not SSA)", got)
	}
}

func TestDeleteVM_ToggleOff_UsesDirect_AgentUntouched(t *testing.T) {
	dyn := newEmptyFakeDynamic()
	direct := &countingAccessor{}
	sess := &writeStubSession{}
	agent := clusteraccess.NewAgentBacked(sess, "lk", "zone-1", "dc-api", clusteraccess.DefaultGVKMapper(), zerolog.Nop())

	c := &Client{
		dynamic: dyn,
		access:  writeRouted(direct, agent, false),
	}

	// No object present → Direct returns NotFound, which DeleteVM swallows as a
	// successful idempotent delete. The point is the DIRECT seam ran, not the agent.
	if err := c.DeleteVM(context.Background(), testBackendUID); err != nil {
		t.Fatalf("DeleteVM error: %v", err)
	}
	if got := sess.deleteCount(); got != 0 {
		t.Errorf("agent Delete called %d times with toggle OFF, want 0", got)
	}
	if got := direct.deleteCount(); got != 1 {
		t.Errorf("direct Delete called %d times, want 1", got)
	}
}

// ── Toggle ON + live session: routes to the agent ───────────────────────────

func TestCreateVM_ToggleOn_RoutesToAgentApply(t *testing.T) {
	dyn := newFakeDynamicWithImage("ubuntu-22.04", "harvester-longhorn-default-image-abc123")
	direct := &countingAccessor{}
	sess := &writeStubSession{
		applyResult: agentgw.ApplyResult{
			APIVersion:      "kubevirt.io/v1",
			Kind:            "VirtualMachine",
			Namespace:       "dc-tenant-proj",
			Name:            testVMName,
			UID:             "uid-1",
			ResourceVersion: "42",
		},
	}
	agent := clusteraccess.NewAgentBacked(sess, "lk", "zone-1", "dc-api", clusteraccess.DefaultGVKMapper(), zerolog.Nop())

	c := &Client{
		dynamic: dyn,
		access:  writeRouted(direct, agent, true), // toggle ON
	}

	res, err := c.CreateVM(context.Background(), "tenant", "proj", sampleVMSpec())
	if err != nil {
		t.Fatalf("CreateVM error: %v", err)
	}

	// Agent create is a server-side apply (Session.Apply) — the agent's only create
	// mechanism — so the agent's apply counter is what increments here.
	if got := sess.applyCount(); got != 1 {
		t.Errorf("agent Apply called %d times, want 1", got)
	}
	if got := direct.createCount(); got != 0 {
		t.Errorf("direct Create called %d times with toggle ON, want 0", got)
	}
	if got := direct.applyCount(); got != 0 {
		t.Errorf("direct Apply called %d times with toggle ON, want 0", got)
	}
	// BackendUID is built from ns:name regardless of seam — verify the mapping.
	if want := "dc-tenant-proj:" + testVMName; res.BackendUID != want {
		t.Errorf("BackendUID = %q, want %q", res.BackendUID, want)
	}
	if res.Status != models.StatusPending {
		t.Errorf("status = %q, want PENDING", res.Status)
	}

	// The agent path must NOT have written to the local fake dynamic store.
	if _, err := dyn.Resource(harvesterVMResource).Namespace("dc-tenant-proj").Get(context.Background(), testVMName, metav1.GetOptions{}); err == nil {
		t.Error("VM unexpectedly present in the direct store — agent path should not touch it")
	}
}

func TestDeleteVM_ToggleOn_RoutesToAgentDelete(t *testing.T) {
	dyn := newEmptyFakeDynamic()
	direct := &countingAccessor{}
	sess := &writeStubSession{deleteResult: agentgw.DeleteResult{Existed: true}}
	agent := clusteraccess.NewAgentBacked(sess, "lk", "zone-1", "dc-api", clusteraccess.DefaultGVKMapper(), zerolog.Nop())

	c := &Client{
		dynamic: dyn,
		access:  writeRouted(direct, agent, true),
	}

	if err := c.DeleteVM(context.Background(), testBackendUID); err != nil {
		t.Fatalf("DeleteVM error: %v", err)
	}
	if got := sess.deleteCount(); got != 1 {
		t.Errorf("agent Delete called %d times, want 1", got)
	}
	if got := direct.deleteCount(); got != 0 {
		t.Errorf("direct Delete called %d times with toggle ON, want 0", got)
	}
}

// TestDeleteVM_ToggleOn_AgentMissingObjectIsSuccess proves the idempotent-delete
// contract on the agent seam: Existed==false maps to a nil error (no double-run,
// no spurious failure), matching Direct's IsNotFound-is-success behaviour.
func TestDeleteVM_ToggleOn_AgentMissingObjectIsSuccess(t *testing.T) {
	dyn := newEmptyFakeDynamic()
	direct := &countingAccessor{}
	sess := &writeStubSession{deleteResult: agentgw.DeleteResult{Existed: false}}
	agent := clusteraccess.NewAgentBacked(sess, "lk", "zone-1", "dc-api", clusteraccess.DefaultGVKMapper(), zerolog.Nop())

	c := &Client{dynamic: dyn, access: writeRouted(direct, agent, true)}

	if err := c.DeleteVM(context.Background(), testBackendUID); err != nil {
		t.Fatalf("DeleteVM error on absent object: %v", err)
	}
	if got := sess.deleteCount(); got != 1 {
		t.Errorf("agent Delete called %d times, want 1", got)
	}
	if got := direct.deleteCount(); got != 0 {
		t.Errorf("direct Delete called %d times, want 0 (no fallback on idempotent delete)", got)
	}
}

// ── The load-bearing test: agent write error surfaces, NO direct fallback ────

func TestCreateVM_ToggleOn_AgentError_SurfacesNoDirectFallback(t *testing.T) {
	cases := []struct {
		name     string
		applyErr error
	}{
		{"plain transport error", errors.New("connection reset by peer")},
		{"op unsupported", &agentgw.AgentError{Code: agentgw.CodeOpUnsupported, Message: "apply not implemented"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dyn := newFakeDynamicWithImage("ubuntu-22.04", "harvester-longhorn-default-image-abc123")
			direct := &countingAccessor{}
			sess := &writeStubSession{applyErr: tc.applyErr}
			agent := clusteraccess.NewAgentBacked(sess, "lk", "zone-1", "dc-api", clusteraccess.DefaultGVKMapper(), zerolog.Nop())

			c := &Client{dynamic: dyn, access: writeRouted(direct, agent, true)}

			_, err := c.CreateVM(context.Background(), "tenant", "proj", sampleVMSpec())
			if err == nil {
				t.Fatal("CreateVM returned nil error; the agent write failure must surface")
			}

			// The agent was attempted exactly once (via Session.Apply, its only
			// create mechanism)...
			if got := sess.applyCount(); got != 1 {
				t.Errorf("agent Apply called %d times, want 1", got)
			}
			// ...and the DIRECT seam was NEVER called — no double-execution. Both the
			// POST create path and the (unused) Apply path must stay at 0.
			if got := direct.createCount(); got != 0 {
				t.Errorf("direct Create called %d times after agent error, want 0 (no fallback / no double-execution)", got)
			}
			if got := direct.applyCount(); got != 0 {
				t.Errorf("direct Apply called %d times after agent error, want 0 (no fallback / no double-execution)", got)
			}
			// And nothing landed in the direct store.
			if _, gerr := dyn.Resource(harvesterVMResource).Namespace("dc-tenant-proj").Get(context.Background(), testVMName, metav1.GetOptions{}); gerr == nil {
				t.Error("VM present in direct store after agent error — Direct must not have run")
			}
		})
	}
}

func TestDeleteVM_ToggleOn_AgentError_SurfacesNoDirectFallback(t *testing.T) {
	cases := []struct {
		name      string
		deleteErr error
	}{
		{"plain transport error", errors.New("connection reset by peer")},
		{"op unsupported", &agentgw.AgentError{Code: agentgw.CodeOpUnsupported, Message: "delete not implemented"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dyn := newEmptyFakeDynamic()
			direct := &countingAccessor{}
			sess := &writeStubSession{deleteErr: tc.deleteErr}
			agent := clusteraccess.NewAgentBacked(sess, "lk", "zone-1", "dc-api", clusteraccess.DefaultGVKMapper(), zerolog.Nop())

			c := &Client{dynamic: dyn, access: writeRouted(direct, agent, true)}

			err := c.DeleteVM(context.Background(), testBackendUID)
			if err == nil {
				t.Fatal("DeleteVM returned nil error; the agent write failure must surface")
			}
			if got := sess.deleteCount(); got != 1 {
				t.Errorf("agent Delete called %d times, want 1", got)
			}
			if got := direct.deleteCount(); got != 0 {
				t.Errorf("direct Delete called %d times after agent error, want 0 (no fallback / no double-execution)", got)
			}
		})
	}
}

// ── No connected agent: decision returns false → Direct ──────────────────────

// TestCreateVM_NoConnectedAgent_UsesDirect models the registry's behaviour when
// the toggle is ON but agentGateway.Session(...) returns !ok (no live agent): the
// decision returns (_, false) and Routed uses Direct. We model that exactly with
// allow=false (the closure short-circuits identically). This is the safety belt:
// flipping the env var can never strand the zone when the agent is down.
func TestCreateVM_NoConnectedAgent_UsesDirect(t *testing.T) {
	dyn := newFakeDynamicWithImage("ubuntu-22.04", "harvester-longhorn-default-image-abc123")
	direct := &countingAccessor{}
	sess := &writeStubSession{}
	agent := clusteraccess.NewAgentBacked(sess, "lk", "zone-1", "dc-api", clusteraccess.DefaultGVKMapper(), zerolog.Nop())

	// decision returns false (no live session) → Direct, even though "writes" are
	// conceptually enabled.
	c := &Client{dynamic: dyn, access: writeRouted(direct, agent, false)}

	if _, err := c.CreateVM(context.Background(), "tenant", "proj", sampleVMSpec()); err != nil {
		t.Fatalf("CreateVM error: %v", err)
	}
	if got := sess.applyCount(); got != 0 {
		t.Errorf("agent Apply called %d times with no live agent, want 0", got)
	}
	if got := direct.createCount(); got != 1 {
		t.Errorf("direct Create called %d times, want 1", got)
	}
	if got := direct.applyCount(); got != 0 {
		t.Errorf("direct Apply called %d times with no live agent, want 0 (create must be a POST)", got)
	}
}

// Compile-time assertion that the stub really satisfies the neutral Session
// interface the seam depends on (catches a signature drift early).
var _ agentgw.Session = (*writeStubSession)(nil)

// Silence the unused-import linter for dynamic in case a future refactor drops
// the only typed use; harmless and explicit.
var _ dynamic.Interface = (*dynamicfake.FakeDynamicClient)(nil)
