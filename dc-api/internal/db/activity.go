// Package db — activity.go
//
// The activity/audit framework: every resource family's lifecycle lands in
// the append-only audit_events table through ONE writer, AppendAuditEvent.
//
// ── THE CONTRACT FOR NEW RESOURCE FAMILIES ───────────────────────────────────
//
// A family is auditable when it has an arm in auditSnapshotArms below. The
// arm resolves the family's UUID into an identity snapshot (name, kind,
// tenant_uuid, project_uuid) that is written ONTO the event row, so events
// render forever — deleting the resource never erases its history. To audit
// a new resource family:
//
//  1. Add one SELECT arm to auditSnapshotArms (join a parent table if the
//     family doesn't carry tenant_uuid/project_uuid itself — see peerings).
//  2. Add the kind value to the ActivityEvent.resource_type enum in
//     openapi.yaml, and (if it has a detail page) to cloud-ui's
//     ACTIVITY_RESOURCE_ROUTES map.
//  3. Call AppendAuditEvent from the family's handlers on CREATE / DELETE /
//     async STATUS_CHANGE, exactly like vnet.go does.
//
// Handlers that already call AppendAuditEvent need no changes when a family
// is registered — the writer resolves the UUID against every arm.
package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog/log"
	"github.com/wso2/dc-api/internal/audit"
	"github.com/wso2/dc-api/internal/models"
)

// ListProjectActivity returns one page of audit events for the project,
// newest first, plus the total event count across all pages so callers can
// paginate. limit/offset are trusted here — the handler validates them
// (limit 1..100, offset >= 0) before calling.
//
// Ordering is (created_at DESC, id DESC): created_at alone is not unique
// (bulk operations land in the same millisecond), and a non-deterministic
// tie-break would let rows repeat or vanish across pages.
func (r *Repository) ListProjectActivity(ctx context.Context, projectUUID uuid.UUID, limit, offset int) ([]models.ActivityEntry, int, error) {
	const countQ = `
		SELECT COUNT(*)
		FROM   audit_events
		WHERE  project_uuid = $1`

	var total int
	if err := r.pool.QueryRow(ctx, countQ, projectUUID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("db count project activity: %w", err)
	}

	q := `
		SELECT ae.id, ae.resource_id, ae.resource_name, ae.resource_type,
		       ae.actor_id, ae.action, ae.from_status, ae.to_status, ae.message,
		       ae.created_at,
		       (ae.resource_id IS NOT NULL AND ` + auditLivenessSQL + `) AS live
		FROM   audit_events ae
		WHERE  ae.project_uuid = $1
		ORDER  BY ae.created_at DESC, ae.id DESC
		LIMIT  $2 OFFSET $3`

	rows, err := r.pool.Query(ctx, q, projectUUID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("db list project activity: %w", err)
	}
	defer rows.Close()

	var entries []models.ActivityEntry
	for rows.Next() {
		var e models.ActivityEntry
		var resourceID *uuid.UUID
		var name, rtype *string // NULL only on rows that predate snapshots
		var fromStatus, toStatus, message *string
		var live bool
		if err := rows.Scan(
			&e.ID, &resourceID, &name, &rtype,
			&e.ActorID, &e.Action, &fromStatus, &toStatus, &message,
			&e.CreatedAt, &live,
		); err != nil {
			return nil, 0, fmt.Errorf("db scan project activity: %w", err)
		}
		// The pointer is exposed only while the resource exists, so clients
		// never render deep links to deleted resources.
		if resourceID != nil && live {
			e.ResourceID = *resourceID
		}
		if name != nil {
			e.ResourceName = *name
		}
		if rtype != nil {
			e.ResourceType = models.ResourceType(*rtype)
		}
		if fromStatus != nil {
			e.FromStatus = models.ResourceStatus(*fromStatus)
		}
		if toStatus != nil {
			e.ToStatus = models.ResourceStatus(*toStatus)
		}
		if message != nil {
			e.Message = *message
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("db list project activity: %w", err)
	}
	return entries, total, nil
}

// auditSnapshotArms — the audit framework's registry. Each arm yields
// (name, kind, tenant_uuid, project_uuid) for $1 = the resource UUID; the
// writer unions them and takes the single match. Kinds are the
// ActivityEvent.resource_type values published in openapi.yaml.
var auditSnapshotArms = []string{
	`SELECT name, type::text AS kind, tenant_uuid, project_uuid FROM resources WHERE id = $1`,
	`SELECT name, 'VNET', tenant_uuid, project_uuid FROM vnets WHERE id = $1`,
	`SELECT name, 'SUBNET', tenant_uuid, project_uuid FROM subnets WHERE id = $1`,
	`SELECT name, 'NSG', tenant_uuid, project_uuid FROM network_security_groups WHERE id = $1`,
	// Peerings and DNS zones carry no tenant/project UUIDs of their own —
	// identity flows from the parent VNet.
	`SELECT p.name, 'PEERING', v.tenant_uuid, v.project_uuid FROM peerings p JOIN vnets v ON v.id = p.vnet_id WHERE p.id = $1`,
	`SELECT z.zone_name, 'PRIVATE_DNS_ZONE', v.tenant_uuid, v.project_uuid FROM private_dns_zones z JOIN vnets v ON v.id = z.vnet_id WHERE z.id = $1`,
	`SELECT name, 'KEYVAULT', tenant_uuid, project_uuid FROM key_vaults WHERE id = $1`,
	`SELECT name, 'DATABASE', tenant_uuid, project_uuid FROM databases WHERE id = $1`,
	`SELECT name, 'PRIVATE_ENDPOINT', tenant_uuid, project_uuid FROM private_endpoints WHERE id = $1`,
}

// AppendAuditEvent records a lifecycle event for ANY registered resource
// family. Append-only — never updated. The owning resource's identity is
// snapshotted onto the event row atomically (INSERT … SELECT across the
// registry arms), so the activity feed keeps rendering the event after the
// resource is deleted. An unregistered or already-deleted UUID makes the
// insert a silent no-op.
func (r *Repository) AppendAuditEvent(ctx context.Context, ev *models.AuditEvent) error {
	q := `
		INSERT INTO audit_events
			(resource_id, actor_id, action, from_status, to_status, message,
			 resource_name, resource_type, tenant_uuid, project_uuid)
		SELECT $1, $2, $3, $4::resource_status, $5::resource_status, $6, s.name, s.kind, s.tenant_uuid, s.project_uuid
		FROM (
			` + auditSnapshotUnion + `
			LIMIT 1
		) s`

	_, err := r.pool.Exec(ctx, q,
		ev.ResourceID, ev.ActorID, ev.Action,
		nilIfStatusEmpty(ev.FromStatus), nilIfStatusEmpty(ev.ToStatus),
		nilIfEmpty(ev.Message),
	)
	if err != nil {
		return fmt.Errorf("db append audit event: %w", err)
	}
	return nil
}
// auditSnapshotUnion is the registry arms joined for the writer's subquery.
var auditSnapshotUnion = func() string {
	out := ""
	for i, arm := range auditSnapshotArms {
		if i > 0 {
			out += "\n\t\t\tUNION ALL "
		}
		out += arm + " "
	}
	return out
}()

// ── Write side: the framework's recorder ─────────────────────────────────────

// execer abstracts pool vs transaction so recording can join the caller's tx.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// auditInsert is one lifecycle event with its identity snapshot already
// resolved — repository methods capture these values from their own
// INSERT/UPDATE/DELETE … RETURNING, so no extra lookup happens here.
type auditInsert struct {
	ID          uuid.UUID
	Name        string
	Kind        string // ActivityEvent.resource_type value, e.g. "VNET"
	TenantUUID  *uuid.UUID
	ProjectUUID *uuid.UUID
	Action      string // audit.Action*
	From        models.ResourceStatus
	To          models.ResourceStatus
	Message     string
}

// recordAudit writes one event. It never fails the caller's operation —
// auditing is best-effort — but unlike the bad old days it LOGS failures:
// a silently-failing audit write hid an entire missing feature once.
// No-op transitions (From == To on a STATUS_CHANGE) are skipped here so the
// rule holds for every family, not per call site.
func (r *Repository) recordAudit(ctx context.Context, q execer, in auditInsert) {
	if in.Action == audit.ActionStatusChange && in.From == in.To {
		return
	}
	const sql = `
		INSERT INTO audit_events
			(resource_id, actor_id, action, from_status, to_status, message,
			 resource_name, resource_type, tenant_uuid, project_uuid)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`
	_, err := q.Exec(ctx, sql,
		in.ID, audit.ActorFromContext(ctx), in.Action,
		nilIfStatusEmpty(in.From), nilIfStatusEmpty(in.To), nilIfEmpty(in.Message),
		nilIfEmpty(in.Name), nilIfEmpty(in.Kind), in.TenantUUID, in.ProjectUUID,
	)
	if err != nil {
		log.Warn().Err(err).
			Str("kind", in.Kind).Str("action", in.Action).Str("resource", in.ID.String()).
			Msg("audit: event write failed (operation unaffected)")
	}
}

// auditedTable describes one family for the generic audited helpers below —
// the second half of the framework. Families whose tables don't carry
// tenant/project UUIDs resolve identity through their parent VNet.
type auditedTable struct {
	kind         string
	table        string
	nameCol      string // display-name column ("name", "zone_name", …)
	parentVNetFK string // "" = own tenant_uuid/project_uuid columns
}

// auditedStatusUpdate is the framework's standard status transition: update
// the row, capture the previous status via a self-join RETURNING, and record
// the STATUS_CHANGE (no-ops skipped inside recordAudit).
func (r *Repository) auditedStatusUpdate(ctx context.Context, f auditedTable, id uuid.UUID, status models.ResourceStatus, message, backendUID string) error {
	var q string
	if f.parentVNetFK == "" {
		q = `UPDATE ` + f.table + ` x
			SET status = $2, message = $3, backend_uid = COALESCE(NULLIF($4,''), x.backend_uid)
			FROM ` + f.table + ` old
			WHERE x.id = $1 AND old.id = x.id
			RETURNING old.status::text, x.` + f.nameCol + `, x.tenant_uuid, x.project_uuid`
	} else {
		q = `UPDATE ` + f.table + ` x
			SET status = $2, message = $3, backend_uid = COALESCE(NULLIF($4,''), x.backend_uid)
			FROM ` + f.table + ` old, vnets v
			WHERE x.id = $1 AND old.id = x.id AND v.id = old.` + f.parentVNetFK + `
			RETURNING old.status::text, x.` + f.nameCol + `, v.tenant_uuid, v.project_uuid`
	}
	var from, name string
	var tuid, puid *uuid.UUID
	err := r.pool.QueryRow(ctx, q, id, string(status), message, backendUID).
		Scan(&from, &name, &tuid, &puid)
	if err == pgx.ErrNoRows {
		return nil // row already gone — keep the old Exec semantics
	}
	if err != nil {
		return fmt.Errorf("db update %s status %s: %w", f.table, id, err)
	}
	r.recordAudit(ctx, r.pool, auditInsert{
		ID: id, Name: name, Kind: f.kind, TenantUUID: tuid, ProjectUUID: puid,
		Action: audit.ActionStatusChange,
		From:   models.ResourceStatus(from), To: status, Message: message,
	})
	return nil
}

// auditedDelete is the framework's standard terminal event: delete the row
// and record DELETE with to_status DELETED, identity captured from the row's
// last breath via RETURNING.
func (r *Repository) auditedDelete(ctx context.Context, f auditedTable, id uuid.UUID) error {
	var q string
	if f.parentVNetFK == "" {
		q = `DELETE FROM ` + f.table + ` x WHERE x.id = $1
			RETURNING x.status::text, x.` + f.nameCol + `, x.tenant_uuid, x.project_uuid`
	} else {
		q = `DELETE FROM ` + f.table + ` x USING vnets v
			WHERE x.id = $1 AND v.id = x.` + f.parentVNetFK + `
			RETURNING x.status::text, x.` + f.nameCol + `, v.tenant_uuid, v.project_uuid`
	}
	var from, name string
	var tuid, puid *uuid.UUID
	err := r.pool.QueryRow(ctx, q, id).Scan(&from, &name, &tuid, &puid)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("db delete %s %s: %w", f.table, id, err)
	}
	r.recordAudit(ctx, r.pool, auditInsert{
		ID: id, Name: name, Kind: f.kind, TenantUUID: tuid, ProjectUUID: puid,
		Action: audit.ActionDelete,
		From:   models.ResourceStatus(from), To: models.StatusDeleted,
	})
	return nil
}

// The families wired through the generic helpers. resources, databases, and
// private endpoints have bespoke methods (extra columns / tx semantics) that
// call recordAudit directly.
var (
	familyVNet       = auditedTable{kind: "VNET", table: "vnets", nameCol: "name"}
	familySubnet     = auditedTable{kind: "SUBNET", table: "subnets", nameCol: "name"}
	familyRouteTable = auditedTable{kind: "ROUTE_TABLE", table: "route_tables", nameCol: "name"}
	familyNSG        = auditedTable{kind: "NSG", table: "network_security_groups", nameCol: "name"}
	familyPeering    = auditedTable{kind: "PEERING", table: "peerings", nameCol: "name", parentVNetFK: "vnet_id"}
	familyDNSZone    = auditedTable{kind: "PRIVATE_DNS_ZONE", table: "private_dns_zones", nameCol: "zone_name", parentVNetFK: "vnet_id"}
	familyKeyVault   = auditedTable{kind: "KEYVAULT", table: "key_vaults", nameCol: "name"}
)

// vnetIdentity resolves a VNet's tenant/project UUIDs for child events
// whose own tables don't carry them. Best-effort: nils on a missing row.
func (r *Repository) vnetIdentity(ctx context.Context, vnetID uuid.UUID) (tuid, puid *uuid.UUID) {
	_ = r.pool.QueryRow(ctx,
		`SELECT tenant_uuid, project_uuid FROM vnets WHERE id = $1`, vnetID).
		Scan(&tuid, &puid)
	return tuid, puid
}

// auditedLivenessTables lists every table a resource_id pointer may refer
// to — used by the feed to decide whether the pointer still resolves (the
// UI only renders deep links for live resources).
var auditedLivenessTables = []string{
	"resources", "vnets", "subnets", "route_tables", "network_security_groups",
	"peerings", "private_dns_zones", "key_vaults", "databases", "private_endpoints",
}

// auditLivenessSQL is "(EXISTS(...) OR EXISTS(...) …)" for $-substitution-free
// embedding in the feed query (table names are compile-time constants).
var auditLivenessSQL = func() string {
	out := "("
	for i, t := range auditedLivenessTables {
		if i > 0 {
			out += " OR "
		}
		out += "EXISTS (SELECT 1 FROM " + t + " WHERE id = ae.resource_id)"
	}
	return out + ")"
}()

