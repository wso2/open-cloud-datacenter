//go:build integration

package integration

// nop_env_test.go — cluster-free ("nop") mode for the integration suite.
//
// The pure-authz tests (RBAC role matrix, members, service accounts, tenants,
// option-D, phase-6a) assert only on HTTP status codes — the authorization
// decision happens in the handler before any provider call — so they don't need
// a real Harvester/KubeOVN cluster at all. Opt in with DCAPI_TEST_NOP=1 and the
// shared env + sub-envs are built with all-nop backends and no kubeconfig, so
// the authz matrix can gate every PR in CI without a cluster.
//
// IMPORTANT: only the authz subset is meaningful in nop mode. Resource tests
// (vnet/subnet/vm/cluster/keyvault/...) genuinely provision on the cluster and
// will fail here — scope CI to the authz tests with -run.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/rs/zerolog"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/wso2/dc-api/internal/api"
	"github.com/wso2/dc-api/internal/api/middleware"
	"github.com/wso2/dc-api/internal/db"
	"github.com/wso2/dc-api/internal/models"
	"github.com/wso2/dc-api/internal/providers"
)

// nopMode reports whether the suite runs cluster-free (DCAPI_TEST_NOP=1).
func nopMode() bool { return os.Getenv("DCAPI_TEST_NOP") == "1" }

var errNop = errors.New("nop: provider disabled in cluster-free test mode")

// ── nopNetwork: a no-op NetworkProvider (mirrors test/contract) ──────────────
type nopNetwork struct{}

func (nopNetwork) Name() string { return "nop" }
func (nopNetwork) CreateVNet(context.Context, string, string, models.VNetSpec) (*models.VNetResource, error) {
	return nil, errNop
}
func (nopNetwork) GetVNet(context.Context, string) (*models.VNetResource, error) { return nil, errNop }
func (nopNetwork) DeleteVNet(context.Context, string) error                      { return errNop }
func (nopNetwork) CreateSubnet(context.Context, string, models.SubnetSpec) (*models.SubnetResource, error) {
	return nil, errNop
}
func (nopNetwork) GetSubnet(context.Context, string) (*models.SubnetResource, error) {
	return nil, errNop
}
func (nopNetwork) DeleteSubnet(context.Context, string) error { return errNop }
func (nopNetwork) CreateRouteTable(context.Context, string, models.RouteTableSpec) (*models.RouteTableResource, error) {
	return nil, errNop
}
func (nopNetwork) UpdateRouteTableRoutes(context.Context, string, []models.RouteRule) error {
	return errNop
}
func (nopNetwork) DeleteRouteTable(context.Context, string) error               { return errNop }
func (nopNetwork) AssociateRouteTable(context.Context, string, string) error    { return errNop }
func (nopNetwork) DisassociateRouteTable(context.Context, string, string) error { return errNop }
func (nopNetwork) CreateNSG(context.Context, string, string, models.NSGSpec) (*models.NSGResource, error) {
	return nil, errNop
}
func (nopNetwork) UpdateNSGRules(context.Context, string, []models.NSGRule) error { return errNop }
func (nopNetwork) DeleteNSG(context.Context, string) error                        { return errNop }
func (nopNetwork) AttachNSGToSubnet(context.Context, string, string) error        { return errNop }
func (nopNetwork) DetachNSGFromSubnet(context.Context, string, string) error      { return errNop }
func (nopNetwork) CreatePeering(context.Context, string, string, models.PeeringSpec) (*models.PeeringResource, error) {
	return nil, errNop
}
func (nopNetwork) DeletePeering(context.Context, string, []string, []string) error { return errNop }
func (nopNetwork) CreatePrivateDnsZone(context.Context, string, models.DnsZoneSpec) (*models.DnsZoneResource, error) {
	return nil, errNop
}
func (nopNetwork) DeletePrivateDnsZone(context.Context, string) error              { return errNop }
func (nopNetwork) UpsertDnsRecord(context.Context, string, models.DnsRecord) error { return errNop }
func (nopNetwork) DeleteDnsRecord(context.Context, string, string) error           { return errNop }

var _ providers.NetworkProvider = nopNetwork{}

// nopRouter builds an api.Router with all-nop backends and the same composite
// auth chain (SA first, then TestMode JWT) as the real env.
func nopRouter(repo *db.Repository, jwt *JWTMinter, cfg middleware.AuthConfig) (http.Handler, error) {
	testAuth, err := middleware.NewTestModeAuth(jwt.PublicKeyJWKS(), cfg, repo)
	if err != nil {
		return nil, err
	}
	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, NoColor: true}).With().Timestamp().Logger()
	saAuth := middleware.NewServiceAccountAuth(repo, logger)
	composite := middleware.NewCompositeAuth(saAuth, testAuth)
	return api.NewRouter(api.RouterDeps{
		Repo:            repo,
		ComputeProvider: &nopComputeProvider{},
		ClusterProvider: &nopClusterProvider{},
		NetworkProvider: nopNetwork{},
		AuthMiddleware:  composite,
		Log:             logger,
	}), nil
}

// newNopTestEnv builds the shared cluster-free env: testcontainers Postgres + a
// JWT minter + an all-nop router. KubeClient and NSProvisioner are nil, so the
// fixtures' "if NSProvisioner != nil" guards skip real namespace creation.
func newNopTestEnv(ctx context.Context) (*TestEnv, error) {
	pgc, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("dc_api_test"),
		tcpostgres.WithUsername("dc_api"),
		tcpostgres.WithPassword("testpass"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		return nil, fmt.Errorf("nop env: start postgres: %w", err)
	}
	connStr, err := pgc.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return nil, fmt.Errorf("nop env: conn string: %w", err)
	}
	pool, err := db.Connect(ctx, connStr)
	if err != nil {
		return nil, fmt.Errorf("nop env: connect: %w", err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		return nil, fmt.Errorf("nop env: migrate: %w", err)
	}
	// Narrow lk reserved-CIDR set so VNet POSTs don't 400 (mirrors newTestEnv).
	if _, err := pool.Exec(ctx, `
		INSERT INTO regions (name, description, reserved_cidrs)
		VALUES ('lk', 'test region', ARRAY[
			'192.168.10.0/24:harvester-mgmt',
			'10.42.0.0/16:rke2-pod-cidr',
			'10.43.0.0/16:rke2-service-cidr'
		])
		ON CONFLICT (name) DO UPDATE
		   SET reserved_cidrs = EXCLUDED.reserved_cidrs,
		       description    = EXCLUDED.description`); err != nil {
		return nil, fmt.Errorf("nop env: seed lk region: %w", err)
	}
	repo := db.NewRepository(pool)

	jwtMinter, err := NewJWTMinter()
	if err != nil {
		return nil, fmt.Errorf("nop env: create JWT minter: %w", err)
	}

	router, err := nopRouter(repo, jwtMinter, middleware.AuthConfig{
		TenantGroupPrefix:    "dc-tenant-",
		AdminGroup:           "dc-admin",
		AutoProvisionMembers: true,
	})
	if err != nil {
		return nil, fmt.Errorf("nop env: build router: %w", err)
	}
	srv := httptest.NewServer(router)
	return &TestEnv{
		Server:        srv,
		BaseURL:       srv.URL,
		DB:            repo,
		KubeClient:    nil,
		JWT:           jwtMinter,
		NSProvisioner: nil,
		pgContainer:   pgc,
	}, nil
}

// newNopSubEnv mirrors newSubEnv but cluster-free: a fresh server over the
// shared (nop) env's repo + JWT, with the caller's AuthConfig.
func newNopSubEnv(t *testing.T, cfg middleware.AuthConfig) *TestEnv {
	t.Helper()
	router, err := nopRouter(env.DB, env.JWT, cfg)
	if err != nil {
		t.Fatalf("newNopSubEnv: build router: %v", err)
	}
	srv := httptest.NewServer(router)
	t.Cleanup(func() { srv.Close() })
	return &TestEnv{
		Server:        srv,
		BaseURL:       srv.URL,
		DB:            env.DB,
		KubeClient:    env.KubeClient, // nil in nop mode
		JWT:           env.JWT,
		NSProvisioner: nil,
		pgContainer:   nil,
	}
}
