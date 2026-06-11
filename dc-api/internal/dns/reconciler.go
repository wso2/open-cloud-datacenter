// Package dns drives the per-VPC CoreDNS Corefile toward agreement with
// dc-api's view of the world.
//
// Today there is exactly one driver: DatabaseDNSReconciler, a background
// goroutine that watches the `databases` table and, when a VPC-mode DB
// reaches ACTIVE with an assigned IP, writes a host record into the
// tenant's per-VPC CoreDNS pod. The same Corefile is shared with the M3
// Key Vault Private Endpoint feature (every host-record source for the
// VPC is rewritten together on each sync), so KeyVault PEs and DBaaS DNS
// records coexist without either feature touching the other's code path.
//
// Design notes:
//
//   - The loop reads endpoint_address from Postgres, NOT from the live
//     DBInstance CR. The handler-side status overlay (GET /databases/{id})
//     is responsible for persisting endpoint_address into the row; cloud-ui
//     polls GET continuously while a DB is provisioning, so the field is
//     populated reliably within a few seconds of the controller reporting
//     it. This keeps the reconciler decoupled from the dbaas provider.
//
//   - SyncCorefile is idempotent (rewrites the entire hosts block from the
//     given list every time), so a failure between SyncCorefile and the
//     INSERT into private_endpoints just causes the next tick to redo
//     both — no partial state can corrupt the loop.
//
//   - The reconciler ONLY registers; it does not deregister. Teardown
//     happens inline in the DELETE /databases/{id} handler so customers
//     see DNS removed synchronously when they delete a DB, with no
//     window where DNS resolves to a dead VM.
package dns

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/wso2/dc-api/internal/db"
	"github.com/wso2/dc-api/internal/models"
	"github.com/wso2/dc-api/internal/providers/endpoints"
)

// ServiceClassDatabase is the DNS suffix segment for managed databases:
// "<db-name>.db.dc.internal". Matches KeyVault's "kv" convention.
const ServiceClassDatabase = "db"

// DefaultInterval is the production poll cadence. Picked to balance
// "DNS appears promptly after a DB provisions" against DB load — at
// 30s a 50-DB tenant generates two index-only scans per minute.
const DefaultInterval = 30 * time.Second

// DatabaseDNSReconciler keeps the per-VPC CoreDNS Corefile in sync with
// the `databases` table. Construct with New and start with Run(ctx).
type DatabaseDNSReconciler struct {
	repo        *db.Repository
	provisioner endpoints.Provisioner
	interval    time.Duration
	log         zerolog.Logger
}

// New builds a reconciler. The provisioner must implement SyncCorefile
// (every Provisioner does — it's part of the interface).
func New(repo *db.Repository, provisioner endpoints.Provisioner, log zerolog.Logger) *DatabaseDNSReconciler {
	return &DatabaseDNSReconciler{
		repo:        repo,
		provisioner: provisioner,
		interval:    DefaultInterval,
		log:         log.With().Str("component", "database-dns-reconciler").Logger(),
	}
}

// WithInterval overrides the default poll cadence. Tests use this to drive
// the loop fast; production should leave it at DefaultInterval.
func (r *DatabaseDNSReconciler) WithInterval(d time.Duration) *DatabaseDNSReconciler {
	r.interval = d
	return r
}

// Run blocks until ctx is cancelled. Call from main.go inside a goroutine.
// A best-effort initial tick runs immediately so a restarted server
// catches up without waiting a full interval.
func (r *DatabaseDNSReconciler) Run(ctx context.Context) {
	r.log.Info().Dur("interval", r.interval).Msg("database DNS reconciler started")
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	r.reconcileAll(ctx)
	for {
		select {
		case <-ctx.Done():
			r.log.Info().Msg("database DNS reconciler stopping")
			return
		case <-ticker.C:
			r.reconcileAll(ctx)
		}
	}
}

// reconcileAll processes every DB that needs a DNS record this tick.
func (r *DatabaseDNSReconciler) reconcileAll(ctx context.Context) {
	pending, err := r.repo.ListVPCDatabasesPendingDNS(ctx)
	if err != nil {
		r.log.Error().Err(err).Msg("list vpc databases pending dns")
		return
	}
	if len(pending) == 0 {
		return
	}
	r.log.Info().Int("count", len(pending)).Msg("reconciler tick")

	for _, d := range pending {
		if err := r.reconcileOne(ctx, d); err != nil {
			r.log.Error().Err(err).
				Str("db_id", d.ID.String()).
				Str("db_name", d.Name).
				Msg("reconcile database dns")
		}
	}
}

// reconcileOne registers a single DB's DNS record. Returns nil on success
// and on every "expected, recoverable" condition (e.g. VPC not yet
// bootstrapped — will retry next tick). Returns a non-nil error only for
// surprises worth logging.
func (r *DatabaseDNSReconciler) reconcileOne(ctx context.Context, d *models.Database) error {
	if d.VNetID == nil || d.SubnetID == nil {
		// SQL filter excludes NULL vnet_id; subnet_id is always set on VPC mode.
		return fmt.Errorf("database %s has nil vnet/subnet id (filter bypass?)", d.ID)
	}

	vnet, err := r.repo.GetVNetInternal(ctx, *d.VNetID)
	if err != nil {
		return fmt.Errorf("get vnet %s: %w", *d.VNetID, err)
	}
	if vnet.BackendUID == "" {
		// VPC not yet bootstrapped on KubeOVN — skip; next tick will catch up.
		r.log.Debug().
			Str("db_id", d.ID.String()).
			Str("vnet_id", d.VNetID.String()).
			Msg("vnet has no backend_uid yet; deferring dns registration")
		return nil
	}

	hostname := buildHostname(d.Name)
	if hostname == "" {
		return fmt.Errorf("database %s name %q sanitizes to empty hostname label", d.ID, d.Name)
	}

	// Build the full host-record list for this VPC: existing PE rows + our new
	// record. The Corefile is rewritten from this full list, so omitting any
	// would silently drop them.
	existing, err := r.repo.ListPrivateEndpointsByVNet(ctx, *d.VNetID)
	if err != nil {
		return fmt.Errorf("list private endpoints in vnet %s: %w", *d.VNetID, err)
	}
	hosts := make([]endpoints.HostRecord, 0, len(existing)+1)
	var collidedID uuid.UUID
	for _, ep := range existing {
		if ep.IPAddress == "" || ep.Hostname == "" {
			// Skip rows still being provisioned (no IP/hostname yet).
			continue
		}
		if strings.EqualFold(ep.Hostname, hostname) {
			collidedID = ep.ID
		}
		hosts = append(hosts, endpoints.HostRecord{
			Hostname:  ep.Hostname,
			IPAddress: ep.IPAddress,
		})
	}
	if collidedID != (uuid.UUID{}) {
		// Mark the attempt as FAILED via a PE row so the reconciler doesn't
		// spin on it every tick, and the operator gets a visible signal.
		_, insertErr := r.repo.CreatePrivateEndpoint(ctx, &models.PrivateEndpoint{
			TenantID:    d.TenantID,
			TenantUUID:  d.TenantUUID,
			ProjectID:   d.ProjectID,
			ProjectUUID: d.ProjectUUID,
			TargetType:  models.PrivateEndpointTargetDatabase,
			TargetID:    d.ID,
			VNetID:      *d.VNetID,
			SubnetID:    *d.SubnetID,
			Name:        d.Name,
			BackendAddr: fmt.Sprintf("%s:%d", d.EndpointAddress, d.EndpointPort),
			Status:      models.StatusFailed,
			Message:     fmt.Sprintf("hostname %q collides with private_endpoint %s in same VPC; rename to resolve", hostname, collidedID),
		})
		if insertErr != nil {
			return fmt.Errorf("record collision: %w", insertErr)
		}
		r.log.Warn().
			Str("db_id", d.ID.String()).
			Str("hostname", hostname).
			Str("collides_with", collidedID.String()).
			Msg("hostname collision; marked database PE as FAILED")
		return nil
	}
	hosts = append(hosts, endpoints.HostRecord{
		Hostname:  hostname,
		IPAddress: d.EndpointAddress,
	})

	// Rewrite the per-VPC Corefile. Idempotent — safe to retry.
	if err := r.provisioner.SyncCorefile(ctx, vnet.BackendUID, hosts); err != nil {
		return fmt.Errorf("sync corefile for vpc %s: %w", vnet.BackendUID, err)
	}

	// Record success. UNIQUE (target_type, target_id, vnet_id) guards against
	// a duplicate insertion if two reconciler instances ever run together.
	_, err = r.repo.CreatePrivateEndpoint(ctx, &models.PrivateEndpoint{
		TenantID:    d.TenantID,
		TenantUUID:  d.TenantUUID,
		ProjectID:   d.ProjectID,
		ProjectUUID: d.ProjectUUID,
		TargetType:  models.PrivateEndpointTargetDatabase,
		TargetID:    d.ID,
		VNetID:      *d.VNetID,
		SubnetID:    *d.SubnetID,
		Name:        d.Name,
		IPAddress:   d.EndpointAddress,
		Hostname:    hostname,
		BackendAddr: fmt.Sprintf("%s:%d", d.EndpointAddress, d.EndpointPort),
		Status:      models.StatusActive,
	})
	if err != nil {
		// DNS is already written but the row failed to insert. Next tick will
		// re-run the whole sequence — SyncCorefile is idempotent so the DNS
		// stays correct, and CreatePrivateEndpoint will eventually succeed.
		return fmt.Errorf("insert private_endpoint row: %w", err)
	}

	r.log.Info().
		Str("db_id", d.ID.String()).
		Str("db_name", d.Name).
		Str("hostname", hostname).
		Str("ip", d.EndpointAddress).
		Str("vpc", vnet.BackendUID).
		Msg("dns record registered")
	return nil
}

// ── Hostname construction ────────────────────────────────────────────────────

var nonHostnameChar = regexp.MustCompile(`[^a-z0-9-]+`)
var multiHyphen = regexp.MustCompile(`-+`)

// BuildHostname is exported so the API handler can compute the same name
// the reconciler will when populating Database.EndpointHostname on GET.
//
// Sanitization rules (matches RFC 1123 DNS label syntax):
//   - lowercase
//   - allow only a-z, 0-9, hyphen
//   - collapse runs of invalid characters into a single hyphen
//   - trim leading/trailing hyphens
//   - truncate at 63 chars (single-label DNS max)
//
// Returns "" if the result has an empty label (e.g. name was "---").
func BuildHostname(dbName string) string {
	return buildHostname(dbName)
}

func buildHostname(dbName string) string {
	label := strings.ToLower(dbName)
	label = nonHostnameChar.ReplaceAllString(label, "-")
	label = multiHyphen.ReplaceAllString(label, "-")
	label = strings.Trim(label, "-")
	if label == "" {
		return ""
	}
	if len(label) > 63 {
		label = strings.TrimRight(label[:63], "-")
	}
	return fmt.Sprintf("%s.%s.dc.internal", label, ServiceClassDatabase)
}
