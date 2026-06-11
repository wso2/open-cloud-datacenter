//go:build contract

// Contract test: boot dc-api against a testcontainers Postgres with all
// providers nopped out, mint a JWT the in-process TestModeAuth accepts,
// then exec the schemathesis CLI against the running server to assert
// every response shape matches openapi.yaml.
//
// Run locally:
//
//	pip install --user schemathesis    # or: brew install schemathesis
//	go test -tags contract -timeout 5m ./test/contract/...
//
// CI: see .github/workflows/contract.yaml.
//
// Tag filter: schemathesis only exercises the data-plane-light endpoints
// (health, keyvaults, members, projects, service-accounts, tenants). The compute /
// networking / cluster tags need a real Harvester+Rancher backend and are
// out of scope here — those are covered by the live integration suite.
package contract

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/google/uuid"
	"github.com/wso2/dc-api/internal/api"
	"github.com/wso2/dc-api/internal/api/middleware"
	"github.com/wso2/dc-api/internal/db"
	"github.com/wso2/dc-api/internal/models"
)

func TestSpec_Conformance(t *testing.T) {
	schemathesisPath := schemathesisOrSkip(t)

	// 8-minute budget. Locally a full run completes in ~17s; the in-cluster
	// dc-runner is ~5x slower (Examples phase alone took 13.88s on
	// 2026-05-16, vs ~3s locally) and a 3-minute cap was killing the
	// fuzzing phase with SIGKILL mid-run. Keep generous headroom — the
	// outer `go test -timeout` in contract.yaml is the real ceiling.
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	// 1. Postgres testcontainer + migrations.
	pgc, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("dc_api_contract"),
		tcpostgres.WithUsername("dc_api"),
		tcpostgres.WithPassword("testpass"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = pgc.Terminate(context.Background()) })

	connStr, err := pgc.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connstr: %v", err)
	}
	pool, err := db.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repo := db.NewRepository(pool)

	// 2. JWT minter + matching TestModeAuth.
	minter, err := newJWTMinter()
	if err != nil {
		t.Fatalf("jwt minter: %v", err)
	}
	testAuth, err := middleware.NewTestModeAuth(minter.PublicKeyJWKS(), middleware.AuthConfig{
		AdminGroup: "dc-admin",
	})
	if err != nil {
		t.Fatalf("test auth: %v", err)
	}
	saAuth := middleware.NewServiceAccountAuth(repo, zerolog.Nop())
	composite := middleware.NewCompositeAuth(saAuth, testAuth)

	// 3. Router with all providers nopped out — schemathesis only validates
	// response *shapes*, not real provisioning. DirectoryProvider is
	// intentionally nil (feature dark): the /directory endpoints then answer
	// the documented 501 and invite-by-email the documented 422, both
	// deterministic — exactly what the spec's DirectoryNotConfigured /
	// InviteEmailUnprocessable responses describe.
	router := api.NewRouter(api.RouterDeps{
		Repo:            repo,
		ComputeProvider: nopCompute{},
		ClusterProvider: nopCluster{},
		NetworkProvider: nopNetwork{},
		AuthMiddleware:  composite,
		Log:             zerolog.Nop(),
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	// 4. Mint a JWT for the contract-test tenant and seed its membership
	// explicitly — tenant access comes from role_assignments rows, never
	// from IdP groups. Schemathesis attaches the token via --header.
	const tenantID = "contract-test"
	token, err := minter.MintToken(tenantID, "schemathesis@contract")
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	if _, err := repo.UpsertTenant(ctx, tenantID, tenantID, "", "contract-fixture"); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if _, err := repo.CreateRoleAssignment(ctx, models.RoleAssignment{
		ID:            uuid.New(),
		PrincipalType: models.PrincipalTypeUser,
		PrincipalID:   tenantID,
		ScopeType:     models.ScopeTypeTenant,
		ScopeID:       tenantID,
		Role:          models.RoleMember,
		GrantedBy:     "contract-fixture",
	}); err != nil {
		t.Fatalf("seed membership: %v", err)
	}

	// 5. Locate openapi.yaml relative to this test file.
	specPath, err := filepath.Abs(filepath.Join("..", "..", "openapi.yaml"))
	if err != nil {
		t.Fatalf("openapi path: %v", err)
	}
	if _, err := os.Stat(specPath); err != nil {
		t.Fatalf("openapi.yaml not found at %s: %v", specPath, err)
	}

	// 6. Run schemathesis. We keep the operation set tight on purpose:
	//   - health: anonymous /healthz
	//   - keyvaults, projects: pure DB CRUD
	//   - roleAssignments, service-accounts, tenants: pure DB + RBAC
	//     (roleAssignments is the RBAC-v2 name of the old `members` tag —
	//     the regex previously said `members`, which no longer matches any
	//     operation, so these endpoints had silently dropped out of coverage)
	//   - directory: works backend-free BY DESIGN — with a nil
	//     DirectoryProvider the endpoints return the documented 501
	//     feature-detection response, and the RBAC gate / tenant guard cover
	//     the 403/404 shapes
	// Networking, VMs, clusters, bastions, images need real backends and
	// are out of scope. --include-tag matches a single tag value, so we use
	// the regex variant to express the allowlist.
	//   - activity: pure DB read (audit_events ⋈ resources) — no provider calls
	tagRegex := `^(health|directory|keyvaults|roleAssignments|projects|service-accounts|tenants|activity)$`
	checks := strings.Join([]string{
		"not_a_server_error",
		"status_code_conformance",
		"content_type_conformance",
		"response_schema_conformance",
	}, ",")

	cmd := exec.CommandContext(ctx, schemathesisPath, "run",
		specPath,
		"--url", srv.URL,
		"--include-tag-regex", tagRegex,
		"--checks", checks,
		"--workers", "4",
		"--header", "Authorization: Bearer "+token,
		"--phases", "examples,fuzzing",
		"--warnings", "off",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(),
		// Hypothesis writes example databases under HOME by default; redirect
		// to TempDir so the test leaves no residue.
		"HYPOTHESIS_STORAGE_DIRECTORY="+t.TempDir(),
	)
	t.Logf("running: schemathesis run %s --url %s ...", specPath, srv.URL)
	if err := cmd.Run(); err != nil {
		t.Fatalf("schemathesis reported conformance failures: %v", err)
	}
}

// schemathesisOrSkip returns the path to the schemathesis binary, or skips
// the test if it isn't installed. Looks in $PATH and a couple of common pip
// --user locations.
func schemathesisOrSkip(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath("schemathesis"); err == nil {
		return p
	}
	// pip install --user fallbacks (CI uses these too).
	candidates := []string{
		filepath.Join(os.Getenv("HOME"), "Library/Python/3.9/bin/schemathesis"),
		filepath.Join(os.Getenv("HOME"), ".local/bin/schemathesis"),
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("schemathesis not on PATH — install with `pip install --user schemathesis`")
	return ""
}

// ── Minimal JWT minter — paralleling test/integration/jwt.go without the
// integration build tag so the contract suite can run on its own.

type jwtMinter struct{ key *rsa.PrivateKey }

func newJWTMinter() (*jwtMinter, error) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	return &jwtMinter{key: k}, nil
}

func (m *jwtMinter) PublicKeyJWKS() []byte {
	pub := &m.key.PublicKey
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

func (m *jwtMinter) MintToken(tenantID, email string) (string, error) {
	return m.mintWithGroups(tenantID, email, nil)
}

func (m *jwtMinter) mintWithGroups(sub, email string, groups []string) (string, error) {
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "test-1"})
	pay, _ := json.Marshal(map[string]interface{}{
		"sub": sub, "email": email, "groups": groups,
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	})
	hE := base64.RawURLEncoding.EncodeToString(hdr)
	pE := base64.RawURLEncoding.EncodeToString(pay)
	h := crypto.SHA256.New()
	h.Write([]byte(hE + "." + pE))
	sig, err := rsa.SignPKCS1v15(rand.Reader, m.key, crypto.SHA256, h.Sum(nil))
	if err != nil {
		return "", fmt.Errorf("sign: %w", err)
	}
	return strings.Join([]string{hE, pE, base64.RawURLEncoding.EncodeToString(sig)}, "."), nil
}
