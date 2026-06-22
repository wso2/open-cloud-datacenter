// Package providers — registry.go
//
// Registry resolves the provider set for a (region, zone) and decides, per call,
// whether that zone's provider READ ops go through the direct dynamic client or
// the zone's dc-agent command channel.
//
// ── M-C scope ─────────────────────────────────────────────────────────────────
// The registry holds exactly ONE zone — the local zone (cfg.LocalRegion /
// cfg.LocalZone) — built from the process-global DCAPI_* credentials, i.e. the
// same Direct providers main.go built before M-C. The compute provider for that
// zone is constructed with a clusteraccess.Routed accessor whose decision
// closure consults:
//
//  1. cfg.AgentRouteReads  (DCAPI_AGENT_ROUTE_READS; default false), AND
//  2. a non-nil agent gateway, AND
//  3. a LIVE agent session for the zone right now, AND
//  4. the verb being in the routed allow-set ({Get} for the read slice).
//
// If ANY is false the accessor uses Direct — today's behaviour, byte-identical.
// Condition 3 is the safety belt: even with the toggle on, a zone with no
// connected agent silently uses Direct, so flipping the env var can never strand
// the local zone if the agent is down.
//
// cluster/network providers in the set are the plain Direct ones for now —
// rancher is an HTTP Steve client (does not fit the Accessor seam) and network
// routing is a later phase (M-E).
package providers

import (
	"fmt"
	"sync"

	"github.com/rs/zerolog"

	"github.com/wso2/dc-api/internal/agentgw"
	"github.com/wso2/dc-api/internal/config"
	"github.com/wso2/dc-api/internal/providers/clusteraccess"
	"github.com/wso2/dc-api/internal/providers/harvester"
)

// ProviderSet is the trio of providers that serve one zone.
type ProviderSet struct {
	Compute ComputeProvider
	Cluster ClusterProvider
	Network NetworkProvider
}

// Registry resolves the provider set for a (region, zone). It mirrors
// handlers.Registry's map+RWMutex+zoneKey shape.
type Registry struct {
	mu  sync.RWMutex
	set map[string]*ProviderSet // key: region "/" zone

	// agentGateway resolves a zone's live agent Session. nil ⇒ the agent path is
	// disabled entirely (always the Direct seam).
	agentGateway agentgw.SessionResolver

	// routeReads gates the dark read slice (DCAPI_AGENT_ROUTE_READS).
	routeReads bool

	localRegion, localZone string
	log                    zerolog.Logger
}

func registryKey(region, zone string) string { return region + "/" + zone }

// NewRegistry builds the local-zone ProviderSet from cfg and wires its compute
// provider's cluster-access seam to the agent gateway behind the read toggle.
//
// agentGateway may be nil (agent path disabled). It is the handlers.Registry
// adapted to agentgw.SessionResolver (handlers.NewAgentGateway), constructed in
// main.go BEFORE providers so the same registry instance feeds both the WS
// handler and provider routing.
func NewRegistry(cfg *config.Config, agentGateway agentgw.SessionResolver, log zerolog.Logger) (*Registry, error) {
	r := &Registry{
		set:          make(map[string]*ProviderSet, 1),
		agentGateway: agentGateway,
		routeReads:   cfg.AgentRouteReads,
		localRegion:  cfg.LocalRegion,
		localZone:    cfg.LocalZone,
		log:          log,
	}

	// ── Compute provider (harvester) with the routed cluster-access seam ───────
	// Only the harvester path supports the M-C read slice. WithRoutedAccessor
	// builds the Routed accessor over the client's OWN dynamic client (the Direct
	// fallback), so the off-path and every not-yet-routed method share one client.
	var compute ComputeProvider
	switch cfg.VMProvider {
	case "harvester":
		hc, err := harvester.NewClient(cfg.HarvesterKubeconfig, cfg.HarvesterNamespace)
		if err != nil {
			return nil, fmt.Errorf("build harvester client for registry: %w", err)
		}
		decide := r.agentDecision(cfg)
		hc.WithRoutedAccessor(func(direct clusteraccess.Accessor) clusteraccess.Accessor {
			return clusteraccess.NewRouted(direct, decide)
		})
		compute = hc
	default:
		// Non-harvester compute providers don't have the seam yet — fall back to
		// the plain factory (Direct behaviour). Nothing routes through the agent.
		c, err := NewComputeProvider(cfg)
		if err != nil {
			return nil, err
		}
		compute = c
	}

	// ── Cluster + Network providers: plain Direct (unchanged) ──────────────────
	cluster, err := NewClusterProvider(cfg)
	if err != nil {
		return nil, err
	}
	network, err := NewNetworkProvider(cfg)
	if err != nil {
		return nil, err
	}

	r.set[registryKey(cfg.LocalRegion, cfg.LocalZone)] = &ProviderSet{
		Compute: compute,
		Cluster: cluster,
		Network: network,
	}

	if agentGateway != nil && cfg.AgentRouteReads {
		log.Info().
			Str("region", cfg.LocalRegion).Str("zone", cfg.LocalZone).
			Msg("provider registry: agent read-routing ENABLED (DCAPI_AGENT_ROUTE_READS=true) — routes only when a live agent is connected for the zone")
	} else {
		log.Info().
			Str("region", cfg.LocalRegion).Str("zone", cfg.LocalZone).
			Bool("toggle", cfg.AgentRouteReads).Bool("gateway", agentGateway != nil).
			Msg("provider registry: agent read-routing disabled — using the direct cluster path")
	}
	return r, nil
}

// For returns the cached provider set for a (region, zone). Only the local zone
// exists in M-C; any other zone is an error (no second zone is provisioned yet).
func (r *Registry) For(region, zone string) (*ProviderSet, error) {
	r.mu.RLock()
	set, ok := r.set[registryKey(region, zone)]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("no provider set for zone %s/%s (only the local zone %s/%s is configured)",
			region, zone, r.localRegion, r.localZone)
	}
	return set, nil
}

// agentDecision builds the live decision closure for the local zone's Routed
// accessor. It re-evaluates the toggle + live-session check + verb allow-set on
// EVERY call, so "toggle on / toggle off" and "agent connected / disconnected"
// are observable on the very next request with no provider reconstruction.
//
// The routed allow-set starts as {VerbGet} for the M-C read slice. Each write
// phase widens it verb-by-verb IN THE SAME PR as the matching dc-agent RBAC rule
// — never one without the other. Until a verb is added here it always returns
// (_, false) → Direct.
func (r *Registry) agentDecision(cfg *config.Config) clusteraccess.AgentDecision {
	mapper := clusteraccess.DefaultGVKMapper()
	fieldManager := "dc-api"
	region, zone := cfg.LocalRegion, cfg.LocalZone

	return func(verb clusteraccess.Verb) (clusteraccess.Accessor, bool) {
		// (1) read toggle, (2) gateway present.
		if !r.routeReads || r.agentGateway == nil {
			return nil, false
		}
		// (4) verb allow-set for the current phase: reads only (Get).
		if verb != clusteraccess.VerbGet {
			return nil, false
		}
		// (3) a live agent session must exist for the zone RIGHT NOW.
		sess, ok := r.agentGateway.Session(region, zone)
		if !ok {
			return nil, false
		}
		return clusteraccess.NewAgentBacked(sess, region, zone, fieldManager, mapper, r.log), true
	}
}
