// Package providers — registry.go
//
// Registry resolves the provider set for a (region, zone) and decides, per call,
// whether that zone's provider ops go through the direct dynamic client or the
// zone's dc-agent command channel.
//
// ── M-C scope ─────────────────────────────────────────────────────────────────
// The registry holds exactly ONE zone — the local zone (cfg.LocalRegion /
// cfg.LocalZone) — built from the process-global DCAPI_* credentials, i.e. the
// same Direct providers main.go built before M-C. The compute provider for that
// zone is constructed with a clusteraccess.Routed accessor whose decision
// closure consults:
//
//  1. the per-family toggle for the verb — AgentRouteReads (DCAPI_AGENT_ROUTE_READS,
//     default false) for reads, AgentRouteWrites (DCAPI_AGENT_ROUTE_WRITES, default
//     false) for the VM write slice — AND
//  2. a non-nil agent gateway, AND
//  3. a LIVE agent session for the zone right now, AND
//  4. the verb being in the routed allow-set ({Get} for reads; {Create, Delete}
//     for the VM create/delete write slice).
//
// If ANY is false the accessor uses Direct — today's behaviour, byte-identical.
// Condition 3 is the safety belt: even with a toggle on, a zone with no
// connected agent silently uses Direct, so flipping the env var can never strand
// the local zone if the agent is down. The toggles are independent: reads can
// route while writes stay on the direct path.
//
// cluster/network providers in the set are the plain Direct ones for now —
// rancher is an HTTP Steve client (does not fit the Accessor seam) and network
// routing is a later phase (M-E).
package providers

import (
	"fmt"
	"sync"
	"time"

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

	// routeWrites gates the write slice (DCAPI_AGENT_ROUTE_WRITES): VM
	// create (SSA apply) + delete first. Independent of routeReads.
	routeWrites bool

	localRegion, localZone string
	log                    zerolog.Logger

	// noAgentWarn rate-limits the routing-time "toggle on but no agent connected"
	// warning to at most once per zone per noAgentWarnEvery, so a routed verb
	// hammered at request rate doesn't flood the log. Keyed by registryKey.
	noAgentMu   sync.Mutex
	noAgentWarn map[string]time.Time
}

// noAgentWarnEvery bounds how often the routing-time no-agent warning fires per
// zone. Long enough to avoid per-request floods, short enough that an operator
// watching logs sees it promptly after flipping a toggle on with no agent up.
const noAgentWarnEvery = 30 * time.Second

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
		routeWrites:  cfg.AgentRouteWrites,
		localRegion:  cfg.LocalRegion,
		localZone:    cfg.LocalZone,
		log:          log,
		noAgentWarn:  make(map[string]time.Time),
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

	// One explicit "which zone do I route to" line at boot. This is the routing
	// TARGET (cfg.LocalRegion/LocalZone) — the zone whose token an agent must be
	// minted for to receive routed traffic. Surfacing it here makes the
	// colombo-vs-zone-1 class of misconfig visible at startup rather than as a
	// silent no-route at request time.
	log.Info().
		Str("route_region", cfg.LocalRegion).Str("route_zone", cfg.LocalZone).
		Bool("reads", cfg.AgentRouteReads).Bool("writes", cfg.AgentRouteWrites).
		Bool("gateway", agentGateway != nil).
		Msg("agent routing config: routed traffic targets this region/zone; an agent must present a token minted for it (else routing warns and uses the direct path)")

	// One log line covering BOTH toggles so an operator can tell from the logs
	// whether reads and/or writes route. Each only ever engages when a live agent
	// is connected for the zone; with the gateway absent both are forced off.
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
	if agentGateway != nil && cfg.AgentRouteWrites {
		log.Info().
			Str("region", cfg.LocalRegion).Str("zone", cfg.LocalZone).
			Msg("provider registry: agent write-routing ENABLED (DCAPI_AGENT_ROUTE_WRITES=true) — VM create/delete route only when a live agent is connected for the zone and its RBAC permits the verb")
	} else {
		log.Info().
			Str("region", cfg.LocalRegion).Str("zone", cfg.LocalZone).
			Bool("toggle", cfg.AgentRouteWrites).Bool("gateway", agentGateway != nil).
			Msg("provider registry: agent write-routing disabled — VM writes use the direct cluster path")
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
// accessor. It re-evaluates the toggles + live-session check + verb allow-set on
// EVERY call, so "toggle on / toggle off" and "agent connected / disconnected"
// are observable on the very next request with no provider reconstruction.
//
// The routed allow-set is gated per family: reads (VerbGet) by AgentRouteReads,
// writes (VerbCreate, VerbDelete — the VM create/delete slice) by AgentRouteWrites.
// Each verb is added here IN THE SAME PR as the matching dc-agent RBAC rule —
// never one without the other. Any verb not listed always returns (_, false) →
// Direct.
//
// NOTE: the write allow-set uses VerbCreate (not VerbApply) because CreateVM
// calls c.access.Create(...). On the Direct path that is a byte-identical POST;
// only the AgentBacked path turns VerbCreate into a server-side apply, since SSA
// is the agent's only create mechanism. VerbApply/VerbUpdate stay out until a
// later phase needs them.
func (r *Registry) agentDecision(cfg *config.Config) clusteraccess.AgentDecision {
	mapper := clusteraccess.DefaultGVKMapper()
	fieldManager := "dc-api"
	region, zone := cfg.LocalRegion, cfg.LocalZone

	// routable is the union of every capability's RouteVerbs — the registry-
	// derived membership test that REPLACES the old hand-written switch. With one
	// onboarded family today it equals {VerbGet, VerbCreate, VerbDelete}, exactly
	// the pre-refactor allow-set, so decisions are byte-identical. The reads vs
	// writes split (which toggle gates which verb) stays here.
	routable := clusteraccess.UnionRouteVerbs()

	return func(verb clusteraccess.Verb) (clusteraccess.Accessor, bool) {
		// (2) gateway present — shared precondition for any routing.
		if r.agentGateway == nil {
			return nil, false
		}
		// (1)+(4) registry membership AND per-family toggle, together.
		//   reads  → AgentRouteReads  gates read verbs  (VerbGet today)
		//   writes → AgentRouteWrites gates write verbs (VerbCreate, VerbDelete today)
		// A verb not in the registry's routable union always falls through to Direct.
		if !routable[verb] {
			return nil, false
		}
		switch {
		case clusteraccess.IsReadVerb(verb):
			if !r.routeReads {
				return nil, false
			}
		case clusteraccess.IsWriteVerb(verb):
			if !r.routeWrites {
				return nil, false
			}
		default:
			return nil, false
		}
		// (3) a live agent session must exist for the zone RIGHT NOW.
		sess, ok := r.agentGateway.Session(region, zone)
		if !ok {
			// The toggle for this verb is ON (we passed the checks above) but no
			// agent is connected for the routing zone. Direct is still the correct,
			// byte-identical safety belt — but this used to be SILENT, which is
			// exactly how the colombo-vs-zone-1 misconfig hid. Make it observable
			// without flooding: warn at most once per zone per noAgentWarnEvery.
			r.warnNoAgent(region, zone)
			return nil, false
		}
		return clusteraccess.NewAgentBacked(sess, region, zone, fieldManager, mapper, r.log), true
	}
}

// warnNoAgent emits a rate-limited WARNING when a routed verb's toggle is on but
// no agent is connected for the zone dc-api routes to. It fires at most once per
// zone per noAgentWarnEvery so a routed call hammered at request rate cannot
// flood the log. The caller still returns Direct — this is observability only,
// the byte-identical fallthrough behaviour is unchanged.
func (r *Registry) warnNoAgent(region, zone string) {
	key := registryKey(region, zone)
	now := time.Now()
	r.noAgentMu.Lock()
	last, seen := r.noAgentWarn[key]
	if seen && now.Sub(last) < noAgentWarnEvery {
		r.noAgentMu.Unlock()
		return
	}
	r.noAgentWarn[key] = now
	r.noAgentMu.Unlock()

	r.log.Warn().
		Str("region", region).Str("zone", zone).
		Msg("agent routing enabled for this region/zone but no agent is connected; using the direct cluster path — mint and connect an agent for this zone, or the routed traffic stays direct")
}
