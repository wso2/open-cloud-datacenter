//go:build integration

package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/wso2/dc-api/internal/models"
)

// setupZoneDB spins up a fresh Postgres container, runs Migrate, seeds the
// minimal regions → tenants → projects FK chain, and returns a connected
// Repository plus the seeded tenant/project identifiers. Mirrors the
// one-container-per-test-file pattern used by cluster_node_pools_test.go.
//
// These tests cover the phase-0 zone slice (slice 3a): root resources stamp
// DCAPI_LOCAL_ZONE, VPC children inherit the parent VNet's zone, and the
// backfill converges pre-existing rows to 'zone-1'.
func setupZoneDB(t *testing.T) (repo *Repository, tenantID string, tenantUUID, projectUUID uuid.UUID, teardown func()) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)

	pgc, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("dc_api_zone_test"),
		tcpostgres.WithUsername("dc_api"),
		tcpostgres.WithPassword("testpass"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		cancel()
		t.Fatalf("start postgres: %v", err)
	}

	connStr, err := pgc.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		cancel()
		_ = pgc.Terminate(ctx)
		t.Fatalf("conn string: %v", err)
	}

	pool, err := Connect(ctx, connStr)
	if err != nil {
		cancel()
		_ = pgc.Terminate(ctx)
		t.Fatalf("connect: %v", err)
	}

	if err := Migrate(ctx, pool); err != nil {
		cancel()
		pool.Close()
		_ = pgc.Terminate(ctx)
		t.Fatalf("migrate: %v", err)
	}

	repo = NewRepository(pool)

	// regions 'lk' is seeded by Migrate; ensure it (defensive) and seed the
	// tenant + project FK chain.
	if _, err := pool.Exec(ctx,
		`INSERT INTO regions (name) VALUES ('lk') ON CONFLICT (name) DO NOTHING`); err != nil {
		t.Fatalf("seed region: %v", err)
	}

	tenantID = "zone-test-tenant"
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenants (id, name, created_by) VALUES ($1, $1, 'test') ON CONFLICT (id) DO NOTHING`,
		tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT tenant_uuid FROM tenants WHERE id = $1`, tenantID).Scan(&tenantUUID); err != nil {
		t.Fatalf("read tenant_uuid: %v", err)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO projects (id, tenant_id, tenant_uuid, name, created_by)
		 VALUES ('default', $1, $2, 'default', 'test')`,
		tenantID, tenantUUID); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT project_uuid FROM projects WHERE tenant_id = $1 AND id = 'default'`, tenantID).Scan(&projectUUID); err != nil {
		t.Fatalf("read project_uuid: %v", err)
	}

	teardown = func() {
		pool.Close()
		cancel()
		_ = pgc.Terminate(context.Background())
	}
	return repo, tenantID, tenantUUID, projectUUID, teardown
}

// dbResourceZone reads the raw zone column for a resource row.
func dbResourceZone(t *testing.T, repo *Repository, id uuid.UUID) string {
	t.Helper()
	var zone *string
	if err := repo.pool.QueryRow(context.Background(),
		`SELECT zone FROM resources WHERE id = $1`, id).Scan(&zone); err != nil {
		t.Fatalf("read resources.zone: %v", err)
	}
	if zone == nil {
		return ""
	}
	return *zone
}

// dbVNetZone reads the raw zone column for a vnet row.
func dbVNetZone(t *testing.T, repo *Repository, id uuid.UUID) string {
	t.Helper()
	var zone *string
	if err := repo.pool.QueryRow(context.Background(),
		`SELECT zone FROM vnets WHERE id = $1`, id).Scan(&zone); err != nil {
		t.Fatalf("read vnets.zone: %v", err)
	}
	if zone == nil {
		return ""
	}
	return *zone
}

// TestZoneStamp_RootResourceDefaultsToLocalZone proves a root resource (no
// caller-supplied zone) is stamped with the repo's local zone, falling back to
// "zone-1" when SetLocalZone was never called and honouring it when it was.
func TestZoneStamp_RootResourceDefaultsToLocalZone(t *testing.T) {
	repo, tenantID, tenantUUID, projectUUID, teardown := setupZoneDB(t)
	defer teardown()
	ctx := context.Background()

	// (a) No SetLocalZone → falls back to the seeded "zone-1".
	res, err := repo.Create(ctx, &models.Resource{
		TenantID:     tenantID,
		TenantUUID:   tenantUUID,
		ProjectID:    "default",
		ProjectUUID:  projectUUID,
		OwnerID:      "owner-1",
		Name:         "root-default",
		Type:         models.ResourceTypeVM,
		Status:       models.StatusPending,
		ProviderType: "harvester",
	})
	if err != nil {
		t.Fatalf("Create root (default zone): %v", err)
	}
	if got := dbResourceZone(t, repo, res.ID); got != "zone-1" {
		t.Errorf("root resource default zone = %q, want %q", got, "zone-1")
	}

	// (b) SetLocalZone("zone-7") → root resource is stamped from DCAPI_LOCAL_ZONE.
	repo.SetLocalZone("zone-7")
	res2, err := repo.Create(ctx, &models.Resource{
		TenantID:     tenantID,
		TenantUUID:   tenantUUID,
		ProjectID:    "default",
		ProjectUUID:  projectUUID,
		OwnerID:      "owner-1",
		Name:         "root-localzone",
		Type:         models.ResourceTypeVM,
		Status:       models.StatusPending,
		ProviderType: "harvester",
	})
	if err != nil {
		t.Fatalf("Create root (local zone): %v", err)
	}
	if got := dbResourceZone(t, repo, res2.ID); got != "zone-7" {
		t.Errorf("root resource zone = %q, want %q (from SetLocalZone)", got, "zone-7")
	}
}

// TestZoneStamp_VNetRootDefaultsToLocalZone proves the VNet (a root resource)
// stamps its zone from the repo's local zone when the caller leaves it empty.
func TestZoneStamp_VNetRootDefaultsToLocalZone(t *testing.T) {
	repo, tenantID, tenantUUID, projectUUID, teardown := setupZoneDB(t)
	defer teardown()
	ctx := context.Background()

	repo.SetLocalZone("zone-9")
	vnet, err := repo.CreateVNet(ctx, &models.VNet{
		TenantID:     tenantID,
		TenantUUID:   tenantUUID,
		ProjectID:    "default",
		ProjectUUID:  projectUUID,
		Name:         "root-vnet",
		Region:       "lk",
		AddressSpace: []string{"10.50.0.0/16"},
		Status:       models.StatusPending,
		ProviderType: "kubeovn",
		// Zone intentionally left empty → CreateVNet defaults it to zoneStamp().
	})
	if err != nil {
		t.Fatalf("CreateVNet: %v", err)
	}
	if vnet.Zone != "zone-9" {
		t.Errorf("returned VNet.Zone = %q, want %q", vnet.Zone, "zone-9")
	}
	if got := dbVNetZone(t, repo, vnet.ID); got != "zone-9" {
		t.Errorf("persisted vnets.zone = %q, want %q", got, "zone-9")
	}

	// Read-back via GetVNet must surface the zone (the peering 422 depends on it).
	roundTrip, err := repo.GetVNet(ctx, vnet.ID, tenantUUID, projectUUID)
	if err != nil {
		t.Fatalf("GetVNet: %v", err)
	}
	if roundTrip.Zone != "zone-9" {
		t.Errorf("GetVNet returned zone %q, want %q", roundTrip.Zone, "zone-9")
	}
}

// TestZoneStamp_VNetHonoursCallerSuppliedZone proves a caller-selected zone is
// persisted verbatim (not overwritten by the local-zone stamp) and surfaced on
// read-back — the data path behind the optional `zone` selector on
// POST /v1/vnets. The local zone is deliberately different from the selected
// zone so an accidental local-stamp would be caught.
func TestZoneStamp_VNetHonoursCallerSuppliedZone(t *testing.T) {
	repo, tenantID, tenantUUID, projectUUID, teardown := setupZoneDB(t)
	defer teardown()
	ctx := context.Background()

	// Register a second zone in the catalog and make the local zone something else.
	if err := repo.EnsureRegionZone(ctx, "lk", "zone-5"); err != nil {
		t.Fatalf("EnsureRegionZone: %v", err)
	}
	repo.SetLocalZone("zone-1")

	// The catalog must report the selected zone as known (the handler's 400 guard).
	known, err := repo.IsKnownZone(ctx, "lk", "zone-5")
	if err != nil {
		t.Fatalf("IsKnownZone(known): %v", err)
	}
	if !known {
		t.Fatalf("IsKnownZone(lk, zone-5) = false, want true")
	}

	vnet, err := repo.CreateVNet(ctx, &models.VNet{
		TenantID:     tenantID,
		TenantUUID:   tenantUUID,
		ProjectID:    "default",
		ProjectUUID:  projectUUID,
		Name:         "zoned-vnet",
		Region:       "lk",
		AddressSpace: []string{"10.55.0.0/16"},
		Status:       models.StatusPending,
		ProviderType: "kubeovn",
		Zone:         "zone-5", // caller-supplied selector
	})
	if err != nil {
		t.Fatalf("CreateVNet: %v", err)
	}
	if vnet.Zone != "zone-5" {
		t.Errorf("returned VNet.Zone = %q, want %q (not local zone-1)", vnet.Zone, "zone-5")
	}
	if got := dbVNetZone(t, repo, vnet.ID); got != "zone-5" {
		t.Errorf("persisted vnets.zone = %q, want %q (not local zone-1)", got, "zone-5")
	}
	rt, err := repo.GetVNet(ctx, vnet.ID, tenantUUID, projectUUID)
	if err != nil {
		t.Fatalf("GetVNet: %v", err)
	}
	if rt.Zone != "zone-5" {
		t.Errorf("GetVNet returned zone %q, want %q", rt.Zone, "zone-5")
	}
}

// TestZone_IsKnownZoneRejectsUnknown proves the catalog lookup behind the
// handler's 400 guard returns false for a zone that does not exist in the
// requested region, while returning true for the seeded one.
func TestZone_IsKnownZoneRejectsUnknown(t *testing.T) {
	repo, _, _, _, teardown := setupZoneDB(t)
	defer teardown()
	ctx := context.Background()

	if err := repo.EnsureRegionZone(ctx, "lk", "zone-1"); err != nil {
		t.Fatalf("EnsureRegionZone: %v", err)
	}

	known, err := repo.IsKnownZone(ctx, "lk", "zone-1")
	if err != nil {
		t.Fatalf("IsKnownZone(known): %v", err)
	}
	if !known {
		t.Errorf("IsKnownZone(lk, zone-1) = false, want true")
	}

	unknown, err := repo.IsKnownZone(ctx, "lk", "no-such-zone")
	if err != nil {
		t.Fatalf("IsKnownZone(unknown): %v", err)
	}
	if unknown {
		t.Errorf("IsKnownZone(lk, no-such-zone) = true, want false")
	}
}

// TestZoneStamp_ChildInheritsParentVNetZone proves a child resource (VM/cluster
// on the VPC path) takes the parent VNet's zone, NOT the local zone, exactly as
// the handlers wire res.Zone = vnet.Zone before repo.Create.
func TestZoneStamp_ChildInheritsParentVNetZone(t *testing.T) {
	repo, tenantID, tenantUUID, projectUUID, teardown := setupZoneDB(t)
	defer teardown()
	ctx := context.Background()

	// The control plane's local zone is "zone-1", but the parent VNet lives in
	// "zone-3". The child must inherit "zone-3", proving inheritance beats the
	// local-zone fallback.
	repo.SetLocalZone("zone-1")
	parentZone := "zone-3"
	vnet, err := repo.CreateVNet(ctx, &models.VNet{
		TenantID:     tenantID,
		TenantUUID:   tenantUUID,
		ProjectID:    "default",
		ProjectUUID:  projectUUID,
		Name:         "parent-vnet",
		Region:       "lk",
		AddressSpace: []string{"10.51.0.0/16"},
		Status:       models.StatusActive,
		ProviderType: "kubeovn",
		Zone:         parentZone,
	})
	if err != nil {
		t.Fatalf("CreateVNet parent: %v", err)
	}

	child, err := repo.Create(ctx, &models.Resource{
		TenantID:     tenantID,
		TenantUUID:   tenantUUID,
		ProjectID:    "default",
		ProjectUUID:  projectUUID,
		OwnerID:      "owner-1",
		Name:         "child-vm",
		Type:         models.ResourceTypeVM,
		Status:       models.StatusPending,
		ProviderType: "harvester",
		VNetID:       &vnet.ID,
		Zone:         vnet.Zone, // handler wiring: child inherits parent VNet zone
	})
	if err != nil {
		t.Fatalf("Create child: %v", err)
	}
	if got := dbResourceZone(t, repo, child.ID); got != parentZone {
		t.Errorf("child zone = %q, want inherited %q (not local zone-1)", got, parentZone)
	}
}

// TestZone_CrossZoneMismatchIsDetectable seeds two VNets in different zones and
// confirms the read-back surfaces the differing zones so the peering handler's
// `if vnet.Zone != peerVNet.Zone` guard (422) fires. The guard itself lives in
// the handler; this test proves the data plumbing it relies on is correct.
// Under the single seeded zone in production both would be "zone-1" and the
// guard never triggers — here we force a mismatch to exercise the seam.
func TestZone_CrossZoneMismatchIsDetectable(t *testing.T) {
	repo, tenantID, tenantUUID, projectUUID, teardown := setupZoneDB(t)
	defer teardown()
	ctx := context.Background()

	vnetA, err := repo.CreateVNet(ctx, &models.VNet{
		TenantID: tenantID, TenantUUID: tenantUUID,
		ProjectID: "default", ProjectUUID: projectUUID,
		Name: "vnet-a", Region: "lk", AddressSpace: []string{"10.60.0.0/16"},
		Status: models.StatusActive, ProviderType: "kubeovn", Zone: "zone-1",
	})
	if err != nil {
		t.Fatalf("CreateVNet A: %v", err)
	}
	vnetB, err := repo.CreateVNet(ctx, &models.VNet{
		TenantID: tenantID, TenantUUID: tenantUUID,
		ProjectID: "default", ProjectUUID: projectUUID,
		Name: "vnet-b", Region: "lk", AddressSpace: []string{"10.61.0.0/16"},
		Status: models.StatusActive, ProviderType: "kubeovn", Zone: "zone-2",
	})
	if err != nil {
		t.Fatalf("CreateVNet B: %v", err)
	}

	gotA, err := repo.GetVNetByTenant(ctx, vnetA.ID, tenantUUID)
	if err != nil {
		t.Fatalf("GetVNetByTenant A: %v", err)
	}
	gotB, err := repo.GetVNetByTenant(ctx, vnetB.ID, tenantUUID)
	if err != nil {
		t.Fatalf("GetVNetByTenant B: %v", err)
	}
	if gotA.Zone == gotB.Zone {
		t.Fatalf("expected differing zones for the cross-zone guard, got A=%q B=%q", gotA.Zone, gotB.Zone)
	}
	// This is the exact condition the peering handler maps to HTTP 422.
	if !(gotA.Zone != gotB.Zone) {
		t.Errorf("cross-zone guard condition did not hold (A=%q B=%q)", gotA.Zone, gotB.Zone)
	}
}

// TestZone_BackfillConvergesNullRows proves the schema.sql backfill stamps
// 'zone-1' on pre-existing rows that predate the zone column, and that a second
// Migrate is a no-op (idempotent). It simulates the upgrade path by NULLing the
// zone on a row, then re-running Migrate.
func TestZone_BackfillConvergesNullRows(t *testing.T) {
	repo, tenantID, tenantUUID, projectUUID, teardown := setupZoneDB(t)
	defer teardown()
	ctx := context.Background()

	res, err := repo.Create(ctx, &models.Resource{
		TenantID: tenantID, TenantUUID: tenantUUID,
		ProjectID: "default", ProjectUUID: projectUUID,
		OwnerID: "owner-1", Name: "legacy-row", Type: models.ResourceTypeVM,
		Status: models.StatusPending, ProviderType: "harvester",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Simulate a pre-stamp (legacy) row by NULLing its zone.
	if _, err := repo.pool.Exec(ctx, `UPDATE resources SET zone = NULL WHERE id = $1`, res.ID); err != nil {
		t.Fatalf("null out zone: %v", err)
	}
	if got := dbResourceZone(t, repo, res.ID); got != "" {
		t.Fatalf("precondition: expected NULL zone, got %q", got)
	}

	// Re-run Migrate → the IS-NULL-guarded backfill must converge it to zone-1.
	if err := Migrate(ctx, repo.pool); err != nil {
		t.Fatalf("Migrate (backfill): %v", err)
	}
	if got := dbResourceZone(t, repo, res.ID); got != "zone-1" {
		t.Errorf("after backfill zone = %q, want %q", got, "zone-1")
	}

	// Second Migrate is a no-op (idempotent) and must not change the row.
	if err := Migrate(ctx, repo.pool); err != nil {
		t.Fatalf("Migrate (second pass): %v", err)
	}
	if got := dbResourceZone(t, repo, res.ID); got != "zone-1" {
		t.Errorf("after idempotent re-run zone = %q, want %q", got, "zone-1")
	}

	// No NULL zone rows must remain across the five backfilled tables.
	for _, table := range []string{"resources", "key_vaults", "databases", "private_endpoints", "vnets"} {
		var n int
		if err := repo.pool.QueryRow(ctx,
			"SELECT COUNT(*) FROM "+table+" WHERE zone IS NULL").Scan(&n); err != nil {
			t.Fatalf("count NULL zone in %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%s has %d rows with NULL zone after backfill, want 0", table, n)
		}
	}
}
