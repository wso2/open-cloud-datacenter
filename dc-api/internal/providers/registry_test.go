package providers

import (
	"context"
	"encoding/json"
	"testing"

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
func decisionRegistry(routeReads bool, gw agentgw.SessionResolver) *Registry {
	return &Registry{
		agentGateway: gw,
		routeReads:   routeReads,
		localRegion:  "lk",
		localZone:    "zone-1",
		log:          zerolog.Nop(),
	}
}

func cfgWith(routeReads bool) *config.Config {
	return &config.Config{LocalRegion: "lk", LocalZone: "zone-1", AgentRouteReads: routeReads}
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
	r := decisionRegistry(true, fakeResolver{connected: true})
	decide := r.agentDecision(cfgWith(true))

	if _, ok := decide(clusteraccess.VerbGet); !ok {
		t.Error("toggle on + live agent must route Get through the agent")
	}
	// Write verbs are NOT in the M-C allow-set — must stay Direct even with a
	// live agent and the read toggle on.
	for _, v := range []clusteraccess.Verb{clusteraccess.VerbList, clusteraccess.VerbCreate, clusteraccess.VerbApply, clusteraccess.VerbUpdate, clusteraccess.VerbDelete} {
		if _, ok := decide(v); ok {
			t.Errorf("verb %v must NOT route in M-C (read-only allow-set)", v)
		}
	}
}
