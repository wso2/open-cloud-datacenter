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
// The only customisation point is the group-to-tenant mapping, which is
// controlled by two env vars:
//
//	DCAPI_TENANT_GROUP_PREFIX  (default: "dc-tenant-")
//	DCAPI_ADMIN_GROUP          (default: "dc-admin")
//
// If your IdP uses a different group naming scheme, just change these values.
// No code changes required.
//
// ── How JWT Validation Works ──────────────────────────────────────────────────
//
//  1. Client sends: Authorization: Bearer <access_token>
//  2. We extract the token string.
//  3. go-oidc fetches the IdP's JWKS (public keys) from /.well-known/openid-configuration.
//     Keys are cached; we don't hit the IdP on every request.
//  4. We verify: signature valid, not expired, audience matches.
//  5. We extract the "groups" claim and map it to a tenantID.
//  6. We inject tenantID + userID into the request context.
//
// ── M1.5 RBAC additions (Chunk 3) ────────────────────────────────────────────
//
// The Validate() flow now also:
//   - Injects principal_type, principal_id, is_admin into context.
//   - Enforces autoprovision policy: first login with a valid dc-tenant-<x> group
//     either inserts a 'member' role_assignment row (DCAPI_RBAC_AUTOPROVISION=true,
//     the default) or returns 403 (=false, for stricter production environments).
//   - Admins (dc-admin group) bypass the membership lookup entirely.
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
	"github.com/rs/zerolog/log"
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
	ContextKeyUserID        contextKey = "user_id"
	ContextKeyPrincipalType contextKey = "principal_type"
	ContextKeyPrincipalID   contextKey = "principal_id"
	ContextKeyIsAdmin       contextKey = "is_admin"
	// ContextKeyIdPTenants holds the slice of tenant names (with the
	// dc-tenant- prefix stripped) the IdP claimed for the caller. Used by
	// TenantContext middleware to distinguish "tenant not found" (404) from
	// "in IdP group, no role row yet" (403).
	ContextKeyIdPTenants contextKey = "idp_tenants"
)

// IdPTenantsFromContext returns the tenant names the IdP claimed for the
// caller. Empty slice for service accounts (their tenant comes from the SA
// record, not the JWT) and admins.
func IdPTenantsFromContext(ctx context.Context) []string {
	v, _ := ctx.Value(ContextKeyIdPTenants).([]string)
	return v
}

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
//   - ListRoleAssignmentsForPrincipal — OIDC autoprovision check (Chunk 3).
//   - CreateRoleAssignment — OIDC autoprovision insert (Chunk 3).
//   - UpsertTenant — populate the tenants registry on first sighting of
//     any dc-tenant-<id> group claim, so admins can enumerate empty tenants.
//   - GetServiceAccountByTokenLookupID — SA auth lookup by indexed prefix (Chunk 5).
//   - UpdateServiceAccountLastUsed — SA auth last-used timestamp (Chunk 5).
type AuthRepo interface {
	ListRoleAssignmentsForPrincipal(
		ctx context.Context,
		principalType models.PrincipalType,
		principalID string,
	) ([]models.RoleAssignment, error)

	CreateRoleAssignment(
		ctx context.Context,
		ra models.RoleAssignment,
	) (*models.RoleAssignment, error)

	UpsertTenant(
		ctx context.Context,
		id, name, asgardeoGroup, createdBy string,
	) (*models.Tenant, error)

	// GetTenantUUIDBySlug returns the immutable tenant_uuid for the slug.
	// Returns (uuid.Nil, nil) when no row exists (TenantContext maps that to
	// 404); a non-nil error means a genuine DB failure (mapped to 500).
	GetTenantUUIDBySlug(ctx context.Context, slug string) (uuid.UUID, error)

	// GetProjectUUIDByTenantAndSlug returns the immutable project_uuid for the
	// (tenantID, projectSlug) pair. Returns (uuid.Nil, nil) when no row exists;
	// non-nil error means DB failure.
	GetProjectUUIDByTenantAndSlug(ctx context.Context, tenantID, projectSlug string) (uuid.UUID, error)

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
	// TenantGroupPrefix is the prefix used to identify tenant groups.
	// Groups matching "<prefix><name>" map to tenant "<name>".
	// Default: "dc-tenant-"
	TenantGroupPrefix string

	// AdminGroup is the group name whose members get the "admin" tenant role.
	// Default: "dc-admin". Kept as a legacy fallback alongside
	// PlatformAdminSubs (which is the Option D preferred mechanism).
	AdminGroup string

	// PlatformAdminSubs is the set of IdP `sub` values that should be
	// treated as platform admin. Built from DCAPI_PLATFORM_ADMIN_SUBS env
	// var (comma-separated). When the caller's sub is in this set OR they
	// hold the AdminGroup, isAdmin is true. Membership check is O(1).
	PlatformAdminSubs map[string]struct{}

	// AutoProvisionMembers, when true, inserts a 'member' role_assignment row
	// the first time a user with a matching dc-tenant-<x> group is seen in JWT.
	// When false, users with no role_assignments row get 403 — an owner must
	// explicitly invite them first.
	// Default (Option D): false — autoprovision is a legacy fallback.
	AutoProvisionMembers bool
}

// defaults fills zero-value string fields with their defaults.
// AutoProvisionMembers is intentionally NOT touched here: the zero value of a
// bool is false, so we cannot distinguish "caller explicitly passed false" from
// "caller forgot to set it". main.go reads DCAPI_RBAC_AUTOPROVISION and sets
// the field before calling NewAuth. Integration tests set it explicitly in
// AuthConfig{AutoProvisionMembers: true/false}. Only empty AuthConfig{} (no
// auto-provision field set at all) would silently default to false — which is
// the safe production default.
func (c *AuthConfig) defaults() {
	if c.TenantGroupPrefix == "" {
		c.TenantGroupPrefix = "dc-tenant-"
	}
	if c.AdminGroup == "" {
		c.AdminGroup = "dc-admin"
	}
}

// ─────────────────────────── Auth (production) ──────────────────────────────

// Auth is the authentication + tenant-resolution middleware.
// Create once at startup: auth, err := middleware.NewAuth(ctx, issuer, audiences, cfg, repo)
// Use with chi: r.Use(auth.Validate)
type Auth struct {
	verifier  *oidc.IDTokenVerifier
	audiences []string
	cfg       AuthConfig
	repo      AuthRepo
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
// cfg: group-to-tenant mapping config (use AuthConfig{} for all defaults).
// repo: data-access implementation used for the M1.5 autoprovision membership check.
func NewAuth(ctx context.Context, issuerURL string, audiences []string, cfg AuthConfig, repo AuthRepo) (*Auth, error) {
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
	return &Auth{verifier: verifier, audiences: audiences, cfg: cfg, repo: repo}, nil
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

		ctx, ok := a.cfg.buildContext(r.Context(), claims, a.repo)
		if !ok {
			respond.Error(w, http.StatusForbidden,
				fmt.Sprintf("Forbidden: user has no DC tenant group (expected group prefixed with %q or %q)",
					a.cfg.TenantGroupPrefix, a.cfg.AdminGroup))
			return
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ─────────────────────────── Helpers ────────────────────────────────────────

// buildContext runs the per-request context-enrichment logic after JWT claims
// are verified. Shared between Auth (production) and TestModeAuth.
//
// Tenant selection is NOT performed here. Auth middleware only establishes
// "who you are" + "what tenants the IdP says you belong to" + autoprovisions
// missing rows. The active tenant for a request is set by the
// TenantContext middleware mounted on /v1/tenants/{tenant_id}/... based on
// the URL path. This allows a single JWT carrying multiple dc-tenant-* groups
// to drive the cloud-ui tenant switcher.
//
// Steps:
//  1. Walk claims.Groups to determine isAdmin and collect every dc-tenant-*
//     group (after stripping the prefix).
//  2. If !isAdmin AND no dc-tenant-* groups AND repo lookup yields no existing
//     role_assignments rows: return (nil, false) → caller writes 403.
//  3. For non-admin callers: list role_assignments once; for each dc-tenant-*
//     group not already represented and AutoProvisionMembers=true, insert a
//     'member' row. (AutoProvisionMembers=false skips inserts — the user can
//     still authenticate; the TenantContext middleware will 403 them when
//     they try to access a tenant they don't have a row for.)
//  4. Inject principal_type / principal_id / user_id / is_admin into context.
//     ContextKeyTenantID is NOT set here.
//
// Return values:
//   - (ctx, true)  — success; principal context populated
//   - (nil, false) — no usable IdP group AND no existing membership; 403
func (c *AuthConfig) buildContext(reqCtx context.Context, claims Claims, repo AuthRepo) (context.Context, bool) {
	isAdmin := false
	tenantGroups := make([]string, 0, len(claims.Groups))
	seen := make(map[string]struct{})

	// Option D: env-var admin list takes precedence; AdminGroup is the
	// legacy fallback. Either path promotes.
	if _, ok := c.PlatformAdminSubs[claims.Sub]; ok {
		isAdmin = true
	}
	for _, g := range claims.Groups {
		if g == c.AdminGroup {
			isAdmin = true
			continue
		}
		if strings.HasPrefix(g, c.TenantGroupPrefix) {
			t := strings.TrimPrefix(g, c.TenantGroupPrefix)
			if t == "" {
				continue
			}
			if _, ok := seen[t]; ok {
				continue
			}
			seen[t] = struct{}{}
			tenantGroups = append(tenantGroups, t)
		}
	}

	// Option D: tenants registry is populated explicitly:
	//   - POST /v1/admin/tenants (admin pre-creates)
	//   - POST /v1/tenants/{tid}/members (handler UPSERTs on first invite)
	// The middleware no longer touches the registry. With autoprovision off
	// by default and IdP groups no longer treated as membership truth,
	// every tenant registration becomes a deliberate operator action.

	// For non-admin callers we also need to know about any role_assignments
	// rows that aren't tied to a current dc-tenant-* group (e.g. tenants the
	// user was explicitly invited into without being in the IdP group). Those
	// rows still entitle them to authenticate.
	var hadExistingMembership bool
	if !isAdmin && repo != nil {
		assignments, err := repo.ListRoleAssignmentsForPrincipal(reqCtx, models.PrincipalTypeUser, claims.Sub)
		if err != nil {
			log.Error().Err(err).Str("sub", claims.Sub).Msg("auth: failed to list role assignments")
			// Fail open on DB hiccup — TenantContext middleware will still
			// gate every tenant-scoped request individually.
		} else {
			hadExistingMembership = len(assignments) > 0

			// Build a set of tenants the user already has any row for, so
			// we only insert autoprovision rows for the genuinely-missing
			// ones.
			already := make(map[string]struct{}, len(assignments))
			for _, a := range assignments {
				if a.ScopeType == models.ScopeTypeTenant {
					already[a.ScopeID] = struct{}{}
				}
			}

			if c.AutoProvisionMembers {
				for _, t := range tenantGroups {
					if _, ok := already[t]; ok {
						continue
					}
					// Phase 6a: TenantContext refuses any request whose URL
					// slug isn't in `tenants`. Autoprovision used to rely on
					// just CreateRoleAssignment; we now ALSO UPSERT the
					// tenants row so the freshly-provisioned membership is
					// actually usable on the next request.
					if _, err := repo.UpsertTenant(reqCtx, t, t, c.TenantGroupPrefix+t, "autoprovision-from-asgardeo-group"); err != nil {
						log.Warn().Err(err).Str("tenant", t).
							Msg("auth: autoprovision UpsertTenant failed (best-effort, continuing)")
					}
					_, err := repo.CreateRoleAssignment(reqCtx, models.RoleAssignment{
						ID:            uuid.New(),
						PrincipalType: models.PrincipalTypeUser,
						PrincipalID:   claims.Sub,
						ScopeType:     models.ScopeTypeTenant,
						ScopeID:       t,
						Role:          models.RoleMember,
						GrantedBy:     "autoprovision-from-asgardeo-group",
					})
					if err != nil {
						// Unique-constraint violations are harmless races
						// between concurrent first-logins for the same group.
						log.Warn().Err(err).Str("sub", claims.Sub).Str("tenant", t).
							Msg("auth: autoprovision role_assignment insert failed (may be duplicate)")
					}
				}
			}
		}
	}

	// Reject only when there is no way for the caller to act on dc-api: not
	// admin, no IdP tenant groups, and no pre-existing membership rows.
	if !isAdmin && len(tenantGroups) == 0 && !hadExistingMembership {
		return nil, false
	}

	// Inject principal context. Tenant scoping comes from TenantContext later.
	ctx := context.WithValue(reqCtx, ContextKeyUserID, claims.Sub)
	ctx = context.WithValue(ctx, ContextKeyPrincipalType, models.PrincipalTypeUser)
	ctx = context.WithValue(ctx, ContextKeyPrincipalID, claims.Sub)
	ctx = context.WithValue(ctx, ContextKeyIsAdmin, isAdmin)
	ctx = context.WithValue(ctx, ContextKeyIdPTenants, tenantGroups)
	return ctx, true
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
	repo    AuthRepo // may be nil; nil means skip autoprovision (legacy test behaviour)
}

// NewTestModeAuth constructs a TestModeAuth from a JWK Set JSON blob.
// The jwksJSON is produced by JWTMinter.PublicKeyJWKS() in the test package.
//
// Pass repo to enable the M1.5 autoprovision membership check in integration
// tests. Pass nil to keep legacy behaviour (no membership lookup).
//
// Example test wiring:
//
//	minter, _ := test.NewJWTMinter()
//	auth, _ := middleware.NewTestModeAuth(minter.PublicKeyJWKS(), middleware.AuthConfig{...}, repo)
func NewTestModeAuth(jwksJSON []byte, cfg AuthConfig, repo AuthRepo) (*TestModeAuth, error) {
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
	return &TestModeAuth{cfg: cfg, pubKeys: keys, repo: repo}, nil
}

// Validate is the Chi middleware entrypoint for test-mode auth.
// Runs the same buildContext logic as the production Auth so integration tests
// exercise the autoprovision code path.
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
		ctx, ok := a.cfg.buildContext(r.Context(), *claims, a.repo)
		if !ok {
			respond.Error(w, http.StatusForbidden,
				fmt.Sprintf("Forbidden: user has no DC tenant group (expected prefix %q or %q)",
					a.cfg.TenantGroupPrefix, a.cfg.AdminGroup))
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
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
