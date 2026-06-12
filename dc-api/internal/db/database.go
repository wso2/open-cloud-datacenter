// Package db — database.go
//
// Repository methods for Task 1 (DBaaS). All SQL touching the `databases`
// table lives here — handlers call these methods, never pool.Query directly
// (repository pattern; see dc-api/CLAUDE.md §"Design Patterns").
//
// Mirrors the shape of db/keyvault.go: one row per managed Database, scoped
// by (tenant_uuid, project_uuid). Status updates and shown-once credential
// consumption follow the same race-safe pattern KVI uses.
package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/wso2/dc-api/internal/audit"
	"github.com/wso2/dc-api/internal/models"
)

// ErrDatabaseNotFound is returned by GetDatabase/DeleteDatabase when the
// requested row does not exist (or is invisible to the caller's tenant
// after the handler's scope check). Handlers map directly to 404.
var ErrDatabaseNotFound = errors.New("database not found")

// CreateDatabase inserts a new Database row. Caller fills in spec fields;
// id/created_at/updated_at are populated via RETURNING. Unique constraint
// violations (same name within a project) surface as Postgres SQLSTATE
// 23505; handlers map that to 409 Conflict.
func (r *Repository) CreateDatabase(ctx context.Context, d *models.Database) (*models.Database, error) {
	const q = `
		INSERT INTO databases (
			tenant_id, tenant_uuid, project_id, project_uuid,
			name, engine, engine_version, instance_class, allocated_storage_gb,
			network_mode, vnet_id, subnet_id, nad_ref,
			status, message
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7, $8, $9,
			$10, $11, $12, $13,
			$14, $15
		)
		RETURNING id, created_at, updated_at`

	if err := r.pool.QueryRow(ctx, q,
		d.TenantID, d.TenantUUID, d.ProjectID, d.ProjectUUID,
		d.Name, string(d.Engine), nilIfEmpty(d.EngineVersion), d.InstanceClass, d.AllocatedStorageGB,
		string(d.NetworkMode), d.VNetID, d.SubnetID, nilIfEmpty(d.NadRef),
		string(d.Status), nilIfEmpty(d.Message),
	).Scan(&d.ID, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return nil, fmt.Errorf("db create database: %w", err)
	}
	r.recordAudit(ctx, r.pool, auditInsert{
		ID: d.ID, Name: d.Name, Kind: "DATABASE",
		TenantUUID: &d.TenantUUID, ProjectUUID: &d.ProjectUUID,
		Action: audit.ActionCreate, To: d.Status,
	})
	return d, nil
}

// GetDatabase fetches a Database by ID. Returns ErrDatabaseNotFound on
// missing rows so handlers can map directly to 404 without string-matching
// pgx errors.
func (r *Repository) GetDatabase(ctx context.Context, id, tenantUUID, projectUUID uuid.UUID) (*models.Database, error) {
	const q = `
		SELECT id, tenant_id, tenant_uuid, project_id, project_uuid,
		       name, engine, engine_version, instance_class, allocated_storage_gb,
		       network_mode, vnet_id, subnet_id, nad_ref,
		       status, message,
		       endpoint_address, endpoint_port,
		       credentials_consumed_at, created_at, updated_at
		FROM   databases
		WHERE  id = $1 AND tenant_uuid = $2 AND project_uuid = $3`

	d := &models.Database{}
	var engine, networkMode, status string
	var engineVersion, nadRef, message, endpointAddress *string
	var endpointPort *int
	err := r.pool.QueryRow(ctx, q, id, tenantUUID, projectUUID).Scan(
		&d.ID, &d.TenantID, &d.TenantUUID, &d.ProjectID, &d.ProjectUUID,
		&d.Name, &engine, &engineVersion, &d.InstanceClass, &d.AllocatedStorageGB,
		&networkMode, &d.VNetID, &d.SubnetID, &nadRef,
		&status, &message,
		&endpointAddress, &endpointPort,
		&d.CredentialsConsumedAt, &d.CreatedAt, &d.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDatabaseNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db get database: %w", err)
	}

	d.Engine = models.DatabaseEngine(engine)
	d.NetworkMode = models.DatabaseNetworkMode(networkMode)
	d.Status = models.ResourceStatus(status)
	if engineVersion != nil {
		d.EngineVersion = *engineVersion
	}
	if nadRef != nil {
		d.NadRef = *nadRef
	}
	if message != nil {
		d.Message = *message
	}
	if endpointAddress != nil {
		d.EndpointAddress = *endpointAddress
	}
	if endpointPort != nil {
		d.EndpointPort = *endpointPort
	}
	return d, nil
}

// ListDatabasesByProject returns every Database in the given project,
// newest first. Filter is on (tenant_uuid, project_uuid) — the immutable
// UUIDs, not the slugs (Phase 6a / M2.5 pattern).
func (r *Repository) ListDatabasesByProject(ctx context.Context, tenantUUID, projectUUID uuid.UUID) ([]*models.Database, error) {
	const q = `
		SELECT id, tenant_id, tenant_uuid, project_id, project_uuid,
		       name, engine, engine_version, instance_class, allocated_storage_gb,
		       network_mode, vnet_id, subnet_id, nad_ref,
		       status, message,
		       endpoint_address, endpoint_port,
		       credentials_consumed_at, created_at, updated_at
		FROM   databases
		WHERE  tenant_uuid = $1 AND project_uuid = $2
		ORDER  BY created_at DESC`

	rows, err := r.pool.Query(ctx, q, tenantUUID, projectUUID)
	if err != nil {
		return nil, fmt.Errorf("db list databases by project: %w", err)
	}
	defer rows.Close()

	out := make([]*models.Database, 0)
	for rows.Next() {
		d := &models.Database{}
		var engine, networkMode, status string
		var engineVersion, nadRef, message, endpointAddress *string
		var endpointPort *int
		if err := rows.Scan(
			&d.ID, &d.TenantID, &d.TenantUUID, &d.ProjectID, &d.ProjectUUID,
			&d.Name, &engine, &engineVersion, &d.InstanceClass, &d.AllocatedStorageGB,
			&networkMode, &d.VNetID, &d.SubnetID, &nadRef,
			&status, &message,
			&endpointAddress, &endpointPort,
			&d.CredentialsConsumedAt, &d.CreatedAt, &d.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("db scan database: %w", err)
		}
		d.Engine = models.DatabaseEngine(engine)
		d.NetworkMode = models.DatabaseNetworkMode(networkMode)
		d.Status = models.ResourceStatus(status)
		if engineVersion != nil {
			d.EngineVersion = *engineVersion
		}
		if nadRef != nil {
			d.NadRef = *nadRef
		}
		if message != nil {
			d.Message = *message
		}
		if endpointAddress != nil {
			d.EndpointAddress = *endpointAddress
		}
		if endpointPort != nil {
			d.EndpointPort = *endpointPort
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeleteDatabase removes a Database row by ID. The dbaas controller's
// finalizer handles the VM/Secret/PVC teardown; once we delete the CR (in
// the handler/adapter) we can hard-delete the row immediately. Returns
// ErrDatabaseNotFound if the row was already gone.
func (r *Repository) DeleteDatabase(ctx context.Context, id uuid.UUID) error {
	const q = `
		DELETE FROM databases
		WHERE  id = $1
		RETURNING status::text, name, tenant_uuid, project_uuid`
	var from, name string
	var tuid, puid *uuid.UUID
	err := r.pool.QueryRow(ctx, q, id).Scan(&from, &name, &tuid, &puid)
	if err == pgx.ErrNoRows {
		return ErrDatabaseNotFound
	}
	if err != nil {
		return fmt.Errorf("db delete database: %w", err)
	}
	r.recordAudit(ctx, r.pool, auditInsert{
		ID: id, Name: name, Kind: "DATABASE", TenantUUID: tuid, ProjectUUID: puid,
		Action: audit.ActionDelete,
		From:   models.ResourceStatus(from), To: models.StatusDeleted,
	})
	return nil
}

// UpdateDatabaseStatus writes the latest controller-reported state to the
// row. Called from the GET handler (status overlay) and (later) the
// reconciler. endpointAddress/endpointPort may be empty/0 until the
// controller reaches DatabaseReady.
func (r *Repository) UpdateDatabaseStatus(
	ctx context.Context,
	id uuid.UUID,
	status models.ResourceStatus,
	message, endpointAddress string,
	endpointPort int,
) error {
	const q = `
		UPDATE databases t
		SET    status            = $2,
		       message           = $3,
		       endpoint_address  = $4,
		       endpoint_port     = $5
		FROM   databases old
		WHERE  t.id = $1 AND old.id = t.id
		RETURNING old.status::text, t.name, t.tenant_uuid, t.project_uuid`
	var from, name string
	var tuid, puid *uuid.UUID
	err := r.pool.QueryRow(ctx, q, id,
		string(status),
		nilIfEmpty(message),
		nilIfEmpty(endpointAddress),
		nilIfZeroInt(endpointPort),
	).Scan(&from, &name, &tuid, &puid)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("db update database status: %w", err)
	}
	r.recordAudit(ctx, r.pool, auditInsert{
		ID: id, Name: name, Kind: "DATABASE", TenantUUID: tuid, ProjectUUID: puid,
		Action: audit.ActionStatusChange,
		From:   models.ResourceStatus(from), To: status, Message: message,
	})
	return nil
}

// MarkDatabaseCredentialsConsumed stamps credentials_consumed_at = NOW()
// on the row, but only if it is still NULL. Returns the shared
// ErrCredentialsAlreadyConsumed (defined alongside the KeyVault repo) when
// the row had already been marked — the handler maps to 410 Gone.
//
// The IS NULL predicate is the race guard: if two callers race
// GET /credentials, only one wins the UPDATE; the other sees zero rows
// affected and gets ErrCredentialsAlreadyConsumed.
func (r *Repository) MarkDatabaseCredentialsConsumed(ctx context.Context, id uuid.UUID) error {
	const q = `
		UPDATE databases
		SET    credentials_consumed_at = NOW()
		WHERE  id = $1 AND credentials_consumed_at IS NULL`
	tag, err := r.pool.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("db mark database credentials consumed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrCredentialsAlreadyConsumed
	}
	return nil
}

// nilIfZeroInt returns nil for 0, otherwise a pointer to v. Used to keep
// endpoint_port NULL in the DB until the controller reports a real port
// (rather than storing 0, which would round-trip back as a non-empty value
// in JSON responses).
func nilIfZeroInt(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
}
