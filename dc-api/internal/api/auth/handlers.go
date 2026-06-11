package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/rs/zerolog"
	"github.com/wso2/dc-api/internal/api/respond"
	"golang.org/x/oauth2"
)

// stateCookieMaxAge bounds the redirect-round-trip window. Five minutes
// is generous — a normal browser completes the round-trip in seconds.
// Anything longer than this and the cookie expires before /callback
// reads it, so an attacker can't replay a stale state cookie indefinitely.
const stateCookieMaxAge = 5 * 60

// HandleLogin redirects the browser to Asgardeo's authorize endpoint
// with PKCE+nonce, after setting an encrypted state cookie that
// /callback will decrypt to recover the verifier.
//
// Query param `return_to` is honoured so cloud-ui can deep-link into
// a page that needs auth and have the user land back on it. It MUST
// be an absolute URL pointing at the configured cloud-ui origin — any
// other host is rejected to stop open-redirect attacks.
func (s *Service) HandleLogin(log zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		verifier, err := NewPKCEVerifier()
		if err != nil {
			s.error(w, log, http.StatusInternalServerError, "PKCE verifier", err)
			return
		}
		nonce, err := NewNonce()
		if err != nil {
			s.error(w, log, http.StatusInternalServerError, "nonce", err)
			return
		}
		returnTo := s.sanitizeReturnTo(r.URL.Query().Get("return_to"))

		stateValue, err := s.codec.EncodeState(&State{
			CodeVerifier: verifier,
			Nonce:        nonce,
			ReturnTo:     returnTo,
		})
		if err != nil {
			s.error(w, log, http.StatusInternalServerError, "encode state", err)
			return
		}
		s.setStateCookie(w, stateValue)

		// AuthCodeURL with custom params: code_challenge + code_challenge_method
		// for PKCE; nonce for ID-token-level replay protection. The `state` we
		// pass here is unused for round-trip carry (we use a separate cookie
		// for that, encrypted, so it doesn't bloat the URL) — but Asgardeo
		// echoes back any opaque state, so we send the nonce again as a
		// belt-and-braces tamper check at callback time.
		authURL := s.oauth2.AuthCodeURL(nonce,
			oauth2.SetAuthURLParam("code_challenge", PKCEChallenge(verifier)),
			oauth2.SetAuthURLParam("code_challenge_method", "S256"),
			oidc.Nonce(nonce),
		)
		http.Redirect(w, r, authURL, http.StatusFound)
	}
}

// HandleCallback completes the auth-code-with-PKCE exchange and sets
// the dcapi_session cookie. On success it 302s to the cloud-ui origin
// (or to the requested return_to, if it was valid).
func (s *Service) HandleCallback(log zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stateCk, err := r.Cookie(stateCookieName)
		if err != nil {
			s.error(w, log, http.StatusBadRequest, "missing state cookie — restart sign-in", err)
			return
		}
		state, err := s.codec.DecodeState(stateCk.Value)
		if err != nil {
			s.error(w, log, http.StatusBadRequest, "state cookie invalid — restart sign-in", err)
			return
		}
		// Tamper check — `state` echoed by Asgardeo must equal our nonce.
		if got := r.URL.Query().Get("state"); got != state.Nonce {
			s.error(w, log, http.StatusBadRequest, "state mismatch — sign-in aborted", errors.New("state value did not match"))
			return
		}

		// Code → token exchange with the PKCE verifier.
		token, err := s.oauth2.Exchange(r.Context(),
			r.URL.Query().Get("code"),
			oauth2.SetAuthURLParam("code_verifier", state.CodeVerifier),
		)
		if err != nil {
			s.error(w, log, http.StatusBadGateway, "token exchange failed", err)
			return
		}

		// Pull and verify the ID token — confirms the Issuer and signature
		// and pins the nonce to *this* login round-trip.
		rawIDToken, _ := token.Extra("id_token").(string)
		if rawIDToken == "" {
			s.error(w, log, http.StatusBadGateway, "Asgardeo returned no id_token", errors.New("missing id_token"))
			return
		}
		idToken, err := s.verifier.Verify(r.Context(), rawIDToken)
		if err != nil {
			s.error(w, log, http.StatusBadGateway, "id_token verify failed", err)
			return
		}
		if idToken.Nonce != state.Nonce {
			s.error(w, log, http.StatusBadGateway, "id_token nonce mismatch", errors.New("nonce mismatch"))
			return
		}

		// Pull a few claims so /v1/auth/me can answer without re-verifying.
		// `groups` is parsed here only to derive IsAdmin — tenant
		// membership comes from the role_assignments table (GET /v1/tenants),
		// never from IdP groups.
		var claims struct {
			Sub        string   `json:"sub"`
			Email      string   `json:"email"`
			Name       string   `json:"name"`
			GivenName  string   `json:"given_name"`
			FamilyName string   `json:"family_name"`
			Groups     []string `json:"groups"`
		}
		_ = idToken.Claims(&claims)
		displayName := claims.Name
		if displayName == "" {
			displayName = strings.TrimSpace(claims.GivenName + " " + claims.FamilyName)
		}

		// Env-var sub list and AdminGroup both promote.
		var sessionIsAdmin bool
		if _, ok := s.cfg.PlatformAdminSubs[claims.Sub]; ok {
			sessionIsAdmin = true
		}
		for _, g := range claims.Groups {
			if g == s.cfg.AdminGroup {
				sessionIsAdmin = true
				break
			}
		}

		// Seal the session cookie. We store the *ID token* here, not the
		// OAuth access token, even though the field is named AccessToken.
		// Reason: Asgardeo's per-app default omits the `groups` claim
		// from access tokens issued to `cloud_ui_bff` (confirmed empirically
		// on 2026-05-16 — the access token only carries `scope: "groups …"`,
		// not the claim itself), so the middleware's group→tenant mapping
		// produces an empty tenantID and 403s every /v1/* call. The ID
		// token from the same callback DOES carry `groups` and is signed
		// by the same JWKS, so the middleware accepts it identically.
		// TODO(F7-followup): switch Asgardeo's `cloud_ui_bff` app to
		// include groups in the access token too, then drop this swap.
		sess := &Session{
			AccessToken:  rawIDToken,
			RefreshToken: token.RefreshToken,
			ExpiresAt:    token.Expiry,
			Subject:      claims.Sub,
			Email:        claims.Email,
			Name:         displayName,
			IsAdmin:      sessionIsAdmin,
		}
		sessionValue, err := s.codec.EncodeSession(sess)
		if err != nil {
			s.error(w, log, http.StatusInternalServerError, "encode session", err)
			return
		}
		s.setSessionCookie(w, sessionValue, time.Until(sess.ExpiresAt))
		s.clearStateCookie(w)

		http.Redirect(w, r, s.resolveReturnTo(state.ReturnTo), http.StatusFound)
	}
}

// HandleLogout clears the dcapi_session cookie and 302s to Asgardeo's
// end-session endpoint with a post-logout redirect back to cloud-ui.
// Asgardeo invalidates the IdP session and bounces the browser back.
//
// POST-only by convention — a GET /logout linked from the page is
// trivially CSRF-able. SameSite=Lax + same-site POST keeps this safe.
func (s *Service) HandleLogout(log zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Capture the IdP id_token_hint from the session before clearing,
		// so end-session can target this exact authentication.
		var idTokenHint string
		if ck, err := r.Cookie(SessionCookieName); err == nil {
			if sess, err := s.codec.DecodeSession(ck.Value); err == nil {
				idTokenHint = sess.AccessToken // some IdPs accept access_token; Asgardeo accepts either
			}
		}
		s.clearSessionCookie(w)

		// Discover end_session_endpoint from the provider. The
		// coreos/go-oidc Provider doesn't expose it via a typed accessor,
		// so we unmarshal the discovery doc into a local struct.
		var d struct {
			EndSessionEndpoint string `json:"end_session_endpoint"`
		}
		if err := s.provider.Claims(&d); err != nil || d.EndSessionEndpoint == "" {
			// Provider didn't advertise end_session — fall back to a
			// local redirect. Session cookie is already cleared.
			http.Redirect(w, r, s.cfg.PostLogoutRedirect, http.StatusFound)
			return
		}

		u, err := url.Parse(d.EndSessionEndpoint)
		if err != nil {
			s.error(w, log, http.StatusInternalServerError, "parse end_session_endpoint", err)
			return
		}
		q := u.Query()
		if idTokenHint != "" {
			q.Set("id_token_hint", idTokenHint)
		}
		q.Set("post_logout_redirect_uri", s.cfg.PostLogoutRedirect)
		q.Set("client_id", s.cfg.ClientID)
		u.RawQuery = q.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	}
}

// HandleMe returns the current user identity from the session cookie.
// cloud-ui calls this on app load to populate the AuthProvider context.
// Returns 401 when no valid session cookie is present.
func (s *Service) HandleMe(log zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ck, err := r.Cookie(SessionCookieName)
		if err != nil {
			respond.Error(w, http.StatusUnauthorized, "no session")
			return
		}
		sess, err := s.codec.DecodeSession(ck.Value)
		if err != nil {
			respond.Error(w, http.StatusUnauthorized, "session invalid")
			return
		}
		// Treat past-expiry sessions as if the cookie wasn't there. Without
		// this, a client with a stale-but-not-yet-cleared cookie gets 200
		// here, then 401 on its next /v1/* call — confusing for the SPA's
		// AuthProvider mount path. The spec documents this behaviour;
		// AccessTokenFromCookieReq (used by the auth middleware) already
		// applies the same check.
		if sess.Expired() {
			respond.Error(w, http.StatusUnauthorized, "session expired")
			return
		}
		// We don't re-verify the access token's signature here — the auth
		// middleware already does that on every /v1/* call, and /v1/auth/me
		// is meant to be cheap for the SPA to hit on mount.
		//
		// is_admin is derived once at callback time from the ID token's
		// `groups` claim and cached in the session cookie. The SPA reads
		// tenant membership from GET /v1/tenants (role_assignments-backed),
		// not from here.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sub":        sess.Subject,
			"email":      sess.Email,
			"name":       sess.Name,
			"expires_at": sess.ExpiresAt,
			"is_admin":   sess.IsAdmin,
		})
	}
}

// ── Cookie helpers ──────────────────────────────────────────────────────────

func (s *Service) setSessionCookie(w http.ResponseWriter, value string, ttl time.Duration) {
	// Browser caps Max-Age at 24h for a lot of UA combinations; clamp
	// the session lifetime ourselves so we don't issue a cookie that
	// outlives the access token's exp claim. If ttl <= 0 (token already
	// past expiry, shouldn't happen), default to 1h so the middleware
	// at least gets a chance to surface "expired" on the next request.
	if ttl <= 0 {
		ttl = time.Hour
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    value,
		Path:     "/",
		Domain:   s.cfg.CookieDomain,
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ttl.Seconds()),
	})
}

func (s *Service) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		Domain:   s.cfg.CookieDomain,
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func (s *Service) setStateCookie(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    value,
		Path:     "/v1/auth",
		Domain:   s.cfg.CookieDomain,
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   stateCookieMaxAge,
	})
}

func (s *Service) clearStateCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    "",
		Path:     "/v1/auth",
		Domain:   s.cfg.CookieDomain,
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// sanitizeReturnTo accepts the caller's return_to ONLY when it parses
// and shares the configured cloud-ui origin. Anything else falls back
// to PostLoginRedirect — silent rather than 4xx so a tampered URL
// doesn't break sign-in.
func (s *Service) sanitizeReturnTo(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	base, err := url.Parse(s.cfg.PostLoginRedirect)
	if err != nil {
		return ""
	}
	if u.Scheme == base.Scheme && u.Host == base.Host {
		return raw
	}
	return ""
}

// resolveReturnTo returns the previously-sanitized URL or falls back
// to PostLoginRedirect.
func (s *Service) resolveReturnTo(returnTo string) string {
	if returnTo != "" {
		return returnTo
	}
	return s.cfg.PostLoginRedirect
}

func (s *Service) error(w http.ResponseWriter, log zerolog.Logger, status int, msg string, err error) {
	log.Warn().Err(err).Msg("auth: " + msg)
	respond.Error(w, status, msg)
}

// AccessTokenFromCookie decodes a sealed cookie value and returns the
// access token within. Used by the middleware to bridge cookie auth
// into the existing JWT validation path.
func (s *Service) AccessTokenFromCookie(value string) (string, error) {
	sess, err := s.codec.DecodeSession(value)
	if err != nil {
		return "", err
	}
	if sess.Expired() {
		return "", fmt.Errorf("session expired at %s", sess.ExpiresAt)
	}
	return sess.AccessToken, nil
}

// AccessTokenFromCookieReq is the request-level adapter the middleware
// uses. Returns (token, true) only when both the cookie is present AND
// the sealed session decodes cleanly AND the token isn't expired. Any
// failure → (empty, false), and the middleware falls through to the
// "no Authorization header" 401 path.
func (s *Service) AccessTokenFromCookieReq(r *http.Request) (string, bool) {
	ck, err := r.Cookie(SessionCookieName)
	if err != nil {
		return "", false
	}
	tok, err := s.AccessTokenFromCookie(ck.Value)
	if err != nil {
		return "", false
	}
	return tok, true
}

// Ensure context is unused-import safe in case we drop it later.
var _ = context.Background
