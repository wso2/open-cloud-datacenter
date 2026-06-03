// Package db — rbac.go
//
// Repository methods for M1.5 RBAC: role_assignments and service_accounts.
//
// All methods are on *Repository (defined in db.go) — no separate connection
// is introduced. SQL lives here; handlers and the RBAC helper never call pool
// directly.
//
// Patterns mirror network.go:
//   - RETURNING clause on INSERT to avoid a second round-trip.
//   - Nullable columns scanned into pointer types (*string, *time.Time).
//   - "not found" returns (nil, nil) for lookup-style methods; callers convert
//     to 404 or a context-appropriate error.
//   - Delete is idempotent: no error if the row is already gone.
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/wso2/dc-api/internal/models"
)

// ─────────────────────────── Role Assignments ───────────────────────────────

// CreateRoleAssignment inserts a new role_assignments row and returns it with
// ID and GrantedAt populated from the database RETURNING clause.
//
// If the same (principal_type, principal_id, scope_type, scope_id, role) tuple
// already exists the INSERT will fail with a unique-constraint violation —
// the handler should convert that to 409 Conflict.
// Phase 6a: also writes scope_uuid so role lookups can use the immutable UUID.
func (r *Repository) CreateRoleAssignment(ctx context.Context, ra models.RoleAssignment) (*models.RoleAssignment, error) {
	// RBAC v2 keys scopes by UUID and the engine matches assignments by
	// scope_uuid, so it must be populated at write time — not left to the
	// Phase-6a backfill (which only runs on boot). When a caller leaves it unset
	// for a tenant-scoped grant, resolve it from the tenant slug here. Project-
	// and resource-scoped callers pass the UUID from request context.
	if ra.ScopeUUID == uuid.Nil && ra.ScopeType == models.ScopeTypeTenant {
		tu, err := r.GetTenantUUIDBySlug(ctx, ra.ScopeID)
		if err != nil {
			return nil, fmt.Errorf("db create role assignment: resolve tenant uuid for %q: %w", ra.ScopeID, err)
		}
		ra.ScopeUUID = tu
	}

	// Prefer an explicit v2 role-definition key; fall back to mapping the v1 rank.
	roleDef := ra.RoleDefinition
	if roleDef == "" {
		roleDef = models.RoleDefinitionForRole(ra.Role)
	}

	const q = `
		INSERT INTO role_assignments
			(principal_type, principal_id, scope_type, scope_id, scope_uuid, role_definition, granted_by, display_alias)
		VALUES
			($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''))
		RETURNING id, granted_at`

	err := r.pool.QueryRow(ctx, q,
		string(ra.PrincipalType),
		ra.PrincipalID,
		string(ra.ScopeType),
		ra.ScopeID,
		ra.ScopeUUID,
		roleDef,
		ra.GrantedBy,
		ra.DisplayAlias,
	).Scan(&ra.ID, &ra.GrantedAt)
	if err != nil {
		return nil, fmt.Errorf("db create role assignment: %w", err)
	}
	return &ra, nil
}

// scanRoleAssignment is a small helper to keep the scan list in one place —
// every selector below pulls the same columns in the same order.
// Phase 6a: also scans scope_uuid (immutable tenant reference).
// scope_uuid is nullable for rows created before the 6a migration, so we
// scan into a pointer and only copy when non-nil.
func scanRoleAssignment(scanner interface{ Scan(dest ...any) error }) (models.RoleAssignment, error) {
	var ra models.RoleAssignment
	var displayAlias *string
	var scopeUUID *uuid.UUID // nullable — pre-6a rows may have NULL
	var roleDef string       // RBAC v2: role_definition key; mapped back to the v1 rank below
	if err := scanner.Scan(
		&ra.ID,
		&ra.PrincipalType,
		&ra.PrincipalID,
		&ra.ScopeType,
		&ra.ScopeID,
		&scopeUUID,
		&roleDef,
		&ra.GrantedAt,
		&ra.GrantedBy,
		&displayAlias,
	); err != nil {
		return ra, err
	}
	ra.Role = models.RoleForRoleDefinition(roleDef)
	ra.RoleDefinition = roleDef // RBAC v2 key, consumed by requireAction / the engine
	if scopeUUID != nil {
		ra.ScopeUUID = *scopeUUID
	}
	if displayAlias != nil {
		ra.DisplayAlias = *displayAlias
	}
	return ra, nil
}

// GetRoleAssignment retrieves a single role_assignments row by its UUID.
// Returns (nil, nil) if the row does not exist — callers should treat that
// as a 404.
func (r *Repository) GetRoleAssignment(ctx context.Context, id uuid.UUID) (*models.RoleAssignment, error) {
	const q = `
		SELECT id, principal_type, principal_id, scope_type, scope_id, scope_uuid, role_definition,
		       granted_at, granted_by, display_alias
		FROM   role_assignments
		WHERE  id = $1`

	ra, err := scanRoleAssignment(r.pool.QueryRow(ctx, q, id))
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db get role assignment %s: %w", id, err)
	}
	return &ra, nil
}

// ListRoleAssignmentsForPrincipal returns all role assignments for a given
// principal (user or service account), across all scopes. Used by the scope-
// walk helper to compute effective role.
func (r *Repository) ListRoleAssignmentsForPrincipal(ctx context.Context, principalType models.PrincipalType, principalID string) ([]models.RoleAssignment, error) {
	const q = `
		SELECT id, principal_type, principal_id, scope_type, scope_id, scope_uuid, role_definition,
		       granted_at, granted_by, display_alias
		FROM   role_assignments
		WHERE  principal_type = $1 AND principal_id = $2
		ORDER  BY granted_at DESC`

	rows, err := r.pool.Query(ctx, q, string(principalType), principalID)
	if err != nil {
		return nil, fmt.Errorf("db list role assignments for principal: %w", err)
	}
	defer rows.Close()

	var results []models.RoleAssignment
	for rows.Next() {
		ra, err := scanRoleAssignment(rows)
		if err != nil {
			return nil, fmt.Errorf("db scan role assignment: %w", err)
		}
		results = append(results, ra)
	}
	return results, rows.Err()
}

// ListRoleAssignmentsForScope returns all role assignments for a specific scope
// (e.g., all principals that have a role on tenant "acme"). Used by the
// membership-management handlers.
func (r *Repository) ListRoleAssignmentsForScope(ctx context.Context, scopeType models.ScopeType, scopeID string) ([]models.RoleAssignment, error) {
	const q = `
		SELECT id, principal_type, principal_id, scope_type, scope_id, scope_uuid, role_definition,
		       granted_at, granted_by, display_alias
		FROM   role_assignments
		WHERE  scope_type = $1 AND scope_id = $2
		ORDER  BY granted_at DESC`

	rows, err := r.pool.Query(ctx, q, string(scopeType), scopeID)
	if err != nil {
		return nil, fmt.Errorf("db list role assignments for scope: %w", err)
	}
	defer rows.Close()

	var results []models.RoleAssignment
	for rows.Next() {
		ra, err := scanRoleAssignment(rows)
		if err != nil {
			return nil, fmt.Errorf("db scan role assignment: %w", err)
		}
		results = append(results, ra)
	}
	return results, rows.Err()
}

// ListRoleAssignmentsForScopeUUID returns all role assignments for a specific
// scope identified by its immutable UUID (a tenant_uuid, project_uuid, or a
// resource UUID). Unlike ListRoleAssignmentsForScope — which filters on the slug
// scope_id — this keys on scope_uuid. That matters below the tenant: project and
// resource slugs are NOT globally unique (the same project name can exist in two
// tenants), so a slug filter would leak assignments across tenants. Tenant slugs
// are globally unique, so either method is safe at tenant scope.
func (r *Repository) ListRoleAssignmentsForScopeUUID(ctx context.Context, scopeUUID uuid.UUID) ([]models.RoleAssignment, error) {
	const q = `
		SELECT id, principal_type, principal_id, scope_type, scope_id, scope_uuid, role_definition,
		       granted_at, granted_by, display_alias
		FROM   role_assignments
		WHERE  scope_uuid = $1
		ORDER  BY granted_at DESC`

	rows, err := r.pool.Query(ctx, q, scopeUUID)
	if err != nil {
		return nil, fmt.Errorf("db list role assignments for scope uuid %s: %w", scopeUUID, err)
	}
	defer rows.Close()

	var results []models.RoleAssignment
	for rows.Next() {
		ra, err := scanRoleAssignment(rows)
		if err != nil {
			return nil, fmt.Errorf("db scan role assignment: %w", err)
		}
		results = append(results, ra)
	}
	return results, rows.Err()
}

// DeleteRoleAssignment removes a role_assignments row by ID.
// Idempotent: returns nil if the row does not exist.
func (r *Repository) DeleteRoleAssignment(ctx context.Context, id uuid.UUID) error {
	if _, err := r.pool.Exec(ctx,
		`DELETE FROM role_assignments WHERE id = $1`, id,
	); err != nil {
		return fmt.Errorf("db delete role assignment %s: %w", id, err)
	}
	return nil
}

// ─────────────────────────── Service Accounts ───────────────────────────────

// CreateServiceAccount inserts a new service_accounts row and returns it with
// ID and CreatedAt populated from the RETURNING clause.
//
// The caller is responsible for:
//   - Setting TokenLookupID to the first 12 chars of the raw token (plaintext).
//   - Setting TokenHash to the bcrypt hash of the secret portion (chars 13+).
//
// UNIQUE(tenant_id, name) or UNIQUE(token_lookup_id) conflict → handler converts to 409.
// Phase 6a: includes tenant_uuid in INSERT.
func (r *Repository) CreateServiceAccount(ctx context.Context, sa models.ServiceAccount) (*models.ServiceAccount, error) {
	const q = `
		INSERT INTO service_accounts
			(tenant_id, tenant_uuid, name, token_lookup_id, token_hash, description)
		VALUES
			($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at`

	err := r.pool.QueryRow(ctx, q,
		sa.TenantID,
		sa.TenantUUID,
		sa.Name,
		sa.TokenLookupID,
		sa.TokenHash,
		nilIfEmpty(sa.Description),
	).Scan(&sa.ID, &sa.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("db create service account: %w", err)
	}
	return &sa, nil
}

// GetServiceAccount retrieves a service_accounts row by its UUID.
// Returns (nil, nil) if the row does not exist.
func (r *Repository) GetServiceAccount(ctx context.Context, id uuid.UUID) (*models.ServiceAccount, error) {
	const q = `
		SELECT id, tenant_id, name, token_lookup_id, token_hash, description, created_at, last_used
		FROM   service_accounts
		WHERE  id = $1`

	return r.scanServiceAccount(
		r.pool.QueryRow(ctx, q, id),
		fmt.Sprintf("db get service account %s", id),
	)
}

// GetServiceAccountByTokenLookupID looks up a service account by its
// token_lookup_id (the first 12 chars of the raw token, stored in plaintext).
//
// The auth middleware calls this to find the candidate row in O(1) before
// running bcrypt.CompareHashAndPassword against the stored hash. Using a
// lookup_id avoids an O(N) scan across all service accounts on every request.
//
// Returns (nil, nil) if no matching row exists — middleware treats this as 401.
func (r *Repository) GetServiceAccountByTokenLookupID(ctx context.Context, lookupID string) (*models.ServiceAccount, error) {
	const q = `
		SELECT id, tenant_id, name, token_lookup_id, token_hash, description, created_at, last_used
		FROM   service_accounts
		WHERE  token_lookup_id = $1`

	return r.scanServiceAccount(
		r.pool.QueryRow(ctx, q, lookupID),
		"db get service account by token lookup id",
	)
}

// ListServiceAccountsForTenant returns all service accounts for a tenant,
// newest first. token_hash and token_lookup_id are intentionally omitted from
// the SELECT list so that raw security material never reaches handler/API layer code.
// Phase 6a: filters on tenant_uuid (immutable); also includes it in the SELECT.
func (r *Repository) ListServiceAccountsForTenant(ctx context.Context, tenantUUID uuid.UUID) ([]models.ServiceAccount, error) {
	const q = `
		SELECT id, tenant_id, tenant_uuid, project_id, project_uuid, name, description, created_at, last_used
		FROM   service_accounts
		WHERE  tenant_uuid = $1
		ORDER  BY created_at DESC`

	rows, err := r.pool.Query(ctx, q, tenantUUID)
	if err != nil {
		return nil, fmt.Errorf("db list service accounts: %w", err)
	}
	defer rows.Close()

	var results []models.ServiceAccount
	for rows.Next() {
		var sa models.ServiceAccount
		var desc, projectID *string
		var projectUUID *uuid.UUID
		var lastUsed *time.Time
		if err := rows.Scan(
			&sa.ID, &sa.TenantID, &sa.TenantUUID,
			&projectID, &projectUUID,
			&sa.Name,
			&desc, &sa.CreatedAt, &lastUsed,
		); err != nil {
			return nil, fmt.Errorf("db scan service account: %w", err)
		}
		if desc != nil {
			sa.Description = *desc
		}
		if projectID != nil {
			sa.ProjectID = *projectID
		}
		if projectUUID != nil {
			sa.ProjectUUID = *projectUUID
		}
		sa.LastUsed = lastUsed
		results = append(results, sa)
	}
	return results, rows.Err()
}

// GetRoleForServiceAccount returns the role held by the given service account
// at any scope (project or tenant). Checks project scope first, then tenant scope.
// Returns "" if no role_assignments row exists.
func (r *Repository) GetRoleForServiceAccount(ctx context.Context, saID uuid.UUID, tenantID string) (models.Role, error) {
	const q = `
		SELECT role_definition
		FROM   role_assignments
		WHERE  principal_type = 'service_account'
		  AND  principal_id   = $1
		  AND  scope_type     IN ('tenant', 'project')
		  AND  (scope_id = $2 OR scope_type = 'project')
		ORDER  BY CASE scope_type WHEN 'project' THEN 0 ELSE 1 END
		LIMIT  1`

	var roleDef string
	err := r.pool.QueryRow(ctx, q, saID.String(), tenantID).Scan(&roleDef)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("db get role for SA %s in tenant %s: %w", saID, tenantID, err)
	}
	return models.RoleForRoleDefinition(roleDef), nil
}

// ListServiceAccountsByProject returns all service accounts for a project, newest first.
// M2.5: project-scoped list for project routes.
func (r *Repository) ListServiceAccountsByProject(ctx context.Context, tenantUUID, projectUUID uuid.UUID) ([]models.ServiceAccount, error) {
	const q = `
		SELECT id, tenant_id, tenant_uuid, project_id, project_uuid, name, description, created_at, last_used
		FROM   service_accounts
		WHERE  tenant_uuid = $1 AND project_uuid = $2
		ORDER  BY created_at DESC`

	rows, err := r.pool.Query(ctx, q, tenantUUID, projectUUID)
	if err != nil {
		return nil, fmt.Errorf("db list service accounts by project: %w", err)
	}
	defer rows.Close()

	var results []models.ServiceAccount
	for rows.Next() {
		var sa models.ServiceAccount
		var desc, projectID *string
		var pUUID *uuid.UUID
		var lastUsed *time.Time
		if err := rows.Scan(
			&sa.ID, &sa.TenantID, &sa.TenantUUID,
			&projectID, &pUUID,
			&sa.Name, &desc, &sa.CreatedAt, &lastUsed,
		); err != nil {
			return nil, fmt.Errorf("db scan service account by project: %w", err)
		}
		if desc != nil {
			sa.Description = *desc
		}
		if projectID != nil {
			sa.ProjectID = *projectID
		}
		if pUUID != nil {
			sa.ProjectUUID = *pUUID
		}
		sa.LastUsed = lastUsed
		results = append(results, sa)
	}
	return results, rows.Err()
}

// GetServiceAccountForProject retrieves a single service_accounts row by UUID,
// scoped to the given project. Returns (nil, nil) if not found.
func (r *Repository) GetServiceAccountForProject(ctx context.Context, saID, tenantUUID, projectUUID uuid.UUID) (*models.ServiceAccount, error) {
	const q = `
		SELECT id, tenant_id, tenant_uuid, project_id, project_uuid, name, description, created_at, last_used
		FROM   service_accounts
		WHERE  id = $1 AND tenant_uuid = $2 AND project_uuid = $3`

	var sa models.ServiceAccount
	var desc, projectID *string
	var pUUID *uuid.UUID
	var lastUsed *time.Time
	err := r.pool.QueryRow(ctx, q, saID, tenantUUID, projectUUID).Scan(
		&sa.ID, &sa.TenantID, &sa.TenantUUID,
		&projectID, &pUUID,
		&sa.Name, &desc, &sa.CreatedAt, &lastUsed,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db get service account %s for project %s: %w", saID, projectUUID, err)
	}
	if desc != nil {
		sa.Description = *desc
	}
	if projectID != nil {
		sa.ProjectID = *projectID
	}
	if pUUID != nil {
		sa.ProjectUUID = *pUUID
	}
	sa.LastUsed = lastUsed
	return &sa, nil
}

// GetServiceAccountForTenant retrieves a single service_accounts row by UUID,
// scoped to the given tenantUUID. Returns (nil, nil) if the row does not exist
// or belongs to a different tenant (prevents cross-tenant enumeration).
// token_hash and token_lookup_id are intentionally excluded from the SELECT.
// Phase 6a: filters on tenant_uuid (immutable) instead of tenant_id.
func (r *Repository) GetServiceAccountForTenant(ctx context.Context, saID uuid.UUID, tenantUUID uuid.UUID) (*models.ServiceAccount, error) {
	const q = `
		SELECT id, tenant_id, tenant_uuid, project_id, project_uuid, name, description, created_at, last_used
		FROM   service_accounts
		WHERE  id = $1 AND tenant_uuid = $2`

	var sa models.ServiceAccount
	var desc, projectID *string
	var projectUUID *uuid.UUID
	var lastUsed *time.Time
	err := r.pool.QueryRow(ctx, q, saID, tenantUUID).Scan(
		&sa.ID, &sa.TenantID, &sa.TenantUUID,
		&projectID, &projectUUID,
		&sa.Name, &desc, &sa.CreatedAt, &lastUsed,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db get service account %s for tenant uuid %s: %w", saID, tenantUUID, err)
	}
	if desc != nil {
		sa.Description = *desc
	}
	if projectID != nil {
		sa.ProjectID = *projectID
	}
	if projectUUID != nil {
		sa.ProjectUUID = *projectUUID
	}
	sa.LastUsed = lastUsed
	return &sa, nil
}

// UpdateServiceAccountLastUsed sets last_used = NOW() for the given service
// account. Called from the service-account auth middleware on every successful
// authentication so operators can audit stale tokens.
func (r *Repository) UpdateServiceAccountLastUsed(ctx context.Context, id uuid.UUID) error {
	if _, err := r.pool.Exec(ctx,
		`UPDATE service_accounts SET last_used = NOW() WHERE id = $1`, id,
	); err != nil {
		return fmt.Errorf("db update service account last used %s: %w", id, err)
	}
	return nil
}

// DeleteServiceAccount removes a service_accounts row by ID.
// Idempotent: returns nil if the row does not exist.
// The caller (handler) should also delete any role_assignments rows where
// principal_type='service_account' and principal_id=id.String() — this is not
// done here to keep the repository methods focused and testable independently.
func (r *Repository) DeleteServiceAccount(ctx context.Context, id uuid.UUID) error {
	if _, err := r.pool.Exec(ctx,
		`DELETE FROM service_accounts WHERE id = $1`, id,
	); err != nil {
		return fmt.Errorf("db delete service account %s: %w", id, err)
	}
	return nil
}

// ─────────────────────────── Member management helpers ──────────────────────

// CountOwnersForTenant returns the number of role_assignments rows where
// scope_type='tenant', scope_id=tenantID, role='owner', and
// principal_type='user'. Used by the DELETE /members/{principal_id} handler
// to enforce the "cannot remove the last owner" invariant before deletion.
func (r *Repository) CountOwnersForTenant(ctx context.Context, tenantID string) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM   role_assignments
		WHERE  scope_type      = 'tenant'
		  AND  scope_id        = $1
		  AND  principal_type  = 'user'
		  AND  role_definition = 'Owner'`,
		tenantID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("db count owners for tenant %s: %w", tenantID, err)
	}
	return count, nil
}

// DeleteRoleAssignmentsForPrincipalAtScope removes ALL role_assignments rows
// that match the given (principal_type, principal_id, scope_type, scope_id)
// tuple. A single principal might theoretically hold multiple roles at the same
// scope (the UNIQUE constraint only prevents an exact tuple duplicate, but not
// two different role values), so we delete all matching rows at once.
//
// Idempotent: returns nil if no rows match.
func (r *Repository) DeleteRoleAssignmentsForPrincipalAtScope(
	ctx context.Context,
	principalType models.PrincipalType,
	principalID string,
	scopeType models.ScopeType,
	scopeID string,
) error {
	_, err := r.pool.Exec(ctx, `
		DELETE FROM role_assignments
		WHERE  principal_type = $1
		  AND  principal_id   = $2
		  AND  scope_type     = $3
		  AND  scope_id       = $4`,
		string(principalType),
		principalID,
		string(scopeType),
		scopeID,
	)
	if err != nil {
		return fmt.Errorf("db delete role assignments for principal %s at scope %s/%s: %w",
			principalID, scopeType, scopeID, err)
	}
	return nil
}

// DeleteRoleAssignmentsForPrincipalAtScopeUUID removes ALL role_assignments rows
// matching (principal_type, principal_id, scope_uuid). It is the UUID-keyed twin
// of DeleteRoleAssignmentsForPrincipalAtScope — required below the tenant, where
// slugs are not globally unique, so deleting by (scope_type, scope_id) could
// remove grants in a same-named project of another tenant. Idempotent: returns
// nil if no rows match.
func (r *Repository) DeleteRoleAssignmentsForPrincipalAtScopeUUID(
	ctx context.Context,
	principalType models.PrincipalType,
	principalID string,
	scopeUUID uuid.UUID,
) error {
	_, err := r.pool.Exec(ctx, `
		DELETE FROM role_assignments
		WHERE  principal_type = $1
		  AND  principal_id   = $2
		  AND  scope_uuid     = $3`,
		string(principalType),
		principalID,
		scopeUUID,
	)
	if err != nil {
		return fmt.Errorf("db delete role assignments for principal %s at scope uuid %s: %w",
			principalID, scopeUUID, err)
	}
	return nil
}

// ─────────────────────────── Transactional SA helpers ───────────────────────

// CreateServiceAccountWithRole inserts a service_accounts row AND a matching
// role_assignments row in a single transaction. Either both land or neither.
// Returns the persisted SA with ID and CreatedAt populated from the DB.
//
// UNIQUE violations on (tenant_id, name) or token_lookup_id bubble up as-is so
// the handler can convert them to 409 Conflict.
func (r *Repository) CreateServiceAccountWithRole(
	ctx context.Context,
	sa models.ServiceAccount,
	role models.Role,
	grantedBy string,
) (*models.ServiceAccount, error) {
	// Determine whether the SA is project-scoped or tenant-scoped based on
	// whether ProjectID is set. M2.5: SAs under project routes always have
	// ProjectID; legacy tenant-level SAs have empty ProjectID.
	scopeType := models.ScopeTypeTenant
	scopeID := sa.TenantID
	scopeUUID := sa.TenantUUID
	if sa.ProjectID != "" {
		scopeType = models.ScopeTypeProject
		scopeID = sa.ProjectID
		scopeUUID = sa.ProjectUUID
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("db begin tx (create SA with role): %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Insert the service_accounts row.
	// M2.5: include project_id, project_uuid alongside tenant columns.
	const saQ = `
		INSERT INTO service_accounts
			(tenant_id, tenant_uuid, project_id, project_uuid, name, token_lookup_id, token_hash, description)
		VALUES
			($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at`

	err = tx.QueryRow(ctx, saQ,
		sa.TenantID,
		sa.TenantUUID,
		nilIfEmpty(sa.ProjectID),
		nilIfNilUUID(sa.ProjectUUID),
		sa.Name,
		sa.TokenLookupID,
		sa.TokenHash,
		nilIfEmpty(sa.Description),
	).Scan(&sa.ID, &sa.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("db insert service account: %w", err)
	}

	// Insert the role_assignments row scoped to project or tenant.
	const raQ = `
		INSERT INTO role_assignments
			(principal_type, principal_id, scope_type, scope_id, scope_uuid, role_definition, granted_by)
		VALUES
			($1, $2, $3, $4, $5, $6, $7)`

	if _, err := tx.Exec(ctx, raQ,
		string(models.PrincipalTypeServiceAccount),
		sa.ID.String(),
		string(scopeType),
		scopeID,
		scopeUUID,
		models.RoleDefinitionForRole(role),
		grantedBy,
	); err != nil {
		return nil, fmt.Errorf("db insert role assignment for SA: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("db commit tx (create SA with role): %w", err)
	}
	return &sa, nil
}

// DeleteServiceAccountWithRole removes the service_accounts row identified by
// saID AND all role_assignments rows where principal_type='service_account' and
// principal_id=saID in a single transaction. Idempotent: no error if the SA
// row does not exist.
func (r *Repository) DeleteServiceAccountWithRole(ctx context.Context, saID uuid.UUID) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("db begin tx (delete SA with role): %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Delete role_assignments first (FK safety — service_accounts has no FK, but
	// ordering is explicit for clarity and future-proofing).
	if _, err := tx.Exec(ctx, `
		DELETE FROM role_assignments
		WHERE  principal_type = $1
		  AND  principal_id   = $2`,
		string(models.PrincipalTypeServiceAccount),
		saID.String(),
	); err != nil {
		return fmt.Errorf("db delete role assignments for SA %s: %w", saID, err)
	}

	if _, err := tx.Exec(ctx,
		`DELETE FROM service_accounts WHERE id = $1`, saID,
	); err != nil {
		return fmt.Errorf("db delete service account %s: %w", saID, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("db commit tx (delete SA with role): %w", err)
	}
	return nil
}

// ─────────────────────────── Internal helpers ────────────────────────────────

// scanServiceAccount is the shared scan logic for GetServiceAccount and
// GetServiceAccountByTokenLookupID. Both queries return the same column set:
// id, tenant_id, name, token_lookup_id, token_hash, description, created_at, last_used.
// Returns (nil, nil) on pgx.ErrNoRows so callers can treat missing rows as
// "not found" without an error path.
func (r *Repository) scanServiceAccount(row pgx.Row, errPrefix string) (*models.ServiceAccount, error) {
	var sa models.ServiceAccount
	var desc *string
	var lastUsed *time.Time
	err := row.Scan(
		&sa.ID, &sa.TenantID, &sa.Name, &sa.TokenLookupID, &sa.TokenHash,
		&desc, &sa.CreatedAt, &lastUsed,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", errPrefix, err)
	}
	if desc != nil {
		sa.Description = *desc
	}
	sa.LastUsed = lastUsed
	return &sa, nil
}
