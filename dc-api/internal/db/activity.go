// Package db — activity.go
//
// Read side of the audit log: the per-project activity feed. The write side
// (AppendAuditEvent) lives in db.go and snapshots the owning resource's
// name/type and tenant/project UUIDs onto every event row, so this query
// never joins resources and events survive resource deletion. project_uuid
// on the event is the immutable isolation filter
// (see docs/dc-api-architecture.md).
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
