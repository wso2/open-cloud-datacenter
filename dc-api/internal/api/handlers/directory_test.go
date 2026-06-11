// Package handlers — directory_test.go
//
// Unit tests for DirectoryHandler (the optional IdP SCIM2 directory proxy).
// These exercise the handler layer in isolation with a fake directory.Provider:
// no DB, no httptest.Server — requests are built with httptest.NewRequest and
// the tenant/principal context is injected exactly as the auth + tenant-context
// middleware would. The directory package's own SCIM2 behaviour is covered in
// internal/directory/scim2_test.go and is NOT duplicated here.
//
// RBAC gating (authorization/roleAssignments/write on the route) is applied in
// router.go via Gate, not inside the handler — it is covered by the integration
// suite (directory_invite_test.go), not here.
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wso2/dc-api/internal/api/middleware"
	"github.com/wso2/dc-api/internal/directory"
)

// ── Fake directory.Provider ───────────────────────────────────────────────────

// fakeDirectory implements directory.Provider with pluggable function fields
// and records the arguments of the last call so tests can assert passthrough.
type fakeDirectory struct {
	lookupFn func(ctx context.Context, email string) (*directory.User, error)
	searchFn func(ctx context.Context, filter string, limit, offset int) ([]directory.User, int, error)
	groupsFn func(ctx context.Context, limit, offset int) ([]directory.Group, int, error)

	searchCalls int
	groupCalls  int
	gotFilter   string
	gotLimit    int
	gotOffset   int
}

func (f *fakeDirectory) LookupUserByEmail(ctx context.Context, email string) (*directory.User, error) {
	if f.lookupFn == nil {
		return nil, errors.New("fakeDirectory: LookupUserByEmail not stubbed")
	}
	return f.lookupFn(ctx, email)
}

func (f *fakeDirectory) SearchUsers(ctx context.Context, filter string, limit, offset int) ([]directory.User, int, error) {
	f.searchCalls++
	f.gotFilter, f.gotLimit, f.gotOffset = filter, limit, offset
	if f.searchFn == nil {
		return nil, 0, errors.New("fakeDirectory: SearchUsers not stubbed")
	}
	return f.searchFn(ctx, filter, limit, offset)
}

func (f *fakeDirectory) ListGroups(ctx context.Context, limit, offset int) ([]directory.Group, int, error) {
	f.groupCalls++
	f.gotLimit, f.gotOffset = limit, offset
	if f.groupsFn == nil {
		return nil, 0, errors.New("fakeDirectory: ListGroups not stubbed")
	}
	return f.groupsFn(ctx, limit, offset)
}

// Compile-time interface check.
var _ directory.Provider = (*fakeDirectory)(nil)

// ── Request builder ───────────────────────────────────────────────────────────

// directoryRequest builds a GET request to target with the chi tenant_id URL
// param and the context values the auth middleware chain would inject:
// ContextKeyTenantID (the caller's tenant) and ContextKeyIsAdmin.
func directoryRequest(target, urlTenant, ctxTenant string, admin bool) *http.Request {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("tenant_id", urlTenant)
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, rctx)
	if ctxTenant != "" {
		ctx = context.WithValue(ctx, middleware.ContextKeyTenantID, ctxTenant)
	}
	if admin {
		ctx = context.WithValue(ctx, middleware.ContextKeyIsAdmin, true)
	}
	return r.WithContext(ctx)
}

// errorField decodes the standard {"error": "..."} envelope.
func errorField(t *testing.T, body []byte) string {
	t.Helper()
	var e struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(body, &e), "response must be the spec's Error envelope; got: %s", body)
	require.NotEmpty(t, e.Error, "Error envelope requires a non-empty `error` field; got: %s", body)
	return e.Error
}

// ── Nil provider → 501 (feature-detection contract) ───────────────────────────

func TestDirectoryListUsers_NilProvider_Returns501(t *testing.T) {
	t.Parallel()
	h := NewDirectoryHandler(nil, zerolog.Nop())
	w := httptest.NewRecorder()

	h.ListUsers(w, directoryRequest("/v1/tenants/acme/directory/users", "acme", "acme", false))

	assert.Equal(t, http.StatusNotImplemented, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	assert.Equal(t, "no directory provider is configured on this deployment",
		errorField(t, w.Body.Bytes()),
		"501 body must match the spec's DirectoryNotConfigured example")
}

func TestDirectoryListGroups_NilProvider_Returns501(t *testing.T) {
	t.Parallel()
	h := NewDirectoryHandler(nil, zerolog.Nop())
	w := httptest.NewRecorder()

	h.ListGroups(w, directoryRequest("/v1/tenants/acme/directory/groups", "acme", "acme", false))

	assert.Equal(t, http.StatusNotImplemented, w.Code)
	assert.Equal(t, "no directory provider is configured on this deployment",
		errorField(t, w.Body.Bytes()))
}

// ── Success shapes ────────────────────────────────────────────────────────────

func TestDirectoryListUsers_Success_ReturnsUsersAndTotalResults(t *testing.T) {
	t.Parallel()
	fake := &fakeDirectory{
		searchFn: func(_ context.Context, _ string, _, _ int) ([]directory.User, int, error) {
			return []directory.User{
				{Sub: "sub-1", Email: "alice@example.com", DisplayName: "Alice A"},
				{Sub: "sub-2", Email: "aldo@example.com"}, // no display name
			}, 7, nil // total > page length proves total_results is the SCIM totalResults passthrough
		},
	}
	h := NewDirectoryHandler(fake, zerolog.Nop())
	w := httptest.NewRecorder()

	h.ListUsers(w, directoryRequest("/v1/tenants/acme/directory/users?filter=al", "acme", "acme", false))

	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

	var resp struct {
		Users []map[string]any `json:"users"`
		Total *int             `json:"total_results"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotNil(t, resp.Total, "total_results must be present")
	assert.Equal(t, 7, *resp.Total)
	require.Len(t, resp.Users, 2)

	assert.Equal(t, "sub-1", resp.Users[0]["sub"])
	assert.Equal(t, "alice@example.com", resp.Users[0]["email"])
	assert.Equal(t, "Alice A", resp.Users[0]["display_name"])

	assert.Equal(t, "sub-2", resp.Users[1]["sub"])
	_, hasDisplayName := resp.Users[1]["display_name"]
	assert.False(t, hasDisplayName, "empty display_name must be omitted (omitempty), not rendered as \"\"")
}

func TestDirectoryListUsers_EmptyResult_ReturnsEmptyArrayNotNull(t *testing.T) {
	t.Parallel()
	fake := &fakeDirectory{
		searchFn: func(_ context.Context, _ string, _, _ int) ([]directory.User, int, error) {
			return nil, 0, nil
		},
	}
	h := NewDirectoryHandler(fake, zerolog.Nop())
	w := httptest.NewRecorder()

	h.ListUsers(w, directoryRequest("/v1/tenants/acme/directory/users", "acme", "acme", false))

	require.Equal(t, http.StatusOK, w.Code)
	body := strings.TrimSpace(w.Body.String())
	assert.Contains(t, body, `"users":[]`, "zero matches must serialize as [] (spec: array), not null")
	assert.Contains(t, body, `"total_results":0`)
}

func TestDirectoryListGroups_Success_ReturnsGroupsAndTotalResults(t *testing.T) {
	t.Parallel()
	fake := &fakeDirectory{
		groupsFn: func(_ context.Context, _, _ int) ([]directory.Group, int, error) {
			return []directory.Group{
				{ID: "g-1", Name: "platform-team"},
				{ID: "g-2", Name: "sre"},
			}, 12, nil
		},
	}
	h := NewDirectoryHandler(fake, zerolog.Nop())
	w := httptest.NewRecorder()

	h.ListGroups(w, directoryRequest("/v1/tenants/acme/directory/groups", "acme", "acme", false))

	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

	var resp struct {
		Groups []map[string]any `json:"groups"`
		Total  *int             `json:"total_results"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotNil(t, resp.Total)
	assert.Equal(t, 12, *resp.Total)
	require.Len(t, resp.Groups, 2)
	assert.Equal(t, "g-1", resp.Groups[0]["id"])
	assert.Equal(t, "platform-team", resp.Groups[0]["name"])
}

// ── Filter + paging passthrough ───────────────────────────────────────────────

func TestDirectoryListUsers_FilterAndPagingPassedToProvider(t *testing.T) {
	t.Parallel()
	fake := &fakeDirectory{
		searchFn: func(_ context.Context, _ string, _, _ int) ([]directory.User, int, error) {
			return nil, 0, nil
		},
	}
	h := NewDirectoryHandler(fake, zerolog.Nop())
	w := httptest.NewRecorder()

	h.ListUsers(w, directoryRequest(
		"/v1/tenants/acme/directory/users?filter=alice&limit=5&offset=10", "acme", "acme", false))

	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	assert.Equal(t, 1, fake.searchCalls)
	assert.Equal(t, "alice", fake.gotFilter, "filter must be passed through verbatim")
	assert.Equal(t, 5, fake.gotLimit)
	assert.Equal(t, 10, fake.gotOffset)
}

func TestDirectoryListUsers_DefaultPaging(t *testing.T) {
	t.Parallel()
	fake := &fakeDirectory{
		searchFn: func(_ context.Context, _ string, _, _ int) ([]directory.User, int, error) {
			return nil, 0, nil
		},
	}
	h := NewDirectoryHandler(fake, zerolog.Nop())
	w := httptest.NewRecorder()

	h.ListUsers(w, directoryRequest("/v1/tenants/acme/directory/users", "acme", "acme", false))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "", fake.gotFilter, "omitted filter must arrive as empty string (list-all)")
	assert.Equal(t, 50, fake.gotLimit, "spec default limit is 50")
	assert.Equal(t, 0, fake.gotOffset, "spec default offset is 0")
}

func TestDirectoryListGroups_PagingPassedToProvider(t *testing.T) {
	t.Parallel()
	fake := &fakeDirectory{
		groupsFn: func(_ context.Context, _, _ int) ([]directory.Group, int, error) {
			return nil, 0, nil
		},
	}
	h := NewDirectoryHandler(fake, zerolog.Nop())
	w := httptest.NewRecorder()

	h.ListGroups(w, directoryRequest("/v1/tenants/acme/directory/groups?limit=200&offset=3", "acme", "acme", false))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 1, fake.groupCalls)
	assert.Equal(t, 200, fake.gotLimit, "limit=200 is the inclusive maximum and must be accepted")
	assert.Equal(t, 3, fake.gotOffset)
}

// ── Upstream failure → 502 ────────────────────────────────────────────────────

func TestDirectoryListUsers_UpstreamError_Returns502(t *testing.T) {
	t.Parallel()
	fake := &fakeDirectory{
		searchFn: func(_ context.Context, _ string, _, _ int) ([]directory.User, int, error) {
			return nil, 0, errors.New("IdP returned 503")
		},
	}
	h := NewDirectoryHandler(fake, zerolog.Nop())
	w := httptest.NewRecorder()

	h.ListUsers(w, directoryRequest("/v1/tenants/acme/directory/users", "acme", "acme", false))

	assert.Equal(t, http.StatusBadGateway, w.Code)
	assert.Equal(t, directoryUpstreamErrorMsg, errorField(t, w.Body.Bytes()),
		"502 body must be the generic DirectoryUpstreamError message")
	assert.NotContains(t, errorField(t, w.Body.Bytes()), "connection refused",
		"upstream error detail must never leak to API clients")
}

func TestDirectoryListGroups_UpstreamError_Returns502(t *testing.T) {
	t.Parallel()
	fake := &fakeDirectory{
		groupsFn: func(_ context.Context, _, _ int) ([]directory.Group, int, error) {
			return nil, 0, errors.New("connection refused")
		},
	}
	h := NewDirectoryHandler(fake, zerolog.Nop())
	w := httptest.NewRecorder()

	h.ListGroups(w, directoryRequest("/v1/tenants/acme/directory/groups", "acme", "acme", false))

	assert.Equal(t, http.StatusBadGateway, w.Code)
	assert.Equal(t, directoryUpstreamErrorMsg, errorField(t, w.Body.Bytes()),
		"502 body must be the generic DirectoryUpstreamError message")
	assert.NotContains(t, errorField(t, w.Body.Bytes()), "connection refused",
		"upstream error detail must never leak to API clients")
}

// ── Paging validation → 400 (provider must NOT be called) ─────────────────────

func TestDirectoryListUsers_InvalidPaging_Returns400(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		query string
	}{
		{"limit zero", "?limit=0"},
		{"limit above max", "?limit=201"},
		{"limit negative", "?limit=-1"},
		{"limit non-integer", "?limit=abc"},
		{"offset negative", "?offset=-1"},
		{"offset non-integer", "?offset=xyz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeDirectory{}
			h := NewDirectoryHandler(fake, zerolog.Nop())
			w := httptest.NewRecorder()

			h.ListUsers(w, directoryRequest(
				"/v1/tenants/acme/directory/users"+tc.query, "acme", "acme", false))

			assert.Equal(t, http.StatusBadRequest, w.Code, "body=%s", w.Body.String())
			errorField(t, w.Body.Bytes()) // must be the Error envelope
			assert.Zero(t, fake.searchCalls, "provider must not be hit on invalid paging")
		})
	}
}

func TestDirectoryListGroups_InvalidPaging_Returns400(t *testing.T) {
	t.Parallel()
	cases := []string{"?limit=0", "?limit=201", "?limit=abc", "?offset=-1", "?offset=abc"}
	for _, q := range cases {
		t.Run(q, func(t *testing.T) {
			t.Parallel()
			fake := &fakeDirectory{}
			h := NewDirectoryHandler(fake, zerolog.Nop())
			w := httptest.NewRecorder()

			h.ListGroups(w, directoryRequest("/v1/tenants/acme/directory/groups"+q, "acme", "acme", false))

			assert.Equal(t, http.StatusBadRequest, w.Code, "body=%s", w.Body.String())
			assert.Zero(t, fake.groupCalls, "provider must not be hit on invalid paging")
		})
	}
}

// ── Filter validation → 400 (SCIM injection defence) ──────────────────────────

func TestDirectoryListUsers_InvalidFilter_Returns400(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		filter string
	}{
		{"double quote", `ali"ce`},
		{"backslash", `ali\ce`},
		{"over max length", strings.Repeat("a", 257)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeDirectory{}
			h := NewDirectoryHandler(fake, zerolog.Nop())
			w := httptest.NewRecorder()

			req := directoryRequest("/v1/tenants/acme/directory/users", "acme", "acme", false)
			q := req.URL.Query()
			q.Set("filter", tc.filter)
			req.URL.RawQuery = q.Encode()

			h.ListUsers(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code, "body=%s", w.Body.String())
			assert.Zero(t, fake.searchCalls, "unsafe filter must be rejected before the provider call")
		})
	}
}

func TestDirectoryListUsers_FilterAtMaxLength_Accepted(t *testing.T) {
	t.Parallel()
	fake := &fakeDirectory{
		searchFn: func(_ context.Context, _ string, _, _ int) ([]directory.User, int, error) {
			return nil, 0, nil
		},
	}
	h := NewDirectoryHandler(fake, zerolog.Nop())
	w := httptest.NewRecorder()

	filter := strings.Repeat("a", 256) // inclusive boundary
	req := directoryRequest("/v1/tenants/acme/directory/users", "acme", "acme", false)
	q := req.URL.Query()
	q.Set("filter", filter)
	req.URL.RawQuery = q.Encode()

	h.ListUsers(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	assert.Equal(t, filter, fake.gotFilter)
}

// ── Tenant guard ──────────────────────────────────────────────────────────────

func TestDirectoryListUsers_CrossTenantURL_Returns404(t *testing.T) {
	t.Parallel()
	fake := &fakeDirectory{}
	h := NewDirectoryHandler(fake, zerolog.Nop())
	w := httptest.NewRecorder()

	// Caller's tenant context is tenant-a, but the URL names tenant-b.
	h.ListUsers(w, directoryRequest("/v1/tenants/tenant-b/directory/users", "tenant-b", "tenant-a", false))

	assert.Equal(t, http.StatusNotFound, w.Code,
		"cross-tenant access must 404 so tenant identifiers are not enumerable")
	assert.Equal(t, "tenant not found", errorField(t, w.Body.Bytes()))
	assert.Zero(t, fake.searchCalls)
}

func TestDirectoryListGroups_CrossTenantURL_Returns404(t *testing.T) {
	t.Parallel()
	fake := &fakeDirectory{}
	h := NewDirectoryHandler(fake, zerolog.Nop())
	w := httptest.NewRecorder()

	h.ListGroups(w, directoryRequest("/v1/tenants/tenant-b/directory/groups", "tenant-b", "tenant-a", false))

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Zero(t, fake.groupCalls)
}

func TestDirectoryListUsers_CrossTenantURL_AdminExempt(t *testing.T) {
	t.Parallel()
	fake := &fakeDirectory{
		searchFn: func(_ context.Context, _ string, _, _ int) ([]directory.User, int, error) {
			return nil, 0, nil
		},
	}
	h := NewDirectoryHandler(fake, zerolog.Nop())
	w := httptest.NewRecorder()

	// Admin context with a mismatched URL tenant must pass the guard.
	h.ListUsers(w, directoryRequest("/v1/tenants/tenant-b/directory/users", "tenant-b", "tenant-a", true))

	assert.Equal(t, http.StatusOK, w.Code, "platform admins operate across tenants; body=%s", w.Body.String())
	assert.Equal(t, 1, fake.searchCalls)
}

func TestDirectoryListUsers_NoTenantInContext_Returns401(t *testing.T) {
	t.Parallel()
	h := NewDirectoryHandler(&fakeDirectory{}, zerolog.Nop())
	w := httptest.NewRecorder()

	h.ListUsers(w, directoryRequest("/v1/tenants/acme/directory/users", "acme", "" /* no tenant ctx */, false))

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
