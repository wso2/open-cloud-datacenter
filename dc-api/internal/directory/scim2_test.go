package directory

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scimStub records requests and serves canned SCIM list responses keyed by path.
type scimStub struct {
	t        *testing.T
	requests []*url.URL
	// respond maps "path|filter" → response body. Empty filter key matches
	// requests without a filter parameter.
	respond map[string]string
	status  int
}

func (s *scimStub) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.requests = append(s.requests, r.URL)
		if s.status != 0 && s.status != http.StatusOK {
			w.WriteHeader(s.status)
			return
		}
		key := r.URL.Path + "|" + r.URL.Query().Get("filter")
		body, ok := s.respond[key]
		if !ok {
			s.t.Fatalf("unexpected SCIM request: %s", r.URL)
		}
		w.Header().Set("Content-Type", "application/scim+json")
		_, _ = w.Write([]byte(body))
	}
}

func newTestClient(t *testing.T, stub *scimStub) (*SCIM2Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)
	return NewSCIM2Client(srv.URL, "", "", "", "", "", srv.Client()), srv
}

// listBody splices pre-marshalled JSON resource objects into a SCIM list response.
func listBody(total int, resources ...string) string {
	raw := "[" + strings.Join(resources, ",") + "]"
	return fmt.Sprintf(`{"totalResults":%d,"Resources":%s}`, total, raw)
}

const aliceJSON = `{
	"id": "sub-alice-1",
	"userName": "alice@example.com",
	"name": {"formatted": "Alice Perera", "givenName": "Alice", "familyName": "Perera"},
	"emails": [{"value": "alice@example.com", "primary": true}]
}`

func TestLookupUserByEmail_FoundViaUserName(t *testing.T) {
	stub := &scimStub{t: t, respond: map[string]string{
		`/Users|userName eq "alice@example.com"`: listBody(1, aliceJSON),
	}}
	c, _ := newTestClient(t, stub)

	u, err := c.LookupUserByEmail(context.Background(), "alice@example.com")
	require.NoError(t, err)
	assert.Equal(t, "sub-alice-1", u.Sub)
	assert.Equal(t, "alice@example.com", u.Email)
	assert.Equal(t, "Alice Perera", u.DisplayName)
	require.Len(t, stub.requests, 1)
}

func TestLookupUserByEmail_FallsBackToEmailsFilter(t *testing.T) {
	// userName lookup misses (userName is not the email); emails filter hits.
	bob := `{"id":"sub-bob","userName":"bob.k","emails":["bob@example.com"]}`
	stub := &scimStub{t: t, respond: map[string]string{
		`/Users|userName eq "bob@example.com"`: listBody(0),
		`/Users|emails eq "bob@example.com"`:   listBody(1, bob),
	}}
	c, _ := newTestClient(t, stub)

	u, err := c.LookupUserByEmail(context.Background(), "bob@example.com")
	require.NoError(t, err)
	assert.Equal(t, "sub-bob", u.Sub)
	assert.Equal(t, "bob@example.com", u.Email) // string-encoded email decoded
	assert.Equal(t, "bob.k", u.DisplayName)     // no name → userName fallback
	require.Len(t, stub.requests, 2)
}

func TestLookupUserByEmail_NotFound(t *testing.T) {
	stub := &scimStub{t: t, respond: map[string]string{
		`/Users|userName eq "ghost@example.com"`: listBody(0),
		`/Users|emails eq "ghost@example.com"`:   listBody(0),
	}}
	c, _ := newTestClient(t, stub)

	_, err := c.LookupUserByEmail(context.Background(), "ghost@example.com")
	assert.ErrorIs(t, err, ErrUserNotFound)
}

func TestLookupUserByEmail_Ambiguous(t *testing.T) {
	stub := &scimStub{t: t, respond: map[string]string{
		`/Users|userName eq "dup@example.com"`: listBody(2, aliceJSON, aliceJSON),
	}}
	c, _ := newTestClient(t, stub)

	_, err := c.LookupUserByEmail(context.Background(), "dup@example.com")
	assert.ErrorIs(t, err, ErrAmbiguous)
}

func TestLookupUserByEmail_RejectsFilterInjection(t *testing.T) {
	c := NewSCIM2Client("http://unused", "", "", "", "", "", &http.Client{})
	for _, bad := range []string{`a" or id pr or userName eq "`, "a\\b@x.com", "a\nb@x.com"} {
		_, err := c.LookupUserByEmail(context.Background(), bad)
		assert.ErrorIs(t, err, ErrBadFilter, "input %q", bad)
	}
}

func TestSearchUsers_PagingFilterAndCache(t *testing.T) {
	filter := `userName co "ali" or name.formatted co "ali" or emails co "ali"`
	stub := &scimStub{t: t, respond: map[string]string{
		"/Users|" + filter: listBody(137, aliceJSON),
	}}
	c, _ := newTestClient(t, stub)

	users, total, err := c.SearchUsers(context.Background(), "ali", 50, 100)
	require.NoError(t, err)
	assert.Equal(t, 137, total)
	require.Len(t, users, 1)
	assert.Equal(t, "sub-alice-1", users[0].Sub)

	// SCIM paging: startIndex = offset+1, count = limit.
	require.Len(t, stub.requests, 1)
	q := stub.requests[0].Query()
	assert.Equal(t, "101", q.Get("startIndex"))
	assert.Equal(t, "50", q.Get("count"))

	// Second identical call is served from cache — no new request.
	_, total2, err := c.SearchUsers(context.Background(), "ali", 50, 100)
	require.NoError(t, err)
	assert.Equal(t, 137, total2)
	assert.Len(t, stub.requests, 1, "second call must hit the cache")
}

func TestSearchUsers_EmptyFilterListsAll(t *testing.T) {
	stub := &scimStub{t: t, respond: map[string]string{
		"/Users|": listBody(1, aliceJSON),
	}}
	c, _ := newTestClient(t, stub)

	_, _, err := c.SearchUsers(context.Background(), "", 50, 0)
	require.NoError(t, err)
	require.Len(t, stub.requests, 1)
	_, hasFilter := stub.requests[0].Query()["filter"]
	assert.False(t, hasFilter, "empty filter must omit the filter parameter")
}

func TestListGroups_MapsAndExcludesMembers(t *testing.T) {
	stub := &scimStub{t: t, respond: map[string]string{
		"/Groups|": `{"totalResults":12,"Resources":[{"id":"grp-1","displayName":"platform-engineering","members":[{"value":"should-not-be-read"}]}]}`,
	}}
	c, _ := newTestClient(t, stub)

	groups, total, err := c.ListGroups(context.Background(), 50, 0)
	require.NoError(t, err)
	assert.Equal(t, 12, total)
	require.Len(t, groups, 1)
	assert.Equal(t, Group{ID: "grp-1", Name: "platform-engineering"}, groups[0])

	q := stub.requests[0].Query()
	assert.Equal(t, "members", q.Get("excludedAttributes"))
	assert.Equal(t, "1", q.Get("startIndex"))
	assert.Equal(t, "50", q.Get("count"))
}

func TestUpstreamErrorIsWrappedNotSentinel(t *testing.T) {
	stub := &scimStub{t: t, status: http.StatusServiceUnavailable, respond: map[string]string{}}
	c, _ := newTestClient(t, stub)

	_, _, err := c.SearchUsers(context.Background(), "x", 10, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "IdP returned 503")
	assert.False(t, errors.Is(err, ErrUserNotFound))
	assert.False(t, errors.Is(err, ErrBadFilter))
}

func TestParseScopes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace only", "   \t ", nil},
		{"commas only", " , , ", nil},
		{"single", "internal_user_mgt_view", []string{"internal_user_mgt_view"}},
		{
			"space separated",
			"internal_user_mgt_list internal_user_mgt_view internal_group_mgt_view",
			[]string{"internal_user_mgt_list", "internal_user_mgt_view", "internal_group_mgt_view"},
		},
		{
			"comma separated",
			"internal_user_mgt_list,internal_user_mgt_view,internal_group_mgt_view",
			[]string{"internal_user_mgt_list", "internal_user_mgt_view", "internal_group_mgt_view"},
		},
		{
			"mixed separators and padding",
			" internal_user_mgt_list, internal_user_mgt_view  internal_group_mgt_view , ",
			[]string{"internal_user_mgt_list", "internal_user_mgt_view", "internal_group_mgt_view"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, parseScopes(tc.in))
		})
	}
}

// tokenEndpoint is an httptest server that captures the OAuth2 token request's
// form values and answers with a canned client_credentials token. It lets the
// test assert exactly what scopes (if any) the SCIM client requested.
func tokenEndpoint(t *testing.T, captured *url.Values) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		*captured = r.Form
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"Bearer","expires_in":3600}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTokenRequestCarriesConfiguredScopes(t *testing.T) {
	var form url.Values
	tokenSrv := tokenEndpoint(t, &form)

	// SCIM endpoint returns an empty list; we only care that a token was fetched.
	scimStub := &scimStub{t: t, respond: map[string]string{"/Users|": listBody(0)}}
	scimSrv := httptest.NewServer(scimStub.handler())
	t.Cleanup(scimSrv.Close)

	scopes := "internal_user_mgt_list internal_user_mgt_view internal_group_mgt_view"
	// httpOverride nil → real OAuth2 client_credentials exchange against tokenSrv.
	c := NewSCIM2Client(scimSrv.URL, tokenSrv.URL, "cid", "secret", scopes, "", nil)

	_, _, err := c.SearchUsers(context.Background(), "", 10, 0)
	require.NoError(t, err)

	// The OAuth2 wire format space-joins scopes into a single "scope" field.
	assert.Equal(t,
		"internal_user_mgt_list internal_user_mgt_view internal_group_mgt_view",
		form.Get("scope"))
	assert.Equal(t, "client_credentials", form.Get("grant_type"))
}

func TestTokenRequestOmitsScopesWhenUnconfigured(t *testing.T) {
	var form url.Values
	tokenSrv := tokenEndpoint(t, &form)

	scimStub := &scimStub{t: t, respond: map[string]string{"/Users|": listBody(0)}}
	scimSrv := httptest.NewServer(scimStub.handler())
	t.Cleanup(scimSrv.Close)

	c := NewSCIM2Client(scimSrv.URL, tokenSrv.URL, "cid", "secret", "", "", nil)

	_, _, err := c.SearchUsers(context.Background(), "", 10, 0)
	require.NoError(t, err)

	// No scopes configured → no scope parameter on the token request.
	_, present := form["scope"]
	assert.False(t, present, "token request must omit scope when none configured")
}

func TestToUser_StripsUserstoreDomain(t *testing.T) {
	cases := []struct {
		name        string
		user        scimUser
		wantEmail   string
		wantDisplay string
	}{
		{
			name:        "DEFAULT-store user with no name attributes",
			user:        scimUser{ID: "u1", UserName: "DEFAULT/alice@example.com"},
			wantEmail:   "alice@example.com",
			wantDisplay: "alice@example.com",
		},
		{
			name:        "primary-store user passes through unchanged",
			user:        scimUser{ID: "u2", UserName: "bob@example.com"},
			wantEmail:   "bob@example.com",
			wantDisplay: "bob@example.com",
		},
		{
			name: "name attributes still win over the stripped userName",
			user: func() scimUser {
				u := scimUser{ID: "u3", UserName: "DEFAULT/carol@example.com"}
				u.Name.GivenName = "Carol"
				u.Name.FamilyName = "Day"
				return u
			}(),
			wantEmail:   "carol@example.com",
			wantDisplay: "Carol Day",
		},
		{
			name:        "slash inside an address segment is not a store prefix",
			user:        scimUser{ID: "u4", UserName: "weird@example.com/extra"},
			wantEmail:   "weird@example.com/extra",
			wantDisplay: "weird@example.com/extra",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.user.toUser()
			assert.Equal(t, tc.wantEmail, got.Email)
			assert.Equal(t, tc.wantDisplay, got.DisplayName)
		})
	}
}

func TestUserstoreDomainParameter(t *testing.T) {
	stub := &scimStub{t: t, respond: map[string]string{
		"/Users|":                                listBody(1, aliceJSON),
		`/Users|userName eq "alice@example.com"`: listBody(1, aliceJSON),
		"/Groups|":                               `{"totalResults":0,"Resources":[]}`,
	}}
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)
	c := NewSCIM2Client(srv.URL, "", "", "", "", "DEFAULT", srv.Client())

	_, _, err := c.SearchUsers(context.Background(), "", 10, 0)
	require.NoError(t, err)
	_, err = c.LookupUserByEmail(context.Background(), "alice@example.com")
	require.NoError(t, err)
	_, _, err = c.ListGroups(context.Background(), 10, 0)
	require.NoError(t, err)

	require.Len(t, stub.requests, 3)
	for _, r := range stub.requests {
		assert.Equal(t, "DEFAULT", r.Query().Get("domain"),
			"request %s must carry the userstore domain", r)
	}

	// Empty domain → the parameter is omitted entirely.
	stub2 := &scimStub{t: t, respond: map[string]string{"/Users|": listBody(0)}}
	c2, _ := newTestClient(t, stub2)
	_, _, err = c2.SearchUsers(context.Background(), "", 10, 0)
	require.NoError(t, err)
	require.Len(t, stub2.requests, 1)
	_, has := stub2.requests[0].Query()["domain"]
	assert.False(t, has, "empty domain must omit the parameter")
}
