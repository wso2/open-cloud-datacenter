package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Config holds the BFF wiring. Operators set these via DCAPI_BFF_* env
// vars; main.go constructs the Service. Empty ClientID disables BFF
// (the /v1/auth/* routes don't register).
type Config struct {
	// IdP issuer URL. Reuses DCAPI_OIDC_ISSUER if not overridden — the
	// BFF and the API surface authenticate against the same Asgardeo org.
	Issuer string

	// Confidential Asgardeo application's client_id and client_secret.
	// Created in 03-asgardeo-auth as a NEW app — *not* the public SPA
	// client (that one stays for now to avoid breaking the
	// VITE_DEV_TOKEN escape hatch).
	ClientID     string
	ClientSecret string

	// Public dc-api URL the user-agent lands on after Asgardeo's redirect
	// (e.g. https://dcapi.lk-dev.internal.wso2.com/v1/auth/callback).
	// Must be added verbatim to the BFF app's callback_urls in Asgardeo.
	RedirectURL string

	// Where to send the browser after a successful login — the cloud-ui
	// origin. Single value for now (one cloud-ui host per dc-api).
	PostLoginRedirect string

	// Where to send the browser after Asgardeo's end-session redirect.
	// Typically the same as PostLoginRedirect — landing on the cloud-ui
	// home route, which then shows the "Sign in" button.
	PostLogoutRedirect string

	// Cookie attributes.
	CookieDomain string // empty = host-only cookie (safer default)
	CookieSecure bool   // true in any non-localhost deploy

	// 32-byte AES-256 key. Generate once: `openssl rand -base64 32` and
	// store via TF (dc-api Secret). Same key reused across replicas so a
	// session set by one pod is readable by the next.
	SessionKey []byte

	// AdminGroup mirrors the auth middleware's setting. Used at callback
	// time to derive Session.IsAdmin from the ID token's `groups` claim,
	// so /v1/auth/me can answer the admin question without re-parsing
	// JWTs. The only IdP group dc-api interprets — tenant membership
	// lives in the role_assignments table, never in groups.
	// When AdminGroup is empty the default "dc-admin" is used.
	AdminGroup string

	// PlatformAdminSubs is the same env-driven set the auth middleware
	// uses. When the caller's IdP sub is in this set, IsAdmin is true
	// regardless of group membership.
	PlatformAdminSubs map[string]struct{}
}

// Service is the runtime BFF. One instance per dc-api process. Safe for
// concurrent use — every method is read-only against the underlying
// Provider and oauth2.Config (which themselves are immutable post-init).
type Service struct {
	cfg      Config
	provider *oidc.Provider
	oauth2   *oauth2.Config
	verifier *oidc.IDTokenVerifier
	codec    *CookieCodec
}

// NewService constructs the BFF. Returns an error when any required
// field is missing or when the issuer's discovery endpoint isn't
// reachable. Callers MUST check the error — a half-built Service
// would no-op the auth flow silently.
func NewService(ctx context.Context, cfg Config) (*Service, error) {
	if cfg.Issuer == "" {
		return nil, errors.New("auth.Config.Issuer is required")
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, errors.New("auth.Config.ClientID and ClientSecret are required (BFF needs a confidential Asgardeo client)")
	}
	if cfg.RedirectURL == "" {
		return nil, errors.New("auth.Config.RedirectURL is required (must match Asgardeo callback_urls)")
	}
	if cfg.PostLoginRedirect == "" {
		return nil, errors.New("auth.Config.PostLoginRedirect is required (cloud-ui origin)")
	}
	codec, err := NewCookieCodec(cfg.SessionKey)
	if err != nil {
		return nil, fmt.Errorf("session key: %w", err)
	}

	// Defaults mirror the auth middleware. Keeping these defaulted here
	// (rather than at construction call sites) prevents the BFF callback
	// from silently treating every user as a non-admin when the caller
	// forgot to pass them through.
	if cfg.AdminGroup == "" {
		cfg.AdminGroup = "dc-admin"
	}

	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc provider %q: %w", cfg.Issuer, err)
	}
	oauth2Cfg := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  cfg.RedirectURL,
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email", "groups"},
	}
	verifier := provider.Verifier(&oidc.Config{ClientID: cfg.ClientID})

	return &Service{
		cfg:      cfg,
		provider: provider,
		oauth2:   oauth2Cfg,
		verifier: verifier,
		codec:    codec,
	}, nil
}

// Codec exposes the AES-GCM codec so the auth middleware can decode the
// session cookie when no Authorization header is present. The middleware
// holds a *CookieCodec via dependency injection rather than reaching
// into Service directly — that keeps the import graph one-way.
func (s *Service) Codec() *CookieCodec { return s.codec }

// SessionCookieName is the cookie that holds the BFF session. Exported
// so the middleware can read it without a struct pointer dependency.
const SessionCookieName = "dcapi_session"

// stateCookieName is the per-login PKCE+nonce+return_to cookie.
const stateCookieName = "dcapi_oidc_state"
