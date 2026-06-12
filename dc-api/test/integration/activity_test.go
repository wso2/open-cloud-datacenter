//go:build integration

package integration

// activity_test.go — GET /v1/tenants/{tid}/projects/{pid}/activity.
//
// The activity feed is a pure DB read (audit_events ⋈ resources), so the whole
// file runs cluster-free with DCAPI_TEST_NOP=1: CreateVM against the nop
// compute provider still writes the resource row and the CREATE audit event
// synchronously (the handler records both before the async provider call), and
// the failed background provisioning adds the STATUS_CHANGE event the
// stabilisation wait below keys on.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wso2/dc-api/internal/models"
)

// activityRawTenant is the unprefixed tenant label; clientForTenant adds the
// "test-" prefix, so the slug (and the JWT sub) is "test-" + this.
const activityRawTenant = "tenant-activity"

func activityTenantID() string { return "test-" + activityRawTenant }

// findActivityEvent returns the first item matching resourceID+action, or nil.
func findActivityEvent(items []ActivityEventDTO, resourceID, action string) *ActivityEventDTO {
	for i := range items {
		if items[i].ResourceID == resourceID && items[i].Action == action {
			return &items[i]
		}
	}
	return nil
}

func TestActivity_FeedPaginationAndIsolation(t *testing.T) {
	ctx := context.Background()
	client := clientForTenant(t, activityRawTenant)
	mustGrantOwnerForClient(t, activityRawTenant)
	tenantID := activityTenantID()

	// ── 1. Create a resource through the API ───────────────────────────────
	// The VM handler writes the resource row + the CREATE audit event before
	// returning 202; the background provisioning then fails against the nop
	// provider and appends a STATUS_CHANGE (PENDING → FAILED) event.
	vmName := randomName("act-vm")
	resp, body, status, err := client.CreateVM(ctx, CreateVMRequest{
		Name:        vmName,
		Size:        "small",
		ImageName:   "ubuntu-22.04",
		NetworkName: "default/test-net",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status, "CreateVM: %s", ErrorBody(body))
	vmID := resp.Resource.ID
	require.NotEmpty(t, vmID)

	// ── 2. The CREATE event appears, joined with name/type ──────────────────
	page, body, status, err := client.ListActivity(ctx, "")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "ListActivity: %s", ErrorBody(body))
	created := findActivityEvent(page.Items, vmID, "CREATE")
	require.NotNil(t, created, "CREATE event for %s must appear in the feed; got %d items", vmID, len(page.Items))
	assert.Equal(t, vmName, created.ResourceName, "resource_name must come from the resources join")
	assert.Equal(t, string(models.ResourceTypeVM), created.ResourceType, "resource_type must come from the resources join")
	assert.Equal(t, tenantID, created.ActorID, "actor_id must be the caller's sub")
	assert.Equal(t, string(models.StatusPending), created.ToStatus)
	assert.Empty(t, created.FromStatus, "CREATE has no from_status — field must be omitted")
	_, perr := time.Parse(time.RFC3339, created.CreatedAt)
	assert.NoError(t, perr, "created_at must be RFC3339: %q", created.CreatedAt)
	assert.GreaterOrEqual(t, page.Total, 1)

	// ── 3. Wait for the async STATUS_CHANGE so the count is stable ──────────
	// UpdateStatus(FAILED) lands before the audit insert, so poll the feed
	// itself (not the VM status) to avoid the tiny insert race.
	require.Eventually(t, func() bool {
		p, _, s, e := client.ListActivity(ctx, "?limit=100")
		return e == nil && s == http.StatusOK && findActivityEvent(p.Items, vmID, "STATUS_CHANGE") != nil
	}, 30*time.Second, 250*time.Millisecond,
		"STATUS_CHANGE event from the failed nop provisioning never appeared")

	page, _, status, err = client.ListActivity(ctx, "")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	baseTotal := page.Total

	// ── 4. Newest-first ordering ─────────────────────────────────────────────
	for i := 1; i < len(page.Items); i++ {
		prev, e1 := time.Parse(time.RFC3339, page.Items[i-1].CreatedAt)
		cur, e2 := time.Parse(time.RFC3339, page.Items[i].CreatedAt)
		require.NoError(t, e1)
		require.NoError(t, e2)
		assert.False(t, prev.Before(cur),
			"items must be newest first: item %d (%s) is older than item %d (%s)",
			i-1, page.Items[i-1].CreatedAt, i, page.Items[i].CreatedAt)
	}

	// ── 5. Pagination: append deterministic events via the repository ────────
	for i := 0; i < 5; i++ {
		require.NoError(t, env.DB.AppendAuditEvent(ctx, &models.AuditEvent{
			ResourceID: uuid.MustParse(vmID),
			ActorID:    "test-fixture",
			Action:     fmt.Sprintf("TEST_EVENT_%d", i),
			Message:    "pagination fixture",
		}))
	}
	want := baseTotal + 5

	page1, _, status, err := client.ListActivity(ctx, "?limit=3&offset=0")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, want, page1.Total, "total must count all events across pages")
	require.Len(t, page1.Items, 3, "limit=3 must cap the page")

	page2, _, status, err := client.ListActivity(ctx, "?limit=3&offset=3")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, want, page2.Total, "total must be identical on every page")
	require.NotEmpty(t, page2.Items)

	seen := map[string]bool{}
	for _, it := range page1.Items {
		seen[it.ID] = true
	}
	for _, it := range page2.Items {
		assert.False(t, seen[it.ID], "event %s appears on both pages — broken pagination ordering", it.ID)
	}

	// Offset past the end: 200 with an empty (non-null) items array.
	tail, raw, status, err := client.ListActivity(ctx, fmt.Sprintf("?offset=%d", want))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, want, tail.Total)
	assert.Empty(t, tail.Items)
	assert.Contains(t, strings.ReplaceAll(string(raw), " ", ""), `"items":[]`,
		"an empty page must serialize items as [] (spec: array), not null")

	// ── 6. Invalid paging → 400 ──────────────────────────────────────────────
	for _, q := range []string{"?limit=0", "?limit=101", "?limit=abc", "?offset=-1", "?offset=xyz"} {
		_, body, status, err := client.ListActivity(ctx, q)
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadRequest, status, "%s must 400: %s", q, ErrorBody(body))
	}

	// ── 7. Cross-project isolation ───────────────────────────────────────────
	// Events recorded in the default project must be invisible from a sibling
	// project in the same tenant.
	const projB = "act-feed-b"
	_, body, status, err = client.CreateProject(ctx, CreateProjectRequest{
		ID: projB, Name: "Activity isolation B", CPUCores: 4, MemoryGB: 8, StorageGB: 50,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, "CreateProject %s: %s", projB, ErrorBody(body))

	tokenB, err := env.JWT.MintToken(tenantID, tenantID+"-user")
	require.NoError(t, err)
	clientB := NewAPIClientForProject(env.BaseURL, tokenB, tenantID, projB)

	pageB, body, status, err := clientB.ListActivity(ctx, "")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "ListActivity in project B: %s", ErrorBody(body))
	assert.Equal(t, 0, pageB.Total, "project B has no resources — its feed must be empty")
	assert.Empty(t, pageB.Items)
	assert.Nil(t, findActivityEvent(pageB.Items, vmID, "CREATE"),
		"project A's CREATE event leaked into project B's feed")

	// ── 8. History survives resource deletion ────────────────────────────────
	// Deleting the resource row (what the reconciler does once the backend
	// confirms deletion) must NOT erase the feed: the snapshot columns keep
	// every event renderable; only the live resource_id pointer disappears.
	require.NoError(t, env.DB.Delete(ctx, uuid.MustParse(vmID)),
		"direct resource-row delete (reconciler path)")

	after, _, status, err := client.ListActivity(ctx, "?limit=100")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, want+1, after.Total,
		"row removal must add exactly one terminal DELETE event")

	// The terminal event is the framework's: DELETING/old status → DELETED.
	var terminal *ActivityEventDTO
	for i := range after.Items {
		if after.Items[i].ResourceName == vmName && after.Items[i].Action == "DELETE" {
			terminal = &after.Items[i]
			break
		}
	}
	require.NotNil(t, terminal, "terminal DELETE event must exist")
	assert.Equal(t, "DELETED", terminal.ToStatus, "terminal event must end in DELETED")

	var survived *ActivityEventDTO
	for i := range after.Items {
		if after.Items[i].ResourceName == vmName && after.Items[i].Action == "CREATE" {
			survived = &after.Items[i]
			break
		}
	}
	require.NotNil(t, survived, "CREATE event must survive resource deletion")
	assert.Empty(t, survived.ResourceID,
		"resource_id must be omitted once the resource is gone (no dangling deep links)")
	assert.Equal(t, string(models.ResourceTypeVM), survived.ResourceType,
		"resource_type snapshot must survive deletion")
}

// TestActivity_FamilyCoverage_VNet proves the audit framework covers resource
// families outside the legacy resources table: a VNet's CREATE and DELETE
// land in the feed with the registry-resolved VNET kind, and survive the row
// being removed.
func TestActivity_FamilyCoverage_VNet(t *testing.T) {
	ctx := context.Background()
	client := clientForTenant(t, "tenant-act-vnet")
	mustGrantOwnerForClient(t, "tenant-act-vnet")

	vnetName := randomName("act-vnet-fam")
	created, body, status, err := client.CreateVNet(ctx, CreateVNetRequest{
		Name: vnetName, AddressSpace: []string{"10.242.0.0/16"}, Region: "lk",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status, "CreateVNet: %s", ErrorBody(body))
	vnetID := created.Resource.ID

	// CREATE lands with the registry-resolved kind.
	var ev *ActivityEventDTO
	require.Eventually(t, func() bool {
		p, _, s, e := client.ListActivity(ctx, "?limit=100")
		if e != nil || s != http.StatusOK {
			return false
		}
		ev = findActivityEvent(p.Items, vnetID, "CREATE")
		return ev != nil
	}, 10*time.Second, 250*time.Millisecond, "VNet CREATE event never appeared")
	assert.Equal(t, vnetName, ev.ResourceName)
	assert.Equal(t, "VNET", ev.ResourceType, "kind must come from the audit registry")

	// DELETE is recorded before the row goes away and persists afterwards.
	body, status, err = client.DeleteVNet(ctx, vnetID)
	require.NoError(t, err)
	require.Contains(t, []int{http.StatusAccepted, http.StatusNoContent}, status,
		"DeleteVNet: %s", ErrorBody(body))

	require.Eventually(t, func() bool {
		p, _, s, e := client.ListActivity(ctx, "?limit=100")
		if e != nil || s != http.StatusOK {
			return false
		}
		for i := range p.Items {
			it := p.Items[i]
			if it.ResourceName == vnetName && it.Action == "DELETE" && it.ToStatus == "DELETED" {
				// Once deleted, the live pointer must be gone.
				return it.ResourceID == ""
			}
		}
		return false
	}, 15*time.Second, 250*time.Millisecond,
		"VNet terminal DELETE→DELETED event (without a live pointer) never appeared")
}

// TestActivity_NoOpTransitionsAreSkipped pins the framework rule that a
// status update to the SAME status records nothing — the bug class that
// produced DELETING → DELETING entries.
func TestActivity_NoOpTransitionsAreSkipped(t *testing.T) {
	ctx := context.Background()
	client := clientForTenant(t, "tenant-act-noop")
	mustGrantOwnerForClient(t, "tenant-act-noop")

	created, body, status, err := client.CreateVNet(ctx, CreateVNetRequest{
		Name: randomName("act-noop"), AddressSpace: []string{"10.243.0.0/16"}, Region: "lk",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status, "CreateVNet: %s", ErrorBody(body))

	// Wait for the async nop provisioning to settle (PENDING → FAILED).
	require.Eventually(t, func() bool {
		p, _, s, e := client.ListActivity(ctx, "?limit=100")
		return e == nil && s == http.StatusOK && p.Total >= 2
	}, 10*time.Second, 250*time.Millisecond)

	page, _, _, err := client.ListActivity(ctx, "?limit=100")
	require.NoError(t, err)
	before := page.Total

	// Same-status update: must record nothing, for every family by construction.
	require.NoError(t, env.DB.UpdateVNetStatus(ctx,
		uuid.MustParse(created.Resource.ID), models.StatusFailed, "still failed", ""))

	page, _, _, err = client.ListActivity(ctx, "?limit=100")
	require.NoError(t, err)
	assert.Equal(t, before, page.Total, "a no-op status transition must not be recorded")
}

// TestActivity_ViewerCanRead proves the gate is at viewer level: a principal
// holding only the v1 'viewer' rank (→ Reader, `*/read`) gets the feed.
func TestActivity_ViewerCanRead(t *testing.T) {
	ctx := context.Background()
	// Reuse no state from the other test: a dedicated tenant.
	client := clientForTenant(t, "tenant-act-view")
	_ = client // clientForTenant registers the tenant + default project
	tenantID := "test-tenant-act-view"

	viewerSub := tenantID + "-viewer"
	_, err := env.DB.CreateRoleAssignment(ctx, models.RoleAssignment{
		PrincipalType: models.PrincipalTypeUser,
		PrincipalID:   viewerSub,
		ScopeType:     models.ScopeTypeTenant,
		ScopeID:       tenantID,
		Role:          models.RoleViewer,
		GrantedBy:     "test-setup",
	})
	if err != nil && !strings.Contains(err.Error(), "23505") && !strings.Contains(err.Error(), "duplicate key") {
		require.NoError(t, err, "grant viewer role")
	}
	// MintTokenWithGroups does NOT seed a member role — the viewer grant above
	// is this principal's only access.
	token, err := env.JWT.MintTokenWithGroups(viewerSub, viewerSub, nil)
	require.NoError(t, err)
	viewer := NewAPIClientForProject(env.BaseURL, token, tenantID, defaultProjectID)

	page, body, status, err := viewer.ListActivity(ctx, "")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status,
		"a viewer-only principal must be able to read the activity feed: %s", ErrorBody(body))
	assert.Equal(t, 0, page.Total)
}
