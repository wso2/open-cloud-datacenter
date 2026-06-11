//go:build integration

package integration

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wso2/dc-api/internal/db"
	"github.com/wso2/dc-api/internal/models"
)

// JWTMinter generates per-test-run RSA keys and mints signed JWTs.
// Generated once per TestMain — never persisted to disk.
//
// When Repo is set (wired by the env constructors), MintToken /
// MintTokenMultiTenant ALSO seed the tenants + role_assignments rows the
// minted principal needs. Production dc-api derives membership exclusively
// from role_assignments (IdP groups play no part), so the old
// group-autoprovision shortcut tests relied on lives here now, as an
// explicit fixture step.
type JWTMinter struct {
	privKey *rsa.PrivateKey
	Repo    *db.Repository
}

// seedMembership upserts the tenants row and a tenant-scope 'member'
// role_assignment for (sub, tenantID). Idempotent: existing rows are left
// alone so repeated mints for the same principal/tenant don't error.
func (m *JWTMinter) seedMembership(sub, tenantID string) error {
	if m.Repo == nil {
		return nil
	}
	ctx := context.Background()
	if _, err := m.Repo.UpsertTenant(ctx, tenantID, tenantID, "", "test-fixture"); err != nil {
		return fmt.Errorf("seed tenant %s: %w", tenantID, err)
	}
	existing, err := m.Repo.ListRoleAssignmentsForPrincipal(ctx, models.PrincipalTypeUser, sub)
	if err != nil {
		return fmt.Errorf("seed membership lookup: %w", err)
	}
	for _, a := range existing {
		if a.ScopeType == models.ScopeTypeTenant && a.ScopeID == tenantID {
			return nil
		}
	}
	if _, err := m.Repo.CreateRoleAssignment(ctx, models.RoleAssignment{
		ID:            uuid.New(),
		PrincipalType: models.PrincipalTypeUser,
		PrincipalID:   sub,
		ScopeType:     models.ScopeTypeTenant,
		ScopeID:       tenantID,
		Role:          models.RoleMember,
		GrantedBy:     "test-fixture",
	}); err != nil {
		return fmt.Errorf("seed membership %s@%s: %w", sub, tenantID, err)
	}
	return nil
}

// NewJWTMinter generates a fresh RSA-2048 key pair.
func NewJWTMinter() (*JWTMinter, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generate test RSA key: %w", err)
	}
	return &JWTMinter{privKey: key}, nil
}

// PublicKeyJWKS returns the public key as JWK Set JSON.
// Pass directly to middleware.NewTestModeAuth.
func (m *JWTMinter) PublicKeyJWKS() []byte {
	pub := &m.privKey.PublicKey
	n := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(new(big.Int).SetInt64(int64(pub.E)).Bytes())
	jwks := map[string]interface{}{
		"keys": []interface{}{
			map[string]string{"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "test-1", "n": n, "e": e},
		},
	}
	b, _ := json.Marshal(jwks)
	return b
}

// MintToken mints a JWT whose sub is tenantID and seeds a tenant-scope
// 'member' role_assignment for it (when Repo is wired) — the explicit
// fixture replacement for the removed group-autoprovision behaviour.
func (m *JWTMinter) MintToken(tenantID, userID string) (string, error) {
	if err := m.seedMembership(tenantID, tenantID); err != nil {
		return "", err
	}
	return m.MintTokenWithGroups(tenantID, userID, nil)
}

// MintTokenMultiTenant mints a JWT for userID and seeds tenant-scope
// 'member' role_assignments for every tenantID in the slice. Use this in
// tests that need a single principal with access to multiple tenants
// (e.g. cloud-ui tenant switcher).
func (m *JWTMinter) MintTokenMultiTenant(userID string, tenantIDs []string) (string, error) {
	for _, t := range tenantIDs {
		if err := m.seedMembership(userID, t); err != nil {
			return "", err
		}
	}
	return m.MintTokenWithGroups(userID, userID, nil)
}

// MintTokenWithGroups mints a JWT with explicit group claims (for admin tests, etc.).
func (m *JWTMinter) MintTokenWithGroups(sub, userID string, groups []string) (string, error) {
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "test-1"})
	pay, _ := json.Marshal(map[string]interface{}{
		"sub": sub, "email": userID + "@test.dc", "groups": groups,
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	})
	hE := base64.RawURLEncoding.EncodeToString(hdr)
	pE := base64.RawURLEncoding.EncodeToString(pay)
	h := crypto.SHA256.New()
	h.Write([]byte(hE + "." + pE))
	sig, err := rsa.SignPKCS1v15(rand.Reader, m.privKey, crypto.SHA256, h.Sum(nil))
	if err != nil {
		return "", fmt.Errorf("sign test JWT: %w", err)
	}
	return strings.Join([]string{hE, pE, base64.RawURLEncoding.EncodeToString(sig)}, "."), nil
}
