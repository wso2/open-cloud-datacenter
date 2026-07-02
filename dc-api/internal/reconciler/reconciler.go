// Package reconciler bridges the gap between DC-API's PostgreSQL state and
// the actual state of resources in Harvester and Rancher.
//
// The problem it solves:
//   - When a VM is created, DC-API records it as PENDING and calls Harvester
//     asynchronously. The VM takes 2-5 minutes to boot.
//   - Without a reconciler, the resource stays PENDING forever — DC-API never
//     finds out that Harvester finished (or failed).
//   - The reconciler is a goroutine that wakes up every 60 seconds, asks the
//     provider for the current state, and updates PostgreSQL if it changed.
//
// Think of it like a control loop — the same concept as a Kubernetes controller.
// DC-API is the "desired state" store; Harvester/Rancher hold the "actual state".
// The reconciler continuously drives them toward agreement.
package reconciler

import (
	"context"
	"errors"
	"time"

	"github.com/rs/zerolog"
	"github.com/wso2/dc-api/internal/audit"
	"github.com/wso2/dc-api/internal/db"
	"github.com/wso2/dc-api/internal/models"
	"github.com/wso2/dc-api/internal/providers"
)

// reconcileTimeout bounds one resource's reconcile so a hung backend can't
// stall every other PENDING/DELETING resource in the sequential reconcileAll
// loop; matches the per-request client timeout on the provider rest.Configs.
const reconcileTimeout = 30 * time.Second

// orphanTimeout is how long a PENDING/DELETING row may sit with no
// backend_uid before the orphan sweep recovers it (PENDING → FAILED,
// DELETING → row removed). Must STRICTLY exceed the largest async-provision
// context ceiling in the handlers — 15 minutes for cluster creates, 10 for
// everything else — so an in-flight create is never reaped while its
// goroutine is still working. 20 minutes gives clusters a 5-minute margin.
const orphanTimeout = 20 * time.Minute

// Reconciler polls PENDING and DELETING resources and syncs their status
// from the provider back into PostgreSQL.
type Reconciler struct {
	repo *db.Repository
	// resolve maps a resource's (region, zone) to that zone's provider set. In a
	// single-zone deployment every resource resolves to the same local set
	// (pointer-identical to the providers dc-api injected before per-resource
	// routing); in a multi-zone deployment a remote resource resolves to its own
	// zone's agent-backed set. Resolution errors (unknown zone, remote zone with
	// no agent) are isolated per resource so one unreachable zone never stalls
	// reconciling resources in other zones.
	resolve  providers.Resolver
	interval time.Duration
	log      zerolog.Logger
}

// New creates a Reconciler. Call Run(ctx) to start it. resolve is the per-zone
// provider resolver (*providers.Registry).
func New(
	repo *db.Repository,
	resolve providers.Resolver,
	log zerolog.Logger,
) *Reconciler {
	return &Reconciler{
		repo:     repo,
		resolve:  resolve,
		interval: 60 * time.Second,
		log:      log.With().Str("component", "reconciler").Logger(),
	}
}

// WithInterval overrides the default 60s polling interval. Returns the same
// Reconciler so calls can be chained: reconciler.New(...).WithInterval(5*time.Second).Run(ctx).
// Production callers should NOT lower this — it puts unnecessary load on the
// provider APIs (Harvester/Rancher REST). Intended for integration tests
// that need fast PENDING→ACTIVE transitions.
func (r *Reconciler) WithInterval(d time.Duration) *Reconciler {
	r.interval = d
	return r
}

// Run starts the reconciliation loop. It blocks until ctx is cancelled.
// Call this in a goroutine from main.go:
//
//	go reconciler.New(...).Run(ctx)
//
// When the context is cancelled (Ctrl-C / SIGTERM), the loop exits cleanly.
func (r *Reconciler) Run(ctx context.Context) {
	// Every repository mutation this loop makes is audited automatically —
	// stamp the worker identity once so events read "reconciler", not "system".
	ctx = audit.WithActor(ctx, "reconciler")
	r.log.Info().Dur("interval", r.interval).Msg("reconciler started")
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	// Run once immediately on startup so we don't wait 60s after a restart.
	r.reconcileAll(ctx)

	for {
		select {
		case <-ctx.Done():
			r.log.Info().Msg("reconciler stopping")
			return
		case <-ticker.C:
			r.reconcileAll(ctx)
		}
	}
}

// reconcileAll fetches all PENDING/DELETING resources and reconciles each one.
func (r *Reconciler) reconcileAll(ctx context.Context) {
	// Crash recovery: rows stranded before backend creation (backend_uid IS
	// NULL) are invisible to ListPending below and would stay PENDING/DELETING
	// forever (blocking their unique name). Sweep them first — stranded
	// creates become FAILED, stranded deletes are completed by removing the
	// row. A sweep failure is logged and skipped — it must never abort the tick.
	if reaped, err := r.repo.FailOrphanedUnprovisioned(ctx, orphanTimeout); err != nil {
		r.log.Error().Err(err).Msg("orphan sweep failed — continuing with the reconcile tick")
	} else if reaped > 0 {
		r.log.Warn().Int("count", reaped).
			Msg("orphan sweep: recovered resources stranded before backend creation (interrupted provisioning)")
	}

	resources, err := r.repo.ListPending(ctx)
	if err != nil {
		r.log.Error().Err(err).Msg("failed to list pending resources")
		return
	}
	r.log.Info().Int("count", len(resources)).Msg("reconciler tick")
	if len(resources) == 0 {
		return
	}

	for _, res := range resources {
		// Each resource is reconciled independently. An error on one does not
		// stop the others — we want best-effort reconciliation.
		r.reconcileOne(ctx, res)
	}
}

// reconcileOne reconciles a single resource against its provider.
func (r *Reconciler) reconcileOne(ctx context.Context, res *models.Resource) {
	// Bound this resource's reconcile so a hung backend can't stall every
	// other PENDING/DELETING resource; matches the per-request client timeout.
	ctx, cancel := context.WithTimeout(ctx, reconcileTimeout)
	defer cancel()

	log := r.log.With().
		Str("id", res.ID.String()).
		Str("name", res.Name).
		Str("type", string(res.Type)).
		Str("status", string(res.Status)).
		Str("region", res.Region).
		Str("zone", res.Zone).
		Logger()

	// Resolve the provider set for THIS resource's zone. An error here (unknown
	// zone, or a remote zone whose agent is down) is isolated to this resource:
	// we log a per-resource warning and return, leaving the row in its current
	// status to be retried next tick. Because each resource is reconciled
	// independently in the reconcileAll loop, one unreachable zone never stalls
	// resources in other zones on the same tick.
	set, err := r.resolve.For(res.Region, res.Zone)
	if err != nil {
		log.Warn().Err(err).Msg("cannot resolve provider for resource's zone — skipping this tick")
		return
	}

	var (
		providerStatus models.ResourceStatus
		providerMsg    string
		providerIP     string
		providerMgmtIP string // F10 bastion: mgmt-VLAN IP from the second NIC
		notFound       bool
	)

	switch res.Type {
	case models.ResourceTypeVM, models.ResourceTypeBastion:
		// Bastions are KubeVirt VMs under the hood — same provider call.
		// For bastions providerRes.MgmtIP carries the mgmt-VLAN IP; it's
		// empty for regular VMs.
		providerRes, err := set.Compute.GetVM(ctx, res.BackendUID)
		if err != nil {
			if isNotFound(err) {
				notFound = true
			} else {
				log.Warn().Err(err).Msg("provider GetVM failed — will retry next tick")
				return
			}
		} else {
			providerStatus = providerRes.Status
			providerMsg = providerRes.Message
			providerIP = providerRes.IPAddress
			providerMgmtIP = providerRes.MgmtIP
		}

	case models.ResourceTypeCluster:
		providerRes, err := set.Cluster.GetCluster(ctx, res.BackendUID)
		if err != nil {
			if isNotFound(err) {
				notFound = true
			} else {
				log.Warn().Err(err).Msg("provider GetCluster failed — will retry next tick")
				return
			}
		} else {
			providerStatus = providerRes.Status
			providerMsg = providerRes.Message
		}

	default:
		// Unknown resource type — nothing we can do.
		log.Warn().Msg("unknown resource type; skipping")
		return
	}

	// ── Handle "not found" from the provider ─────────────────────────────────
	// If a DELETING resource is gone from the provider, it was deleted successfully.
	// Remove the row from PostgreSQL.
	if notFound {
		if res.Status == models.StatusDeleting {
			log.Info().Msg("resource confirmed deleted by provider — removing from DB")
			if err := r.repo.Delete(ctx, res.ID); err != nil {
				log.Error().Err(err).Msg("failed to delete resource from DB")
			}
		} else {
			// PENDING resource not found — it may have failed before registering.
			// Mark it as FAILED so it's visible and not polled again forever.
			log.Warn().Msg("PENDING resource not found in provider — marking as FAILED")
			_ = r.repo.UpdateStatus(ctx, res.ID, models.StatusFailed,
				"resource not found in provider", "")
		}
		return
	}

	// ── DELETING resources: only forward, never backward ────────────────────
	// While a resource is being deleted the provider may still return ACTIVE
	// (e.g. Rancher keeps the cluster object with ready=true until it is fully
	// removed). Never let the reconciler overwrite DELETING with ACTIVE/PENDING;
	// only surface a permanent provider-side failure.
	if res.Status == models.StatusDeleting {
		if providerStatus == models.StatusFailed {
			log.Warn().Str("msg", providerMsg).Msg("provider reported failure during deletion")
			_ = r.repo.UpdateStatus(ctx, res.ID, models.StatusFailed, providerMsg, "")
		}
		// Otherwise still deleting — wait for the 404.
		return
	}

	// ── Always persist IP when it first becomes available ────────────────────
	// qemu-guest-agent typically reports the IP 20-60s after the VM reaches
	// Running state. By the time it does, the status is already ACTIVE in DB
	// and the status-change branch below would short-circuit. Check IP first.
	if providerIP != "" && providerIP != res.IPAddress {
		if err := r.repo.UpdateIPAddress(ctx, res.ID, providerIP); err != nil {
			log.Warn().Err(err).Str("ip", providerIP).Msg("failed to persist IP address")
		} else {
			log.Info().Str("ip", providerIP).Msg("IP address updated")
		}
	}
	// F10: bastion mgmt-VLAN IP. Same flow as IPAddress — once qemu-guest-agent
	// reports the second NIC, persist it. Empty for non-bastion resources.
	if providerMgmtIP != "" && providerMgmtIP != res.MgmtIP {
		if err := r.repo.UpdateMgmtIP(ctx, res.ID, providerMgmtIP); err != nil {
			log.Warn().Err(err).Str("mgmt_ip", providerMgmtIP).Msg("failed to persist mgmt IP")
		} else {
			log.Info().Str("mgmt_ip", providerMgmtIP).Msg("mgmt IP address updated")
		}
	}

	// ── Status unchanged — but the progress message may have changed (F39).
	// Persist a message-only update so tenants polling GET /v1/clusters/{id}
	// during a long PENDING see "waiting for viable init node" → "control
	// plane initializing" → "cattle-cluster-agent connecting" without a
	// status flip. No audit event for these; they're informational deltas.
	if providerStatus == res.Status {
		if providerMsg != "" && providerMsg != res.Message {
			if err := r.repo.UpdateStatus(ctx, res.ID, providerStatus, providerMsg, ""); err != nil {
				log.Warn().Err(err).Msg("failed to persist progress message")
			}
		}
		return
	}

	// ── Status changed — update PostgreSQL ───────────────────────────────────
	log.Info().
		Str("from", string(res.Status)).
		Str("to", string(providerStatus)).
		Msg("status changed — updating DB")

	if err := r.repo.UpdateStatus(ctx, res.ID, providerStatus, providerMsg, ""); err != nil {
		log.Error().Err(err).Msg("failed to update resource status in DB")
		return
	}

	// The status transition above is audited automatically by the
	// repository layer (actor stamped at Run).
}

// isNotFound returns true if the error indicates the resource does not exist
// in the provider. Both Harvester and Rancher drivers wrap not-found errors
// with messages containing "not found".
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	// Preferred path: the typed sentinel (providers.NotFoundError implements
	// NotFound() bool; Harvester GetVM and Rancher GetCluster return it).
	var nf interface{ NotFound() bool }
	if errors.As(err, &nf) {
		return nf.NotFound()
	}
	// Fallback: string match, for provider paths (and agent-routed errors)
	// that don't return the typed sentinel yet.
	return containsNotFound(err.Error())
}

func containsNotFound(s string) bool {
	for i := 0; i+9 <= len(s); i++ {
		if s[i:i+9] == "not found" {
			return true
		}
	}
	return false
}
