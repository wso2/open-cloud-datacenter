package kubeovn

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
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/wso2/dc-api/internal/agentgw"
	"github.com/wso2/dc-api/internal/models"
	"github.com/wso2/dc-api/internal/providers/clusteraccess"
	"github.com/wso2/dc-api/internal/providers/common"
)

// This file is the KubeOVN analogue of harvester/vmwrite_seam_test.go. It proves
// the network CRD CRUD slice (Vpc/Subnet/NAD create/get/delete) routed through
// the cluster-access seam:
//
//   - Toggle OFF → the network ops use the DIRECT path; the agent is never
//     touched (byte-identical to the pre-seam behaviour). For writes the explicit
//     direct.applyCount()==0 assertion is the regression guard: a routed create
//     must stay a plain Create (POST), never silently become an SSA Apply.
//   - Toggle ON + agent write ERROR → the error SURFACES and the DIRECT seam is
//     NOT called (the no-double-execution / TERMINAL-on-activation guard), for
//     both a plain transport error and an *agentgw.AgentError{OP_UNSUPPORTED}.
//   - No connected agent (decision returns false) → DIRECT, as the registry does
//     when r.agentGateway.Session(...) returns !ok.
//
// Like the VM write tests, the write-path cases use a synthetic counting Accessor
// (not a real Direct over a fake dynamic): the dynamicfake Create reactor cannot
// round-trip these unstructured CRDs through SSA, and we are testing the ROUTING
// decision + no-fallback contract, which the call counters capture precisely.
// Real Direct CRUD against an apiserver is exercised by the integration suite.

// ── A call-counting agent Session stub (mirrors writeStubSession) ────────────

type netStubSession struct {
	mu sync.Mutex

	applyCalls  int
	deleteCalls int

	applyResult agentgw.ApplyResult
	applyErr    error

	deleteResult agentgw.DeleteResult
	deleteErr    error
}

func (s *netStubSession) GetObject(context.Context, agentgw.ResourceRef) (agentgw.GetObjectResult, error) {
	return agentgw.GetObjectResult{}, nil
}

func (s *netStubSession) GetStatus(context.Context, agentgw.ResourceRef) (agentgw.StatusSnapshot, error) {
	return agentgw.StatusSnapshot{}, nil
}

func (s *netStubSession) Apply(_ context.Context, _ json.RawMessage, _ string, _ bool) (agentgw.ApplyResult, error) {
	s.mu.Lock()
	s.applyCalls++
	s.mu.Unlock()
	return s.applyResult, s.applyErr
}

func (s *netStubSession) Delete(_ context.Context, _ agentgw.ResourceRef, _ string) (agentgw.DeleteResult, error) {
	s.mu.Lock()
	s.deleteCalls++
	s.mu.Unlock()
	return s.deleteResult, s.deleteErr
}

func (s *netStubSession) List(context.Context, agentgw.ListRef) (agentgw.ListResult, error) {
	return agentgw.ListResult{}, nil
}

func (s *netStubSession) WatchStatus(context.Context, agentgw.ResourceRef, int, func(string, agentgw.StatusSnapshot)) (agentgw.WatchResult, error) {
	return agentgw.WatchResult{}, nil
}

func (s *netStubSession) applyCount() int  { s.mu.Lock(); defer s.mu.Unlock(); return s.applyCalls }
func (s *netStubSession) deleteCount() int { s.mu.Lock(); defer s.mu.Unlock(); return s.deleteCalls }

var _ agentgw.Session = (*netStubSession)(nil)

// ── A call-counting Direct decorator (mirrors harvester.countingAccessor) ────

type netCountingAccessor struct {
	mu          sync.Mutex
	getCalls    int
	createCalls int
	applyCalls  int
	deleteCalls int
}

var _ clusteraccess.Accessor = (*netCountingAccessor)(nil)

func (c *netCountingAccessor) Get(_ context.Context, gvr schema.GroupVersionResource, ns, name string, _ metav1.GetOptions) (*unstructured.Unstructured, error) {
	c.mu.Lock()
	c.getCalls++
	c.mu.Unlock()
	// Return a minimally-shaped object carrying the labels DeleteSubnet reads, so
	// the leading Get in DeleteSubnet can derive the namespace deterministically.
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kubeovn.io/v1", "kind": "Subnet",
		"metadata": map[string]interface{}{
			"name": name, "namespace": ns,
			"labels": map[string]interface{}{
				"dc-api/managed": "true",
				"dc-api/tenant":  "tenant",
				"dc-api/project": "proj",
			},
		},
	}}, nil
}

func (c *netCountingAccessor) List(_ context.Context, _ schema.GroupVersionResource, _ string, _ metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	return &unstructured.UnstructuredList{}, nil
}

func (c *netCountingAccessor) Create(_ context.Context, _ schema.GroupVersionResource, _ string, obj *unstructured.Unstructured, _ metav1.CreateOptions) (*unstructured.Unstructured, error) {
	c.mu.Lock()
	c.createCalls++
	c.mu.Unlock()
	return obj, nil
}

func (c *netCountingAccessor) Apply(_ context.Context, _ schema.GroupVersionResource, _ string, obj *unstructured.Unstructured, _ string, _ bool) (*unstructured.Unstructured, error) {
	c.mu.Lock()
	c.applyCalls++
	c.mu.Unlock()
	return obj, nil
}

func (c *netCountingAccessor) Update(_ context.Context, _ schema.GroupVersionResource, _ string, obj *unstructured.Unstructured, _ metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	return obj, nil
}

func (c *netCountingAccessor) Delete(_ context.Context, _ schema.GroupVersionResource, _, _ string, _ metav1.DeleteOptions) error {
	c.mu.Lock()
	c.deleteCalls++
	c.mu.Unlock()
	return nil
}

func (c *netCountingAccessor) getCount() int    { c.mu.Lock(); defer c.mu.Unlock(); return c.getCalls }
func (c *netCountingAccessor) createCount() int { c.mu.Lock(); defer c.mu.Unlock(); return c.createCalls }
func (c *netCountingAccessor) applyCount() int  { c.mu.Lock(); defer c.mu.Unlock(); return c.applyCalls }
func (c *netCountingAccessor) deleteCount() int { c.mu.Lock(); defer c.mu.Unlock(); return c.deleteCalls }

// ── Fake-dynamic helpers ─────────────────────────────────────────────────────

// netGVRToListKind registers the CRD list kinds the kubeovn driver's reads need.
func netGVRToListKind() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{
		vpcGVR:    "VpcList",
		subnetGVR: "SubnetList",
		nadGVR:    "NetworkAttachmentDefinitionList",
		ipGVR:     "IpList",
	}
}

func newEmptyFakeDynamic() *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), netGVRToListKind())
}

// newFakeDynamicForCreateSubnet seeds the objects CreateSubnet reads off
// c.dynamic (which stay DIRECT, not routed): the parent Vpc (for the
// tenant/project labels) and a ready NAD (so waitForNADReady returns immediately,
// since it polls c.dynamic for the network.harvesterhci.io/ready label).
func newFakeDynamicForCreateSubnet(t *testing.T, vnetUID, ns, nadName string) *dynamicfake.FakeDynamicClient {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), netGVRToListKind())

	// dynamicfake's tracker keys an object passed to the constructor by a default
	// pluralizer (NetworkAttachmentDefinition → "networkattachmentdefinitions"),
	// which does NOT match the hyphenated nadGVR the driver uses. Creating through
	// the explicit GVR client stores the object under the exact GVR, so the
	// driver's c.dynamic.Resource(nadGVR).Get can retrieve it. Same for the
	// cluster-scoped Vpc (created without a namespace).
	vpc := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kubeovn.io/v1", "kind": "Vpc",
		"metadata": map[string]interface{}{
			"name": vnetUID,
			"labels": map[string]interface{}{
				"dc-api/tenant":  "tenant",
				"dc-api/project": "proj",
			},
		},
	}}
	if _, err := dyn.Resource(vpcGVR).Create(context.Background(), vpc, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed vpc: %v", err)
	}
	nad := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "k8s.cni.cncf.io/v1", "kind": "NetworkAttachmentDefinition",
		"metadata": map[string]interface{}{
			"name": nadName, "namespace": ns,
			"labels": map[string]interface{}{
				"dc-api/managed":                "true",
				"network.harvesterhci.io/ready": "true",
			},
		},
	}}
	if _, err := dyn.Resource(nadGVR).Namespace(ns).Create(context.Background(), nad, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed nad: %v", err)
	}
	return dyn
}

func sampleVNetSpec() models.VNetSpec {
	return models.VNetSpec{
		Name:         "app",
		AddressSpace: []string{"10.0.0.0/16"},
		Description:  "test vnet",
	}
}

func sampleSubnetSpec() models.SubnetSpec {
	return models.SubnetSpec{
		Name: "sub",
		CIDR: "10.0.1.0/24",
	}
}

// networkRouted builds a Routed accessor whose decision allows ONLY VerbCreate
// and VerbDelete (the network write allow-set) and returns the supplied agent
// accessor for them. direct is the call-counting decorator. allow=false models
// the toggle-off / no-connected-agent case (always Direct). It is GVR-aware to
// match the Part A AgentDecision signature, but ignores the GVR because every
// object this test drives belongs to an onboarded network family.
func networkRouted(direct, agent clusteraccess.Accessor, allow bool) *clusteraccess.Routed {
	return clusteraccess.NewRouted(direct, func(v clusteraccess.Verb, _ schema.GroupVersionResource) (clusteraccess.Accessor, bool) {
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

// ── Toggle OFF: ops use Direct; agent untouched (byte-identical) ─────────────

func TestCreateVNet_ToggleOff_UsesDirect_AgentUntouched(t *testing.T) {
	dyn := newEmptyFakeDynamic()
	direct := &netCountingAccessor{}
	sess := &netStubSession{}
	agent := clusteraccess.NewAgentBacked(sess, "lk", "zone-1", "dc-api", clusteraccess.DefaultGVKMapper(), zerolog.Nop())

	c := &Client{
		dynamic: dyn,
		access:  networkRouted(direct, agent, false), // toggle OFF → never routes
	}

	res, err := c.CreateVNet(context.Background(), "tenant", "proj", sampleVNetSpec())
	if err != nil {
		t.Fatalf("CreateVNet error: %v", err)
	}
	if res.Status != models.StatusPending {
		t.Errorf("status = %q, want PENDING", res.Status)
	}

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

func TestDeleteVNet_ToggleOff_UsesDirect_AgentUntouched(t *testing.T) {
	dyn := newEmptyFakeDynamic()
	direct := &netCountingAccessor{}
	sess := &netStubSession{}
	agent := clusteraccess.NewAgentBacked(sess, "lk", "zone-1", "dc-api", clusteraccess.DefaultGVKMapper(), zerolog.Nop())

	c := &Client{dynamic: dyn, access: networkRouted(direct, agent, false)}

	if err := c.DeleteVNet(context.Background(), "vnet-tenant-proj-app"); err != nil {
		t.Fatalf("DeleteVNet error: %v", err)
	}
	if got := sess.deleteCount(); got != 0 {
		t.Errorf("agent Delete called %d times with toggle OFF, want 0", got)
	}
	if got := direct.deleteCount(); got != 1 {
		t.Errorf("direct Delete called %d times, want 1", got)
	}
}

// TestCreateSubnet_ToggleOff_UsesDirect_AgentUntouched drives the full
// CreateSubnet flow with the toggle OFF: the parent-VPC fetch + the NAD-ready
// poll (both DIRECT on c.dynamic, deferred from routing) read pre-seeded objects,
// and the two routed creates (NAD, Subnet) fall through to the Direct counter.
// The agent must never be touched.
//
// NOTE: dynamicfake's object tracker keys objects by a DEFAULT pluralizer that
// renders NetworkAttachmentDefinition as "networkattachmentdefinitions" — NOT the
// hyphenated "network-attachment-definitions" GVR the driver uses — so a NAD
// seeded by apiVersion/kind is unreachable under nadGVR. We therefore inject the
// fake dynamic ONLY for the deferred-direct reads (VPC + NAD-ready), and route the
// two creates through a separate Direct counter that bypasses the fake store.
func TestCreateSubnet_ToggleOff_UsesDirect_AgentUntouched(t *testing.T) {
	vnetUID := vpcName("app", "tenant", "proj")
	ns := common.NamespaceForProject("tenant", "proj")
	nadName := subnetResourceName(vnetUID, "sub", "tenant", "proj")
	dyn := newFakeDynamicForCreateSubnet(t, vnetUID, ns, nadName)
	direct := &netCountingAccessor{}
	sess := &netStubSession{}
	agent := clusteraccess.NewAgentBacked(sess, "lk", "zone-1", "dc-api", clusteraccess.DefaultGVKMapper(), zerolog.Nop())

	c := &Client{dynamic: dyn, access: networkRouted(direct, agent, false)}

	res, err := c.CreateSubnet(context.Background(), vnetUID, sampleSubnetSpec())
	if err != nil {
		t.Fatalf("CreateSubnet error: %v", err)
	}
	if res.Status != models.StatusPending {
		t.Errorf("status = %q, want PENDING", res.Status)
	}

	// NAD create + Subnet create both ran on Direct (2 creates); agent untouched.
	if got := sess.applyCount(); got != 0 {
		t.Errorf("agent Apply called %d times with toggle OFF, want 0", got)
	}
	if got := direct.createCount(); got != 2 {
		t.Errorf("direct Create called %d times, want 2 (NAD + Subnet)", got)
	}
	if got := direct.applyCount(); got != 0 {
		t.Errorf("direct Apply called %d times with toggle OFF, want 0 (creates must be POSTs)", got)
	}
}

// TestDeleteSubnet_ToggleOff_UsesDirect_AgentUntouched drives DeleteSubnet with
// the toggle OFF. The leading Subnet Get is routed→Direct (countingAccessor
// returns a labelled Subnet so namespace derivation works); the ACL clear, wait
// loop, and finalizer recovery stay DIRECT on the (empty) fake (best-effort,
// logged); and the routed Subnet Delete + NAD Delete fall through to the Direct
// counter. The agent is never touched.
func TestDeleteSubnet_ToggleOff_UsesDirect_AgentUntouched(t *testing.T) {
	dyn := newEmptyFakeDynamic()
	direct := &netCountingAccessor{}
	sess := &netStubSession{}
	agent := clusteraccess.NewAgentBacked(sess, "lk", "zone-1", "dc-api", clusteraccess.DefaultGVKMapper(), zerolog.Nop())

	c := &Client{dynamic: dyn, access: networkRouted(direct, agent, false)}

	if err := c.DeleteSubnet(context.Background(), "subnet-tenant-proj-app-sub"); err != nil {
		t.Fatalf("DeleteSubnet error: %v", err)
	}
	if got := sess.deleteCount(); got != 0 {
		t.Errorf("agent Delete called %d times with toggle OFF, want 0", got)
	}
	// Subnet Delete + NAD Delete both ran on Direct.
	if got := direct.deleteCount(); got != 2 {
		t.Errorf("direct Delete called %d times, want 2 (Subnet + NAD)", got)
	}
	// The leading Subnet Get is a read (toggle off for reads here too) → Direct.
	if got := direct.getCount(); got < 1 {
		t.Errorf("direct Get called %d times, want >=1 (leading subnet fetch)", got)
	}
}

// TestDeleteSubnet_ToggleOn_AgentError_SurfacesNoDirectFallback proves the
// no-double-execution guard on the PRIMARY Subnet delete: with writes routed and
// the agent erroring, the error surfaces and the Direct seam's Delete is NOT
// called as a fallback for that verb. (The leading Get is a read verb not in this
// allow-set, so it stays Direct — the deferred-read path, not a delete fallback.)
func TestDeleteSubnet_ToggleOn_AgentError_SurfacesNoDirectFallback(t *testing.T) {
	dyn := newEmptyFakeDynamic()
	direct := &netCountingAccessor{}
	sess := &netStubSession{deleteErr: &agentgw.AgentError{Code: agentgw.CodeOpUnsupported, Message: "delete not implemented"}}
	agent := clusteraccess.NewAgentBacked(sess, "lk", "zone-1", "dc-api", clusteraccess.DefaultGVKMapper(), zerolog.Nop())

	c := &Client{dynamic: dyn, access: networkRouted(direct, agent, true)}

	err := c.DeleteSubnet(context.Background(), "subnet-tenant-proj-app-sub")
	if err == nil {
		t.Fatal("DeleteSubnet returned nil error; the agent write failure must surface")
	}
	// Agent Delete attempted exactly once (the primary Subnet delete).
	if got := sess.deleteCount(); got != 1 {
		t.Errorf("agent Delete called %d times, want 1", got)
	}
	// The Direct seam must NOT have run a Delete as a fallback for the primary verb.
	if got := direct.deleteCount(); got != 0 {
		t.Errorf("direct Delete called %d times after agent error, want 0 (no fallback)", got)
	}
}

// ── No connected agent: decision returns false → Direct ──────────────────────

func TestCreateVNet_NoConnectedAgent_UsesDirect(t *testing.T) {
	dyn := newEmptyFakeDynamic()
	direct := &netCountingAccessor{}
	sess := &netStubSession{}
	agent := clusteraccess.NewAgentBacked(sess, "lk", "zone-1", "dc-api", clusteraccess.DefaultGVKMapper(), zerolog.Nop())

	// allow=false models the registry returning (_, false) when no live agent.
	c := &Client{dynamic: dyn, access: networkRouted(direct, agent, false)}

	if _, err := c.CreateVNet(context.Background(), "tenant", "proj", sampleVNetSpec()); err != nil {
		t.Fatalf("CreateVNet error: %v", err)
	}
	if got := sess.applyCount(); got != 0 {
		t.Errorf("agent Apply called %d times with no live agent, want 0", got)
	}
	if got := direct.createCount(); got != 1 {
		t.Errorf("direct Create called %d times, want 1", got)
	}
}

// ── Toggle ON + agent write error: surfaces, NO direct fallback ──────────────

func TestCreateVNet_ToggleOn_AgentError_SurfacesNoDirectFallback(t *testing.T) {
	cases := []struct {
		name     string
		applyErr error
	}{
		{"plain transport error", errors.New("connection reset by peer")},
		{"op unsupported", &agentgw.AgentError{Code: agentgw.CodeOpUnsupported, Message: "apply not implemented"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dyn := newEmptyFakeDynamic()
			direct := &netCountingAccessor{}
			sess := &netStubSession{applyErr: tc.applyErr}
			agent := clusteraccess.NewAgentBacked(sess, "lk", "zone-1", "dc-api", clusteraccess.DefaultGVKMapper(), zerolog.Nop())

			c := &Client{dynamic: dyn, access: networkRouted(direct, agent, true)}

			_, err := c.CreateVNet(context.Background(), "tenant", "proj", sampleVNetSpec())
			if err == nil {
				t.Fatal("CreateVNet returned nil error; the agent write failure must surface")
			}
			// Agent attempted exactly once (Vpc create = SSA Apply, its only create
			// mechanism)...
			if got := sess.applyCount(); got != 1 {
				t.Errorf("agent Apply called %d times, want 1", got)
			}
			// ...and the DIRECT seam was NEVER called — no double-execution.
			if got := direct.createCount(); got != 0 {
				t.Errorf("direct Create called %d times after agent error, want 0 (no fallback)", got)
			}
			if got := direct.applyCount(); got != 0 {
				t.Errorf("direct Apply called %d times after agent error, want 0 (no fallback)", got)
			}
			// And nothing landed in the direct store.
			if _, gerr := dyn.Resource(vpcGVR).Get(context.Background(), vpcName("app", "tenant", "proj"), metav1.GetOptions{}); gerr == nil {
				t.Error("Vpc present in direct store after agent error — Direct must not have run")
			}
		})
	}
}

func TestDeleteVNet_ToggleOn_AgentError_SurfacesNoDirectFallback(t *testing.T) {
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
			direct := &netCountingAccessor{}
			sess := &netStubSession{deleteErr: tc.deleteErr}
			agent := clusteraccess.NewAgentBacked(sess, "lk", "zone-1", "dc-api", clusteraccess.DefaultGVKMapper(), zerolog.Nop())

			c := &Client{dynamic: dyn, access: networkRouted(direct, agent, true)}

			err := c.DeleteVNet(context.Background(), "vnet-tenant-proj-app")
			if err == nil {
				t.Fatal("DeleteVNet returned nil error; the agent write failure must surface")
			}
			if got := sess.deleteCount(); got != 1 {
				t.Errorf("agent Delete called %d times, want 1", got)
			}
			if got := direct.deleteCount(); got != 0 {
				t.Errorf("direct Delete called %d times after agent error, want 0 (no fallback)", got)
			}
		})
	}
}

// ── Toggle ON + live session: routes the Vpc create to the agent ─────────────

func TestCreateVNet_ToggleOn_RoutesToAgentApply(t *testing.T) {
	dyn := newEmptyFakeDynamic()
	direct := &netCountingAccessor{}
	sess := &netStubSession{
		applyResult: agentgw.ApplyResult{
			APIVersion: "kubeovn.io/v1", Kind: "Vpc",
			Name: vpcName("app", "tenant", "proj"), UID: "uid-1", ResourceVersion: "7",
		},
	}
	agent := clusteraccess.NewAgentBacked(sess, "lk", "zone-1", "dc-api", clusteraccess.DefaultGVKMapper(), zerolog.Nop())

	c := &Client{dynamic: dyn, access: networkRouted(direct, agent, true)}

	res, err := c.CreateVNet(context.Background(), "tenant", "proj", sampleVNetSpec())
	if err != nil {
		t.Fatalf("CreateVNet error: %v", err)
	}
	// Vpc create is a server-side apply on the agent — its apply counter increments.
	if got := sess.applyCount(); got != 1 {
		t.Errorf("agent Apply called %d times, want 1", got)
	}
	if got := direct.createCount(); got != 0 {
		t.Errorf("direct Create called %d times with toggle ON, want 0", got)
	}
	if res.BackendUID != vpcName("app", "tenant", "proj") {
		t.Errorf("BackendUID = %q, want %q", res.BackendUID, vpcName("app", "tenant", "proj"))
	}
	// The agent path must NOT have written to the local fake dynamic store.
	if _, err := dyn.Resource(vpcGVR).Get(context.Background(), vpcName("app", "tenant", "proj"), metav1.GetOptions{}); err == nil {
		t.Error("Vpc unexpectedly present in the direct store — agent path should not touch it")
	}
}
