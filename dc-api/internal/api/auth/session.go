package auth

import (
	"encoding/json"
	"fmt"
	"time"
)

// Session is the payload encrypted into the dcapi_session cookie. It's
// kept tiny on purpose — browsers cap cookies at 4KB and Asgardeo ID
// tokens already eat ~1.2KB, so room for the rest matters.
//
// We store the *access token* (the JWT dc-api's middleware already knows
// how to verify) plus refresh metadata. The refresh token is sealed in
// the same cookie so the BFF can rotate the access token without the
// user re-authenticating; refresh tokens themselves never leave the
// server.
type Session struct {
	AccessToken  string    `json:"a"`
	RefreshToken string    `json:"r,omitempty"`
	ExpiresAt    time.Time `json:"e"`
	// Subject is the IdP's `sub` claim. Cached here so /v1/auth/me can
	// return identity without re-verifying the access token (verification
	// happens inside the auth middleware on every API call anyway).
	Subject string `json:"s"`
	Email   string `json:"m,omitempty"`
	// IsAdmin is derived from the `groups` claim in the ID token at
	// callback time so /v1/auth/me can answer "am I admin?" without
	// re-parsing the JWT. The same derivation runs again inside the auth
	// middleware on every /v1/* call; storing it here is purely for the
	// BFF endpoint's convenience. Tenant membership is never cached in
	// the session — the SPA reads GET /v1/tenants (role_assignments).
	IsAdmin bool `json:"adm,omitempty"`
}

// Expired returns true once the access token is past its expiry. A
// small skew is built in so we refresh slightly early rather than
// letting an API call land on an expired-but-not-yet-refreshed token.
func (s *Session) Expired() bool {
	return time.Now().After(s.ExpiresAt.Add(-30 * time.Second))
}

// EncodeSession serialises a Session and seals it into a cookie value.
func (c *CookieCodec) EncodeSession(s *Session) (string, error) {
	plaintext, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("marshal session: %w", err)
	}
	return c.Seal(plaintext)
}

// DecodeSession reverses EncodeSession. Any error means "no valid
// session" and the caller should treat the request as unauthenticated.
func (c *CookieCodec) DecodeSession(encoded string) (*Session, error) {
	plaintext, err := c.Open(encoded)
	if err != nil {
		return nil, err
	}
	var s Session
	if err := json.Unmarshal(plaintext, &s); err != nil {
		return nil, fmt.Errorf("unmarshal session: %w", err)
	}
	return &s, nil
}
