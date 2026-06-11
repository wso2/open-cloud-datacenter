// Package directory — scim2.go
//
// SCIM2Client implements Provider against any SCIM 2.0 Users/Groups API
// (Asgardeo's /t/{org}/scim2 is the reference deployment). Authentication is
// OAuth2 client_credentials via golang.org/x/oauth2/clientcredentials, which
// caches the access token and refreshes it transparently.
//
// Caching policy:
//   - SearchUsers / ListGroups responses are cached in-memory for a short TTL
//     (30s, keyed by query+page, bounded size) so a type-ahead UI doesn't
//     hammer a rate-limited SaaS IdP.
//   - LookupUserByEmail (the invite path) is deliberately NOT cached:
//     correctness at grant time beats rate limits there — a stale "not found"
//     would block an invite for the cache TTL after the user is created.
package directory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

const (
	scimTimeout   = 30 * time.Second
	cacheTTL      = 30 * time.Second
	cacheMaxEntry = 256
)

// SCIM2Client implements Provider against a SCIM 2.0 endpoint.
type SCIM2Client struct {
	baseURL string // e.g. https://api.asgardeo.io/t/{org}/scim2 — no trailing slash
	domain  string // userstore domain filter (`domain` query param); "" = all stores
	http    *http.Client

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	users   []User
	groups  []Group
	total   int
	expires time.Time
}

// NewSCIM2Client builds a SCIM2 directory provider.
//
// baseURL is the SCIM2 root (e.g. "https://api.asgardeo.io/t/{org}/scim2");
// tokenURL/clientID/clientSecret configure the OAuth2 client_credentials grant.
//
// scopes is the OAuth2 scope string requested with the client_credentials
// grant, split on spaces AND commas (empties trimmed). It MUST list read-only
// VIEW/LIST scopes only — NEVER write scopes. Asgardeo and WSO2 Identity Server
// only attach SCIM permissions to the token when the scopes are explicitly
// requested, so the read-only set there is
// "internal_user_mgt_list internal_user_mgt_view internal_group_mgt_view";
// other IdPs grant SCIM access to the M2M app directly and need none. As a
// guardrail, dc-api never issues SCIM writes, so even a leaked credential
// scoped this way cannot modify identities.
//
// userstoreDomain restricts every user and group read to one userstore via
// the WSO2 `domain` query parameter ("DEFAULT" is Asgardeo's consumer store;
// on-prem WSO2 Identity Server typically uses "PRIMARY"). Without it the IdP
// returns every account in the organization — including console
// administrators and collaborators, who are not invite candidates. "" sends
// no parameter (all stores); non-WSO2 SCIM servers ignore the parameter.
//
// httpOverride is for tests only: when non-nil it is used verbatim for SCIM
// requests (no OAuth2 token exchange). Pass nil in production.
func NewSCIM2Client(baseURL, tokenURL, clientID, clientSecret, scopes, userstoreDomain string, httpOverride *http.Client) *SCIM2Client {
	c := &SCIM2Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		domain:  userstoreDomain,
		cache:   make(map[string]cacheEntry),
	}
	if httpOverride != nil {
		c.http = httpOverride
		return c
	}
	cc := &clientcredentials.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenURL:     tokenURL,
		Scopes:       parseScopes(scopes),
	}
	// Give the token exchange the same timeout as the SCIM calls.
	tokenCtx := context.WithValue(context.Background(),
		oauth2.HTTPClient, &http.Client{Timeout: scimTimeout})
	c.http = cc.Client(tokenCtx)
	c.http.Timeout = scimTimeout
	return c
}

// parseScopes splits an OAuth2 scope string on spaces AND commas, trims each
// token, and drops empties. "" → nil so the token request carries no scope
// parameter at all (the correct behaviour for IdPs that need none). Both
// separators are accepted because operators reach for either; the OAuth2 wire
// format re-joins them with spaces.
func parseScopes(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ' ' || r == ',' || r == '\t' || r == '\n'
	})
	if len(fields) == 0 {
		return nil
	}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if t := strings.TrimSpace(f); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ── Minimal SCIM response structs ────────────────────────────────────────────
// Only the fields exposed through Provider are decoded. Do not add fields.

type scimListResponse struct {
	TotalResults int               `json:"totalResults"`
	Resources    []json.RawMessage `json:"Resources"`
}

type scimUser struct {
	ID          string `json:"id"`
	UserName    string `json:"userName"`
	DisplayName string `json:"displayName"`
	Name        struct {
		Formatted  string `json:"formatted"`
		GivenName  string `json:"givenName"`
		FamilyName string `json:"familyName"`
	} `json:"name"`
	Emails []scimEmail `json:"emails"`
}

// scimEmail tolerates both SCIM email encodings: a bare string
// ("a@b.c") and the canonical object ({"value":"a@b.c","primary":true}).
// Asgardeo emits both depending on the resource.
type scimEmail struct {
	Value   string
	Primary bool
}

func (e *scimEmail) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		e.Value = s
		return nil
	}
	var obj struct {
		Value   string `json:"value"`
		Primary bool   `json:"primary"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return fmt.Errorf("decoding SCIM email: %w", err)
	}
	e.Value = obj.Value
	e.Primary = obj.Primary
	return nil
}

type scimGroup struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

// stripUserstoreDomain removes the WSO2 IS / Asgardeo userstore-domain prefix
// from a SCIM userName (e.g. "DEFAULT/alice@example.com" → "alice@example.com").
// Users in a secondary userstore carry the prefix; primary-store users don't.
// The segment before the first "/" is a store name, never part of an address,
// so strip only when it contains no "@".
func stripUserstoreDomain(userName string) string {
	if i := strings.IndexByte(userName, '/'); i > 0 && !strings.Contains(userName[:i], "@") {
		return userName[i+1:]
	}
	return userName
}

// toUser maps a SCIM user to the minimal User record.
// Sub ← SCIM `id` (for Asgardeo the OIDC sub equals the SCIM user id).
// Email ← userName (userstore prefix stripped) when it looks like an email,
// else the primary/first email, else userName as-is.
// DisplayName ← name.formatted → displayName → "given family" → userName
// (userstore prefix stripped).
func (u *scimUser) toUser() User {
	email := stripUserstoreDomain(u.UserName)
	if !strings.Contains(email, "@") {
		if v := u.bestEmail(); v != "" {
			email = v
		}
	}
	display := u.Name.Formatted
	if display == "" {
		display = u.DisplayName
	}
	if display == "" {
		display = strings.TrimSpace(u.Name.GivenName + " " + u.Name.FamilyName)
	}
	if display == "" {
		display = stripUserstoreDomain(u.UserName)
	}
	return User{Sub: u.ID, Email: email, DisplayName: display}
}

func (u *scimUser) bestEmail() string {
	for _, e := range u.Emails {
		if e.Primary && e.Value != "" {
			return e.Value
		}
	}
	for _, e := range u.Emails {
		if e.Value != "" {
			return e.Value
		}
	}
	return ""
}

// ── Provider implementation ──────────────────────────────────────────────────

// LookupUserByEmail resolves email → exactly one user via a SCIM point lookup:
// `userName eq "<email>"` first (most IdPs, Asgardeo included, use the email
// as the username), falling back to `emails eq "<email>"` on zero results.
// Never cached — see the package caching policy.
func (c *SCIM2Client) LookupUserByEmail(ctx context.Context, email string) (*User, error) {
	if err := checkFilterValue(email); err != nil {
		return nil, err
	}

	for _, attr := range []string{"userName", "emails"} {
		q := url.Values{}
		q.Set("filter", fmt.Sprintf("%s eq %q", attr, email))
		// count=2 is enough to distinguish "exactly one" from "ambiguous".
		q.Set("startIndex", "1")
		q.Set("count", "2")
		if c.domain != "" {
			q.Set("domain", c.domain)
		}

		var list scimListResponse
		if err := c.get(ctx, "/Users", q, &list); err != nil {
			return nil, err
		}
		if list.TotalResults == 0 {
			continue
		}
		if list.TotalResults > 1 {
			return nil, ErrAmbiguous
		}
		if len(list.Resources) == 0 {
			return nil, fmt.Errorf("IdP reported 1 match for %s but returned no resource", attr)
		}
		var su scimUser
		if err := json.Unmarshal(list.Resources[0], &su); err != nil {
			return nil, fmt.Errorf("decoding SCIM user: %w", err)
		}
		u := su.toUser()
		return &u, nil
	}
	return nil, ErrUserNotFound
}

// SearchUsers returns one page of users matching filter, plus totalResults.
// Pages are cached for cacheTTL.
func (c *SCIM2Client) SearchUsers(ctx context.Context, filter string, limit, offset int) ([]User, int, error) {
	if err := checkFilterValue(filter); err != nil {
		return nil, 0, err
	}
	key := fmt.Sprintf("users|%s|%d|%d", filter, limit, offset)
	if e, ok := c.cacheGet(key); ok {
		return e.users, e.total, nil
	}

	q := url.Values{}
	if filter != "" {
		q.Set("filter", fmt.Sprintf(
			`userName co %q or name.formatted co %q or emails co %q`,
			filter, filter, filter))
	}
	q.Set("startIndex", strconv.Itoa(offset+1)) // SCIM startIndex is 1-based
	q.Set("count", strconv.Itoa(limit))
	if c.domain != "" {
		q.Set("domain", c.domain)
	}

	var list scimListResponse
	if err := c.get(ctx, "/Users", q, &list); err != nil {
		return nil, 0, err
	}
	users := make([]User, 0, len(list.Resources))
	for _, raw := range list.Resources {
		var su scimUser
		if err := json.Unmarshal(raw, &su); err != nil {
			return nil, 0, fmt.Errorf("decoding SCIM user: %w", err)
		}
		users = append(users, su.toUser())
	}
	c.cachePut(key, cacheEntry{users: users, total: list.TotalResults})
	return users, list.TotalResults, nil
}

// ListGroups returns one page of groups plus totalResults. Pages are cached
// for cacheTTL. Membership is excluded server-side (minimal-fields rule).
func (c *SCIM2Client) ListGroups(ctx context.Context, limit, offset int) ([]Group, int, error) {
	key := fmt.Sprintf("groups|%d|%d", limit, offset)
	if e, ok := c.cacheGet(key); ok {
		return e.groups, e.total, nil
	}

	q := url.Values{}
	q.Set("startIndex", strconv.Itoa(offset+1))
	q.Set("count", strconv.Itoa(limit))
	q.Set("excludedAttributes", "members") // never fetch membership
	if c.domain != "" {
		q.Set("domain", c.domain)
	}

	var list scimListResponse
	if err := c.get(ctx, "/Groups", q, &list); err != nil {
		return nil, 0, err
	}
	groups := make([]Group, 0, len(list.Resources))
	for _, raw := range list.Resources {
		var sg scimGroup
		if err := json.Unmarshal(raw, &sg); err != nil {
			return nil, 0, fmt.Errorf("decoding SCIM group: %w", err)
		}
		groups = append(groups, Group{ID: sg.ID, Name: sg.DisplayName})
	}
	c.cachePut(key, cacheEntry{groups: groups, total: list.TotalResults})
	return groups, list.TotalResults, nil
}

// ── Plumbing ─────────────────────────────────────────────────────────────────

// checkFilterValue refuses values that cannot be safely embedded in a SCIM
// filter expression. %q quoting above escapes double quotes, but rejecting
// them outright is both simpler to reason about and matches reality — no
// email or human name contains a double quote or a control character.
func checkFilterValue(v string) error {
	for _, r := range v {
		if r == '"' || r == '\\' || r < 0x20 {
			return ErrBadFilter
		}
	}
	return nil
}

// get performs an authenticated GET against {baseURL}{path}?{query} and
// decodes the JSON body into out. Non-2xx responses become wrapped errors
// (handlers map them to 502); the response body is never included verbatim.
func (c *SCIM2Client) get(ctx context.Context, path string, query url.Values, out interface{}) error {
	reqURL := c.baseURL + path
	if len(query) > 0 {
		reqURL += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("building SCIM request: %w", err)
	}
	req.Header.Set("Accept", "application/scim+json, application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("calling IdP: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// Drain (bounded) so the connection can be reused; don't echo the body.
		_, _ = io.CopyN(io.Discard, resp.Body, 4096)
		return fmt.Errorf("IdP returned %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding IdP response: %w", err)
	}
	return nil
}

func (c *SCIM2Client) cacheGet(key string) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cache[key]
	if !ok || time.Now().After(e.expires) {
		delete(c.cache, key)
		return cacheEntry{}, false
	}
	return e, true
}

func (c *SCIM2Client) cachePut(key string, e cacheEntry) {
	e.expires = time.Now().Add(cacheTTL)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.cache) >= cacheMaxEntry {
		// Bounded size: drop expired entries first; if everything is live,
		// evict arbitrary entries until under the cap. Simple and good
		// enough for a 30s-TTL type-ahead cache.
		now := time.Now()
		for k, v := range c.cache {
			if now.After(v.expires) {
				delete(c.cache, k)
			}
		}
		for k := range c.cache {
			if len(c.cache) < cacheMaxEntry {
				break
			}
			delete(c.cache, k)
		}
	}
	c.cache[key] = e
}
