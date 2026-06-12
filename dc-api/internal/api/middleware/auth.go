// Package middleware provides HTTP middleware for DC-API.
//
// ── DESIGN PATTERN: Middleware Chain ──────────────────────────────────────────
//
// HTTP middleware is a function that wraps an http.Handler with extra behaviour.
// In Go, the signature is always:
//
//	func(next http.Handler) http.Handler
//
// Each request passes through the chain in order: Logger → Auth → Handler.
// If Auth rejects the request (invalid JWT), it writes a 401 and does NOT call
// next.ServeHTTP — so the handler never runs.
//
// ── Auth is Pluggable — Not Asgardeo-Specific ─────────────────────────────────
//
// This middleware validates JWTs using standard OIDC/OAuth2 (RFC 6749, RFC 7519).
// It does NOT contain any Asgardeo-specific code. It works with ANY OIDC-compliant
// identity provider:
//
//	Provider    | DCAPI_OIDC_ISSUER example
//	------------|--------------------------------------------------
//	Asgardeo    | https://api.asgardeo.io/t/<org>
//	Keycloak    | https://keycloak.example.com/realms/<realm>
//	Okta        | https://<domain>.okta.com/oauth2/default
//	Auth0       | https://<domain>.auth0.com/
//	Google      | https://accounts.google.com
//	Dex         | https://dex.example.com
//
// The only group dc-api interprets is the platform-admin group, controlled
// by one env var:
//
//	DCAPI_ADMIN_GROUP  (default: "dc-admin")
//
// Tenant membership is NEVER derived from IdP groups — it lives exclusively
// in the role_assignments table, populated by explicit invites.
//
// ── How JWT Validation Works ──────────────────────────────────────────────────
//
//  1. Client sends: Authorization: Bearer <access_token>
//  2. We extract the token string.
//  3. go-oidc fetches the IdP's JWKS (public keys) from /.well-known/openid-configuration.
//     Keys are cached; we don't hit the IdP on every request.
//  4. We verify: signature valid, not expired, audience matches.
//  5. We inject principal_type, principal_id, user_id and is_admin (admin
//     group / DCAPI_PLATFORM_ADMIN_SUBS) into the request context. Tenant and
//     project scoping happen later, in TenantContext / ProjectContext, against
//     the role_assignments table.
package middleware

import (
	"context"
	"crypto"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"github.com/wso2/dc-api/internal/audit"
	"github.com/wso2/dc-api/internal/api/respond"
	"github.com/wso2/dc-api/internal/models"
	"golang.org/x/oauth2"
)

// contextKey is an unexported type for context keys in this package.
// Using a custom type prevents collisions with other packages.
type contextKey string

const (
	ContextKeyTenantID      contextKey = "tenant_id"
	// ContextKeyTenantUUID is the canonical, immutable identity for the active
	// tenant (Phase 6a). TenantContext middleware resolves the URL slug to a
	// tenants.tenant_uuid once and stashes both — handlers should prefer the
	// UUID when filtering or writing to per-tenant tables, so a re-registered
	// slug never inherits orphan rows.
	ContextKeyTenantUUID    contextKey = "tenant_uuid"
	// ContextKeyProjectID and ContextKeyProjectUUID are the project-level
	// equivalents — injected by ProjectContext middleware on every
	// /v1/tenants/{tenant_id}/projects/{project_id}/... request.
	ContextKeyProjectID   contextKey = "project_id"
	ContextKeyProjectUUID contextKey = "project_uuid"
	// ContextKeyResourceUUID is the narrowest scope (RBAC v2): the UUID of an
	// individual resource (VM, cluster, key vault, database). Injected by the
	// ResourceScope middleware on per-resource routes so the authorization scope
	// chain includes {resource, uuid} and a resource-scope grant can authorize
	// actions on that one resource.
	ContextKeyResourceUUID contextKey = "resource_uuid"
	ContextKeyUserID        contextKey = "user_id"
	ContextKeyPrincipalType contextKey = "principal_type"
	ContextKeyPrincipalID   contextKey = "principal_id"
	ContextKeyIsAdmin       contextKey = "is_admin"
)

// TenantFromContext extracts the tenant ID injected by Auth middleware.
func TenantFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ContextKeyTenantID).(string)
	return v, ok && v != ""
}

// TenantUUIDFromContext extracts the immutable tenant UUID injected by
// TenantContext middleware. Returns (uuid.Nil, false) when no tenant context
// is on the request (e.g. /v1/auth/me or admin-only endpoints).
func TenantUUIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	v, ok := ctx.Value(ContextKeyTenantUUID).(uuid.UUID)
	return v, ok && v != uuid.Nil
}

// ProjectFromContext extracts the project slug (project_id) injected by
// ProjectContext middleware. Returns ("", false) when no project context is on
// the request.
func ProjectFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ContextKeyProjectID).(string)
	return v, ok && v != ""
}

// ProjectUUIDFromContext extracts the immutable project UUID injected by
// ProjectContext middleware. Returns (uuid.Nil, false) when no project context
// is on the request.
func ProjectUUIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	v, ok := ctx.Value(ContextKeyProjectUUID).(uuid.UUID)
	return v, ok && v != uuid.Nil
}

// ResourceUUIDFromContext extracts the individual-resource UUID injected by the
// ResourceScope middleware on per-resource routes. Returns (uuid.Nil, false)
// when the request is not scoped to a single resource.
func ResourceUUIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	v, ok := ctx.Value(ContextKeyResourceUUID).(uuid.UUID)
	return v, ok && v != uuid.Nil
}

// UserFromContext extracts the user subject ID from the request context.
func UserFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ContextKeyUserID).(string)
	return v, ok && v != ""
}

// PrincipalFromContext extracts the principal type and ID injected by M1.5 auth.
// Returns (type, id, true) when present; ("", "", false) otherwise.
func PrincipalFromContext(ctx context.Context) (models.PrincipalType, string, bool) {
	pt, ok1 := ctx.Value(ContextKeyPrincipalType).(models.PrincipalType)
	id, ok2 := ctx.Value(ContextKeyPrincipalID).(string)
	if !ok1 || !ok2 || id == "" {
		return "", "", false
	}
	return pt, id, true
}

// IsAdminFromContext returns true when the request was made by a platform admin
// (i.e., the JWT contained the configured admin group). Handlers and RBAC
// helpers use this to short-circuit the membership check.
func IsAdminFromContext(ctx context.Context) bool {
	v, _ := ctx.Value(ContextKeyIsAdmin).(bool)
	return v
}

// Claims is the subset of JWT claims DC-API cares about.
// The "groups" field name matches what most OIDC providers use.
// If your IdP uses a different claim name, extend AuthConfig with a GroupsClaim field.
type Claims struct {
	Sub    string   `json:"sub"`
	Email  string   `json:"email"`
	Groups []string `json:"groups"`
}

// ─────────────────────────── AuthRepo interface ──────────────────────────────

// AuthRepo is the narrow data-access interface the auth middleware needs.
// It is defined here (at the caller site) to keep the middleware/db dependency
// graph clean — *db.Repository satisfies it implicitly without requiring an
// import of the db package from middleware.
//
// Methods:
//   - ListRoleAssignmentsForPrincipal — membership checks in the
//     TenantContext / ProjectContext middlewares.
//   - GetServiceAccountByTokenLookupID — SA auth lookup by indexed prefix (Chunk 5).
//   - UpdateServiceAccountLastUsed — SA auth last-used timestamp (Chunk 5).
type AuthRepo interface {
	ListRoleAssignmentsForPrincipal(
		ctx context.Context,
		principalType models.PrincipalType,
		principalID string,
	) ([]models.RoleAssignment, error)

	// GetTenantUUIDBySlug returns the immutable tenant_uuid for the slug.
	// Returns (uuid.Nil, nil) when no row exists (TenantContext maps that to
	// 404); a non-nil error means a genuine DB failure (mapped to 500).
	GetTenantUUIDBySlug(ctx context.Context, slug string) (uuid.UUID, error)

	// GetProjectUUIDByTenantAndSlug returns the immutable project_uuid for the
	// (tenantID, projectSlug) pair. Returns (uuid.Nil, nil) when no row exists;
	// non-nil error means DB failure.
	GetProjectUUIDByTenantAndSlug(ctx context.Context, tenantID, projectSlug string) (uuid.UUID, error)

	// GetTenantSlugByProjectUUID resolves a project_uuid to its parent tenant
	// slug, so TenantContext can admit a principal who holds only a project-scope
	// grant to that project's tenant. Returns ("", nil) when no project matches.
	GetTenantSlugByProjectUUID(ctx context.Context, projectUUID uuid.UUID) (string, error)

	// GetResourceLocationByUUID resolves a resource-scope grant's resource UUID
	// to its parent tenant slug and project UUID, so the entry path can admit a
	// principal who holds only a resource-scope grant. Searches every resource
	// type (VMs, clusters, key vaults, databases); found=false when none match.
	GetResourceLocationByUUID(ctx context.Context, resourceUUID uuid.UUID) (tenantSlug string, projectUUID uuid.UUID, found bool, err error)

	// AnyResourceInProject reports whether any of the given resource UUIDs lives
	// in projectUUID — so ProjectContext can admit a resource-only user in one
	// query rather than resolving each grant individually.
	AnyResourceInProject(ctx context.Context, projectUUID uuid.UUID, resourceUUIDs []uuid.UUID) (bool, error)

	GetServiceAccountByTokenLookupID(
		ctx context.Context,
		lookupID string,
	) (*models.ServiceAccount, error)

	UpdateServiceAccountLastUsed(
		ctx context.Context,
		id uuid.UUID,
	) error
}

// ─────────────────────────── AuthConfig ─────────────────────────────────────

// AuthConfig holds the configurable parts of the auth middleware.
// All fields have sensible defaults; only override what you need.
type AuthConfig struct {
	// AdminGroup is the IdP group whose members get the platform "admin"
	// role. Default: "dc-admin". This is the ONLY IdP group dc-api
	// interprets — tenant membership lives exclusively in the
	// role_assignments table (invites), never in IdP groups.
	AdminGroup string

	// PlatformAdminSubs is the set of IdP `sub` values that should be
	// treated as platform admin. Built from DCAPI_PLATFORM_ADMIN_SUBS env
	// var (comma-separated). When the caller's sub is in this set OR they
	// hold the AdminGroup, isAdmin is true. Membership check is O(1).
	PlatformAdminSubs map[string]struct{}
}

// defaults fills zero-value string fields with their defaults.
func (c *AuthConfig) defaults() {
	if c.AdminGroup == "" {
		c.AdminGroup = "dc-admin"
	}
}

// ─────────────────────────── Auth (production) ──────────────────────────────

// Auth is the authentication + tenant-resolution middleware.
// Create once at startup: auth, err := middleware.NewAuth(ctx, issuer, audiences, cfg)
// Use with chi: r.Use(auth.Validate)
type Auth struct {
	verifier  *oidc.IDTokenVerifier
	audiences []string
	cfg       AuthConfig
	// F7 BFF: when set, requests without an Authorization header fall
	// back to extracting the access token from the dcapi_session cookie.
	// nil when BFF is not configured (DCAPI_BFF_* env vars unset) —
	// behaviour matches the pre-F7 Bearer-only auth path exactly.
	cookieAccessToken func(*http.Request) (string, bool)
}

// NewAuth creates an Auth middleware.
//
// issuerURL: the OIDC issuer (e.g., "https://api.asgardeo.io/t/myorg").
//
//	go-oidc fetches /.well-known/openid-configuration automatically.
//
// audiences: the set of accepted "aud" claims. A token is accepted if at
//
//	least one value in its `aud` claim matches one entry in this slice.
//	Pass every IdP client whose tokens should be honoured (dcctl, cloud-ui,
//	future Terraform provider, …). The verifier itself runs with audience
//	checking disabled and we enforce it manually so multi-value lists work.
//
// cfg: admin-group config (use AuthConfig{} for all defaults).
func NewAuth(ctx context.Context, issuerURL string, audiences []string, cfg AuthConfig) (*Auth, error) {
	cfg.defaults()

	if len(audiences) == 0 {
		return nil, fmt.Errorf("NewAuth: at least one audience is required")
	}

	provider, err := oidc.NewProvider(ctx, issuerURL)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery at %s: %w\n\n"+
			"Hint: check that DCAPI_OIDC_ISSUER is correct and reachable", issuerURL, err)
	}

	// Skip the built-in audience check; we enforce it ourselves below so
	// the operator can authorise multiple IdP clients via DCAPI_OIDC_AUDIENCE.
	verifier := provider.Verifier(&oidc.Config{SkipClientIDCheck: true})
	return &Auth{verifier: verifier, audiences: audiences, cfg: cfg}, nil
}

// WithCookieAccessToken wires an optional session-cookie token source.
// main.go calls this after constructing the BFF Service so the middleware
// can accept cloud-ui's HttpOnly session cookie in addition to the Bearer
// header dcctl + terraform-provider use. Returns the same *Auth so the
// call chains: middleware.NewAuth(...).WithCookieAccessToken(svc.AccessTokenFromCookieReq).
//
// The extractor returns (token, true) when a valid session cookie is
// found, ("", false) otherwise. False is silent — the middleware just
// continues to the "no Authorization header" error path.
func (a *Auth) WithCookieAccessToken(extractor func(*http.Request) (string, bool)) *Auth {
	a.cookieAccessToken = extractor
	return a
}

// audienceMatches returns true when at least one element of tokenAud also
// appears in allowed. Comparison is case-sensitive (per RFC 7519).
func audienceMatches(tokenAud, allowed []string) bool {
	for _, a := range allowed {
		for _, t := range tokenAud {
			if a == t {
				return true
			}
		}
	}
	return false
}

// Validate is the Chi middleware function.
// Usage: r.Use(auth.Validate)
func (a *Auth) Validate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenStr, err := bearerToken(r)
		if err != nil {
			// F7 BFF: fall back to the session cookie when no Authorization
			// header is present. Only fires when the BFF was configured at
			// startup; pre-F7 behaviour (Bearer only) is unchanged when
			// cookieAccessToken is nil.
			if a.cookieAccessToken != nil {
				if tok, ok := a.cookieAccessToken(r); ok {
					tokenStr, err = tok, nil
				}
			}
			if err != nil {
				respond.Error(w, http.StatusUnauthorized, "Unauthorized: "+err.Error())
				return
			}
		}

		idToken, err := a.verifier.Verify(r.Context(), tokenStr)
		if err != nil {
			respond.Error(w, http.StatusUnauthorized, "Unauthorized: invalid token")
			return
		}

		if !audienceMatches(idToken.Audience, a.audiences) {
			respond.Error(w, http.StatusUnauthorized, "Unauthorized: audience mismatch")
			return
		}

		var claims Claims
		if err := idToken.Claims(&claims); err != nil {
			respond.Error(w, http.StatusUnauthorized, "Unauthorized: cannot parse claims")
			return
		}

		next.ServeHTTP(w, r.WithContext(a.cfg.buildContext(r.Context(), claims)))
	})
}

// ─────────────────────────── Helpers ────────────────────────────────────────

// buildContext runs the per-request context-enrichment logic after JWT claims
// are verified. Shared between Auth (production) and TestModeAuth.
//
// Auth middleware only establishes "who you are" (principal identity +
// is_admin). Every valid token authenticates; a principal with no
// role_assignments rows simply has access to nothing — the TenantContext /
// ProjectContext middlewares gate each scoped request against the
// role_assignments table (membership truth lives there, never in IdP
// groups). The only IdP group dc-api interprets is AdminGroup; the
// PlatformAdminSubs env list promotes the same way.
func (c *AuthConfig) buildContext(reqCtx context.Context, claims Claims) context.Context {
	isAdmin := false
	if _, ok := c.PlatformAdminSubs[claims.Sub]; ok {
		isAdmin = true
	}
	for _, g := range claims.Groups {
		if g == c.AdminGroup {
			isAdmin = true
			break
		}
	}

	// Inject principal context. Tenant scoping comes from TenantContext later.
	ctx := context.WithValue(reqCtx, ContextKeyUserID, claims.Sub)
	ctx = context.WithValue(ctx, ContextKeyPrincipalType, models.PrincipalTypeUser)
	ctx = context.WithValue(ctx, ContextKeyPrincipalID, claims.Sub)
	ctx = context.WithValue(ctx, ContextKeyIsAdmin, isAdmin)
	// Stamp the actor for the repository layer's automatic audit recording.
	ctx = audit.WithActor(ctx, claims.Sub)
	return ctx
}

// bearerToken extracts the token string from "Authorization: Bearer <token>".
func bearerToken(r *http.Request) (string, error) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return "", fmt.Errorf("missing Authorization header")
	}
	if !strings.HasPrefix(header, "Bearer ") {
		return "", fmt.Errorf("Authorization header must be 'Bearer <token>'")
	}
	token := strings.TrimPrefix(header, "Bearer ")
	if token == "" {
		return "", fmt.Errorf("empty Bearer token")
	}
	return token, nil
}

// StaticTokenSource adapts a static access token to the oauth2.TokenSource interface.
// Used in tests to inject a pre-built token without running a full OIDC flow.
type StaticTokenSource struct {
	AccessToken string
}

func (s StaticTokenSource) Token() (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: s.AccessToken}, nil
}

// ── AuthValidator ────────────────────────────────────────────────────────────

// AuthValidator is implemented by both *Auth (production) and *TestModeAuth
// (integration tests). The router depends only on this interface, so test code
// can substitute a TestModeAuth without touching the router or any handler.
type AuthValidator interface {
	Validate(next http.Handler) http.Handler
}

// ── TestModeAuth ─────────────────────────────────────────────────────────────

// TestModeAuth validates JWTs signed with a supplied RSA public key set.
// Used exclusively in integration tests — never instantiated in production.
//
// The production `Auth` struct still uses go-oidc against Asgardeo. This type
// is a parallel implementation that lets test code mint per-run JWTs and feed
// the public key into a TestModeAuth instance for the in-process router.
type TestModeAuth struct {
	cfg     AuthConfig
	pubKeys []*rsa.PublicKey
}

// NewTestModeAuth constructs a TestModeAuth from a JWK Set JSON blob.
// The jwksJSON is produced by JWTMinter.PublicKeyJWKS() in the test package.
//
// Example test wiring:
//
//	minter, _ := test.NewJWTMinter()
//	auth, _ := middleware.NewTestModeAuth(minter.PublicKeyJWKS(), middleware.AuthConfig{...})
func NewTestModeAuth(jwksJSON []byte, cfg AuthConfig) (*TestModeAuth, error) {
	cfg.defaults()
	var jwkset struct {
		Keys []struct {
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(jwksJSON, &jwkset); err != nil {
		return nil, fmt.Errorf("parse test JWKS: %w", err)
	}
	var keys []*rsa.PublicKey
	for _, k := range jwkset.Keys {
		if k.Kty != "RSA" {
			continue
		}
		nBytes, err1 := base64.RawURLEncoding.DecodeString(k.N)
		eBytes, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil {
			continue
		}
		var e int
		for _, b := range eBytes {
			e = e<<8 | int(b)
		}
		keys = append(keys, &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: e})
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no RSA keys parsed from test JWKS")
	}
	return &TestModeAuth{cfg: cfg, pubKeys: keys}, nil
}

// Validate is the Chi middleware entrypoint for test-mode auth.
// Runs the same buildContext logic as the production Auth.
func (a *TestModeAuth) Validate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenStr, err := bearerToken(r)
		if err != nil {
			respond.Error(w, http.StatusUnauthorized, "Unauthorized: "+err.Error())
			return
		}
		claims, err := verifyRS256JWT(tokenStr, a.pubKeys)
		if err != nil {
			respond.Error(w, http.StatusUnauthorized, "Unauthorized: "+err.Error())
			return
		}
		next.ServeHTTP(w, r.WithContext(a.cfg.buildContext(r.Context(), *claims)))
	})
}

// verifyRS256JWT verifies an RS256-signed JWT against any of the supplied keys.
// Returns the decoded Claims or an error.
func verifyRS256JWT(tokenStr string, keys []*rsa.PublicKey) (*Claims, error) {
	parts := strings.SplitN(tokenStr, ".", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("malformed JWT: expected 3 dot-separated parts")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("decode JWT header: %w", err)
	}
	var hdr struct {
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(headerBytes, &hdr); err != nil || hdr.Alg != "RS256" {
		return nil, fmt.Errorf("JWT must use RS256, got %q", hdr.Alg)
	}
	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("decode JWT signature: %w", err)
	}
	h := crypto.SHA256.New()
	h.Write([]byte(parts[0] + "." + parts[1]))
	digest := h.Sum(nil)

	verified := false
	var lastErr error
	for _, pub := range keys {
		if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest, sigBytes); err == nil {
			verified = true
			break
		} else {
			lastErr = err
		}
	}
	if !verified {
		return nil, fmt.Errorf("JWT signature invalid: %w", lastErr)
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode JWT payload: %w", err)
	}
	var payload struct {
		Sub    string   `json:"sub"`
		Email  string   `json:"email"`
		Groups []string `json:"groups"`
		Exp    int64    `json:"exp"`
	}
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return nil, fmt.Errorf("unmarshal JWT claims: %w", err)
	}
	if payload.Exp > 0 && time.Now().Unix() > payload.Exp {
		return nil, fmt.Errorf("JWT expired")
	}
	return &Claims{Sub: payload.Sub, Email: payload.Email, Groups: payload.Groups}, nil
}
