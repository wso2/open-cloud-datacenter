//go:build integration

// directory_invite_test.go — IdP directory feature integration tests.
//
// Covers the two surfaces the directory feature added:
//
//  1. POST /v1/tenants/{tenant_id}/role-assignments with `user_email` —
//     resolution through the directory provider (nil provider → 422,
//     not-found/ambiguous → 422, upstream failure → 502, success → 201 with
//     PrincipalID = resolved sub and display_alias defaulting to the email).
//  2. GET /v1/tenants/{tenant_id}/directory/{users,groups} through the FULL
//     middleware stack — TenantContext + the roleAssignments/write gate (a
//     write action on GETs is intentional: only inviters may browse the
//     directory). The handler-internal branches (paging/filter validation,
//     response shapes) are unit-tested in internal/api/handlers/directory_test.go.
//
// These paths are unit-untestable at the handler layer because
// RoleAssignmentsHandler holds the concrete *db.Repository and the RBAC gate
// queries it before any directory code runs. None of them touch
// Harvester/KubeOVN, so the suite is fully exercisable in cluster-free mode:
//
//	DCAPI_TEST_NOP=1 go test -tags integration -run 'EmailInvite|DirectoryEndpoints' ./test/integration/...
//
// The directory provider is a per-test fake wired into a dedicated sub-env
// router (directorySubEnv) — no live IdP is involved.
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"github.com/wso2/dc-api/internal/api"
	"github.com/wso2/dc-api/internal/api/middleware"
	"github.com/wso2/dc-api/internal/directory"
	"github.com/wso2/dc-api/internal/models"
)

// ── Fake directory provider ───────────────────────────────────────────────────

// fakeDirectoryProvider is a deterministic directory.Provider double. Function
// fields stub behaviour; counters record invocations (mutex-guarded — the
// httptest server handles requests on its own goroutines).
type fakeDirectoryProvider struct {
	mu          sync.Mutex
	lookupFn    func(ctx context.Context, email string) (*directory.User, error)
	searchFn    func(ctx context.Context, filter string, limit, offset int) ([]directory.User, int, error)
	groupsFn    func(ctx context.Context, limit, offset int) ([]directory.Group, int, error)
	lookupCalls int
	lastEmail   string
}

func (f *fakeDirectoryProvider) LookupUserByEmail(ctx context.Context, email string) (*directory.User, error) {
	f.mu.Lock()
	f.lookupCalls++
	f.lastEmail = email
	fn := f.lookupFn
	f.mu.Unlock()
	if fn == nil {
		return nil, fmt.Errorf("fakeDirectoryProvider: LookupUserByEmail not stubbed")
	}
	return fn(ctx, email)
}

func (f *fakeDirectoryProvider) SearchUsers(ctx context.Context, filter string, limit, offset int) ([]directory.User, int, error) {
	f.mu.Lock()
	fn := f.searchFn
	f.mu.Unlock()
	if fn == nil {
		return nil, 0, fmt.Errorf("fakeDirectoryProvider: SearchUsers not stubbed")
	}
	return fn(ctx, filter, limit, offset)
}

func (f *fakeDirectoryProvider) ListGroups(ctx context.Context, limit, offset int) ([]directory.Group, int, error) {
	f.mu.Lock()
	fn := f.groupsFn
	f.mu.Unlock()
	if fn == nil {
		return nil, 0, fmt.Errorf("fakeDirectoryProvider: ListGroups not stubbed")
	}
	return fn(ctx, limit, offset)
}

func (f *fakeDirectoryProvider) stats() (calls int, lastEmail string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lookupCalls, f.lastEmail
}

var _ directory.Provider = (*fakeDirectoryProvider)(nil)

// ── Sub-env with a directory provider ─────────────────────────────────────────

// directorySubEnv builds a fresh httptest.Server over the shared env's repo +
// JWT minter with all-nop compute/cluster/network backends and the given
// directory provider (nil = feature dark). AutoProvisionMembers=false so tests
// control role rows exactly (mirrors rbacSubEnv). Works identically in live
// and DCAPI_TEST_NOP=1 modes — nothing here touches the cluster.
func directorySubEnv(t *testing.T, dir directory.Provider) *TestEnv {
	t.Helper()
	testAuth, err := middleware.NewTestModeAuth(env.JWT.PublicKeyJWKS(), middleware.AuthConfig{
		TenantGroupPrefix:    "dc-tenant-",
		AdminGroup:           "dc-admin",
		AutoProvisionMembers: false,
	}, env.DB)
	require.NoError(t, err, "directorySubEnv: create test auth")

	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, NoColor: true}).With().Timestamp().Logger()
	saAuth := middleware.NewServiceAccountAuth(env.DB, logger)
	composite := middleware.NewCompositeAuth(saAuth, testAuth)

	router := api.NewRouter(api.RouterDeps{
		Repo:              env.DB,
		ComputeProvider:   &nopComputeProvider{},
		ClusterProvider:   &nopClusterProvider{},
		NetworkProvider:   nopNetwork{},
		DirectoryProvider: dir,
		AuthMiddleware:    composite,
		Log:               logger,
	})
	srv := httptest.NewServer(router)
	t.Cleanup(func() { srv.Close() })
	return &TestEnv{
		Server:  srv,
		BaseURL: srv.URL,
		DB:      env.DB,
		JWT:     env.JWT,
	}
}

// inviteByEmail POSTs a role-assignment grant body with arbitrary fields set,
// so tests can exercise the user_email / user_sub / display_alias combinations
// the typed InviteMember helper doesn't cover.
func inviteByEmail(t *testing.T, c *APIClient, tenantID string, body map[string]string) (MemberResponse, []byte, int) {
	t.Helper()
	raw, status, err := c.do(context.Background(), http.MethodPost,
		fmt.Sprintf("/v1/tenants/%s/role-assignments", tenantID), body)
	require.NoError(t, err)
	var resp MemberResponse
	_ = json.Unmarshal(raw, &resp)
	return resp, raw, status
}

// ownerClientForDirectoryEnv seeds an Owner role for a fresh tenant on the
// given sub-env and returns the tenant ID plus an authenticated client.
func ownerClientForDirectoryEnv(t *testing.T, subEnv *TestEnv, label string) (tenantID string, c *APIClient) {
	t.Helper()
	tenantID = randomTenantID(label)
	ownerSub := "sub-owner-" + randomName("dir")
	insertRole(t, subEnv, ownerSub, tenantID, models.RoleOwner)
	token := mintTokenForSubEnv(t, subEnv, ownerSub, tenantID)
	return tenantID, NewAPIClient(subEnv.BaseURL, token)
}

// ── Email invite: POST /role-assignments with user_email ─────────────────────

func TestEmailInvite_NilProvider_Returns422(t *testing.T) {
	t.Parallel()
	subEnv := directorySubEnv(t, nil) // feature dark
	tenantID, client := ownerClientForDirectoryEnv(t, subEnv, "inv-nilprov")

	_, raw, status := inviteByEmail(t, client, tenantID, map[string]string{
		"user_email":      "alice@example.com",
		"role_definition": "Contributor",
	})
	require.Equal(t, http.StatusUnprocessableEntity, status,
		"user_email with no directory provider must 422 per InviteEmailUnprocessable; body=%s", raw)
	require.Contains(t, ErrorBody(raw), "directory provider",
		"422 body must explain that no directory provider is configured")
}

func TestEmailInvite_ResolvedHappyPath_StoresSubAndDefaultsAlias(t *testing.T) {
	t.Parallel()
	const resolvedSub = "01abc123-resolved-sub-0001"
	const email = "alice@example.com"
	const displayName = "Alice A"
	fake := &fakeDirectoryProvider{
		lookupFn: func(_ context.Context, _ string) (*directory.User, error) {
			return &directory.User{Sub: resolvedSub, Email: email, DisplayName: displayName}, nil
		},
	}
	subEnv := directorySubEnv(t, fake)
	tenantID, client := ownerClientForDirectoryEnv(t, subEnv, "inv-happy")

	created, raw, status := inviteByEmail(t, client, tenantID, map[string]string{
		"user_email":      email,
		"role_definition": "Contributor",
	})
	require.Equal(t, http.StatusCreated, status, "resolved email invite must 201; body=%s", raw)
	require.Equal(t, resolvedSub, created.PrincipalID,
		"principal_id must be the directory-resolved sub, never the email")
	require.Equal(t, displayName, created.DisplayAlias,
		"display_alias must default to the resolved IdP display name when none is supplied")
	require.Equal(t, "Contributor", created.RoleDefinition)

	calls, lastEmail := fake.stats()
	require.Equal(t, 1, calls, "exactly one directory lookup per invite")
	require.Equal(t, email, lastEmail, "the request's user_email must be passed to the provider verbatim")

	// The persisted row (via LIST) must carry the sub + alias, proving that
	// only the resolved sub plus the display-name
	// default survives.
	listResp, listStatus, err := client.ListMembers(context.Background(), tenantID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listStatus)
	var found *MemberResponse
	for i := range listResp.Members {
		if listResp.Members[i].PrincipalID == resolvedSub {
			found = &listResp.Members[i]
		}
	}
	require.NotNil(t, found, "invited principal must appear in the role-assignment list")
	require.Equal(t, displayName, found.DisplayAlias)
}

// TestEmailInvite_NoDisplayName_DefaultsAliasToEmail pins the fallback half of
// the display-name-with-email-fallback default: when the resolved IdP user has
// no display name, the alias falls back to the invited email.
func TestEmailInvite_NoDisplayName_DefaultsAliasToEmail(t *testing.T) {
	t.Parallel()
	const resolvedSub = "01abc123-resolved-sub-0003"
	const email = "carol@example.com"
	fake := &fakeDirectoryProvider{
		lookupFn: func(_ context.Context, _ string) (*directory.User, error) {
			// No DisplayName set — the IdP has none for this user.
			return &directory.User{Sub: resolvedSub, Email: email}, nil
		},
	}
	subEnv := directorySubEnv(t, fake)
	tenantID, client := ownerClientForDirectoryEnv(t, subEnv, "inv-nodisplay")

	created, raw, status := inviteByEmail(t, client, tenantID, map[string]string{
		"user_email":      email,
		"role_definition": "Contributor",
	})
	require.Equal(t, http.StatusCreated, status, "resolved email invite must 201; body=%s", raw)
	require.Equal(t, resolvedSub, created.PrincipalID,
		"principal_id must be the directory-resolved sub, never the email")
	require.Equal(t, email, created.DisplayAlias,
		"display_alias must fall back to the invited email when the IdP has no display name")
}

func TestEmailInvite_ExplicitAliasNotOverwritten(t *testing.T) {
	t.Parallel()
	const resolvedSub = "01abc123-resolved-sub-0002"
	fake := &fakeDirectoryProvider{
		lookupFn: func(_ context.Context, email string) (*directory.User, error) {
			return &directory.User{Sub: resolvedSub, Email: email}, nil
		},
	}
	subEnv := directorySubEnv(t, fake)
	tenantID, client := ownerClientForDirectoryEnv(t, subEnv, "inv-alias")

	created, raw, status := inviteByEmail(t, client, tenantID, map[string]string{
		"user_email":      "bob@example.com",
		"role_definition": "Reader",
		"display_alias":   "bob-from-finance",
	})
	require.Equal(t, http.StatusCreated, status, "body=%s", raw)
	require.Equal(t, resolvedSub, created.PrincipalID)
	require.Equal(t, "bob-from-finance", created.DisplayAlias,
		"a caller-supplied display_alias must win over the email default")
}

func TestEmailInvite_UserNotFound_Returns422(t *testing.T) {
	t.Parallel()
	fake := &fakeDirectoryProvider{
		lookupFn: func(_ context.Context, _ string) (*directory.User, error) {
			return nil, directory.ErrUserNotFound
		},
	}
	subEnv := directorySubEnv(t, fake)
	tenantID, client := ownerClientForDirectoryEnv(t, subEnv, "inv-nouser")

	_, raw, status := inviteByEmail(t, client, tenantID, map[string]string{
		"user_email":      "ghost@example.com",
		"role_definition": "Contributor",
	})
	require.Equal(t, http.StatusUnprocessableEntity, status, "body=%s", raw)
	require.Contains(t, ErrorBody(raw), "does not match any IdP user")
}

func TestEmailInvite_AmbiguousEmail_Returns422(t *testing.T) {
	t.Parallel()
	fake := &fakeDirectoryProvider{
		lookupFn: func(_ context.Context, _ string) (*directory.User, error) {
			return nil, directory.ErrAmbiguous
		},
	}
	subEnv := directorySubEnv(t, fake)
	tenantID, client := ownerClientForDirectoryEnv(t, subEnv, "inv-ambig")

	_, raw, status := inviteByEmail(t, client, tenantID, map[string]string{
		"user_email":      "twins@example.com",
		"role_definition": "Contributor",
	})
	require.Equal(t, http.StatusUnprocessableEntity, status, "body=%s", raw)
	require.Contains(t, ErrorBody(raw), "more than one IdP user")
}

func TestEmailInvite_UpstreamFailure_Returns502(t *testing.T) {
	t.Parallel()
	fake := &fakeDirectoryProvider{
		lookupFn: func(_ context.Context, _ string) (*directory.User, error) {
			return nil, fmt.Errorf("IdP returned 503")
		},
	}
	subEnv := directorySubEnv(t, fake)
	tenantID, client := ownerClientForDirectoryEnv(t, subEnv, "inv-upstream")

	_, raw, status := inviteByEmail(t, client, tenantID, map[string]string{
		"user_email":      "alice@example.com",
		"role_definition": "Contributor",
	})
	require.Equal(t, http.StatusBadGateway, status,
		"a non-sentinel directory error is an upstream failure → 502; body=%s", raw)
	require.Contains(t, ErrorBody(raw), "directory lookup failed")
}

func TestEmailInvite_BothSubAndEmail_Returns400(t *testing.T) {
	t.Parallel()
	fake := &fakeDirectoryProvider{}
	subEnv := directorySubEnv(t, fake)
	tenantID, client := ownerClientForDirectoryEnv(t, subEnv, "inv-both")

	_, raw, status := inviteByEmail(t, client, tenantID, map[string]string{
		"user_sub":        "sub-explicit",
		"user_email":      "alice@example.com",
		"role_definition": "Contributor",
	})
	require.Equal(t, http.StatusBadRequest, status,
		"user_sub and user_email are mutually exclusive; body=%s", raw)
	calls, _ := fake.stats()
	require.Zero(t, calls, "validation must reject before any directory lookup")
}

func TestEmailInvite_NeitherSubNorEmail_Returns400(t *testing.T) {
	t.Parallel()
	subEnv := directorySubEnv(t, &fakeDirectoryProvider{})
	tenantID, client := ownerClientForDirectoryEnv(t, subEnv, "inv-neither")

	_, raw, status := inviteByEmail(t, client, tenantID, map[string]string{
		"role_definition": "Contributor",
	})
	require.Equal(t, http.StatusBadRequest, status,
		"one of user_sub / user_email is required; body=%s", raw)
}

// TestEmailInvite_UserSubPathUnchanged is the regression guard: a classic
// user_sub grant must behave exactly as before the directory feature landed —
// 201, principal_id = the literal sub, no alias default, and zero directory
// provider involvement.
func TestEmailInvite_UserSubPathUnchanged(t *testing.T) {
	t.Parallel()
	fake := &fakeDirectoryProvider{}
	subEnv := directorySubEnv(t, fake)
	tenantID, client := ownerClientForDirectoryEnv(t, subEnv, "inv-subonly")

	inviteeSub := "sub-invitee-" + randomName("u")
	created, raw, status, err := client.InviteMember(context.Background(), tenantID, inviteeSub, "Contributor")
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, "body=%s", raw)
	require.Equal(t, inviteeSub, created.PrincipalID)
	require.Empty(t, created.DisplayAlias,
		"user_sub invites must not invent a display_alias")

	calls, _ := fake.stats()
	require.Zero(t, calls, "user_sub invites must never consult the directory")
}

// ── Directory endpoints through the full middleware stack ────────────────────

func TestDirectoryEndpoints_OwnerGets200(t *testing.T) {
	t.Parallel()
	fake := &fakeDirectoryProvider{
		searchFn: func(_ context.Context, _ string, _, _ int) ([]directory.User, int, error) {
			return []directory.User{{Sub: "s1", Email: "alice@example.com", DisplayName: "Alice"}}, 1, nil
		},
		groupsFn: func(_ context.Context, _, _ int) ([]directory.Group, int, error) {
			return []directory.Group{{ID: "g1", Name: "platform"}}, 1, nil
		},
	}
	subEnv := directorySubEnv(t, fake)
	tenantID, client := ownerClientForDirectoryEnv(t, subEnv, "dir-owner")
	ctx := context.Background()

	rawUsers, status, err := client.do(ctx, http.MethodGet,
		fmt.Sprintf("/v1/tenants/%s/directory/users?filter=ali", tenantID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status,
		"Owner holds roleAssignments/write → directory must be visible; body=%s", rawUsers)
	var users struct {
		Users        []map[string]any `json:"users"`
		TotalResults *int             `json:"total_results"`
	}
	require.NoError(t, json.Unmarshal(rawUsers, &users))
	require.Len(t, users.Users, 1)
	require.NotNil(t, users.TotalResults)
	require.Equal(t, 1, *users.TotalResults)

	rawGroups, status, err := client.do(ctx, http.MethodGet,
		fmt.Sprintf("/v1/tenants/%s/directory/groups", tenantID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "body=%s", rawGroups)
	var groups struct {
		Groups []map[string]any `json:"groups"`
	}
	require.NoError(t, json.Unmarshal(rawGroups, &groups))
	require.Len(t, groups.Groups, 1)
}

// TestDirectoryEndpoints_ContributorGets403 pins the intentional product
// guardrail: the route is gated with authorization/roleAssignments/write even
// though it's a GET, so Contributor (no write) must NOT see the directory.
func TestDirectoryEndpoints_ContributorGets403(t *testing.T) {
	t.Parallel()
	subEnv := directorySubEnv(t, &fakeDirectoryProvider{})

	tenantID := randomTenantID("dir-contrib")
	memberSub := "sub-member-" + randomName("dir")
	insertRole(t, subEnv, memberSub, tenantID, models.RoleMember) // → Contributor
	client := NewAPIClient(subEnv.BaseURL, mintTokenForSubEnv(t, subEnv, memberSub, tenantID))
	ctx := context.Background()

	for _, path := range []string{"users", "groups"} {
		raw, status, err := client.do(ctx, http.MethodGet,
			fmt.Sprintf("/v1/tenants/%s/directory/%s", tenantID, path), nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusForbidden, status,
			"Contributor lacks roleAssignments/write → directory/%s must 403; body=%s", path, raw)
	}
}

func TestDirectoryEndpoints_NilProvider_Returns501ThroughStack(t *testing.T) {
	t.Parallel()
	subEnv := directorySubEnv(t, nil)
	tenantID, client := ownerClientForDirectoryEnv(t, subEnv, "dir-nilprov")
	ctx := context.Background()

	for _, path := range []string{"users", "groups"} {
		raw, status, err := client.do(ctx, http.MethodGet,
			fmt.Sprintf("/v1/tenants/%s/directory/%s", tenantID, path), nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusNotImplemented, status,
			"nil provider → 501 feature-detection on directory/%s; body=%s", path, raw)
		require.Contains(t, ErrorBody(raw), "no directory provider is configured")
	}
}

func TestDirectoryEndpoints_CrossTenant_Returns404(t *testing.T) {
	t.Parallel()
	subEnv := directorySubEnv(t, &fakeDirectoryProvider{})

	tenantA := randomTenantID("dir-xt-a")
	tenantB := randomTenantID("dir-xt-b")
	ownerSub := "sub-owner-" + randomName("dirx")
	insertRole(t, subEnv, ownerSub, tenantA, models.RoleOwner)
	insertRole(t, subEnv, "sub-other-"+randomName("dirx"), tenantB, models.RoleOwner) // tenantB exists
	client := NewAPIClient(subEnv.BaseURL, mintTokenForSubEnv(t, subEnv, ownerSub, tenantA))

	raw, status, err := client.do(context.Background(), http.MethodGet,
		fmt.Sprintf("/v1/tenants/%s/directory/users", tenantB), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status,
		"tenant A's owner must get 404 on tenant B's directory URL (non-enumerable); body=%s", raw)
}
