package providers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/wso2/dc-api/internal/agentgw"
	"github.com/wso2/dc-api/internal/config"
	"github.com/wso2/dc-api/internal/providers/clusteraccess"
)

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

// cfgWith builds a config with only the read toggle set (write toggle off).
func cfgWith(routeReads bool) *config.Config {
	return cfgWithToggles(routeReads, false)
}

// cfgWithToggles builds a config with both per-family agent-routing toggles set.
// agentDecision reads LocalRegion/LocalZone off cfg, so they are populated to
// match decisionRegistry's zone.
func cfgWithToggles(routeReads, routeWrites bool) *config.Config {
	return &config.Config{
		LocalRegion:      "lk",
		LocalZone:        "zone-1",
		AgentRouteReads:  routeReads,
		AgentRouteWrites: routeWrites,
	}
}

func TestAgentDecision_ToggleOff_AlwaysDirect(t *testing.T) {
	r := decisionRegistry(false, fakeResolver{connected: true})
	decide := r.agentDecision(cfgWith(false))
	if _, ok := decide(clusteraccess.VerbGet); ok {
		t.Error("toggle off must NOT route to the agent (Get)")
	}
}

func TestAgentDecision_NoGateway_AlwaysDirect(t *testing.T) {
	r := decisionRegistry(true, nil)
	decide := r.agentDecision(cfgWith(true))
	if _, ok := decide(clusteraccess.VerbGet); ok {
		t.Error("nil gateway must NOT route to the agent")
	}
}

func TestAgentDecision_ToggleOn_NoLiveAgent_Direct(t *testing.T) {
	r := decisionRegistry(true, fakeResolver{connected: false})
	decide := r.agentDecision(cfgWith(true))
	if _, ok := decide(clusteraccess.VerbGet); ok {
		t.Error("toggle on but no connected agent must fall back to Direct")
	}
}

func TestAgentDecision_ToggleOn_LiveAgent_RoutesGetOnly(t *testing.T) {
	// Reads-only configuration: read toggle on, WRITE toggle off.
	r := decisionRegistryToggles(true, false, fakeResolver{connected: true})
	decide := r.agentDecision(cfgWithToggles(true, false))

	if _, ok := decide(clusteraccess.VerbGet); !ok {
		t.Error("toggle on + live agent must route Get through the agent")
	}
	// With ONLY the read toggle on, no write verb may route — Create/Delete are
	// gated by the (off) write toggle; List/Apply/Update are not in any allow-set.
	for _, v := range []clusteraccess.Verb{clusteraccess.VerbList, clusteraccess.VerbCreate, clusteraccess.VerbApply, clusteraccess.VerbUpdate, clusteraccess.VerbDelete} {
		if _, ok := decide(v); ok {
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
	decide := r.agentDecision(cfgWithToggles(false, true))

	for _, v := range []clusteraccess.Verb{clusteraccess.VerbCreate, clusteraccess.VerbDelete} {
		if _, ok := decide(v); !ok {
			t.Errorf("write toggle on + live agent must route verb %v through the agent", v)
		}
	}
	// Read toggle is OFF here → Get must stay Direct.
	if _, ok := decide(clusteraccess.VerbGet); ok {
		t.Error("read toggle off must keep Get on Direct even when writes route")
	}
	// VerbApply/VerbUpdate are NOT in the write allow-set (create is VerbCreate, a
	// POST on Direct); they must never route. VerbList has no agent op.
	for _, v := range []clusteraccess.Verb{clusteraccess.VerbList, clusteraccess.VerbApply, clusteraccess.VerbUpdate} {
		if _, ok := decide(v); ok {
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
	decide := r.agentDecision(cfgWithToggles(true, false))

	for _, v := range []clusteraccess.Verb{clusteraccess.VerbCreate, clusteraccess.VerbDelete} {
		if _, ok := decide(v); ok {
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
	decide := r.agentDecision(cfgWithToggles(true, true))

	for _, v := range []clusteraccess.Verb{clusteraccess.VerbGet, clusteraccess.VerbCreate, clusteraccess.VerbDelete} {
		if _, ok := decide(v); ok {
			t.Errorf("no connected agent must keep verb %v on Direct even with the toggle on", v)
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
