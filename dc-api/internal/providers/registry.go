// Package providers — registry.go
//
// Registry resolves the provider set for a (region, zone) and decides, per call,
// whether that zone's provider ops go through the direct dynamic client or the
// zone's dc-agent command channel.
//
// ── Per-resource zone routing ───────────────────────────────────────────────
// The Registry is the per-(region,zone) ProviderSet cache AND the live per-call
// agent-routing decision. Handlers and the reconciler hold the Registry as a
// providers.Resolver and call For(resource.Region, resource.Zone) per resource,
// so a resource in another zone reaches that zone's agent — instead of one fixed
// provider set keyed to DCAPI_LOCAL_ZONE.
//
//  - LOCAL zone — built eagerly in NewRegistry from cfg (the direct Harvester
//    kubeconfig + Rancher token), wrapped with the Routed accessor. This is the
//    SAME set dc-api injected before this change, so single-cluster / single-zone
//    deployments are byte-identical: every resource is stamped with the local
//    region/zone, every For(local) is a cache HIT on this one set, and the lazy
//    build-on-miss path is never taken.
//
//  - REMOTE zone — built lazily on first For(region,zone) miss for a zone that
//    is in the regions/zones catalog but is NOT the local zone. dc-api holds NO
//    kubeconfig there, so the remote set's Compute/Network providers are agent-
//    only (a Routed accessor whose Direct fallback is a clusteraccess.NoCreds
//    that errors clearly — never a silent fallback to the LOCAL cluster). The
//    Cluster provider is the SAME global Rancher client every zone shares (see
//    rancherScope in the design: Rancher is one global control plane, not per-DC).
//
//  - UNKNOWN zone — neither the local zone nor a catalog zone → For returns a
//    clear "unknown zone" error and never builds or caches anything.
//
// The agent decision closure is bound PER zone (region/zone captured in
// buildSet), so remote traffic routes to the remote zone's agent session, never
// the local one. With the toggles off (default) every op is the byte-identical
// Direct (local) path; remote ops fail closed until an agent connects.
package providers

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/wso2/dc-api/internal/agentgw"
	"github.com/wso2/dc-api/internal/config"
	"github.com/wso2/dc-api/internal/providers/clusteraccess"
	"github.com/wso2/dc-api/internal/providers/harvester"
	"github.com/wso2/dc-api/internal/providers/kubeovn"
)

// ProviderSet is the trio of providers that serve one zone.
type ProviderSet struct {
	Compute ComputeProvider
	Cluster ClusterProvider
	Network NetworkProvider
}

// Resolver is the per-resource provider-set lookup the handlers and the
// reconciler depend on. *Registry satisfies it. The signature is deliberately
// the same For(region,zone) the Registry already exposed, so the per-zone cache,
// the agent-decision closure, and the no-agent warn path are reused with zero
// new machinery — only the injection point moved from "once at boot" to "per
// request / per resource".
type Resolver interface {
	For(region, zone string) (*ProviderSet, error)
}

// FixedResolver is a Resolver that returns the SAME ProviderSet for every
// (region, zone). It exists for callers that build the handler graph from three
// fixed providers without a full Registry — the contract-test harness and unit
// tests that nop the backends, and as the single-cluster fallback in NewRouter
// when no Registry is injected. For these callers there is exactly one (implicit
// local) zone, so resolving any zone to the one set is correct and behavior-
// preserving: it is literally the pre-routing "one fixed provider set" model.
type FixedResolver struct{ set *ProviderSet }

// NewFixedResolver wraps three fixed providers into a Resolver. Every For(...)
// call returns the same set — appropriate only when there is a single zone.
func NewFixedResolver(compute ComputeProvider, cluster ClusterProvider, network NetworkProvider) *FixedResolver {
	return &FixedResolver{set: &ProviderSet{Compute: compute, Cluster: cluster, Network: network}}
}

// For returns the single fixed set regardless of region/zone.
func (f *FixedResolver) For(_, _ string) (*ProviderSet, error) { return f.set, nil }

// ZoneCatalog answers "is this a registered remote zone?" for the build-on-miss
// path. It is consulted OUTSIDE the Registry's write lock (so a slow DB query
// can't serialize unrelated zone lookups) and MUST fail closed: when the catalog
// can't be reached the zone is treated as unknown, never as the local zone.
//
// The db.Repository satisfies this via its regions/zones catalog. A nil catalog
// (router-only tests, the single-cluster default that never asks for a non-local
// zone) means only the local zone ever resolves — any other zone is "unknown".
type ZoneCatalog interface {
	// IsKnownZone reports whether (region, zone) exists in the regions/zones
	// catalog. err is non-nil only on a catalog lookup failure; callers treat a
	// failure as "not known" (fail closed).
	IsKnownZone(ctx context.Context, region, zone string) (bool, error)
}

// Registry resolves the provider set for a (region, zone). It mirrors
// handlers.Registry's map+RWMutex+zoneKey shape.
type Registry struct {
	mu  sync.RWMutex
	set map[string]*ProviderSet // key: region "/" zone

	// agentGateway resolves a zone's live agent Session. nil ⇒ the agent path is
	// disabled entirely (always the Direct seam).
	agentGateway agentgw.SessionResolver

	// catalog gates build-on-miss for non-local zones (fail closed). nil ⇒ only
	// the local zone resolves (the single-cluster default).
	catalog ZoneCatalog

	// cluster is the GLOBAL Rancher cluster provider, built once in NewRegistry
	// and shared by EVERY ProviderSet (local and remote). Rancher is one
	// management control plane across the fleet — there is no per-zone cluster
	// client and no agent seam for it. buildSet never reconstructs it.
	cluster ClusterProvider

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
// and network providers' cluster-access seam to the agent gateway behind the
// per-family toggles. The global Rancher cluster provider is built once here and
// shared by every zone.
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

	// ── Cluster provider: GLOBAL, built once, shared by every zone. Plain Direct
	// (rancher is an HTTP Steve client that does not fit the Accessor seam). ─────
	cluster, err := NewClusterProvider(cfg)
	if err != nil {
		return nil, err
	}
	r.cluster = cluster

	// ── Local-zone set (direct Harvester/KubeOVN, routed seam) ─────────────────
	localSet, err := r.buildLocalSet(cfg)
	if err != nil {
		return nil, err
	}
	r.set[registryKey(cfg.LocalRegion, cfg.LocalZone)] = localSet

	// One explicit "which zone do I route to" line at boot. This is the LOCAL
	// routing target; per-resource resolution now routes other zones to their own
	// agents. Surfacing it here makes the colombo-vs-zone-1 class of misconfig
	// visible at startup rather than as a silent no-route at request time.
	log.Info().
		Str("route_region", cfg.LocalRegion).Str("route_zone", cfg.LocalZone).
		Bool("reads", cfg.AgentRouteReads).Bool("writes", cfg.AgentRouteWrites).
		Bool("gateway", agentGateway != nil).
		Msg("agent routing config: the LOCAL region/zone uses the direct kubeconfig (plus the local agent when routing toggles are on); remote zones route to their own agent")

	if agentGateway != nil && cfg.AgentRouteReads {
		log.Info().
			Str("region", cfg.LocalRegion).Str("zone", cfg.LocalZone).
			Msg("provider registry: agent read-routing ENABLED (DCAPI_AGENT_ROUTE_READS=true) — routes the onboarded read families (VM + network CRD reads) only when a live agent is connected for the zone")
	} else {
		log.Info().
			Str("region", cfg.LocalRegion).Str("zone", cfg.LocalZone).
			Bool("toggle", cfg.AgentRouteReads).Bool("gateway", agentGateway != nil).
			Msg("provider registry: agent read-routing disabled — using the direct cluster path")
	}
	if agentGateway != nil && cfg.AgentRouteWrites {
		log.Info().
			Str("region", cfg.LocalRegion).Str("zone", cfg.LocalZone).
			Msg("provider registry: agent write-routing ENABLED (DCAPI_AGENT_ROUTE_WRITES=true) — the onboarded write families (VM create/delete + network CRD create/delete: Vpc/Subnet/NAD) route only when a live agent is connected for the zone and its RBAC permits the verb")
	} else {
		log.Info().
			Str("region", cfg.LocalRegion).Str("zone", cfg.LocalZone).
			Bool("toggle", cfg.AgentRouteWrites).Bool("gateway", agentGateway != nil).
			Msg("provider registry: agent write-routing disabled — VM and network writes use the direct cluster path")
	}
	return r, nil
}

// WithZoneCatalog injects the regions/zones catalog used to gate build-on-miss
// for remote zones (fail closed). Returns the same *Registry for chaining. Wired
// in main.go after the repo exists. Without it, only the local zone resolves.
func (r *Registry) WithZoneCatalog(c ZoneCatalog) *Registry {
	r.catalog = c
	return r
}

// buildLocalSet builds the LOCAL zone's ProviderSet from cfg: the direct
// Harvester compute provider and direct KubeOVN network provider, each wrapped
// with a Routed accessor whose decision is bound to the LOCAL region/zone. This
// is the exact construction dc-api used before per-resource routing, so the
// local set is byte-identical.
func (r *Registry) buildLocalSet(cfg *config.Config) (*ProviderSet, error) {
	// The decision closure for the LOCAL zone, bound to the local region/zone.
	decide := r.agentDecision(cfg.LocalRegion, cfg.LocalZone)

	// ── Compute provider (harvester) with the routed cluster-access seam ───────
	var compute ComputeProvider
	switch cfg.VMProvider {
	case "harvester":
		hc, err := harvester.NewClient(cfg.HarvesterKubeconfig, cfg.HarvesterNamespace)
		if err != nil {
			return nil, fmt.Errorf("build harvester client for registry: %w", err)
		}
		hc.WithRoutedAccessor(func(direct clusteraccess.Accessor) clusteraccess.Accessor {
			return clusteraccess.NewRouted(direct, decide)
		})
		compute = hc
	default:
		c, err := NewComputeProvider(cfg)
		if err != nil {
			return nil, err
		}
		compute = c
	}

	// ── Network provider (kubeovn) with the routed cluster-access seam ─────────
	var network NetworkProvider
	switch cfg.NetworkProvider {
	case "kubeovn":
		kc, err := kubeovn.New(cfg.HarvesterKubeconfig, cfg.KubeOVNNamespace)
		if err != nil {
			return nil, fmt.Errorf("build kubeovn client for registry: %w", err)
		}
		kc.WithRoutedAccessor(func(direct clusteraccess.Accessor) clusteraccess.Accessor {
			return clusteraccess.NewRouted(direct, decide)
		})
		network = kc
	default:
		n, err := NewNetworkProvider(cfg)
		if err != nil {
			return nil, err
		}
		network = n
	}

	return &ProviderSet{
		Compute: compute,
		Cluster: r.cluster,
		Network: network,
	}, nil
}

// buildRemoteSet builds a REMOTE zone's agent-only ProviderSet. dc-api holds NO
// kubeconfig for the zone, so:
//
//   - Compute/Network are credential-free clients (harvester.NewRemoteClient /
//     kubeovn.NewRemoteClient). The harvester client routes the VM-object CRUD
//     lifecycle through a Routed accessor bound to THIS zone, whose Direct
//     fallback is a clusteraccess.NoCreds — so a missing agent yields a clear
//     "no agent connected for zone" error, never a silent hit on the LOCAL
//     cluster. The kubeovn remote client defers networking behind a clear
//     local-only error (the network plumbing has no agent path yet).
//   - Cluster is the SAME global Rancher client every zone shares.
//
// buildRemoteSet performs NO network I/O (no kubeconfig to dial), so it is safe
// to run under the Registry write lock without serializing unrelated lookups.
func (r *Registry) buildRemoteSet(region, zone string) *ProviderSet {
	decide := r.agentDecision(region, zone)
	// Agent-only accessor: route via the agent, fall back to NoCreds (clear error)
	// — NEVER to the local direct path.
	remoteAccessor := clusteraccess.NewRouted(clusteraccess.NewNoCreds(region, zone), decide)

	return &ProviderSet{
		Compute: harvester.NewRemoteClient(remoteAccessor, region, zone),
		Cluster: r.cluster, // global Rancher
		Network: kubeovn.NewRemoteClient(region, zone),
	}
}

// For returns the cached provider set for a (region, zone), building it lazily
// on a miss.
//
// Fast path (the single-cluster / single-zone case and every repeated lookup):
// an RLock read of the map. The LOCAL zone is always present (built eagerly in
// NewRegistry), so For(local) is a pure cache hit returning the SAME *ProviderSet
// pointer every time — byte-identical to today's fixed providers.
//
// Build-on-miss (multi-zone): empty region/zone (pre-stamp rows) resolve to the
// local zone. Otherwise the catalog is consulted OUTSIDE the lock (fail closed):
//   - not a known zone → clear "unknown zone" error, nothing built or cached;
//   - a known REMOTE zone → build the agent-only set once under the write lock
//     (double-checked), cache it, and return it. buildRemoteSet does no network
//     I/O, so the lock is held only briefly.
//
// A failed catalog lookup is treated as unknown — never a fallback to the local
// cluster (the silent-wrong-cluster failure mode).
func (r *Registry) For(region, zone string) (*ProviderSet, error) {
	// Empty region/zone (pre-stamp rows, or callers that don't carry a zone)
	// resolve to the local zone — that is where those resources actually live.
	if region == "" {
		region = r.localRegion
	}
	if zone == "" {
		zone = r.localZone
	}
	key := registryKey(region, zone)

	// ── Fast path: cache hit (local-first; covers Mode A entirely). ────────────
	r.mu.RLock()
	set, ok := r.set[key]
	r.mu.RUnlock()
	if ok {
		return set, nil
	}

	// The local zone is always cached, so a miss here is a non-local zone. It
	// must be a registered remote zone or it does not resolve. Consult the
	// catalog OUTSIDE the lock (fail closed).
	if r.catalog == nil {
		return nil, fmt.Errorf(
			"no provider set for zone %s/%s: not the local zone %s/%s and no zone catalog is configured",
			region, zone, r.localRegion, r.localZone)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	known, err := r.catalog.IsKnownZone(ctx, region, zone)
	if err != nil {
		// Fail closed: a catalog failure must NOT open a fallback to the local
		// cluster. Surface it as unresolved so the handler/reconciler retries.
		return nil, fmt.Errorf("resolve zone %s/%s: zone catalog lookup failed (failing closed, not falling back to the local cluster): %w", region, zone, err)
	}
	if !known {
		return nil, fmt.Errorf(
			"unknown zone %s/%s: not the local zone %s/%s and not a registered remote zone",
			region, zone, r.localRegion, r.localZone)
	}

	// ── Build-on-miss for a known remote zone (double-checked under the lock). ──
	r.mu.Lock()
	defer r.mu.Unlock()
	if set, ok := r.set[key]; ok {
		// Another goroutine built it between our RUnlock and Lock.
		return set, nil
	}
	set = r.buildRemoteSet(region, zone)
	r.set[key] = set
	r.log.Info().Str("region", region).Str("zone", zone).
		Msg("provider registry: built agent-only provider set for remote zone (no direct credentials; ops route to the zone's agent or fail clearly)")
	return set, nil
}

// agentDecision builds the live decision closure for the given zone's Routed
// accessor. It re-evaluates the toggles + live-session check + verb allow-set on
// EVERY call, so "toggle on / toggle off" and "agent connected / disconnected"
// are observable on the very next request with no provider reconstruction.
//
// region/zone are bound to THIS zone (not cfg.Local*), so the live-session check
// and the no-agent warning target the correct zone — this is what lets a remote
// resource route to its OWN agent session rather than the local one.
//
// The routed allow-set is gated per family via clusteraccess.RoutableVerbs(gvr)
// (the capability registry's RouteVerbs for that GVR), then split reads (VerbGet)
// by AgentRouteReads, writes (VerbCreate, VerbDelete) by AgentRouteWrites.
func (r *Registry) agentDecision(region, zone string) clusteraccess.AgentDecision {
	mapper := clusteraccess.DefaultGVKMapper()
	fieldManager := "dc-api"

	return func(verb clusteraccess.Verb, gvr schema.GroupVersionResource) (clusteraccess.Accessor, bool) {
		// (2) gateway present — shared precondition for any routing.
		if r.agentGateway == nil {
			return nil, false
		}
		// (1)+(4) PER-FAMILY registry membership AND per-family toggle, together.
		routable, ok := clusteraccess.RoutableVerbs(gvr)
		if !ok || !routable[verb] {
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
		// (3) a live agent session must exist for THIS zone RIGHT NOW.
		sess, ok := r.agentGateway.Session(region, zone)
		if !ok {
			// The toggle for this verb is ON but no agent is connected for the
			// zone. For the LOCAL zone the Routed accessor's Direct fallback is the
			// correct byte-identical safety belt; for a REMOTE zone the fallback is
			// NoCreds, which errors clearly. Either way warn (rate-limited) so the
			// colombo-vs-zone-1 misconfig is observable.
			r.warnNoAgent(region, zone)
			return nil, false
		}
		return clusteraccess.NewAgentBacked(sess, region, zone, fieldManager, mapper, r.log), true
	}
}

// warnNoAgent emits a rate-limited WARNING when a routed verb's toggle is on but
// no agent is connected for the zone. It fires at most once per zone per
// noAgentWarnEvery. The caller still returns "not routed" — the Routed accessor's
// Direct fallback then runs (local: the byte-identical direct path; remote: a
// clear NoCreds error).
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
		Msg("agent routing enabled for this region/zone but no agent is connected; the local zone uses the direct cluster path while a remote zone fails clearly — mint and connect an agent for this zone")
}
