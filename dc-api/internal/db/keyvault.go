// Package db — keyvault.go
//
// Repository methods for M3 Key Vault. v1 (chunk 1) only persists the
// logical record. Endpoint, access-policy, and secret tables join later.
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

// ErrKeyVaultNotFound is returned by GetKeyVault / DeleteKeyVault when the
// requested vault does not exist (or is not visible to the caller's tenant
// after the handler's tenant scope check).
var ErrKeyVaultNotFound = errors.New("key vault not found")

// CreateKeyVault inserts a new KeyVault row. The unique (tenant_id, name)
// constraint is mapped by the handler to 409 Conflict. Status defaults to
// ACTIVE because chunk 1 has no async backend provisioning step.
// M2.5: includes project_id, project_uuid in INSERT.
func (r *Repository) CreateKeyVault(ctx context.Context, kv *models.KeyVault) (*models.KeyVault, error) {
	const q = `
		INSERT INTO key_vaults (tenant_id, tenant_uuid, project_id, project_uuid, name, soft_delete_days, status, message, region)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at, updated_at`

	var message *string
	if kv.Message != "" {
		message = &kv.Message
	}
	if err := r.pool.QueryRow(ctx, q,
		kv.TenantID, kv.TenantUUID,
		nilIfEmpty(kv.ProjectID), nilIfNilUUID(kv.ProjectUUID),
		kv.Name, kv.SoftDeleteDays, string(kv.Status), message, r.regionStamp(),
	).Scan(&kv.ID, &kv.CreatedAt, &kv.UpdatedAt); err != nil {
		return nil, fmt.Errorf("db create key_vault: %w", err)
	}
	r.recordAudit(ctx, r.pool, auditInsert{
		ID: kv.ID, Name: kv.Name, Kind: familyKeyVault.kind,
		TenantUUID: &kv.TenantUUID, ProjectUUID: nilIfNilUUID(kv.ProjectUUID),
		Action: audit.ActionCreate, To: kv.Status,
	})
	return kv, nil
}

// GetKeyVault fetches a vault by ID. Returns ErrKeyVaultNotFound on missing
// rows so handlers can map directly to 404 without string-matching pgx errors.
// M2.5: includes project_id, project_uuid in SELECT.
// 9d: includes credentials_consumed_at for the shown-once GET .../credentials.
func (r *Repository) GetKeyVault(ctx context.Context, id, tenantUUID, projectUUID uuid.UUID) (*models.KeyVault, error) {
	const q = `
		SELECT id, tenant_id, tenant_uuid, project_id, project_uuid,
		       name, soft_delete_days, status, message,
		       credentials_consumed_at, created_at, updated_at
		FROM   key_vaults
		WHERE  id = $1 AND tenant_uuid = $2 AND project_uuid = $3`

	var kv models.KeyVault
	var message, projectID *string
	var projectUUIDCol *uuid.UUID
	err := r.pool.QueryRow(ctx, q, id, tenantUUID, projectUUID).Scan(
		&kv.ID, &kv.TenantID, &kv.TenantUUID,
		&projectID, &projectUUIDCol,
		&kv.Name, &kv.SoftDeleteDays,
		&kv.Status, &message,
		&kv.CredentialsConsumedAt, &kv.CreatedAt, &kv.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrKeyVaultNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db get key_vault: %w", err)
	}
	if message != nil {
		kv.Message = *message
	}
	if projectID != nil {
		kv.ProjectID = *projectID
	}
	if projectUUIDCol != nil {
		kv.ProjectUUID = *projectUUIDCol
	}
	return &kv, nil
}

// MarkKeyVaultCredentialsConsumed stamps credentials_consumed_at = NOW()
// on the row, but only if it is still NULL. Returns ErrCredentialsAlreadyConsumed
// when the row had already been marked (race between two concurrent
// GET .../credentials calls) so the handler can return 410 Gone for the loser.
func (r *Repository) MarkKeyVaultCredentialsConsumed(ctx context.Context, id uuid.UUID) error {
	const q = `
		UPDATE key_vaults
		SET    credentials_consumed_at = NOW()
		WHERE  id = $1 AND credentials_consumed_at IS NULL`
	tag, err := r.pool.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("db mark key_vault credentials consumed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrCredentialsAlreadyConsumed
	}
	return nil
}

// ErrCredentialsAlreadyConsumed is returned by MarkKeyVaultCredentialsConsumed
// when the row's credentials_consumed_at column was already non-NULL — the
// caller maps this to 410 Gone.
var ErrCredentialsAlreadyConsumed = errors.New("key vault credentials already consumed")

// SetKeyVaultCredentialsConsumedNow unconditionally stamps
// credentials_consumed_at = NOW() on the row, regardless of any prior value.
// Used by the credentials-rotate path: rotation IS the shown-once event for
// the new secret_id, so we re-stamp even if the row was previously consumed
// (so subsequent GET .../credentials returns 410 against the new value too).
func (r *Repository) SetKeyVaultCredentialsConsumedNow(ctx context.Context, id uuid.UUID) error {
	const q = `
		UPDATE key_vaults
		SET    credentials_consumed_at = NOW()
		WHERE  id = $1`
	_, err := r.pool.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("db set key_vault credentials consumed: %w", err)
	}
	return nil
}

// ListKeyVaultsByProject returns all vaults for a project, newest first.
// M2.5: project-scoped list replaces the old tenant-only list for project endpoints.
func (r *Repository) ListKeyVaultsByProject(ctx context.Context, tenantUUID, projectUUID uuid.UUID) ([]*models.KeyVault, error) {
	const q = `
		SELECT id, tenant_id, tenant_uuid, project_id, project_uuid, name, soft_delete_days, status, message, created_at, updated_at
		FROM   key_vaults
		WHERE  tenant_uuid = $1 AND project_uuid = $2
		ORDER  BY created_at DESC`

	rows, err := r.pool.Query(ctx, q, tenantUUID, projectUUID)
	if err != nil {
		return nil, fmt.Errorf("db list key_vaults by project: %w", err)
	}
	defer rows.Close()

	var out []*models.KeyVault
	for rows.Next() {
		var kv models.KeyVault
		var message, projectID *string
		var pUUID *uuid.UUID
		if err := rows.Scan(
			&kv.ID, &kv.TenantID, &kv.TenantUUID,
			&projectID, &pUUID,
			&kv.Name, &kv.SoftDeleteDays,
			&kv.Status, &message, &kv.CreatedAt, &kv.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("db scan key_vault: %w", err)
		}
		if message != nil {
			kv.Message = *message
		}
		if projectID != nil {
			kv.ProjectID = *projectID
		}
		if pUUID != nil {
			kv.ProjectUUID = *pUUID
		}
		out = append(out, &kv)
	}
	return out, rows.Err()
}

// ListKeyVaults returns every vault owned by the tenant, newest first.
// Phase 6a: filters on tenant_uuid (immutable); also includes it in SELECT.
// Kept for reconciler and other internal callers.
func (r *Repository) ListKeyVaults(ctx context.Context, tenantUUID uuid.UUID) ([]*models.KeyVault, error) {
	const q = `
		SELECT id, tenant_id, tenant_uuid, project_id, project_uuid, name, soft_delete_days, status, message, created_at, updated_at
		FROM   key_vaults
		WHERE  tenant_uuid = $1
		ORDER  BY created_at DESC`

	rows, err := r.pool.Query(ctx, q, tenantUUID)
	if err != nil {
		return nil, fmt.Errorf("db list key_vaults: %w", err)
	}
	defer rows.Close()

	var out []*models.KeyVault
	for rows.Next() {
		var kv models.KeyVault
		var message, projectID *string
		var projectUUID *uuid.UUID
		if err := rows.Scan(
			&kv.ID, &kv.TenantID, &kv.TenantUUID,
			&projectID, &projectUUID,
			&kv.Name, &kv.SoftDeleteDays,
			&kv.Status, &message, &kv.CreatedAt, &kv.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("db scan key_vault: %w", err)
		}
		if message != nil {
			kv.Message = *message
		}
		if projectID != nil {
			kv.ProjectID = *projectID
		}
		if projectUUID != nil {
			kv.ProjectUUID = *projectUUID
		}
		out = append(out, &kv)
	}
	return out, rows.Err()
}

// DeleteKeyVault removes a vault row by ID. In chunk 1 this is a hard delete.
// When the OpenBao mount + endpoints land (chunk 2-3) this becomes a soft
// delete + reconciler-driven teardown.
func (r *Repository) DeleteKeyVault(ctx context.Context, id uuid.UUID) error {
	if err := r.auditedDelete(ctx, familyKeyVault, id); err != nil {
		return fmt.Errorf("db delete key_vault: %w", err)
	}
	return nil
}
