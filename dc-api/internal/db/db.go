// Package db provides the ResourceRepository — the only way handlers interact
// with PostgreSQL.
//
// ── DESIGN PATTERN: Repository Pattern ────────────────────────────────────────
//
// The Repository Pattern wraps all data access logic in a single struct.
// Handlers never write SQL — they call methods like repo.Create() or repo.UpdateStatus().
//
// Benefits for an SRE learning Go:
//   1. If you switch databases (PostgreSQL → CockroachDB), you only change db.go.
//   2. In tests, you can replace the real repository with a mock (a struct that
//      records calls without hitting a database). This makes unit tests fast and
//      deterministic — no need for a running PostgreSQL in CI.
//   3. All SQL is in one place. SQL injection vulnerabilities are easier to audit.
//
// Dependency Injection (related pattern):
//   The repository is created in main.go and INJECTED into handlers:
//     repo := db.NewRepository(pool)
//     vmHandler := handlers.NewVMHandler(repo, computeProvider)
//   The VMHandler does not call db.NewRepository() itself — it receives the
//   repository as an argument. This is Dependency Injection.
//   It means you can pass a *MockRepository in tests instead of a real one.
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wso2/dc-api/internal/models"
)

// Repository encapsulates all PostgreSQL operations for DC-API.
// All methods are safe for concurrent use (pgxpool is connection-pooled).
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository creates a Repository backed by the given connection pool.
// Call pgxpool.New() in main.go and pass the pool here.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Pool returns the underlying connection pool. Used only by integration tests
// that need to seed/manipulate rows directly (e.g. setting per-tenant quotas).
// Production code should NOT call this — use the typed Repository methods.
func (r *Repository) Pool() *pgxpool.Pool { return r.pool }

// Connect opens a pgxpool connection to the given DSN.
// Call this once in main.go; pass the returned pool to NewRepository.
func Connect(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse DB DSN: %w", err)
	}
	// Connection pool tuning: min 2 connections, max 20.
	// These are sensible defaults for a single DC-API instance.
	cfg.MinConns = 2
	cfg.MaxConns = 20
	cfg.MaxConnLifetime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open DB pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping DB: %w", err)
	}
	return pool, nil
}

// Create inserts a new Resource row and returns it with the auto-generated ID
// populated. Uses a PostgreSQL RETURNING clause to avoid a second round-trip.
//
// If a FAILED resource with the same (tenant_uuid, name, type) already exists
// it is deleted inside the same transaction before the insert. This allows
// retrying a failed VM/cluster with the same name without the caller having to
// manually clean up the dead row first.
//
// If the conflicting row is in any other state (PENDING, ACTIVE, DELETING) the
// insert will fail with a unique-constraint error — callers should convert that
// to a 409 Conflict.
func (r *Repository) Create(ctx context.Context, res *models.Resource) (*models.Resource, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("db begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Remove any FAILED tombstone with the same identity so the insert below
	// succeeds even on a direct retry after a provisioning failure.
	// Phase 6a: scope cleanup to tenant_uuid so a re-registered slug can't
	// accidentally purge a FAILED row from a prior tenant incarnation.
	_, err = tx.Exec(ctx,
		`DELETE FROM resources
		 WHERE tenant_uuid = $1 AND name = $2 AND type = $3 AND status = 'FAILED'`,
		res.TenantUUID, res.Name, string(res.Type),
	)
	if err != nil {
		return nil, fmt.Errorf("db cleanup failed resource: %w", err)
	}

	const q = `
		INSERT INTO resources
			(tenant_id, tenant_uuid, project_id, project_uuid, owner_id, name, type, size, status, provider_type, backend_uid, vnet_id, subnet_id, message)
		VALUES
			($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		RETURNING id, created_at, updated_at`

	row := tx.QueryRow(ctx, q,
		res.TenantID,
		res.TenantUUID,
		nilIfEmpty(res.ProjectID),
		nilIfNilUUID(res.ProjectUUID),
		res.OwnerID,
		res.Name,
		string(res.Type),
		nilIfEmpty(res.Size),
		string(res.Status),
		res.ProviderType,
		nilIfEmpty(res.BackendUID),
		res.VNetID,
		res.SubnetID,
		nilIfEmpty(res.Message),
	)
	if err := row.Scan(&res.ID, &res.CreatedAt, &res.UpdatedAt); err != nil {
		return nil, fmt.Errorf("db create resource: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("db commit create resource: %w", err)
	}
	return res, nil
}

// Get retrieves a Resource by its DC-API UUID, scoped to the given tenantUUID.
// Returns an error wrapping "not found" when the row is missing or belongs to a
// different tenant — callers should convert this to a 404.
// Phase 6a: added tenantUUID parameter so the WHERE clause enforces isolation
// at the DB layer, replacing the old post-fetch `if x.TenantID != tenantID` check.
func (r *Repository) Get(ctx context.Context, id uuid.UUID, tenantUUID uuid.UUID) (*models.Resource, error) {
	const q = `
		SELECT id, tenant_id, tenant_uuid, project_id, project_uuid, owner_id, name, type, size, status,
		       provider_type, backend_uid, ip_address, mgmt_ip, vnet_id, subnet_id, message, created_at, updated_at
		FROM   resources
		WHERE  id = $1 AND tenant_uuid = $2`

	var res models.Resource
	var size, backendUID, ipAddress, mgmtIP, message, projectID *string // nullable columns
	var projectUUID *uuid.UUID
	err := r.pool.QueryRow(ctx, q, id, tenantUUID).Scan(
		&res.ID, &res.TenantID, &res.TenantUUID,
		&projectID, &projectUUID,
		&res.OwnerID, &res.Name,
		&res.Type, &size, &res.Status,
		&res.ProviderType, &backendUID, &ipAddress, &mgmtIP, &res.VNetID, &res.SubnetID, &message,
		&res.CreatedAt, &res.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("resource %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("db get resource: %w", err)
	}
	if projectID != nil {
		res.ProjectID = *projectID
	}
	if projectUUID != nil {
		res.ProjectUUID = *projectUUID
	}
	if size != nil {
		res.Size = *size
	}
	if backendUID != nil {
		res.BackendUID = *backendUID
	}
	if ipAddress != nil {
		res.IPAddress = *ipAddress
	}
	if mgmtIP != nil {
		res.MgmtIP = *mgmtIP
	}
	if message != nil {
		res.Message = *message
	}
	return &res, nil
}

// GetForProject fetches a resource by ID scoped to BOTH its tenant and the
// active project. A resource that exists in the tenant but a different project
// is reported as not found — the immutable filter that keeps one project's VMs
// and clusters out of another's. Mirrors Get's not-found error so handlers 404
// uniformly.
func (r *Repository) GetForProject(ctx context.Context, id, tenantUUID, projectUUID uuid.UUID) (*models.Resource, error) {
	res, err := r.Get(ctx, id, tenantUUID)
	if err != nil {
		return nil, err
	}
	if res.ProjectUUID != projectUUID {
		return nil, fmt.Errorf("resource %s not found", id)
	}
	return res, nil
}

// GetResourceLocationByUUID resolves a resource's immutable UUID to its parent
// tenant slug and project UUID, searching every resource type — compute
// resources (VMs, clusters), key vaults, and databases — because a
// resource-scope role_assignment is keyed only by the resource UUID, not its
// type. The entry path uses it to admit a principal who holds only a
// resource-scope grant: the grant implies access to that resource's tenant and
// project for navigation, with the per-resource action gate still deciding what
// they may do. Returns found=false when no resource has that UUID, or when the
// resource is not bound to a project (no navigable entry point).
//
// MAINTENANCE: adding a new resource-scope-able type means extending the UNION
// below, the one in ListSharedResources, the one in AnyResourceInProject, and
// the kind mapping in handlers.sharedResourceKind.
func (r *Repository) GetResourceLocationByUUID(ctx context.Context, resourceUUID uuid.UUID) (tenantSlug string, projectUUID uuid.UUID, found bool, err error) {
	const q = `
		SELECT tenant_id, project_uuid FROM resources  WHERE id = $1
		UNION ALL
		SELECT tenant_id, project_uuid FROM key_vaults WHERE id = $1
		UNION ALL
		SELECT tenant_id, project_uuid FROM databases  WHERE id = $1
		LIMIT 1`
	var slug string
	var puuid *uuid.UUID
	err = r.pool.QueryRow(ctx, q, resourceUUID).Scan(&slug, &puuid)
	if err == pgx.ErrNoRows {
		return "", uuid.Nil, false, nil
	}
	if err != nil {
		return "", uuid.Nil, false, fmt.Errorf("db get resource location %s: %w", resourceUUID, err)
	}
	if puuid == nil {
		return "", uuid.Nil, false, nil
	}
	return slug, *puuid, true, nil
}

// AnyResourceInProject reports whether any of the given resource UUIDs lives in
// projectUUID, across every resource type. ProjectContext uses it to admit a
// resource-only user in a single query rather than resolving each grant one by
// one. An empty id list is false (no grants to match).
func (r *Repository) AnyResourceInProject(ctx context.Context, projectUUID uuid.UUID, resourceUUIDs []uuid.UUID) (bool, error) {
	if len(resourceUUIDs) == 0 {
		return false, nil
	}
	const q = `
		SELECT 1 FROM (
			SELECT project_uuid FROM resources  WHERE id = ANY($1)
			UNION ALL
			SELECT project_uuid FROM key_vaults WHERE id = ANY($1)
			UNION ALL
			SELECT project_uuid FROM databases  WHERE id = ANY($1)
		) t WHERE project_uuid = $2 LIMIT 1`
	var one int
	err := r.pool.QueryRow(ctx, q, resourceUUIDs, projectUUID).Scan(&one)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("db any resource in project %s: %w", projectUUID, err)
	}
	return true, nil
}

// SharedResource is the DB view of a resource reachable via a resource-scope
// grant — enough for the UI to list it and deep-link to its detail page. Type
// is the stored resource type (e.g. VIRTUAL_MACHINE, keyvault); the handler
// maps it to a URL path segment.
type SharedResource struct {
	ID        uuid.UUID
	Type      string
	Name      string
	ProjectID string
	Status    string
}

// ListSharedResources returns display info for the given resource UUIDs that
// live in tenantUUID, across every resource type (compute resources, key
// vaults, databases). The shared-resources entry endpoint uses it to show a
// resource-only user the resources they hold a direct grant on. Rows with no
// project (not navigable) are skipped.
func (r *Repository) ListSharedResources(ctx context.Context, tenantUUID uuid.UUID, ids []uuid.UUID) ([]SharedResource, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	const q = `
		SELECT id, name, project_id, status::text, type::text
		FROM   resources  WHERE tenant_uuid = $1 AND id = ANY($2)
		UNION ALL
		SELECT id, name, project_id, status::text, 'keyvault'
		FROM   key_vaults WHERE tenant_uuid = $1 AND id = ANY($2)
		UNION ALL
		SELECT id, name, project_id, status::text, 'database'
		FROM   databases  WHERE tenant_uuid = $1 AND id = ANY($2)`
	rows, err := r.pool.Query(ctx, q, tenantUUID, ids)
	if err != nil {
		return nil, fmt.Errorf("db list shared resources: %w", err)
	}
	defer rows.Close()
	out := make([]SharedResource, 0, len(ids))
	for rows.Next() {
		var sr SharedResource
		var projectID *string
		if err := rows.Scan(&sr.ID, &sr.Name, &projectID, &sr.Status, &sr.Type); err != nil {
			return nil, fmt.Errorf("scan shared resource: %w", err)
		}
		if projectID == nil {
			continue // not project-bound → no navigable link
		}
		sr.ProjectID = *projectID
		out = append(out, sr)
	}
	return out, rows.Err()
}

// GetInternal retrieves a Resource by its DC-API UUID without any tenant
// scope filter. Used by the reconciler and other internal callers that walk
// all tenants' resources for status synchronisation.
func (r *Repository) GetInternal(ctx context.Context, id uuid.UUID) (*models.Resource, error) {
	const q = `
		SELECT id, tenant_id, tenant_uuid, project_id, project_uuid, owner_id, name, type, size, status,
		       provider_type, backend_uid, ip_address, mgmt_ip, vnet_id, subnet_id, message, created_at, updated_at
		FROM   resources
		WHERE  id = $1`

	var res models.Resource
	var size, backendUID, ipAddress, mgmtIP, message, projectID *string
	var projectUUID *uuid.UUID
	err := r.pool.QueryRow(ctx, q, id).Scan(
		&res.ID, &res.TenantID, &res.TenantUUID,
		&projectID, &projectUUID,
		&res.OwnerID, &res.Name,
		&res.Type, &size, &res.Status,
		&res.ProviderType, &backendUID, &ipAddress, &mgmtIP, &res.VNetID, &res.SubnetID, &message,
		&res.CreatedAt, &res.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("resource %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("db get resource internal: %w", err)
	}
	if projectID != nil {
		res.ProjectID = *projectID
	}
	if projectUUID != nil {
		res.ProjectUUID = *projectUUID
	}
	if size != nil {
		res.Size = *size
	}
	if backendUID != nil {
		res.BackendUID = *backendUID
	}
	if ipAddress != nil {
		res.IPAddress = *ipAddress
	}
	if mgmtIP != nil {
		res.MgmtIP = *mgmtIP
	}
	if message != nil {
		res.Message = *message
	}
	return &res, nil
}

// ListByTenant returns all resources of a given type for a tenant, ordered by
// creation time (newest first). Phase 6a: filters on tenant_uuid (immutable)
// instead of tenant_id (slug) to prevent a re-registered slug from inheriting
// orphan rows.
func (r *Repository) ListByTenant(ctx context.Context, tenantUUID uuid.UUID, resType models.ResourceType) ([]*models.Resource, error) {
	const q = `
		SELECT id, tenant_id, tenant_uuid, project_id, project_uuid, owner_id, name, type, size, status,
		       provider_type, backend_uid, ip_address, mgmt_ip, vnet_id, subnet_id, message, created_at, updated_at
		FROM   resources
		WHERE  tenant_uuid = $1 AND type = $2
		ORDER  BY created_at DESC`

	rows, err := r.pool.Query(ctx, q, tenantUUID, string(resType))
	if err != nil {
		return nil, fmt.Errorf("db list resources: %w", err)
	}
	defer rows.Close()

	var results []*models.Resource
	for rows.Next() {
		var res models.Resource
		var size, backendUID, ipAddress, mgmtIP, message, projectID *string
		var projectUUID *uuid.UUID
		if err := rows.Scan(
			&res.ID, &res.TenantID, &res.TenantUUID,
			&projectID, &projectUUID,
			&res.OwnerID, &res.Name,
			&res.Type, &size, &res.Status,
			&res.ProviderType, &backendUID, &ipAddress, &mgmtIP, &res.VNetID, &res.SubnetID, &message,
			&res.CreatedAt, &res.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("db scan resource: %w", err)
		}
		if projectID != nil {
			res.ProjectID = *projectID
		}
		if projectUUID != nil {
			res.ProjectUUID = *projectUUID
		}
		if size != nil {
			res.Size = *size
		}
		if backendUID != nil {
			res.BackendUID = *backendUID
		}
		if ipAddress != nil {
			res.IPAddress = *ipAddress
		}
		if mgmtIP != nil {
			res.MgmtIP = *mgmtIP
		}
		if message != nil {
			res.Message = *message
		}
		results = append(results, &res)
	}
	return results, rows.Err()
}

// ListByProject returns all resources of a given type for a project, ordered by
// creation time (newest first). Used by project-scoped VM/cluster/bastion list endpoints.
func (r *Repository) ListByProject(ctx context.Context, tenantUUID, projectUUID uuid.UUID, resType models.ResourceType) ([]*models.Resource, error) {
	const q = `
		SELECT id, tenant_id, tenant_uuid, project_id, project_uuid, owner_id, name, type, size, status,
		       provider_type, backend_uid, ip_address, mgmt_ip, vnet_id, subnet_id, message, created_at, updated_at
		FROM   resources
		WHERE  tenant_uuid = $1 AND project_uuid = $2 AND type = $3
		ORDER  BY created_at DESC`

	rows, err := r.pool.Query(ctx, q, tenantUUID, projectUUID, string(resType))
	if err != nil {
		return nil, fmt.Errorf("db list resources by project: %w", err)
	}
	defer rows.Close()

	var results []*models.Resource
	for rows.Next() {
		var res models.Resource
		var size, backendUID, ipAddress, mgmtIP, message, projectID *string
		var pUUID *uuid.UUID
		if err := rows.Scan(
			&res.ID, &res.TenantID, &res.TenantUUID,
			&projectID, &pUUID,
			&res.OwnerID, &res.Name,
			&res.Type, &size, &res.Status,
			&res.ProviderType, &backendUID, &ipAddress, &mgmtIP, &res.VNetID, &res.SubnetID, &message,
			&res.CreatedAt, &res.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("db scan resource by project: %w", err)
		}
		if projectID != nil {
			res.ProjectID = *projectID
		}
		if pUUID != nil {
			res.ProjectUUID = *pUUID
		}
		if size != nil {
			res.Size = *size
		}
		if backendUID != nil {
			res.BackendUID = *backendUID
		}
		if ipAddress != nil {
			res.IPAddress = *ipAddress
		}
		if mgmtIP != nil {
			res.MgmtIP = *mgmtIP
		}
		if message != nil {
			res.Message = *message
		}
		results = append(results, &res)
	}
	return results, rows.Err()
}

// CountByProject counts active + pending resources of a given type for a project.
// Used by the quota engine for project-scoped VM/cluster limits.
func (r *Repository) CountByProject(ctx context.Context, tenantUUID, projectUUID uuid.UUID, resType models.ResourceType) (int, error) {
	const q = `
		SELECT COUNT(*) FROM resources
		WHERE  tenant_uuid = $1 AND project_uuid = $2 AND type = $3
		AND    status IN ('PENDING', 'ACTIVE')`

	var count int
	if err := r.pool.QueryRow(ctx, q, tenantUUID, projectUUID, string(resType)).Scan(&count); err != nil {
		return 0, fmt.Errorf("db count resources by project: %w", err)
	}
	return count, nil
}

// ListResourcesBySubnet returns every resource row whose subnet_id column
// points at the given subnet (any status). Used by SubnetHandler.Delete to
// refuse with 409 when a VM / bastion / cluster is still attached — without
// the pre-flight check the delete asynchronously fails inside KubeOVN's
// finalizer with the subnet stuck in FAILED until the resources are torn down.
//
// We return all rows regardless of status: even a FAILED resource may still
// have a live LSP if it failed after the NIC was allocated, and the caller
// must decide what to do about each row by name+type.
func (r *Repository) ListResourcesBySubnet(ctx context.Context, subnetID uuid.UUID) ([]*models.Resource, error) {
	const q = `
		SELECT id, tenant_id, tenant_uuid, project_id, project_uuid, owner_id, name, type, size, status,
		       provider_type, backend_uid, ip_address, mgmt_ip, vnet_id, subnet_id, message, created_at, updated_at
		FROM   resources
		WHERE  subnet_id = $1
		ORDER  BY created_at DESC`

	rows, err := r.pool.Query(ctx, q, subnetID)
	if err != nil {
		return nil, fmt.Errorf("db list resources by subnet: %w", err)
	}
	defer rows.Close()

	var results []*models.Resource
	for rows.Next() {
		var res models.Resource
		var size, backendUID, ipAddress, mgmtIP, message, projectID *string
		var projectUUID *uuid.UUID
		if err := rows.Scan(
			&res.ID, &res.TenantID, &res.TenantUUID,
			&projectID, &projectUUID,
			&res.OwnerID, &res.Name,
			&res.Type, &size, &res.Status,
			&res.ProviderType, &backendUID, &ipAddress, &mgmtIP, &res.VNetID, &res.SubnetID, &message,
			&res.CreatedAt, &res.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("db scan resource by subnet: %w", err)
		}
		if projectID != nil {
			res.ProjectID = *projectID
		}
		if projectUUID != nil {
			res.ProjectUUID = *projectUUID
		}
		if size != nil {
			res.Size = *size
		}
		if backendUID != nil {
			res.BackendUID = *backendUID
		}
		if ipAddress != nil {
			res.IPAddress = *ipAddress
		}
		if mgmtIP != nil {
			res.MgmtIP = *mgmtIP
		}
		if message != nil {
			res.Message = *message
		}
		results = append(results, &res)
	}
	return results, rows.Err()
}

// CountByTenant counts active + pending resources of a given type for a tenant.
// Used by the quota engine before allowing new provisioning.
// Phase 6a: filters on tenant_uuid (immutable) instead of tenant_id.
func (r *Repository) CountByTenant(ctx context.Context, tenantUUID uuid.UUID, resType models.ResourceType) (int, error) {
	const q = `
		SELECT COUNT(*) FROM resources
		WHERE  tenant_uuid = $1 AND type = $2
		AND    status IN ('PENDING', 'ACTIVE')`

	var count int
	if err := r.pool.QueryRow(ctx, q, tenantUUID, string(resType)).Scan(&count); err != nil {
		return 0, fmt.Errorf("db count resources: %w", err)
	}
	return count, nil
}

// UpdateStatus updates the status and message of a resource.
// The message field stores human-readable detail (e.g., error messages from providers).
// updated_at is handled automatically by the PostgreSQL trigger.
func (r *Repository) UpdateStatus(ctx context.Context, id uuid.UUID, status models.ResourceStatus, message string, backendUID string) error {
	const q = `
		UPDATE resources
		SET    status = $2, message = $3, backend_uid = COALESCE(NULLIF($4, ''), backend_uid)
		WHERE  id = $1`

	_, err := r.pool.Exec(ctx, q, id, string(status), message, backendUID)
	if err != nil {
		return fmt.Errorf("db update status for %s: %w", id, err)
	}
	return nil
}

// GetQuota retrieves the quota for a tenant.
// If no quota row exists, returns the __default__ quota.
// The quotas table still uses tenant_id as primary key because __default__ is
// a non-UUID fixture. We keep filtering by slug here; the TenantUUID field is
// populated when available.
func (r *Repository) GetQuota(ctx context.Context, tenantID string) (*models.Quota, error) {
	const q = `
		SELECT tenant_id, COALESCE(tenant_uuid, '00000000-0000-0000-0000-000000000000'::uuid),
		       max_vms, max_clusters, max_cpu, max_memory_gb
		FROM   quotas
		WHERE  tenant_id = $1 OR tenant_id = '__default__'
		ORDER  BY CASE WHEN tenant_id = $1 THEN 0 ELSE 1 END
		LIMIT  1`

	var quota models.Quota
	err := r.pool.QueryRow(ctx, q, tenantID).Scan(
		&quota.TenantID, &quota.TenantUUID,
		&quota.MaxVMs, &quota.MaxClusters,
		&quota.MaxCPU, &quota.MaxMemoryGB,
	)
	if err != nil {
		return nil, fmt.Errorf("db get quota for %s: %w", tenantID, err)
	}
	return &quota, nil
}

// ListPending returns all resources in PENDING or DELETING state that have a
// backend_uid. These are the resources the reconciler needs to poll.
//
// Also returns ACTIVE rows missing an IP, so the reconciler can backfill it
// from qemu-guest-agent later. F10: same for bastions missing mgmt_ip.
func (r *Repository) ListPending(ctx context.Context) ([]*models.Resource, error) {
	const q = `
		SELECT id, tenant_id, tenant_uuid, project_id, project_uuid, owner_id, name, type, size, status,
		       provider_type, backend_uid, ip_address, mgmt_ip, vnet_id, subnet_id, message, created_at, updated_at
		FROM   resources
		WHERE  backend_uid IS NOT NULL
		AND    (
		    status IN ('PENDING', 'DELETING')
		    OR (status = 'ACTIVE' AND type = 'VIRTUAL_MACHINE' AND ip_address IS NULL)
		    OR (status = 'ACTIVE' AND type = 'BASTION' AND (ip_address IS NULL OR mgmt_ip IS NULL))
		)
		ORDER  BY created_at ASC`

	rows, err := r.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("db list pending resources: %w", err)
	}
	defer rows.Close()

	var results []*models.Resource
	for rows.Next() {
		var res models.Resource
		var size, backendUID, ipAddress, mgmtIP, message, projectID *string
		var projectUUID *uuid.UUID
		if err := rows.Scan(
			&res.ID, &res.TenantID, &res.TenantUUID,
			&projectID, &projectUUID,
			&res.OwnerID, &res.Name,
			&res.Type, &size, &res.Status,
			&res.ProviderType, &backendUID, &ipAddress, &mgmtIP, &res.VNetID, &res.SubnetID, &message,
			&res.CreatedAt, &res.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("db scan pending resource: %w", err)
		}
		if projectID != nil {
			res.ProjectID = *projectID
		}
		if projectUUID != nil {
			res.ProjectUUID = *projectUUID
		}
		if size != nil {
			res.Size = *size
		}
		if backendUID != nil {
			res.BackendUID = *backendUID
		}
		if ipAddress != nil {
			res.IPAddress = *ipAddress
		}
		if mgmtIP != nil {
			res.MgmtIP = *mgmtIP
		}
		if message != nil {
			res.Message = *message
		}
		results = append(results, &res)
	}
	return results, rows.Err()
}

// UpdateIPAddress stores the IP address reported by qemu-guest-agent.
// Called by the reconciler when a VM transitions to ACTIVE and has an IP.
func (r *Repository) UpdateIPAddress(ctx context.Context, id uuid.UUID, ip string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE resources SET ip_address = $2 WHERE id = $1`,
		id, nilIfEmpty(ip),
	)
	if err != nil {
		return fmt.Errorf("db update ip_address for %s: %w", id, err)
	}
	return nil
}

// UpdateMgmtIP stores the mgmt-VLAN IP of a bastion (F10). Mirrors the
// UpdateIPAddress pattern — the reconciler calls this when qemu-guest-agent
// reports the IP on the bastion's second NIC.
func (r *Repository) UpdateMgmtIP(ctx context.Context, id uuid.UUID, ip string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE resources SET mgmt_ip = $2 WHERE id = $1`,
		id, nilIfEmpty(ip),
	)
	if err != nil {
		return fmt.Errorf("db update mgmt_ip for %s: %w", id, err)
	}
	return nil
}

// Delete removes a resource row by ID. Used by the reconciler when a DELETING
// resource is confirmed gone from the provider.
func (r *Repository) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM resources WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("db delete resource %s: %w", id, err)
	}
	return nil
}

// AppendAuditEvent records a state transition. This is append-only — never updated.
func (r *Repository) AppendAuditEvent(ctx context.Context, ev *models.AuditEvent) error {
	const q = `
		INSERT INTO audit_events
			(resource_id, actor_id, action, from_status, to_status, message)
		VALUES ($1, $2, $3, $4, $5, $6)`

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

// ─────────────────────────── Helpers ────────────────────────────────────────

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nilIfStatusEmpty(s models.ResourceStatus) *string {
	if s == "" {
		return nil
	}
	str := string(s)
	return &str
}

// nilIfNilUUID returns nil when id is the zero UUID, otherwise returns &id.
// Used to write nullable project_uuid / tenant_uuid columns: a zero UUID is
// semantically "not set" and should be stored as NULL rather than the all-zeros
// UUID sentinel.
func nilIfNilUUID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}
