//go:build integration

package db

import (
	"context"
	"testing"
	"time"

	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// TestMigrate_Idempotent runs schema.sql twice against a fresh postgres
// container and confirms both calls succeed. This proves that every
// CREATE TYPE / CREATE TABLE / CREATE TRIGGER / INSERT seed in
// schema.sql is correctly guarded for re-run.
func TestMigrate_Idempotent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pgc, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("dc_api_test"),
		tcpostgres.WithUsername("dc_api"),
		tcpostgres.WithPassword("testpass"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = pgc.Terminate(ctx) })

	connStr, err := pgc.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("conn string: %v", err)
	}
	pool, err := Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("first Migrate (fresh DB) failed: %v", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("second Migrate (existing DB) failed: %v", err)
	}

	// Spot-check a few invariants the second run must not break.
	var resourceCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM resources`).Scan(&resourceCount); err != nil {
		t.Fatalf("query resources: %v", err)
	}
	if resourceCount != 0 {
		t.Fatalf("expected empty resources table, got %d rows", resourceCount)
	}

	// lk region must have the refreshed reserved_cidrs (ON CONFLICT DO UPDATE).
	var cidrs []string
	if err := pool.QueryRow(ctx,
		`SELECT reserved_cidrs FROM regions WHERE name = 'lk'`).Scan(&cidrs); err != nil {
		t.Fatalf("query regions: %v", err)
	}
	want := "10.16.0.0/16:harvester-ovn-default"
	found := false
	for _, c := range cidrs {
		if c == want {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected %q in lk reserved_cidrs, got %v", want, cidrs)
	}

	// BASTION enum value must be present.
	var hasBastion bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_enum WHERE enumlabel = 'BASTION')`).Scan(&hasBastion); err != nil {
		t.Fatalf("query pg_enum: %v", err)
	}
	if !hasBastion {
		t.Fatalf("expected BASTION enum value to exist")
	}
}

// TestMigrate_UpgradesV1RoleColumn simulates an existing pre-RBAC-v2 database —
// role_assignments still has the v1 `role` column with owner/member/viewer rows —
// and verifies Migrate converts it in place to role_definition (Owner/Contributor/
// Reader), drops the old column, builds the new unique index, and stays
// idempotent. This is the path a live dev DB takes on the next dc-api boot; the
// fresh-install path is covered by TestMigrate_Idempotent.
func TestMigrate_UpgradesV1RoleColumn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pgc, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("dc_api_test"),
		tcpostgres.WithUsername("dc_api"),
		tcpostgres.WithPassword("testpass"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = pgc.Terminate(ctx) })

	connStr, err := pgc.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("conn string: %v", err)
	}
	pool, err := Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	// Recreate the PRE-v2 role_assignments table: the v1 `role` column plus the
	// old inline UNIQUE that referenced it. No role_definition column yet.
	if _, err := pool.Exec(ctx, `
		CREATE TABLE role_assignments (
			id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			principal_type TEXT NOT NULL,
			principal_id   TEXT NOT NULL,
			scope_type     TEXT NOT NULL,
			scope_id       TEXT NOT NULL,
			role           TEXT NOT NULL,
			granted_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			granted_by     TEXT NOT NULL,
			UNIQUE (principal_type, principal_id, scope_type, scope_id, role)
		)`); err != nil {
		t.Fatalf("create v1 role_assignments: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO role_assignments (principal_type, principal_id, scope_type, scope_id, role, granted_by)
		VALUES ('user','alice','tenant','acme','owner','seed'),
		       ('user','bob','tenant','acme','member','seed'),
		       ('user','carol','tenant','acme','viewer','seed')`); err != nil {
		t.Fatalf("seed v1 rows: %v", err)
	}

	// Run the full migration. CREATE TABLE IF NOT EXISTS no-ops on our existing
	// table; the RBAC v2 block converts it in place.
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate over v1 DB failed: %v", err)
	}

	// The v1 `role` column must be gone.
	var hasRole bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM information_schema.columns
		               WHERE table_name='role_assignments' AND column_name='role')`).Scan(&hasRole); err != nil {
		t.Fatalf("check role column: %v", err)
	}
	if hasRole {
		t.Fatal("v1 `role` column should have been dropped")
	}

	// Each row's role_definition must be the mapped built-in key.
	for _, tc := range []struct{ principal, want string }{
		{"alice", "Owner"}, {"bob", "Contributor"}, {"carol", "Reader"},
	} {
		var got string
		if err := pool.QueryRow(ctx,
			`SELECT role_definition FROM role_assignments WHERE principal_id=$1`, tc.principal).Scan(&got); err != nil {
			t.Fatalf("query %s: %v", tc.principal, err)
		}
		if got != tc.want {
			t.Errorf("principal %s: role_definition=%q, want %q", tc.principal, got, tc.want)
		}
	}

	// The new uniqueness index must exist.
	var hasIdx bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM pg_indexes
		               WHERE tablename='role_assignments'
		                 AND indexname='uq_role_assignments_principal_scope_roledef')`).Scan(&hasIdx); err != nil {
		t.Fatalf("check unique index: %v", err)
	}
	if !hasIdx {
		t.Fatal("expected uq_role_assignments_principal_scope_roledef index")
	}

	// Idempotent: a second migrate over the now-v2 DB must still succeed.
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("second Migrate (already v2) failed: %v", err)
	}
}
