//go:build integration

package integration

// reaper_test.go — crash-recovery orphan sweep (FailOrphanedUnprovisioned).
//
// A create interrupted before the backend confirmed (crash/redeploy between
// the PENDING insert and the backend_uid update) leaves a PENDING row with
// backend_uid IS NULL. ListPending filters those out, so without the sweep
// the row stays PENDING forever and its unique (tenant, name, type) slot is
// blocked. These tests prove the sweep:
//
//	(a) fails an OLD orphaned PENDING row with the documented message, emits
//	    a STATUS_CHANGE audit event, and frees the name for re-Create;
//	(b) leaves a FRESH orphaned PENDING row alone (in-flight creates are
//	    never reaped — the threshold strictly exceeds every handler's
//	    provision budget: 15 min for cluster creates, 10 min otherwise);
//	(c) leaves a row WITH a backend_uid alone regardless of age (that row is
//	    the reconciler's job, not the sweep's);
//	(d) completes an OLD orphaned DELETING row by removing it (nothing was
//	    ever created on the backend, so deletion just finishes), with a
//	    DELETE audit event.
//
// Cluster-free: pure DB behaviour, runs under DCAPI_TEST_NOP=1 in CI (the
// authz workflow) exactly like the projects-cap suite.

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/wso2/dc-api/internal/models"
)

// Documented sweep message (must match db.FailOrphanedUnprovisioned).
const reaperPendingMsg = "provisioning was interrupted before the backend resource was created; retry the create"

// reaperTenant registers a fresh tenant for one test and returns its slug +
// UUID. Rows are cleaned up so repeated local runs against a shared DB stay
// deterministic.
func reaperTenant(t *testing.T) (string, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	tid := randomTenantID("reaper")
	_, err := env.DB.UpsertTenant(ctx, tid, tid, "dc-tenant-"+tid, "reaper-test")
	require.NoError(t, err, "UpsertTenant")
	tuid, err := env.DB.GetTenantUUIDBySlug(ctx, tid)
	require.NoError(t, err, "GetTenantUUIDBySlug")
	t.Cleanup(func() {
		_, _ = env.DB.Pool().Exec(ctx, `DELETE FROM resources WHERE tenant_uuid = $1`, tuid)
		_, _ = env.DB.Pool().Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tid)
	})
	return tid, tuid
}

// seedReaperResource inserts a resource row through the Repository (same
// path production uses, so region/zone stamping and the CREATE audit event
// behave identically). backendUID "" ⇒ NULL in the row.
func seedReaperResource(t *testing.T, tid string, tuid uuid.UUID, name string, status models.ResourceStatus, backendUID string) uuid.UUID {
	t.Helper()
	res, err := env.DB.Create(context.Background(), &models.Resource{
		TenantID:     tid,
		TenantUUID:   tuid,
		OwnerID:      "reaper-test",
		Name:         name,
		Type:         models.ResourceTypeVM,
		Status:       status,
		ProviderType: "harvester",
		BackendUID:   backendUID,
	})
	require.NoError(t, err, "seed resource %s", name)
	return res.ID
}

// ageReaperResource pushes a row's updated_at into the past. The resources
// table has a BEFORE UPDATE trigger (resources_updated_at) that forces
// updated_at = NOW() on every UPDATE, so a plain SET would be overridden —
// disable the trigger around the backdate, inside one transaction.
func ageReaperResource(t *testing.T, id uuid.UUID, age time.Duration) {
	t.Helper()
	ctx := context.Background()
	tx, err := env.DB.Pool().Begin(ctx)
	require.NoError(t, err, "begin age tx")
	defer tx.Rollback(ctx) //nolint:errcheck
	_, err = tx.Exec(ctx, `ALTER TABLE resources DISABLE TRIGGER resources_updated_at`)
	require.NoError(t, err, "disable updated_at trigger")
	tag, err := tx.Exec(ctx,
		`UPDATE resources SET updated_at = now() - make_interval(secs => $2) WHERE id = $1`,
		id, age.Seconds())
	require.NoError(t, err, "backdate updated_at")
	require.EqualValues(t, 1, tag.RowsAffected(), "expected to age exactly one row")
	_, err = tx.Exec(ctx, `ALTER TABLE resources ENABLE TRIGGER resources_updated_at`)
	require.NoError(t, err, "re-enable updated_at trigger")
	require.NoError(t, tx.Commit(ctx), "commit age tx")
}

// TestReaper_OrphanedPendingReaped — (a): an old PENDING row with NULL
// backend_uid is moved to FAILED with the documented message, the transition
// is audited, and the name becomes reusable via Repository.Create (FAILED
// tombstone replaced).
func TestReaper_OrphanedPendingReaped(t *testing.T) {
	ctx := context.Background()
	tid, tuid := reaperTenant(t)
	name := randomName("orphan-pending")

	id := seedReaperResource(t, tid, tuid, name, models.StatusPending, "")
	ageReaperResource(t, id, 20*time.Minute)

	n, err := env.DB.FailOrphanedUnprovisioned(ctx, 15*time.Minute)
	require.NoError(t, err, "FailOrphanedUnprovisioned")
	// The shared test DB may hold aged orphans from elsewhere; assert ours is
	// included rather than demanding an exact global count.
	require.GreaterOrEqual(t, n, 1, "expected at least our orphan to be reaped")

	res, err := env.DB.Get(ctx, id, tuid)
	require.NoError(t, err, "Get reaped resource")
	require.Equal(t, models.StatusFailed, res.Status, "orphaned PENDING row must be FAILED")
	require.Equal(t, reaperPendingMsg, res.Message, "reaped row must carry the documented message")

	// The sweep must audit like any other status transition.
	var audits int
	require.NoError(t, env.DB.Pool().QueryRow(ctx,
		`SELECT COUNT(*) FROM audit_events
		 WHERE resource_id = $1 AND action = 'STATUS_CHANGE'
		 AND from_status = 'PENDING' AND to_status = 'FAILED'`, id).Scan(&audits))
	require.Equal(t, 1, audits, "expected exactly one PENDING→FAILED audit event for the reaped row")

	// FAILED is a tombstone: creating the same (tenant, name, type) again
	// must succeed — proving the stuck name is released.
	id2 := seedReaperResource(t, tid, tuid, name, models.StatusPending, "")
	require.NotEqual(t, id, id2, "re-create must produce a fresh row")
}

// TestReaper_FreshPendingNotReaped — (b): a fresh orphaned PENDING row (an
// in-flight create) is NOT reaped.
func TestReaper_FreshPendingNotReaped(t *testing.T) {
	ctx := context.Background()
	tid, tuid := reaperTenant(t)

	id := seedReaperResource(t, tid, tuid, randomName("fresh-pending"), models.StatusPending, "")

	_, err := env.DB.FailOrphanedUnprovisioned(ctx, 15*time.Minute)
	require.NoError(t, err, "FailOrphanedUnprovisioned")

	res, err := env.DB.Get(ctx, id, tuid)
	require.NoError(t, err)
	require.Equal(t, models.StatusPending, res.Status, "a fresh orphan (in-flight create) must NOT be reaped")
}

// TestReaper_BackendUIDNotReaped — (c): a PENDING row WITH a backend_uid is
// never reaped regardless of age — the reconciler owns it.
func TestReaper_BackendUIDNotReaped(t *testing.T) {
	ctx := context.Background()
	tid, tuid := reaperTenant(t)

	id := seedReaperResource(t, tid, tuid, randomName("with-uid"), models.StatusPending, "dc-test:with-uid")
	ageReaperResource(t, id, 48*time.Hour)

	_, err := env.DB.FailOrphanedUnprovisioned(ctx, 15*time.Minute)
	require.NoError(t, err, "FailOrphanedUnprovisioned")

	res, err := env.DB.Get(ctx, id, tuid)
	require.NoError(t, err)
	require.Equal(t, models.StatusPending, res.Status, "a row with a backend_uid belongs to the reconciler, not the sweep")
}

// TestReaper_OrphanedDeletingReaped — (d): an old DELETING row that never had
// a backend_uid is removed outright — the user asked for deletion and there
// is nothing on the backend, so the sweep completes the delete — with a
// DELETE audit event recording the transition.
func TestReaper_OrphanedDeletingReaped(t *testing.T) {
	ctx := context.Background()
	tid, tuid := reaperTenant(t)

	id := seedReaperResource(t, tid, tuid, randomName("orphan-deleting"), models.StatusDeleting, "")
	ageReaperResource(t, id, 20*time.Minute)

	n, err := env.DB.FailOrphanedUnprovisioned(ctx, 15*time.Minute)
	require.NoError(t, err, "FailOrphanedUnprovisioned")
	require.GreaterOrEqual(t, n, 1, "expected at least our orphan to be reaped")

	_, err = env.DB.Get(ctx, id, tuid)
	require.Error(t, err, "orphaned DELETING row must be deleted, not resurrected as FAILED")
	require.Contains(t, err.Error(), "not found")

	// The completed delete must be audited like any reconciler-driven delete.
	var audits int
	require.NoError(t, env.DB.Pool().QueryRow(ctx,
		`SELECT COUNT(*) FROM audit_events
		 WHERE resource_id = $1 AND action = 'DELETE'
		 AND from_status = 'DELETING' AND to_status = 'DELETED'`, id).Scan(&audits))
	require.Equal(t, 1, audits, "expected exactly one DELETE audit event for the reaped row")
}
