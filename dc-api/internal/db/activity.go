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

	const q = `
		SELECT ae.id, ae.resource_id, ae.resource_name, ae.resource_type,
		       ae.actor_id, ae.action, ae.from_status, ae.to_status, ae.message,
		       ae.created_at
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
		var resourceID *uuid.UUID // NULL once the resource is deleted
		var name, rtype *string   // NULL only on rows that predate snapshots
		var fromStatus, toStatus, message *string
		if err := rows.Scan(
			&e.ID, &resourceID, &name, &rtype,
			&e.ActorID, &e.Action, &fromStatus, &toStatus, &message,
			&e.CreatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("db scan project activity: %w", err)
		}
		if resourceID != nil {
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

