// Package db — migrate.go
//
// Migrate applies the schema.sql DDL to PostgreSQL on every DC-API startup.
//
// schema.sql is fully idempotent — every statement uses IF NOT EXISTS,
// DO BEGIN…EXCEPTION blocks for CREATE TYPE, DROP TRIGGER IF EXISTS +
// CREATE TRIGGER pairs, ON CONFLICT clauses for seeds, and ALTER TABLE
// ADD COLUMN IF NOT EXISTS for post-launch columns. Running it on a
// fresh database creates the schema; running it on an existing database
// is a no-op except where it picks up new tables/columns/seeds.
//
// New schema state (tables, columns, enum values) MUST be added to
// schema.sql directly. There is no longer a separate alterations slice
// to keep in sync.
//
// One Go-side post-step exists: backfillTenantUUIDs. It populates the
// Phase 6a tenant_uuid columns on existing rows by joining to tenants,
// auto-creating tenants rows for any orphan slugs found along the way.
// It is idempotent — re-running it is a no-op because the UPDATEs are
// gated on tenant_uuid IS NULL and the INSERT uses ON CONFLICT DO NOTHING.
package db

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
)

// schemaSQL is the full DDL embedded from schema.sql at compile time.
//
//go:embed schema.sql
var schemaSQL string

// Migrate runs schema.sql against the target database, then performs the
// Phase 6a tenant_uuid backfill.
// It is safe to call on every boot regardless of database state.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	// Enum extension must run as its own statement: Postgres rejects
	// ALTER TYPE … ADD VALUE inside the multi-statement script's implicit
	// transaction. IF NOT EXISTS keeps it idempotent across boots; running
	// it BEFORE the script keeps fresh and existing databases identical.
	if _, err := pool.Exec(ctx,
		`ALTER TYPE resource_status ADD VALUE IF NOT EXISTS 'DELETED'`); err != nil {
		// Fresh database: the type doesn't exist yet — the script creates it
		// with DELETED included, so a missing type is not an error here.
		if !strings.Contains(err.Error(), "does not exist") {
			return fmt.Errorf("extend resource_status enum: %w", err)
		}
	}

	log.Info().Msg("applying database schema (idempotent)…")
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		return fmt.Errorf("apply schema.sql: %w", err)
	}
	log.Info().Msg("database schema applied")

	if err := backfillTenantUUIDs(ctx, pool); err != nil {
		return fmt.Errorf("phase 6a tenant_uuid backfill: %w", err)
	}
	return nil
}

// backfillOrphanTenantsSQL pulls every distinct tenant_id slug referenced by
// any per-tenant table and UPSERTs a tenants row for it. After this runs,
// every slug that appears anywhere in the DB has an entry in tenants with
// a stable tenant_uuid that the next step can JOIN against.
//
// The '__default__' quota seed is deliberately excluded — it's a fixture,
// not a real tenant.
const backfillOrphanTenantsSQL = `
INSERT INTO tenants (id, name, created_by)
SELECT DISTINCT slug, slug, 'backfill-orphan'
FROM (
    SELECT tenant_id AS slug FROM resources                WHERE tenant_id IS NOT NULL
    UNION SELECT tenant_id FROM vnets                      WHERE tenant_id IS NOT NULL
    UNION SELECT tenant_id FROM subnets                    WHERE tenant_id IS NOT NULL
    UNION SELECT tenant_id FROM route_tables               WHERE tenant_id IS NOT NULL
    UNION SELECT tenant_id FROM route_table_associations   WHERE tenant_id IS NOT NULL
    UNION SELECT tenant_id FROM network_security_groups    WHERE tenant_id IS NOT NULL
    UNION SELECT tenant_id FROM nsg_attachments            WHERE tenant_id IS NOT NULL
    UNION SELECT tenant_id FROM peerings                   WHERE tenant_id IS NOT NULL
    UNION SELECT tenant_id FROM private_dns_zones          WHERE tenant_id IS NOT NULL
    UNION SELECT tenant_id FROM dns_records                WHERE tenant_id IS NOT NULL
    UNION SELECT tenant_id FROM service_accounts           WHERE tenant_id IS NOT NULL
    UNION SELECT tenant_id FROM key_vaults                 WHERE tenant_id IS NOT NULL
    UNION SELECT tenant_id FROM private_endpoints          WHERE tenant_id IS NOT NULL
    UNION SELECT tenant_id FROM quotas                     WHERE tenant_id IS NOT NULL AND tenant_id <> '__default__'
    UNION SELECT scope_id  FROM role_assignments           WHERE scope_type = 'tenant' AND scope_id IS NOT NULL
) s
WHERE slug IS NOT NULL AND slug <> ''
ON CONFLICT (id) DO NOTHING
`

// backfillUpdates list — one UPDATE per per-tenant table. Each is gated on
// `tenant_uuid IS NULL` so re-runs after every row is populated produce zero
// updates (idempotent). The JOIN naturally skips rows whose slug doesn't have
// a tenants row (e.g. quotas.tenant_id='__default__').
var backfillUpdates = []struct {
	name string
	sql  string
}{
	{"resources", `UPDATE resources SET tenant_uuid = t.tenant_uuid FROM tenants t WHERE resources.tenant_id = t.id AND resources.tenant_uuid IS NULL`},
	{"quotas", `UPDATE quotas SET tenant_uuid = t.tenant_uuid FROM tenants t WHERE quotas.tenant_id = t.id AND quotas.tenant_uuid IS NULL`},
	{"vnets", `UPDATE vnets SET tenant_uuid = t.tenant_uuid FROM tenants t WHERE vnets.tenant_id = t.id AND vnets.tenant_uuid IS NULL`},
	{"subnets", `UPDATE subnets SET tenant_uuid = t.tenant_uuid FROM tenants t WHERE subnets.tenant_id = t.id AND subnets.tenant_uuid IS NULL`},
	{"route_tables", `UPDATE route_tables SET tenant_uuid = t.tenant_uuid FROM tenants t WHERE route_tables.tenant_id = t.id AND route_tables.tenant_uuid IS NULL`},
	{"route_table_associations", `UPDATE route_table_associations SET tenant_uuid = t.tenant_uuid FROM tenants t WHERE route_table_associations.tenant_id = t.id AND route_table_associations.tenant_uuid IS NULL`},
	{"network_security_groups", `UPDATE network_security_groups SET tenant_uuid = t.tenant_uuid FROM tenants t WHERE network_security_groups.tenant_id = t.id AND network_security_groups.tenant_uuid IS NULL`},
	{"nsg_attachments", `UPDATE nsg_attachments SET tenant_uuid = t.tenant_uuid FROM tenants t WHERE nsg_attachments.tenant_id = t.id AND nsg_attachments.tenant_uuid IS NULL`},
	{"peerings", `UPDATE peerings SET tenant_uuid = t.tenant_uuid FROM tenants t WHERE peerings.tenant_id = t.id AND peerings.tenant_uuid IS NULL`},
	{"private_dns_zones", `UPDATE private_dns_zones SET tenant_uuid = t.tenant_uuid FROM tenants t WHERE private_dns_zones.tenant_id = t.id AND private_dns_zones.tenant_uuid IS NULL`},
	{"dns_records", `UPDATE dns_records SET tenant_uuid = t.tenant_uuid FROM tenants t WHERE dns_records.tenant_id = t.id AND dns_records.tenant_uuid IS NULL`},
	{"service_accounts", `UPDATE service_accounts SET tenant_uuid = t.tenant_uuid FROM tenants t WHERE service_accounts.tenant_id = t.id AND service_accounts.tenant_uuid IS NULL`},
	{"key_vaults", `UPDATE key_vaults SET tenant_uuid = t.tenant_uuid FROM tenants t WHERE key_vaults.tenant_id = t.id AND key_vaults.tenant_uuid IS NULL`},
	{"private_endpoints", `UPDATE private_endpoints SET tenant_uuid = t.tenant_uuid FROM tenants t WHERE private_endpoints.tenant_id = t.id AND private_endpoints.tenant_uuid IS NULL`},
	{"role_assignments", `UPDATE role_assignments SET scope_uuid = t.tenant_uuid FROM tenants t WHERE role_assignments.scope_id = t.id AND role_assignments.scope_type = 'tenant' AND role_assignments.scope_uuid IS NULL`},
}

// backfillTenantUUIDs runs the Phase 6a backfill in a single transaction:
//   1. UPSERT every slug seen in per-tenant tables into the tenants registry
//      (so they get a stable tenant_uuid).
//   2. For each per-tenant table, populate tenant_uuid by joining on slug.
//
// Idempotent: re-runs after a fully-populated DB produce zero affected rows.
func backfillTenantUUIDs(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	orphans, err := tx.Exec(ctx, backfillOrphanTenantsSQL)
	if err != nil {
		return fmt.Errorf("upsert orphan tenants: %w", err)
	}
	if n := orphans.RowsAffected(); n > 0 {
		log.Info().Int64("rows", n).Msg("phase 6a: registered orphan tenant slugs")
	}

	totalUpdated := int64(0)
	for _, u := range backfillUpdates {
		ct, err := tx.Exec(ctx, u.sql)
		if err != nil {
			return fmt.Errorf("backfill %s.tenant_uuid: %w", u.name, err)
		}
		n := ct.RowsAffected()
		if n > 0 {
			log.Info().Str("table", u.name).Int64("rows", n).Msg("phase 6a: populated tenant_uuid")
		}
		totalUpdated += n
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}

	if totalUpdated == 0 && orphans.RowsAffected() == 0 {
		log.Debug().Msg("phase 6a: nothing to backfill")
	}
	return nil
}
