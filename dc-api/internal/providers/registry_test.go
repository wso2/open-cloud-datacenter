package providers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/wso2/dc-api/internal/agentgw"
	"github.com/wso2/dc-api/internal/providers/clusteraccess"
)

// vmGVR is the onboarded VM family GVR — the decision closure is per-family
// (Part A), so the tests must pass a GVR. All the pre-existing VM-behaviour tests
// use this so they assert the SAME routing decisions as before Part A.
var vmGVR = schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}

// fakeResolver is an agentgw.SessionResolver that returns a fixed presence
// answer. The decision closure only checks the boolean (presence); it never
// calls the Session, but a non-nil one is returned so the contract holds.
type fakeResolver struct{ connected bool }

func (f fakeResolver) Session(region, zone string) (agentgw.Session, bool) {
	if !f.connected {
		return nil, false
	}
	return stubSession{}, true
}

// stubSession is a no-op agentgw.Session (the decision path never invokes it).
type stubSession struct{}

func (stubSession) Apply(context.Context, json.RawMessage, string, bool) (agentgw.ApplyResult, error) {
	return agentgw.ApplyResult{}, nil
}
func (stubSession) Delete(context.Context, agentgw.ResourceRef, string) (agentgw.DeleteResult, error) {
	return agentgw.DeleteResult{}, nil
}
func (stubSession) GetStatus(context.Context, agentgw.ResourceRef) (agentgw.StatusSnapshot, error) {
	return agentgw.StatusSnapshot{}, nil
}
func (stubSession) WatchStatus(context.Context, agentgw.ResourceRef, int, func(string, agentgw.StatusSnapshot)) (agentgw.WatchResult, error) {
	return agentgw.WatchResult{}, nil
}

// decisionRegistry builds a *Registry wired only with the fields agentDecision
// reads (no kubeconfig parsing), so we can unit-test the gating logic directly.
// It sets only the read toggle; tests that exercise write routing use
// decisionRegistryToggles to set both.
func decisionRegistry(routeReads bool, gw agentgw.SessionResolver) *Registry {
	return decisionRegistryToggles(routeReads, false, gw)
}

// decisionRegistryToggles is decisionRegistry with both per-family toggles
// (reads + writes) settable, so the write-routing cases can drive routeWrites
// independently of routeReads.
func decisionRegistryToggles(routeReads, routeWrites bool, gw agentgw.SessionResolver) *Registry {
	return &Registry{
		agentGateway: gw,
		routeReads:   routeReads,
		routeWrites:  routeWrites,
		localRegion:  "lk",
		localZone:    "zone-1",
		log:          zerolog.Nop(),
		noAgentWarn:  make(map[string]time.Time),
	}
}

// ─────────────────────── Per-resource resolver (For) tests ──────────────────
//
// These exercise the per-(region,zone) cache that backs per-resource routing:
// local-zone cache hits are pointer-identical (Mode A byte-identical), unknown
// zones fail clearly, a known remote zone builds an agent-only set whose ops
// fail clearly with no agent, and the local set is never rebuilt.

// fakeCatalog is a providers.ZoneCatalog whose known-set and error are fixed.
type fakeCatalog struct {
	known map[string]bool
	err   error
}

func (f fakeCatalog) IsKnownZone(_ context.Context, region, zone string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.known[region+"/"+zone], nil
}

// resolverRegistry builds a *Registry with a pre-seeded LOCAL set (no kubeconfig
// parsing), so the For() cache path can be tested directly. The local set's
// providers are nil — these tests assert pointers and errors, never call into a
// provider that needs a real backend.
func resolverRegistry(catalog ZoneCatalog, gw agentgw.SessionResolver) *Registry {
	r := &Registry{
		set:          make(map[string]*ProviderSet, 1),
		agentGateway: gw,
		catalog:      catalog,
		localRegion:  "lk",
		localZone:    "zone-1",
		log:          zerolog.Nop(),
		noAgentWarn:  make(map[string]time.Time),
	}
	r.set[registryKey("lk", "zone-1")] = &ProviderSet{} // the local set (sentinel)
	return r
}

func TestFor_LocalZone_CacheHit_PointerIdentical(t *testing.T) {
	r := resolverRegistry(nil, nil)
	a, err := r.For("lk", "zone-1")
	if err != nil {
		t.Fatalf("For(local) must succeed: %v", err)
	}
	b, err := r.For("lk", "zone-1")
	if err != nil {
		t.Fatalf("For(local) repeat must succeed: %v", err)
	}
	if a != b {
		t.Error("For(local) must return the SAME *ProviderSet pointer across calls (no rebuild)")
	}
	// Empty region/zone must resolve to the same local set (pre-stamp rows / Mode A).
	c, err := r.For("", "")
	if err != nil {
		t.Fatalf("For(empty) must resolve to the local zone: %v", err)
	}
	if c != a {
		t.Error("For(\"\",\"\") must resolve to the SAME local set as For(local)")
	}
}

func TestFor_LocalZone_NeverRebuilt_ConcurrentSamePointer(t *testing.T) {
	r := resolverRegistry(nil, nil)
	want, _ := r.For("lk", "zone-1")

	const n = 50
	got := make(chan *ProviderSet, n)
	for i := 0; i < n; i++ {
		go func() {
			set, err := r.For("lk", "zone-1")
			if err != nil {
				got <- nil
				return
			}
			got <- set
		}()
	}
	for i := 0; i < n; i++ {
		if set := <-got; set != want {
			t.Fatal("concurrent For(local) returned a different pointer — the local set must never be rebuilt")
		}
	}
}

func TestFor_UnknownZone_NoCatalog_ClearError(t *testing.T) {
	r := resolverRegistry(nil, nil) // no catalog ⇒ only the local zone resolves
	_, err := r.For("us", "zone-9")
	if err == nil {
		t.Fatal("an unknown zone with no catalog must NOT resolve")
	}
	// Must NOT have built or cached anything for the unknown zone.
	r.mu.RLock()
	_, cached := r.set[registryKey("us", "zone-9")]
	r.mu.RUnlock()
	if cached {
		t.Error("an unknown zone must never be cached")
	}
}

func TestFor_UnknownZone_WithCatalog_ClearError(t *testing.T) {
	cat := fakeCatalog{known: map[string]bool{"lk/zone-1": true}} // remote not known
	r := resolverRegistry(cat, nil)
	_, err := r.For("us", "zone-9")
	if err == nil {
		t.Fatal("a zone absent from the catalog must return a clear unknown-zone error")
	}
}

func TestFor_CatalogFailure_FailsClosed(t *testing.T) {
	cat := fakeCatalog{err: errCatalog}
	r := resolverRegistry(cat, nil)
	_, err := r.For("us", "zone-9")
	if err == nil {
		t.Fatal("a catalog lookup failure must fail CLOSED (unresolved), never fall back to local")
	}
	r.mu.RLock()
	_, cached := r.set[registryKey("us", "zone-9")]
	r.mu.RUnlock()
	if cached {
		t.Error("a failed catalog lookup must not cache a set")
	}
}

func TestFor_RemoteZone_BuildsAgentOnlySet_NoAgent_ClearError(t *testing.T) {
	// Remote zone is in the catalog, but no agent is connected.
	cat := fakeCatalog{known: map[string]bool{"lk/zone-1": true, "us/zone-9": true}}
	r := resolverRegistry(cat, fakeResolver{connected: false})

	set, err := r.For("us", "zone-9")
	if err != nil {
		t.Fatalf("a known remote zone must build an agent-only set: %v", err)
	}
	if set == nil || set.Compute == nil || set.Network == nil {
		t.Fatal("the remote set must have non-nil agent-only providers")
	}
	// The remote set must be cached and pointer-stable.
	set2, _ := r.For("us", "zone-9")
	if set2 != set {
		t.Error("the remote set must be cached (same pointer on the second call)")
	}
	// With NO agent connected, a remote-zone op must fail with a CLEAR error — it
	// must NOT silently hit the local cluster. The remote compute provider's
	// GetVM goes through the NoCreds fallback.
	if _, err := set.Compute.GetVM(context.Background(), "ns:vm"); err == nil {
		t.Error("a remote-zone op with no connected agent must return a clear error, not silently succeed")
	}
}

var errCatalog = errCatalogT{}

type errCatalogT struct{}

func (errCatalogT) Error() string { return "catalog unreachable" }

func TestFixedResolver_AlwaysSameSet(t *testing.T) {
	set := &ProviderSet{}
	f := &FixedResolver{set: set}
	a, err := f.For("lk", "zone-1")
	if err != nil {
		t.Fatalf("FixedResolver.For must not error: %v", err)
	}
	b, _ := f.For("us", "zone-99") // any zone → the one fixed set (single-zone model)
	if a != set || b != set {
		t.Error("FixedResolver must return the SAME set for every zone")
	}
}

func TestAgentDecision_ToggleOff_AlwaysDirect(t *testing.T) {
	r := decisionRegistry(false, fakeResolver{connected: true})
	decide := r.agentDecision("lk", "zone-1")
	if _, ok := decide(clusteraccess.VerbGet, vmGVR); ok {
		t.Error("toggle off must NOT route to the agent (Get)")
	}
}

func TestAgentDecision_NoGateway_AlwaysDirect(t *testing.T) {
	r := decisionRegistry(true, nil)
	decide := r.agentDecision("lk", "zone-1")
	if _, ok := decide(clusteraccess.VerbGet, vmGVR); ok {
		t.Error("nil gateway must NOT route to the agent")
	}
}

func TestAgentDecision_ToggleOn_NoLiveAgent_Direct(t *testing.T) {
	r := decisionRegistry(true, fakeResolver{connected: false})
	decide := r.agentDecision("lk", "zone-1")
	if _, ok := decide(clusteraccess.VerbGet, vmGVR); ok {
		t.Error("toggle on but no connected agent must fall back to Direct")
	}
}

func TestAgentDecision_ToggleOn_LiveAgent_RoutesGetOnly(t *testing.T) {
	// Reads-only configuration: read toggle on, WRITE toggle off.
	r := decisionRegistryToggles(true, false, fakeResolver{connected: true})
	decide := r.agentDecision("lk", "zone-1")

	if _, ok := decide(clusteraccess.VerbGet, vmGVR); !ok {
		t.Error("toggle on + live agent must route Get through the agent")
	}
	// With ONLY the read toggle on, no write verb may route — Create/Delete are
	// gated by the (off) write toggle; List/Apply/Update are not in any allow-set.
	for _, v := range []clusteraccess.Verb{clusteraccess.VerbList, clusteraccess.VerbCreate, clusteraccess.VerbApply, clusteraccess.VerbUpdate, clusteraccess.VerbDelete} {
		if _, ok := decide(v, vmGVR); ok {
			t.Errorf("verb %v must NOT route with only the read toggle on", v)
		}
	}
}

// TestAgentDecision_WritesOn_LiveAgent_RoutesCreateAndDelete asserts the write
// allow-set: with the write toggle on + a live agent, VerbCreate AND VerbDelete
// route to the agent. Get follows the read toggle (off here → Direct), and the
// non-allow-set verbs (List/Apply/Update) never route.
func TestAgentDecision_WritesOn_LiveAgent_RoutesCreateAndDelete(t *testing.T) {
	// writes on, reads off — proves the toggles are independent.
	r := decisionRegistryToggles(false, true, fakeResolver{connected: true})
	decide := r.agentDecision("lk", "zone-1")

	for _, v := range []clusteraccess.Verb{clusteraccess.VerbCreate, clusteraccess.VerbDelete} {
		if _, ok := decide(v, vmGVR); !ok {
			t.Errorf("write toggle on + live agent must route verb %v through the agent", v)
		}
	}
	// Read toggle is OFF here → Get must stay Direct.
	if _, ok := decide(clusteraccess.VerbGet, vmGVR); ok {
		t.Error("read toggle off must keep Get on Direct even when writes route")
	}
	// VerbApply/VerbUpdate are NOT in the write allow-set (create is VerbCreate, a
	// POST on Direct); they must never route. VerbList has no agent op.
	for _, v := range []clusteraccess.Verb{clusteraccess.VerbList, clusteraccess.VerbApply, clusteraccess.VerbUpdate} {
		if _, ok := decide(v, vmGVR); ok {
			t.Errorf("verb %v is not in the write allow-set and must NOT route", v)
		}
	}
}

// TestAgentDecision_WritesOff_CreateDeleteStayDirect asserts that with the write
// toggle OFF, VerbCreate and VerbDelete fall through to Direct — even with a live
// agent and the read toggle on. This is the load-bearing guard for "toggle-OFF
// create stays the byte-identical POST on Direct".
func TestAgentDecision_WritesOff_CreateDeleteStayDirect(t *testing.T) {
	// reads on, writes off.
	r := decisionRegistryToggles(true, false, fakeResolver{connected: true})
	decide := r.agentDecision("lk", "zone-1")

	for _, v := range []clusteraccess.Verb{clusteraccess.VerbCreate, clusteraccess.VerbDelete} {
		if _, ok := decide(v, vmGVR); ok {
			t.Errorf("write toggle off must keep verb %v on Direct", v)
		}
	}
}

// TestAgentDecision_WritesOn_NoLiveAgent_StayDirect asserts the safety belt for
// writes: even with the write toggle on, a zone with no connected agent never
// routes a write — Create/Delete fall through to Direct. Flipping the env var can
// never strand the zone when the agent is down.
func TestAgentDecision_WritesOn_NoLiveAgent_StayDirect(t *testing.T) {
	r := decisionRegistryToggles(true, true, fakeResolver{connected: false})
	decide := r.agentDecision("lk", "zone-1")

	for _, v := range []clusteraccess.Verb{clusteraccess.VerbGet, clusteraccess.VerbCreate, clusteraccess.VerbDelete} {
		if _, ok := decide(v, vmGVR); ok {
			t.Errorf("no connected agent must keep verb %v on Direct even with the toggle on", v)
		}
	}
}

// TestAgentDecision_PerFamily_UnonboardedGVRStaysDirect is the Part A
// no-over-permit guard. With BOTH toggles on AND a live agent, a GVR that is NOT
// an onboarded routable family (here virtualmachineimages — GVK-mapped but with
// no RouteVerbs) must NEVER route, for ANY verb. This proves the decision is
// per-family: a verb that routes for VMs (Get/Create/Delete) does not leak to a
// family that didn't declare it. Before Part A the verb-only union would have
// matched, so this is exactly the over-permit Part A closes.
func TestAgentDecision_PerFamily_UnonboardedGVRStaysDirect(t *testing.T) {
	r := decisionRegistryToggles(true, true, fakeResolver{connected: true})
	decide := r.agentDecision("lk", "zone-1")

	imgGVR := schema.GroupVersionResource{Group: "harvesterhci.io", Version: "v1beta1", Resource: "virtualmachineimages"}
	for _, v := range []clusteraccess.Verb{clusteraccess.VerbGet, clusteraccess.VerbCreate, clusteraccess.VerbDelete} {
		if _, ok := decide(v, imgGVR); ok {
			t.Errorf("verb %v on an unonboarded family (virtualmachineimages) must NOT route, even with toggles+agent on", v)
		}
	}
	// Sanity: the SAME verbs DO route for the onboarded VM family under identical
	// toggles+agent — proving the gate is per-GVR, not a blanket on/off.
	for _, v := range []clusteraccess.Verb{clusteraccess.VerbGet, clusteraccess.VerbCreate, clusteraccess.VerbDelete} {
		if _, ok := decide(v, vmGVR); !ok {
			t.Errorf("verb %v on the onboarded VM family must route under toggles+agent on", v)
		}
	}
}

// TestWarnNoAgent_RateLimited proves the routing-time no-agent warning is
// time-gated per zone: the FIRST call records a timestamp, an immediate SECOND
// call within the window does not refresh it, and a call after the window
// elapses warns again (refreshing the timestamp). This is what stops a routed
// verb hammered at request rate from flooding the log.
func TestWarnNoAgent_RateLimited(t *testing.T) {
	r := decisionRegistryToggles(true, true, fakeResolver{connected: false})

	// First warn records a timestamp for the zone.
	r.warnNoAgent("lk", "zone-1")
	first, ok := r.noAgentWarn[registryKey("lk", "zone-1")]
	if !ok {
		t.Fatal("first warnNoAgent must record a timestamp for the zone")
	}

	// Second warn inside the window must NOT refresh the timestamp.
	r.warnNoAgent("lk", "zone-1")
	if got := r.noAgentWarn[registryKey("lk", "zone-1")]; !got.Equal(first) {
		t.Errorf("warn inside the rate-limit window must not refresh the timestamp: %v != %v", got, first)
	}

	// Simulate the window having elapsed; the next warn must refresh.
	r.noAgentMu.Lock()
	r.noAgentWarn[registryKey("lk", "zone-1")] = time.Now().Add(-2 * noAgentWarnEvery)
	r.noAgentMu.Unlock()
	r.warnNoAgent("lk", "zone-1")
	if got := r.noAgentWarn[registryKey("lk", "zone-1")]; !got.After(first) {
		t.Error("warn after the rate-limit window must refresh the timestamp")
	}
}

// TestWarnNoAgent_PerZoneIndependent proves the gate is keyed per zone: warning
// for one zone does not suppress a first warning for a different zone.
func TestWarnNoAgent_PerZoneIndependent(t *testing.T) {
	r := decisionRegistryToggles(true, true, fakeResolver{connected: false})
	r.warnNoAgent("lk", "zone-1")
	r.warnNoAgent("lk", "zone-2")
	if _, ok := r.noAgentWarn[registryKey("lk", "zone-2")]; !ok {
		t.Error("a second zone must get its own first warning (independent gate)")
	}
}
