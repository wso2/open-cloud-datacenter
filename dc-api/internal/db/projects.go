// Package db — projects.go
//
// Repository methods for the `projects` table. Projects sit beneath Tenants;
// every per-resource table carries a project_uuid FK referencing this table.
//
// Patterns mirror tenants.go:
//   - scanProject for DRY column scanning.
//   - GetProjectUUIDByTenantAndSlug for the hot-path middleware lookup.
//   - DeleteProject refuses if any per-resource table has rows for this project.
package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/wso2/dc-api/internal/models"
)

const projectCols = `id, tenant_id, tenant_uuid, project_uuid, name, description, cpu_cores, memory_gb, storage_gb, created_at, updated_at, created_by`

// scanProject reads one project row (in projectCols order) into a models.Project.
func scanProject(row pgx.Row, p *models.Project) error {
	var desc *string
	if err := row.Scan(
		&p.ID, &p.TenantID, &p.TenantUUID, &p.ProjectUUID,
		&p.Name, &desc,
		&p.CPUCores, &p.MemoryGB, &p.StorageGB,
		&p.CreatedAt, &p.UpdatedAt, &p.CreatedBy,
	); err != nil {
		return err
	}
	if desc != nil {
		p.Description = *desc
	}
	return nil
}

// GetTenantSlugByProjectUUID resolves a project's immutable UUID to its parent
// tenant slug. The entry path (GET /v1/tenants and TenantContext) uses it to
// admit a principal who holds only a project-scope grant to that project's
// tenant — a project grant implies access to its tenant for navigation, with
// per-project access still gated by ProjectContext below. Returns ("", nil) if
// no project has that UUID.
func (r *Repository) GetTenantSlugByProjectUUID(ctx context.Context, projectUUID uuid.UUID) (string, error) {
	var tenantSlug string
	err := r.pool.QueryRow(ctx,
		`SELECT tenant_id FROM projects WHERE project_uuid = $1`, projectUUID,
	).Scan(&tenantSlug)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("db get tenant slug by project uuid %s: %w", projectUUID, err)
	}
	return tenantSlug, nil
}

// ErrProjectAlreadyExists is returned by CreateProject when the slug is taken
// within the same tenant. Distinct from a generic DB error so the handler can
// map it to 409.
var ErrProjectAlreadyExists = errors.New("project already exists")

// ErrProjectNotEmpty is returned by DeleteProject when any per-resource table
// still has rows referencing this project_uuid. Maps to 409.
var ErrProjectNotEmpty = errors.New("project has active resources; delete them first")

// CreateProject inserts a new project row and its companion project_quotas row.
// Uses a transaction so both rows appear atomically. Also enforces the tenant
// cap: the new project's quotas plus the sum of existing projects' quotas in
// the same tenant must not exceed the tenant's cap. Returns
// ErrProjectQuotaExceedsTenantCap (with a populated TenantCapUsage) when the
// cap would be breached, so the handler can render a quota_exceeded body.
//
// The `SELECT FOR UPDATE` on the tenants row serialises concurrent creates;
// without it two simultaneous POSTs could both pass the SUM check and slide
// past the cap.
func (r *Repository) CreateProject(ctx context.Context, p models.Project, q models.ProjectQuota) (*models.Project, *models.ProjectQuota, *models.TenantCapUsage, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("db create project begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Lock the tenant row + read its cap.
	var cap models.TenantCap
	const tenantQ = `
		SELECT cpu_cores_cap, memory_gb_cap, storage_gb_cap
		FROM   tenants
		WHERE  tenant_uuid = $1
		FOR UPDATE`
	if err := tx.QueryRow(ctx, tenantQ, p.TenantUUID).Scan(&cap.CPUCores, &cap.MemoryGB, &cap.StorageGB); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, nil, fmt.Errorf("db create project: tenant %s not found", p.TenantUUID)
		}
		return nil, nil, nil, fmt.Errorf("db lock tenant row: %w", err)
	}

	// Sum existing project allocations within this tenant.
	var alloc models.TenantCap
	const allocQ = `
		SELECT COALESCE(SUM(cpu_cores), 0)::INTEGER,
		       COALESCE(SUM(memory_gb), 0)::INTEGER,
		       COALESCE(SUM(storage_gb), 0)::INTEGER
		FROM   projects
		WHERE  tenant_uuid = $1`
	if err := tx.QueryRow(ctx, allocQ, p.TenantUUID).Scan(&alloc.CPUCores, &alloc.MemoryGB, &alloc.StorageGB); err != nil {
		return nil, nil, nil, fmt.Errorf("db sum project allocation: %w", err)
	}

	// Cap check: would adding this project breach any dimension?
	wouldAlloc := models.TenantCap{
		CPUCores:  alloc.CPUCores + p.CPUCores,
		MemoryGB:  alloc.MemoryGB + p.MemoryGB,
		StorageGB: alloc.StorageGB + p.StorageGB,
	}
	if wouldAlloc.CPUCores > cap.CPUCores || wouldAlloc.MemoryGB > cap.MemoryGB || wouldAlloc.StorageGB > cap.StorageGB {
		usage := &models.TenantCapUsage{
			Cap:       cap,
			Allocated: alloc,
			Available: models.TenantCap{
				CPUCores:  cap.CPUCores - alloc.CPUCores,
				MemoryGB:  cap.MemoryGB - alloc.MemoryGB,
				StorageGB: cap.StorageGB - alloc.StorageGB,
			},
		}
		return nil, nil, usage, ErrProjectQuotaExceedsTenantCap
	}

	insertProject := `
		INSERT INTO projects
			(id, tenant_id, tenant_uuid, name, description, cpu_cores, memory_gb, storage_gb, created_by)
		VALUES ($1, $2, $3, $4, NULLIF($5,''), $6, $7, $8, $9)
		RETURNING ` + projectCols

	row := tx.QueryRow(ctx, insertProject,
		p.ID, p.TenantID, p.TenantUUID, p.Name, p.Description,
		p.CPUCores, p.MemoryGB, p.StorageGB, p.CreatedBy,
	)
	var out models.Project
	if err := scanProject(row, &out); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, nil, nil, ErrProjectAlreadyExists
		}
		return nil, nil, nil, fmt.Errorf("db create project: %w", err)
	}

	insertQuota := `
		INSERT INTO project_quotas (project_uuid, max_vnets, max_clusters, max_volumes, max_public_ips)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING project_uuid, max_vnets, max_clusters, max_volumes, max_public_ips, updated_at`

	var outQ models.ProjectQuota
	qRow := tx.QueryRow(ctx, insertQuota,
		out.ProjectUUID, q.MaxVNets, q.MaxClusters, q.MaxVolumes, q.MaxPublicIPs,
	)
	if err := qRow.Scan(
		&outQ.ProjectUUID, &outQ.MaxVNets, &outQ.MaxClusters, &outQ.MaxVolumes, &outQ.MaxPublicIPs, &outQ.UpdatedAt,
	); err != nil {
		return nil, nil, nil, fmt.Errorf("db create project quotas: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, nil, fmt.Errorf("db create project commit: %w", err)
	}
	return &out, &outQ, nil, nil
}

// UpdateProjectQuota changes a project's capacity quotas with both invariants
// enforced inside the same transaction:
//
//  1. New quota must be ≥ in-use sum across active resources in the project
//     (refusing to shrink below what's actually consuming the budget).
//  2. New quota + other-projects' allocations must be ≤ tenant cap (refusing
//     to push the tenant over its ceiling).
//
// Returns the updated Project on success. On (1) returns ErrProjectQuotaBelowUsage
// with a populated TenantCapUsage describing the in-use sums (overloaded as
// the "min" instead of allocated). On (2) returns ErrProjectQuotaExceedsTenantCap
// with the standard cap usage view.
func (r *Repository) UpdateProjectQuota(ctx context.Context, projectUUID uuid.UUID, newQuota models.TenantCap) (*models.Project, *models.TenantCapUsage, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("db update project quota begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Resolve project row first to get its tenant_uuid.
	var existing models.Project
	const projQ = `SELECT ` + projectCols + ` FROM projects WHERE project_uuid = $1 FOR UPDATE`
	if err := scanProject(tx.QueryRow(ctx, projQ, projectUUID), &existing); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, fmt.Errorf("db update project quota: project %s not found", projectUUID)
		}
		return nil, nil, fmt.Errorf("db lock project row: %w", err)
	}

	// Lock the tenants row (must be acquired AFTER the project row to avoid
	// reverse-order deadlock with CreateProject, which locks tenant first
	// then projects-via-INSERT — TODO: standardise lock order in a future
	// pass, today this is safe because no caller takes tenants→project lock
	// after this method runs).
	var cap models.TenantCap
	const tenantQ = `
		SELECT cpu_cores_cap, memory_gb_cap, storage_gb_cap
		FROM   tenants
		WHERE  tenant_uuid = $1
		FOR UPDATE`
	if err := tx.QueryRow(ctx, tenantQ, existing.TenantUUID).Scan(&cap.CPUCores, &cap.MemoryGB, &cap.StorageGB); err != nil {
		return nil, nil, fmt.Errorf("db lock tenant row: %w", err)
	}

	// (1) Per-project in-use check. Walks resource tables that consume
	// cpu/memory/storage and sums spec usage across rows whose status is
	// PENDING or ACTIVE (DELETING is in flight; FAILED won't consume).
	inUse, err := sumProjectResourceUsageTx(ctx, tx, projectUUID)
	if err != nil {
		return nil, nil, fmt.Errorf("db sum project usage: %w", err)
	}
	if newQuota.CPUCores < inUse.CPUCores || newQuota.MemoryGB < inUse.MemoryGB || newQuota.StorageGB < inUse.StorageGB {
		usage := &models.TenantCapUsage{
			Cap:       newQuota,    // requested
			Allocated: inUse,       // actually in use
			Available: models.TenantCap{}, // not meaningful for this error
		}
		return nil, usage, ErrProjectQuotaBelowUsage
	}

	// (2) Tenant-cap check: sum of OTHER projects + new quota ≤ tenant cap.
	var otherAlloc models.TenantCap
	const otherQ = `
		SELECT COALESCE(SUM(cpu_cores), 0)::INTEGER,
		       COALESCE(SUM(memory_gb), 0)::INTEGER,
		       COALESCE(SUM(storage_gb), 0)::INTEGER
		FROM   projects
		WHERE  tenant_uuid = $1 AND project_uuid <> $2`
	if err := tx.QueryRow(ctx, otherQ, existing.TenantUUID, projectUUID).Scan(&otherAlloc.CPUCores, &otherAlloc.MemoryGB, &otherAlloc.StorageGB); err != nil {
		return nil, nil, fmt.Errorf("db sum other-project allocation: %w", err)
	}
	wouldAlloc := models.TenantCap{
		CPUCores:  otherAlloc.CPUCores + newQuota.CPUCores,
		MemoryGB:  otherAlloc.MemoryGB + newQuota.MemoryGB,
		StorageGB: otherAlloc.StorageGB + newQuota.StorageGB,
	}
	if wouldAlloc.CPUCores > cap.CPUCores || wouldAlloc.MemoryGB > cap.MemoryGB || wouldAlloc.StorageGB > cap.StorageGB {
		usage := &models.TenantCapUsage{
			Cap:       cap,
			Allocated: otherAlloc, // other projects' allocation
			Available: models.TenantCap{
				CPUCores:  cap.CPUCores - otherAlloc.CPUCores,
				MemoryGB:  cap.MemoryGB - otherAlloc.MemoryGB,
				StorageGB: cap.StorageGB - otherAlloc.StorageGB,
			},
		}
		return nil, usage, ErrProjectQuotaExceedsTenantCap
	}

	// Apply the update.
	const updQ = `
		UPDATE projects
		SET    cpu_cores = $2, memory_gb = $3, storage_gb = $4
		WHERE  project_uuid = $1
		RETURNING ` + projectCols
	row := tx.QueryRow(ctx, updQ, projectUUID, newQuota.CPUCores, newQuota.MemoryGB, newQuota.StorageGB)
	var out models.Project
	if err := scanProject(row, &out); err != nil {
		return nil, nil, fmt.Errorf("db update project quota: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, fmt.Errorf("db update project quota commit: %w", err)
	}
	return &out, nil, nil
}

// sumProjectResourceUsageTx walks resource tables that consume project capacity
// and sums spec usage for rows in PENDING or ACTIVE state. DELETING is
// excluded (in-flight teardown); FAILED is excluded (never consumed).
//
// Current scope: VMs (cpu+memory from spec_json), clusters (cpu+memory ×
// machine_pool_size), bastions (fixed small footprint). Storage is summed
// from any volumes table once that lands; for now it's 0.
//
// TODO(M2.5 polish): wire actual storage sum once the volumes table exists.
// TODO(observability): cache result with short TTL to avoid 4 SUMs per
// project update on hot paths.
func sumProjectResourceUsageTx(ctx context.Context, tx pgx.Tx, projectUUID uuid.UUID) (models.TenantCap, error) {
	var u models.TenantCap

	// Resources table holds VMs, clusters, bastions. Spec is JSONB.
	// VM: spec_json->>'cpu'  (int), 'memory_gb' (int).
	// Cluster: spec_json->>'cpu_per_node' × spec_json->>'node_count'.
	// Bastion: fixed allocation (2 CPU, 4 GB) — small, but counted.
	const q = `
		SELECT
			COALESCE(SUM(
				CASE type
					WHEN 'VIRTUAL_MACHINE' THEN COALESCE((metadata->>'cpu')::INTEGER, 0)
					WHEN 'CLUSTER'         THEN COALESCE((metadata->>'cpu_per_node')::INTEGER, 0) * COALESCE(machine_pool_size, 0)
					WHEN 'BASTION'         THEN 2
					ELSE 0
				END
			), 0)::INTEGER AS cpu_used,
			COALESCE(SUM(
				CASE type
					WHEN 'VIRTUAL_MACHINE' THEN COALESCE((metadata->>'memory_gb')::INTEGER, 0)
					WHEN 'CLUSTER'         THEN COALESCE((metadata->>'memory_per_node_gb')::INTEGER, 0) * COALESCE(machine_pool_size, 0)
					WHEN 'BASTION'         THEN 4
					ELSE 0
				END
			), 0)::INTEGER AS mem_used,
			0::INTEGER AS storage_used   -- TODO: sum from volumes when that table exists
		FROM   resources
		WHERE  project_uuid = $1
		  AND  status IN ('PENDING', 'ACTIVE')`

	if err := tx.QueryRow(ctx, q, projectUUID).Scan(&u.CPUCores, &u.MemoryGB, &u.StorageGB); err != nil {
		return u, fmt.Errorf("db sum project resource usage: %w", err)
	}
	return u, nil
}

// GetProject returns a project by (tenantID, projectSlug), or (nil, nil) when
// not found. Callers gate on nil for 404.
func (r *Repository) GetProject(ctx context.Context, tenantID, projectID string) (*models.Project, error) {
	q := `SELECT ` + projectCols + ` FROM projects WHERE tenant_id = $1 AND id = $2`
	row := r.pool.QueryRow(ctx, q, tenantID, projectID)
	var p models.Project
	if err := scanProject(row, &p); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("db get project: %w", err)
	}
	return &p, nil
}

// GetProjectQuota returns the object-guardrail quotas for a project, or
// (nil, nil) when no row exists.
func (r *Repository) GetProjectQuota(ctx context.Context, projectUUID uuid.UUID) (*models.ProjectQuota, error) {
	q := `SELECT project_uuid, max_vnets, max_clusters, max_volumes, max_public_ips, updated_at
		  FROM project_quotas WHERE project_uuid = $1`
	row := r.pool.QueryRow(ctx, q, projectUUID)
	var pq models.ProjectQuota
	if err := row.Scan(&pq.ProjectUUID, &pq.MaxVNets, &pq.MaxClusters, &pq.MaxVolumes, &pq.MaxPublicIPs, &pq.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("db get project quota: %w", err)
	}
	return &pq, nil
}

// ListProjects returns all projects for a tenant, newest first.
func (r *Repository) ListProjects(ctx context.Context, tenantUUID uuid.UUID) ([]models.Project, error) {
	q := `SELECT ` + projectCols + ` FROM projects WHERE tenant_uuid = $1 ORDER BY created_at DESC`
	rows, err := r.pool.Query(ctx, q, tenantUUID)
	if err != nil {
		return nil, fmt.Errorf("db list projects: %w", err)
	}
	defer rows.Close()

	var out []models.Project
	for rows.Next() {
		var p models.Project
		if err := scanProject(rows, &p); err != nil {
			return nil, fmt.Errorf("db list projects scan: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetProjectUUIDByTenantAndSlug is the hot-path middleware lookup. Returns
// (uuid.Nil, nil) when no row exists — callers gate on uuid.Nil for 404.
func (r *Repository) GetProjectUUIDByTenantAndSlug(ctx context.Context, tenantID, projectSlug string) (uuid.UUID, error) {
	var u uuid.UUID
	err := r.pool.QueryRow(ctx,
		`SELECT project_uuid FROM projects WHERE tenant_id = $1 AND id = $2`,
		tenantID, projectSlug,
	).Scan(&u)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, nil
		}
		return uuid.Nil, fmt.Errorf("db get project uuid by slug: %w", err)
	}
	return u, nil
}

// DeleteProject removes the project row if — and only if — no per-resource
// table still references this project_uuid. Returns ErrProjectNotEmpty when
// any row is found. The FK ON DELETE RESTRICT constraint is the DB-level guard;
// this check provides an application-level guard with a friendly error message.
//
// Note: the project_quotas row is removed via ON DELETE CASCADE on the FK.
func (r *Repository) DeleteProject(ctx context.Context, tenantID, projectID string) error {
	// First, look up the project_uuid so we can query the dependent tables.
	var projectUUID uuid.UUID
	err := r.pool.QueryRow(ctx,
		`SELECT project_uuid FROM projects WHERE tenant_id = $1 AND id = $2`,
		tenantID, projectID,
	).Scan(&projectUUID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // already gone — idempotent
	}
	if err != nil {
		return fmt.Errorf("db delete project lookup: %w", err)
	}

	// Check every per-resource table for live rows.
	tables := []string{
		"resources", "vnets", "subnets", "route_tables", "route_table_associations",
		"network_security_groups", "nsg_attachments", "peerings", "private_dns_zones",
		"dns_records", "service_accounts", "key_vaults", "private_endpoints",
	}
	for _, tbl := range tables {
		var count int
		q := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE project_uuid = $1`, tbl)
		if err := r.pool.QueryRow(ctx, q, projectUUID).Scan(&count); err != nil {
			return fmt.Errorf("db delete project check %s: %w", tbl, err)
		}
		if count > 0 {
			return ErrProjectNotEmpty
		}
	}

	if _, err := r.pool.Exec(ctx,
		`DELETE FROM projects WHERE tenant_id = $1 AND id = $2`,
		tenantID, projectID,
	); err != nil {
		return fmt.Errorf("db delete project: %w", err)
	}
	return nil
}
