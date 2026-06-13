// Package db — private_endpoint.go
//
// Repository methods for M3 Private Endpoints. Generic by target_type +
// target_id so the same code path serves Key Vault today and any other
// managed service later.
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

// ErrPrivateEndpointNotFound is returned when a lookup misses.
var ErrPrivateEndpointNotFound = errors.New("private endpoint not found")

// CreatePrivateEndpoint inserts a new endpoint row. Status is set by the caller
// (typically PENDING before the provisioner runs, ACTIVE after). The unique
// (target_type, target_id, vnet_id) constraint maps to 409 Conflict.
// M2.5: includes project_id, project_uuid in INSERT.
func (r *Repository) CreatePrivateEndpoint(ctx context.Context, ep *models.PrivateEndpoint) (*models.PrivateEndpoint, error) {
	const q = `
		INSERT INTO private_endpoints
		    (tenant_id, tenant_uuid, project_id, project_uuid, target_type, target_id, vnet_id, subnet_id, name,
		     ip_address, hostname, backend_addr, proxy_pod_name, status, message, region)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		RETURNING id, created_at, updated_at`

	var ipPtr, hostnamePtr, proxyPodPtr, messagePtr *string
	if ep.IPAddress != "" {
		ipPtr = &ep.IPAddress
	}
	if ep.Hostname != "" {
		hostnamePtr = &ep.Hostname
	}
	if ep.ProxyPodName != "" {
		proxyPodPtr = &ep.ProxyPodName
	}
	if ep.Message != "" {
		messagePtr = &ep.Message
	}
	if err := r.pool.QueryRow(ctx, q,
		ep.TenantID, ep.TenantUUID,
		nilIfEmpty(ep.ProjectID), nilIfNilUUID(ep.ProjectUUID),
		string(ep.TargetType), ep.TargetID, ep.VNetID, ep.SubnetID, ep.Name,
		ipPtr, hostnamePtr, ep.BackendAddr, proxyPodPtr, string(ep.Status), messagePtr, r.regionStamp(),
	).Scan(&ep.ID, &ep.CreatedAt, &ep.UpdatedAt); err != nil {
		return nil, fmt.Errorf("db create private_endpoint: %w", err)
	}
	r.recordAudit(ctx, r.pool, auditInsert{
		ID: ep.ID, Name: ep.Name, Kind: "PRIVATE_ENDPOINT",
		TenantUUID: &ep.TenantUUID, ProjectUUID: nilIfNilUUID(ep.ProjectUUID),
		Action: audit.ActionCreate, To: ep.Status,
	})
	return ep, nil
}

// GetPrivateEndpoint returns the endpoint by ID.
func (r *Repository) GetPrivateEndpoint(ctx context.Context, id uuid.UUID) (*models.PrivateEndpoint, error) {
	return r.queryEndpoint(ctx,
		`SELECT id, tenant_id, tenant_uuid, project_id, project_uuid, target_type, target_id, vnet_id, subnet_id, name,
		        COALESCE(ip_address::TEXT, ''), hostname, backend_addr, proxy_pod_name, status, message,
		        created_at, updated_at
		 FROM   private_endpoints
		 WHERE  id = $1`,
		id)
}

// ListPrivateEndpointsByTarget returns all endpoints for a given service
// resource (e.g. all endpoints for one Key Vault).
func (r *Repository) ListPrivateEndpointsByTarget(
	ctx context.Context,
	targetType models.PrivateEndpointTargetType,
	targetID uuid.UUID,
) ([]*models.PrivateEndpoint, error) {
	return r.queryEndpoints(ctx,
		`SELECT id, tenant_id, tenant_uuid, project_id, project_uuid, target_type, target_id, vnet_id, subnet_id, name,
		        COALESCE(ip_address::TEXT, ''), hostname, backend_addr, proxy_pod_name, status, message,
		        created_at, updated_at
		 FROM   private_endpoints
		 WHERE  target_type = $1 AND target_id = $2
		 ORDER  BY created_at DESC`,
		string(targetType), targetID)
}

// ListPrivateEndpointsByVNet returns endpoints anchored on a VNet — useful
// during VNet teardown to drain dependent endpoints first.
func (r *Repository) ListPrivateEndpointsByVNet(ctx context.Context, vnetID uuid.UUID) ([]*models.PrivateEndpoint, error) {
	return r.queryEndpoints(ctx,
		`SELECT id, tenant_id, tenant_uuid, project_id, project_uuid, target_type, target_id, vnet_id, subnet_id, name,
		        COALESCE(ip_address::TEXT, ''), hostname, backend_addr, proxy_pod_name, status, message,
		        created_at, updated_at
		 FROM   private_endpoints
		 WHERE  vnet_id = $1`,
		vnetID)
}

// UpdatePrivateEndpointStatus is used by the provisioner to flip PENDING→ACTIVE
// (or FAILED) once the network plumbing is in place.
func (r *Repository) UpdatePrivateEndpointStatus(
	ctx context.Context,
	id uuid.UUID,
	status models.ResourceStatus,
	message string,
	ip, hostname, proxyPod string,
) error {
	// COALESCE requires both arguments to be the same type. ip_address is
	// inet, so cast the NULLIF result (text) to inet first before COALESCE.
	// COALESCE requires both arguments to be the same type. ip_address is
	// inet, so cast the NULLIF result (text) to inet first before COALESCE.
	const q = `
		UPDATE private_endpoints t
		SET    status         = $2,
		       message        = $3,
		       ip_address     = COALESCE(NULLIF($4, '')::inet, t.ip_address),
		       hostname       = COALESCE(NULLIF($5, ''),       t.hostname),
		       proxy_pod_name = COALESCE(NULLIF($6, ''),       t.proxy_pod_name)
		FROM   private_endpoints old
		WHERE  t.id = $1 AND old.id = t.id
		RETURNING old.status::text, t.name, t.tenant_uuid, t.project_uuid`
	var from, name string
	var tuid, puid *uuid.UUID
	err := r.pool.QueryRow(ctx, q, id, string(status), message, ip, hostname, proxyPod).
		Scan(&from, &name, &tuid, &puid)
	if err == pgx.ErrNoRows {
		return ErrPrivateEndpointNotFound
	}
	if err != nil {
		return fmt.Errorf("db update private_endpoint status: %w", err)
	}
	r.recordAudit(ctx, r.pool, auditInsert{
		ID: id, Name: name, Kind: "PRIVATE_ENDPOINT", TenantUUID: tuid, ProjectUUID: puid,
		Action: audit.ActionStatusChange,
		From:   models.ResourceStatus(from), To: status, Message: message,
	})
	return nil
}

// DeletePrivateEndpoint removes the row. The handler is responsible for
// having torn down the provisioner-side resources first.
func (r *Repository) DeletePrivateEndpoint(ctx context.Context, id uuid.UUID) error {
	const q = `
		DELETE FROM private_endpoints
		WHERE  id = $1
		RETURNING status::text, name, tenant_uuid, project_uuid`
	var from, name string
	var tuid, puid *uuid.UUID
	err := r.pool.QueryRow(ctx, q, id).Scan(&from, &name, &tuid, &puid)
	if err == pgx.ErrNoRows {
		return ErrPrivateEndpointNotFound
	}
	if err != nil {
		return fmt.Errorf("db delete private_endpoint: %w", err)
	}
	r.recordAudit(ctx, r.pool, auditInsert{
		ID: id, Name: name, Kind: "PRIVATE_ENDPOINT", TenantUUID: tuid, ProjectUUID: puid,
		Action: audit.ActionDelete,
		From:   models.ResourceStatus(from), To: models.StatusDeleted,
	})
	return nil
}

// ── Scan helpers ─────────────────────────────────────────────────────────────

func (r *Repository) queryEndpoint(ctx context.Context, q string, args ...interface{}) (*models.PrivateEndpoint, error) {
	row := r.pool.QueryRow(ctx, q, args...)
	ep, err := scanEndpoint(row.Scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPrivateEndpointNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db get private_endpoint: %w", err)
	}
	return ep, nil
}

func (r *Repository) queryEndpoints(ctx context.Context, q string, args ...interface{}) ([]*models.PrivateEndpoint, error) {
	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("db list private_endpoints: %w", err)
	}
	defer rows.Close()
	var out []*models.PrivateEndpoint
	for rows.Next() {
		ep, err := scanEndpoint(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("db scan private_endpoint: %w", err)
		}
		out = append(out, ep)
	}
	return out, rows.Err()
}

// scanEndpoint reads one row into a PrivateEndpoint, handling all the
// nullable columns uniformly. The scanFn is a closure over either pgx.Row.Scan
// (single-row Get) or pgx.Rows.Scan (list iteration) — both share the same
// argument layout so the column list only lives in one place.
// M2.5: also scans project_id, project_uuid (nullable for pre-M2.5 rows).
func scanEndpoint(scanFn func(dest ...interface{}) error) (*models.PrivateEndpoint, error) {
	var ep models.PrivateEndpoint
	var targetType, ipText string
	var tenantUUID *uuid.UUID  // nullable pre-backfill
	var projectID *string
	var projectUUID *uuid.UUID // nullable for pre-M2.5 rows
	var hostname, proxyPod, message *string
	if err := scanFn(
		&ep.ID, &ep.TenantID, &tenantUUID, &projectID, &projectUUID,
		&targetType, &ep.TargetID, &ep.VNetID, &ep.SubnetID, &ep.Name,
		&ipText, &hostname, &ep.BackendAddr, &proxyPod, &ep.Status, &message,
		&ep.CreatedAt, &ep.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if tenantUUID != nil {
		ep.TenantUUID = *tenantUUID
	}
	if projectID != nil {
		ep.ProjectID = *projectID
	}
	if projectUUID != nil {
		ep.ProjectUUID = *projectUUID
	}
	ep.TargetType = models.PrivateEndpointTargetType(targetType)
	ep.IPAddress = ipText
	if hostname != nil {
		ep.Hostname = *hostname
	}
	if proxyPod != nil {
		ep.ProxyPodName = *proxyPod
	}
	if message != nil {
		ep.Message = *message
	}
	return &ep, nil
}
