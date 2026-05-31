//go:build integration

// rbac_service_account_test.go — M1.5 Chunk 5 integration tests.
//
// These tests verify the service-account auth path introduced in Chunk 5:
//
//   - SA tokens of the form dcapi_sa_<lookup_id>_<secret> are accepted.
//   - Role enforcement (owner vs member vs invalid) applies identically to SAs.
//   - last_used is updated asynchronously after each authenticated request.
//   - OIDC (JWT) requests still work alongside SA tokens (composite chain).
//   - Malformed tokens return 401.
//
// All tests use the package-level env (shared DB + server) rather than
// spinning up a subEnv, because SA validation uses the SA-specific auth path,
// not the OIDC autoprovision path.  We insert service_accounts rows and
// role_assignments rows directly via SQL to simulate what Chunk 7 will do.
package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/wso2/dc-api/internal/api/middleware"
	"github.com/wso2/dc-api/internal/models"
	"golang.org/x/crypto/bcrypt"
)

// ── Test helpers ──────────────────────────────────────────────────────────────

// saLookupIDLen and saCost must match the constants in serviceaccount.go.
const (
	testSALookupIDLen = 12
	testSASecretLen   = 32
	testSABcryptCost  = bcrypt.DefaultCost
)

// generateSAToken generates a raw token of the form:
//
//	dcapi_sa_<12-char lookup_id>_<32-char secret>
//
// Returns (rawToken, lookupID, secret).
func generateSAToken(t *testing.T) (rawToken, lookupID, secret string) {
	t.Helper()
	lookupBytes := make([]byte, testSALookupIDLen/2) // 6 bytes → 12 hex chars
	_, err := rand.Read(lookupBytes)
	require.NoError(t, err, "generateSAToken: rand lookupID")

	secretBytes := make([]byte, testSASecretLen/2) // 16 bytes → 32 hex chars
	_, err = rand.Read(secretBytes)
	require.NoError(t, err, "generateSAToken: rand secret")

	lookupID = hex.EncodeToString(lookupBytes)
	secret = hex.EncodeToString(secretBytes)
	rawToken = middleware.ServiceAccountTokenPrefix + lookupID + "_" + secret
	return
}

// hashSecret bcrypt-hashes the secret portion. Mirrors what Chunk 7 will do at
// SA creation time.
func hashSecret(t *testing.T, secret string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(secret), testSABcryptCost)
	require.NoError(t, err, "hashSecret: bcrypt")
	return string(h)
}

// clientForServiceAccount creates a service account in the DB with the given
// tenantID and role, and returns an *APIClient configured to use the raw token.
//
//  1. Generates a raw token (dcapi_sa_<lookup_id>_<secret>).
//  2. bcrypt-hashes the secret portion.
//  3. Inserts a service_accounts row (via SQL — Chunk 7 handler doesn't exist yet).
//  4. Inserts a role_assignments row binding the SA to the tenant scope.
//  5. Returns a client that sends the raw token.
func clientForServiceAccount(t *testing.T, tenantID string, role models.Role) (*APIClient, uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	rawToken, lookupID, secret := generateSAToken(t)
	tokenHash := hashSecret(t, secret)

	saName := "test-sa-" + randomName("sa")
	saID := uuid.New()

	// Phase 6a: TenantContext refuses requests whose slug isn't in `tenants`.
	// Ensure the tenants row exists and capture its UUID for the raw INSERTs
	// below — every per-tenant table now carries tenant_uuid/scope_uuid.
	if _, err := env.DB.UpsertTenant(ctx, tenantID, tenantID, "dc-tenant-"+tenantID, "test-setup-sa"); err != nil {
		require.NoError(t, err, "UpsertTenant for %s", tenantID)
	}
	tenantUUID, err := env.DB.GetTenantUUIDBySlug(ctx, tenantID)
	require.NoError(t, err, "GetTenantUUIDBySlug")
	require.NotEqual(t, uuid.Nil, tenantUUID, "tenants row must exist after upsert")

	// Insert service_accounts row directly. The Chunk 7 handler will wrap this
	// in an HTTP endpoint; for now we bypass it to test auth in isolation.
	_, err = env.DB.Pool().Exec(ctx, `
		INSERT INTO service_accounts (id, tenant_id, tenant_uuid, name, token_lookup_id, token_hash, description)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		saID, tenantID, tenantUUID, saName, lookupID, tokenHash, "integration test SA",
	)
	require.NoError(t, err, "clientForServiceAccount: insert service_accounts row")

	// Insert role_assignments row.
	_, err = env.DB.Pool().Exec(ctx, `
		INSERT INTO role_assignments
			(principal_type, principal_id, scope_type, scope_id, scope_uuid, role_definition, granted_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		string(models.PrincipalTypeServiceAccount),
		saID.String(),
		string(models.ScopeTypeTenant),
		tenantID,
		tenantUUID,
		models.RoleDefinitionForRole(role),
		"test-setup",
	)
	require.NoError(t, err, "clientForServiceAccount: insert role_assignments row")

	// Ensure the default project exists — VNet endpoints are now project-scoped
	// (/v1/tenants/{tid}/projects/{pid}/vnets) since M2.5. Tests that call
	// ListVNets / CreateVNet / DeleteVNet via this SA client require the project
	// context to resolve. ensureDefaultProject is idempotent.
	ensureDefaultProject(t, tenantID)

	// Bind the client to the tenant AND default project so resource calls route
	// through /v1/tenants/{tenantID}/projects/default/... correctly.
	return NewAPIClientForProject(env.BaseURL, rawToken, tenantID, defaultProjectID), saID
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestRBAC_ServiceAccount_OwnerCanDoEverything verifies that an SA with the
// owner role can list, create, and delete VNets.
func TestRBAC_ServiceAccount_OwnerCanDoEverything(t *testing.T) {
	t.Parallel()
	tenantID := randomTenantID("sa-owner")
	if !strings.HasPrefix(tenantID, "test-") {
		tenantID = "test-" + tenantID
	}

	client, _ := clientForServiceAccount(t, tenantID, models.RoleOwner)
	ctx := context.Background()

	// ── GET /v1/vnets must return 200 ────────────────────────────────────────
	_, listStatus, err := client.ListVNets(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listStatus,
		"SA owner must be able to list VNets")

	// ── POST /v1/vnets must return 202 ───────────────────────────────────────
	createResp, _, createStatus, err := client.CreateVNet(ctx, CreateVNetRequest{
		Name:         randomName("vnet"),
		AddressSpace: []string{"10.110.0.0/16"},
		Region:       "lk",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, createStatus,
		"SA owner must receive 202 on POST /v1/vnets")

	vnetID := createResp.Resource.ID
	require.NotEmpty(t, vnetID, "vnet ID must be set in response")

	// ── DELETE /v1/vnets/{id} must return 202 ────────────────────────────────
	_, deleteStatus, err := client.DeleteVNet(ctx, vnetID)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, deleteStatus,
		"SA owner must receive 202 on DELETE /v1/vnets/{id}")
}

// TestRBAC_ServiceAccount_MemberCannotDelete verifies that an SA with the
// member role can create but not delete VNets.
func TestRBAC_ServiceAccount_MemberCannotDelete(t *testing.T) {
	t.Parallel()
	tenantID := randomTenantID("sa-member")
	if !strings.HasPrefix(tenantID, "test-") {
		tenantID = "test-" + tenantID
	}

	client, _ := clientForServiceAccount(t, tenantID, models.RoleMember)
	ctx := context.Background()

	// ── POST /v1/vnets must return 202 ───────────────────────────────────────
	createResp, _, createStatus, err := client.CreateVNet(ctx, CreateVNetRequest{
		Name:         randomName("vnet"),
		AddressSpace: []string{"10.111.0.0/16"},
		Region:       "lk",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, createStatus,
		"SA member must receive 202 on POST /v1/vnets")

	vnetID := createResp.Resource.ID

	// ── DELETE /v1/vnets/{id} must return 403 ────────────────────────────────
	_, deleteStatus, err := client.DeleteVNet(ctx, vnetID)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, deleteStatus,
		"SA member must receive 403 on DELETE /v1/vnets/{id}")
}

// TestRBAC_ServiceAccount_InvalidTokenReturns401 verifies that malformed or
// invalid SA tokens are rejected with HTTP 401.
func TestRBAC_ServiceAccount_InvalidTokenReturns401(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// ── dcapi_sa_ prefix with wrong format (no second segment) → 401 ─────────
	// Auth middleware rejects before TenantContext runs, so the tenantID in the
	// URL doesn't matter — we use a dummy to satisfy tenantBasePath(). Using
	// cap-usage (a real tenant-scoped route that still exists post-M2.5) so chi
	// runs the auth middleware before returning 404 from routing.
	badFormatClient := NewAPIClientForTenant(env.BaseURL, "dcapi_sa_garbage", "test-invalid-sa")
	_, _, badFmtStatus, err := badFormatClient.GetTenantCapUsage(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, badFmtStatus,
		"dcapi_sa_garbage (no underscore-separated secret) must return 401")

	// ── Valid format but wrong secret → 401 ──────────────────────────────────
	// Create a real SA, then send a token with the correct lookup_id but a
	// corrupted secret. The bcrypt comparison will fail.
	tenantID := randomTenantID("sa-invalid")
	// Generate a token solely for its lookup_id; we will NOT use the matching secret.
	_, lookupID, _ := generateSAToken(t)
	tokenHash := hashSecret(t, "correct-secret-that-we-will-not-send")

	saID := uuid.New()
	_, err = env.DB.Pool().Exec(ctx, `
		INSERT INTO service_accounts (id, tenant_id, name, token_lookup_id, token_hash, description)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		saID, tenantID, "test-sa-invalid-"+randomName("sa"), lookupID, tokenHash, "test",
	)
	require.NoError(t, err, "insert SA for invalid-secret test")

	// Reconstruct a token with the same lookup_id but a different (wrong) secret.
	wrongSecretToken := middleware.ServiceAccountTokenPrefix + lookupID + "_" + hex.EncodeToString(make([]byte, 16))

	wrongSecretClient := NewAPIClientForTenant(env.BaseURL, wrongSecretToken, tenantID)
	_, _, wrongSecretStatus, err := wrongSecretClient.GetTenantCapUsage(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, wrongSecretStatus,
		"valid lookup_id but wrong secret must return 401")

	// ── Completely unknown lookup_id → 401 ────────────────────────────────────
	unknownLookupToken := fmt.Sprintf("%s%s_%s",
		middleware.ServiceAccountTokenPrefix,
		hex.EncodeToString([]byte("unknownlk")), // 18 chars — wrong length → 401
		"somesecret",
	)
	unknownClient := NewAPIClientForTenant(env.BaseURL, unknownLookupToken, tenantID)
	_, _, unknownStatus, err := unknownClient.GetTenantCapUsage(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, unknownStatus,
		"unknown lookup_id must return 401")
}

// TestRBAC_ServiceAccount_LastUsedUpdated verifies that after an authenticated
// SA request, the last_used column is set to a non-nil, recent timestamp.
func TestRBAC_ServiceAccount_LastUsedUpdated(t *testing.T) {
	t.Parallel()
	tenantID := randomTenantID("sa-lastused")

	client, saID := clientForServiceAccount(t, tenantID, models.RoleMember)
	ctx := context.Background()

	// ── Confirm last_used is nil before the first request ─────────────────────
	sa, err := env.DB.GetServiceAccount(ctx, saID)
	require.NoError(t, err)
	require.NotNil(t, sa, "service account must exist")
	require.Nil(t, sa.LastUsed, "last_used must be nil before first request")

	// ── Make an authenticated request ─────────────────────────────────────────
	before := time.Now().UTC().Add(-time.Second) // 1s buffer for clock skew
	_, status, err := client.ListVNets(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "SA member must be able to list VNets")

	// ── Wait for the async goroutine to write last_used ───────────────────────
	// The update is fire-and-forget. Give it up to 3 seconds to land.
	require.Eventually(t, func() bool {
		freshSA, dbErr := env.DB.GetServiceAccount(context.Background(), saID)
		if dbErr != nil || freshSA == nil {
			return false
		}
		return freshSA.LastUsed != nil
	}, 3*time.Second, 100*time.Millisecond,
		"last_used must be set within 3s after an authenticated SA request")

	// ── Verify the timestamp is recent ───────────────────────────────────────
	freshSA, err := env.DB.GetServiceAccount(ctx, saID)
	require.NoError(t, err)
	require.NotNil(t, freshSA.LastUsed)
	require.True(t, freshSA.LastUsed.UTC().After(before),
		"last_used (%v) must be after the request time (%v)", freshSA.LastUsed, before)
}

// TestRBAC_ServiceAccount_OIDCStillWorks verifies that after adding the SA
// composite chain, ordinary OIDC JWT requests (no dcapi_sa_ prefix) still
// work and land with principal_type=user.
func TestRBAC_ServiceAccount_OIDCStillWorks(t *testing.T) {
	t.Parallel()

	// Mint a standard OIDC JWT for a test tenant. clientForTenant does exactly this.
	tenantID := "test-sa-oidc-compat"
	client := clientForTenant(t, tenantID)
	ctx := context.Background()

	// The autoprovision path creates a 'member' role on first request. Since we
	// need to verify that the OIDC path works at all, a simple list (which
	// requires only viewer/member) is sufficient.
	_, status, err := client.ListVNets(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status,
		"OIDC JWT request must still work (return 200) after composite SA chain is wired in")
}
