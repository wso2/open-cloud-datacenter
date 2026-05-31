//go:build integration

// Package integration is the Phase 1 M2 networking integration test suite.
//
// Run with:
//
//	export KUBECONFIG=$HOME/.kube/config
//	export KUBE_CONTEXT=harvester-dev
//	cd dc-api
//	go test -tags integration -v ./test/integration/... -count=1 -timeout 15m
package integration

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/wso2/dc-api/internal/api"
	"github.com/wso2/dc-api/internal/api/middleware"
	"github.com/wso2/dc-api/internal/db"
	"github.com/wso2/dc-api/internal/models"
	"github.com/wso2/dc-api/internal/providers"
	"github.com/wso2/dc-api/internal/providers/endpoints"
	"github.com/wso2/dc-api/internal/providers/kubeovn"
	"github.com/wso2/dc-api/internal/providers/kvi"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

// TestEnv holds all live dependencies shared across the suite.
type TestEnv struct {
	Server      *httptest.Server
	BaseURL     string
	DB          *db.Repository
	KubeClient  dynamic.Interface
	JWT         *JWTMinter
	// NSProvisioner is the kubeovn client surfaced as a ProjectNamespaceProvisioner
	// so test fixtures can create real K8s project namespaces (with their
	// ResourceQuota) after CreateProject inserts the DB row. Without this, the
	// subnet/VM/etc. provisioner calls fail with "namespace not found" because
	// the DB-only CreateProject doesn't create the underlying K8s namespace.
	NSProvisioner providers.ProjectNamespaceProvisioner
	pgContainer *tcpostgres.PostgresContainer
}

// env is the package-level singleton initialised by TestMain.
var env *TestEnv

// init disables testcontainers' Ryuk reaper sidecar. Ryuk bind-mounts the
// Docker socket into itself for orphan cleanup, which fails on Rancher
// Desktop's user-mode socket (`~/.rd/docker.sock` isn't a real socket file
// from the daemon's perspective). Cleanup falls back to TestMain's explicit
// pgContainer.Terminate() call, which is what we want anyway.
func init() {
	if os.Getenv("TESTCONTAINERS_RYUK_DISABLED") == "" {
		_ = os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")
	}
}

func TestMain(m *testing.M) {
	ctx := context.Background()
	if err := preflight(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "INTEGRATION PREFLIGHT FAILED: %v\n", err)
		os.Exit(1)
	}
	var err error
	env, err = newTestEnv(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "INTEGRATION SETUP FAILED: %v\n", err)
		os.Exit(1)
	}
	scrubStaleTestResources(ctx, env.KubeClient)
	code := m.Run()
	// Always clean up resources we created during this run, regardless of pass/fail.
	// Without this, namespaces accumulate forever — the startup scrub only catches
	// >1h old leftovers, which leaves a window where back-to-back runs pile up
	// before being garbage-collected.
	cleanupTestResources(ctx, env.KubeClient)
	if env.pgContainer != nil {
		_ = env.pgContainer.Terminate(ctx)
	}
	if env.Server != nil {
		env.Server.Close()
	}
	os.Exit(code)
}

func newTestEnv(ctx context.Context) (*TestEnv, error) {
	pgc, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("dc_api_test"),
		tcpostgres.WithUsername("dc_api"),
		tcpostgres.WithPassword("testpass"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		return nil, fmt.Errorf("start postgres container: %w", err)
	}
	connStr, err := pgc.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return nil, fmt.Errorf("postgres connection string: %w", err)
	}
	pool, err := db.Connect(ctx, connStr)
	if err != nil {
		return nil, fmt.Errorf("connect to test postgres: %w", err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		return nil, fmt.Errorf("migrate test schema: %w", err)
	}
	// Override the production schema.sql seed of 'lk' so tests run against a
	// narrower reserved-CIDR set. schema.sql seeds 'lk' with the full
	// production list (including 10.96.0.0/12 from F45) — which collides with
	// the 10.10x test CIDRs and would 400 every VNet POST. ON CONFLICT DO
	// UPDATE ensures the test seed wins regardless of order.
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
		return nil, fmt.Errorf("seed lk region: %w", err)
	}
	repo := db.NewRepository(pool)

	jwtMinter, err := NewJWTMinter()
	if err != nil {
		return nil, fmt.Errorf("create JWT minter: %w", err)
	}

	kubeconfigRaw, err := loadKubeconfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	netProvider, err := kubeovn.New(base64.StdEncoding.EncodeToString(kubeconfigRaw), "kube-ovn")
	if err != nil {
		return nil, fmt.Errorf("create kubeovn provider: %w", err)
	}

	// F15: wire VPC external network if env vars are set so NAT provisioning
	// is exercised. Mirrors what cmd/dc-api/main.go does at startup.
	natProvisioner, err := configureF15(ctx, netProvider, repo)
	if err != nil {
		return nil, fmt.Errorf("configure F15: %w", err)
	}

	// F20: wire per-VPC DNS if env vars are set. Mirrors main.go's F20 block.
	dnsProvisioner, dnsSearchDomain, err := configureF20(ctx, netProvider)
	if err != nil {
		return nil, fmt.Errorf("configure F20: %w", err)
	}

	restCfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigRaw)
	if err != nil {
		return nil, fmt.Errorf("parse kubeconfig: %w", err)
	}
	dynClient, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("create dynamic kube client: %w", err)
	}

	// Pass repo so the M1.5 autoprovision flow runs in integration tests.
	// AutoProvisionMembers=true preserves M1 behaviour: any user with a valid
	// dc-tenant-<x> group is auto-enrolled as a 'member' on first request.
	testAuth, err := middleware.NewTestModeAuth(jwtMinter.PublicKeyJWKS(), middleware.AuthConfig{
		TenantGroupPrefix:    "dc-tenant-",
		AdminGroup:           "dc-admin",
		AutoProvisionMembers: true,
	}, repo)
	if err != nil {
		return nil, fmt.Errorf("create test auth middleware: %w", err)
	}

	// Use a real logger so provider errors aren't silently dropped — they'd
	// otherwise vanish into Nop() and we'd have no way to know why an
	// asyncProvision goroutine failed during a test.
	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, NoColor: true}).With().Timestamp().Logger()

	// M1.5 Chunk 5: wrap with CompositeAuth so SA tokens are validated first,
	// then fall through to OIDC (TestModeAuth) for JWT-based requests.
	saAuth := middleware.NewServiceAccountAuth(repo, logger)
	composite := middleware.NewCompositeAuth(saAuth, testAuth)

	// M3 chunk 2: generic Private Endpoint provisioner.
	epProvisioner := endpoints.NewKubeOVNProvisioner(netProvider.Dynamic(), endpoints.KubeOVNProvisionerOptions{})

	// M3 chunk 3: KVI provisioner for secret CRUD. Wired here so the /secrets
	// routes are registered and integration tests for secret CRUD can exercise
	// the real KVI operator on harvester-dev. The REST config comes from the
	// same kubeconfig used to build dynClient — no second connection pool.
	kviProvisioner := kvi.NewClient(netProvider.Dynamic(), restCfg)

	router := api.NewRouter(api.RouterDeps{
		Repo:                repo,
		ComputeProvider:     &nopComputeProvider{},
		ClusterProvider:     &nopClusterProvider{},
		NetworkProvider:     netProvider,
		NSProvisioner:       netProvider,
		TenantNSProvisioner: netProvider,
		NATProvisioner:      natProvisioner,
		DNSProvisioner:      dnsProvisioner,
		DNSSearchDomain:     dnsSearchDomain,
		EndpointProvisioner: epProvisioner,
		KVIProvisioner:      kviProvisioner,
		KeyVaultBackendAddr: "openbao.dc-api-vault.svc.cluster.local",
		KeyVaultBackendPort: 8200,
		AuthMiddleware:      composite,
		Log:                 logger,
	})
	srv := httptest.NewServer(router)

	return &TestEnv{
		Server:        srv,
		BaseURL:       srv.URL,
		DB:            repo,
		KubeClient:    dynClient,
		JWT:           jwtMinter,
		NSProvisioner: netProvider,
		pgContainer:   pgc,
	}, nil
}

// newSubEnv builds a lightweight TestEnv that re-uses the shared repo, JWT
// minter, and KubeClient from the package-level env singleton but creates a
// fresh httptest.Server with a custom AuthConfig. Used by the RBAC tests to
// exercise the autoprovision=true and autoprovision=false code paths.
//
// The returned TestEnv has nil pgContainer (DB lifecycle is managed by TestMain).
// Callers must call subEnv.Server.Close() when done (typically via t.Cleanup).
func newSubEnv(t *testing.T, cfg middleware.AuthConfig) *TestEnv {
	t.Helper()
	kubeconfigRaw, err := loadKubeconfig()
	if err != nil {
		t.Fatalf("newSubEnv: load kubeconfig: %v", err)
	}
	netProvider, err := kubeovn.New(base64.StdEncoding.EncodeToString(kubeconfigRaw), "kube-ovn")
	if err != nil {
		t.Fatalf("newSubEnv: create kubeovn provider: %v", err)
	}
	// F15: wire external network so VNet handler triggers SNAT in sub-envs too.
	natProvisioner, err := configureF15(context.Background(), netProvider, env.DB)
	if err != nil {
		t.Fatalf("newSubEnv: configure F15: %v", err)
	}
	// F20: wire DNS provisioner in sub-envs too.
	dnsProvisioner, dnsSearchDomain, err := configureF20(context.Background(), netProvider)
	if err != nil {
		t.Fatalf("newSubEnv: configure F20: %v", err)
	}
	testAuth, err := middleware.NewTestModeAuth(env.JWT.PublicKeyJWKS(), cfg, env.DB)
	if err != nil {
		t.Fatalf("newSubEnv: create test auth: %v", err)
	}
	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, NoColor: true}).With().Timestamp().Logger()

	// M1.5 Chunk 5: composite chain — SA first, then OIDC (TestModeAuth).
	saAuth := middleware.NewServiceAccountAuth(env.DB, logger)
	composite := middleware.NewCompositeAuth(saAuth, testAuth)

	// M3 chunk 2: generic Private Endpoint provisioner — same wiring as main.go,
	// gated on KubeOVN being the network provider.
	var epProvisioner endpoints.Provisioner
	epProvisioner = endpoints.NewKubeOVNProvisioner(netProvider.Dynamic(), endpoints.KubeOVNProvisionerOptions{})

	router := api.NewRouter(api.RouterDeps{
		Repo:                env.DB,
		ComputeProvider:     &nopComputeProvider{},
		ClusterProvider:     &nopClusterProvider{},
		NetworkProvider:     netProvider,
		NSProvisioner:       netProvider,
		TenantNSProvisioner: netProvider,
		NATProvisioner:      natProvisioner,
		DNSProvisioner:      dnsProvisioner,
		DNSSearchDomain:     dnsSearchDomain,
		EndpointProvisioner: epProvisioner,
		KeyVaultBackendAddr: "openbao.dc-api-vault.svc.cluster.local",
		KeyVaultBackendPort: 8200,
		AuthMiddleware:      composite,
		Log:                 logger,
	})
	srv := httptest.NewServer(router)
	t.Cleanup(func() { srv.Close() })
	return &TestEnv{
		Server:        srv,
		BaseURL:       srv.URL,
		DB:            env.DB,
		KubeClient:    env.KubeClient,
		JWT:           env.JWT,
		NSProvisioner: netProvider,
		pgContainer:   nil,
	}
}

// configureF15 wires VPC external network on the kubeovn client and ensures
// the IP pool + bootstrap resources exist. Mirrors cmd/dc-api/main.go's F15
// startup block. Returns nil natProvisioner if env vars aren't set (in which
// case NAT-dependent tests are skipped via f15EnvConfigured()).
func configureF15(ctx context.Context, netProvider *kubeovn.Client, _ *db.Repository) (providers.VPCNATProvisioner, error) {
	bridge := os.Getenv("DCAPI_VPC_EXTERNAL_BRIDGE")
	cidr := os.Getenv("DCAPI_VPC_EXTERNAL_CIDR")
	gateway := os.Getenv("DCAPI_VPC_EXTERNAL_GATEWAY")
	if bridge == "" || cidr == "" || gateway == "" {
		return nil, nil
	}
	vlanID := 0
	if v := os.Getenv("DCAPI_VPC_EXTERNAL_VLAN_ID"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			vlanID = n
		}
	}
	var reserved []string
	if r := os.Getenv("DCAPI_VPC_EXTERNAL_RESERVED_IPS"); r != "" {
		for _, part := range strings.Split(r, ",") {
			if t := strings.TrimSpace(part); t != "" {
				reserved = append(reserved, t)
			}
		}
	}
	netProvider.WithExternalNetwork(kubeovn.ExternalNetworkConfig{
		Bridge:      bridge,
		CIDR:        cidr,
		Gateway:     gateway,
		ReservedIPs: reserved,
		VLANID:      vlanID,
	})
	if err := netProvider.EnsureExternalNetworkBootstrap(ctx); err != nil {
		return nil, fmt.Errorf("F15: bootstrap external network: %w", err)
	}
	return netProvider, nil
}

// configureF20 wires the per-VPC CoreDNS provisioner on the kubeovn client and
// runs the one-time bootstrap (ServiceAccount + ConfigMap). Returns nil
// dnsProvisioner if DCAPI_VPC_DNS_FORWARDERS is not set (DNS tests will skip).
// Mirrors cmd/dc-api/main.go's F20 startup block.
func configureF20(ctx context.Context, netProvider *kubeovn.Client) (providers.VPCDNSProvisioner, string, error) {
	forwarders := os.Getenv("DCAPI_VPC_DNS_FORWARDERS")
	if forwarders == "" {
		return nil, "", nil
	}

	var fwdList []string
	for _, f := range strings.Split(forwarders, ",") {
		if f = strings.TrimSpace(f); f != "" {
			fwdList = append(fwdList, f)
		}
	}

	dnsImage := os.Getenv("DCAPI_VPC_DNS_IMAGE")
	if dnsImage == "" {
		dnsImage = netProvider.AutoDetectCoreDNSImage(ctx)
	}
	searchDomain := os.Getenv("DCAPI_VPC_DNS_SEARCH_DOMAIN")

	netProvider.WithDNSConfig(kubeovn.DNSConfig{
		Forwarders:   fwdList,
		Image:        dnsImage,
		SearchDomain: searchDomain,
	})
	if err := netProvider.EnsureVpcDNSBootstrap(ctx); err != nil {
		return nil, "", fmt.Errorf("F20: bootstrap DNS resources: %w", err)
	}
	return netProvider, searchDomain, nil
}

func preflight(ctx context.Context) error {
	if os.Getenv("KUBECONFIG") == "" {
		return fmt.Errorf("KUBECONFIG not set — export KUBECONFIG=$HOME/.kube/config")
	}
	if os.Getenv("KUBE_CONTEXT") == "" {
		return fmt.Errorf("KUBE_CONTEXT not set — export KUBE_CONTEXT=harvester-dev")
	}
	// Honour DOCKER_HOST if set (covers remote/colima/lima/non-default sockets).
	dockerOK := false
	if dh := os.Getenv("DOCKER_HOST"); dh != "" {
		// strip "unix://" prefix if present
		path := strings.TrimPrefix(dh, "unix://")
		if path != dh {
			if _, err := os.Stat(path); err == nil {
				dockerOK = true
			}
		} else {
			// non-unix DOCKER_HOST (tcp://...) — trust it
			dockerOK = true
		}
	}
	if !dockerOK {
		home, _ := os.UserHomeDir()
		dockerSockets := []string{
			"/var/run/docker.sock",                      // Docker Desktop default
			home + "/.docker/run/docker.sock",           // Docker Desktop user-mode
			home + "/.rd/docker.sock",                   // Rancher Desktop
			home + "/.colima/default/docker.sock",       // Colima default profile
			home + "/.lima/default/sock/docker.sock",    // Lima
		}
		for _, s := range dockerSockets {
			if _, err := os.Stat(s); err == nil {
				dockerOK = true
				// Help testcontainers-go find it.
				if os.Getenv("DOCKER_HOST") == "" {
					_ = os.Setenv("DOCKER_HOST", "unix://"+s)
				}
				break
			}
		}
	}
	if !dockerOK {
		return fmt.Errorf("Docker socket not found — start Docker Desktop / Rancher Desktop / Colima / Lima, or set DOCKER_HOST")
	}
	kubeconfigRaw, err := loadKubeconfig()
	if err != nil {
		return err
	}
	restCfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigRaw)
	if err != nil {
		return fmt.Errorf("parse kubeconfig: %w", err)
	}
	dynClient, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return fmt.Errorf("create dynamic client: %w", err)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	vpcGVR := schema.GroupVersionResource{Group: "kubeovn.io", Version: "v1", Resource: "vpcs"}
	if _, err := dynClient.Resource(vpcGVR).List(probeCtx, metav1.ListOptions{Limit: 1}); err != nil {
		return fmt.Errorf("KubeOVN CRD vpcs.kubeovn.io not found — is KubeOVN installed? (%w)", err)
	}
	return nil
}

func loadKubeconfig() ([]byte, error) {
	path := os.Getenv("KUBECONFIG")
	if path == "" {
		home, _ := os.UserHomeDir()
		path = home + "/.kube/config"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read KUBECONFIG %s: %w", path, err)
	}
	kubeCtx := os.Getenv("KUBE_CONTEXT")
	if kubeCtx == "" {
		return raw, nil
	}
	cfg, err := clientcmd.NewClientConfigFromBytes(raw)
	if err != nil {
		return nil, fmt.Errorf("parse KUBECONFIG: %w", err)
	}
	rawCfg, err := cfg.RawConfig()
	if err != nil {
		return nil, fmt.Errorf("get raw kubeconfig: %w", err)
	}
	rawCfg.CurrentContext = kubeCtx
	return clientcmd.Write(rawCfg)
}

func scrubStaleTestResources(ctx context.Context, client dynamic.Interface) {
	cutoff := time.Now().Add(-time.Hour)

	// Remove KVI instance finalizers on stale test namespaces so they can
	// terminate cleanly. Must run before the namespace delete below.
	forceRemoveKVIFinalizers(ctx, client)

	// Scrub stale Vpc/Subnet objects created by previous crashed runs.
	for _, gvr := range []schema.GroupVersionResource{
		{Group: "kubeovn.io", Version: "v1", Resource: "subnets"},
		{Group: "kubeovn.io", Version: "v1", Resource: "vpcs"},
	} {
		list, err := client.Resource(gvr).List(ctx, metav1.ListOptions{})
		if err != nil {
			continue
		}
		for _, item := range list.Items {
			n := item.GetName()
			ts := item.GetCreationTimestamp()
			if len(n) >= 5 && n[:5] == "test-" && ts.Before(&metav1.Time{Time: cutoff}) {
				_ = client.Resource(gvr).Delete(ctx, n, metav1.DeleteOptions{})
			}
		}
	}

	// Scrub stale tenant namespaces. The kubeovn driver creates a "dc-<tenantID>"
	// namespace per tenant (for NADs); test tenants are prefixed "test-" so the
	// resulting namespace is "dc-test-..." — easy to identify and remove.
	nsGVR := schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	nsList, err := client.Resource(nsGVR).List(ctx, metav1.ListOptions{})
	if err == nil {
		for _, item := range nsList.Items {
			n := item.GetName()
			ts := item.GetCreationTimestamp()
			if strings.HasPrefix(n, "dc-test-") && ts.Before(&metav1.Time{Time: cutoff}) {
				_ = client.Resource(nsGVR).Delete(ctx, n, metav1.DeleteOptions{})
			}
		}
	}
}

// forceRemoveKVIFinalizers strips the keyvault.opencloud.wso2.com/keyvault-cleanup
// finalizer from any KeyVaultInstance CR in every dc-test-* namespace. Called
// before namespace deletion so the operator's async finalizer doesn't block the
// namespace from terminating (which would cause the NEXT run's EnsureProjectNamespace
// to time out with "stuck in Terminating beyond timeout").
//
// Best-effort: errors are ignored per-item.
func forceRemoveKVIFinalizers(ctx context.Context, client dynamic.Interface) {
	kviGVR := schema.GroupVersionResource{
		Group:    "keyvault.opencloud.wso2.com",
		Version:  "v1alpha1",
		Resource: "keyvaultinstances",
	}
	nsGVR := schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	nsList, err := client.Resource(nsGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return
	}
	for _, ns := range nsList.Items {
		nsName := ns.GetName()
		if !strings.HasPrefix(nsName, "dc-test-") {
			continue
		}
		// List all KVI instances in this namespace, regardless of phase.
		kviList, err := client.Resource(kviGVR).Namespace(nsName).List(ctx, metav1.ListOptions{})
		if err != nil {
			continue // CRD may not exist or namespace is already gone
		}
		for _, kvi := range kviList.Items {
			if len(kvi.GetFinalizers()) == 0 {
				continue
			}
			// Strip all finalizers so the Kubernetes GC can proceed.
			patch := kvi.DeepCopy()
			patch.SetFinalizers(nil)
			_, _ = client.Resource(kviGVR).Namespace(nsName).Update(ctx, patch, metav1.UpdateOptions{})
		}
	}
}

// cleanupTestResources deletes all resources this run created on the cluster:
// every dc-test-* namespace and every test-* Vpc/Subnet, regardless of age.
// Run as the suite teardown so consecutive runs don't accumulate state.
//
// Best-effort: errors are ignored, deletion is non-blocking. The next run's
// startup scrub picks up anything KubeOVN finalizers haven't yet cleared.
func cleanupTestResources(ctx context.Context, client dynamic.Interface) {
	// Strip KVI instance finalizers first so the operator's async cleanup
	// doesn't block dc-test-* namespace termination.
	forceRemoveKVIFinalizers(ctx, client)

	for _, gvr := range []schema.GroupVersionResource{
		{Group: "kubeovn.io", Version: "v1", Resource: "subnets"},
		{Group: "kubeovn.io", Version: "v1", Resource: "vpcs"},
	} {
		list, err := client.Resource(gvr).List(ctx, metav1.ListOptions{})
		if err != nil {
			continue
		}
		for _, item := range list.Items {
			if strings.HasPrefix(item.GetName(), "test-") {
				_ = client.Resource(gvr).Delete(ctx, item.GetName(), metav1.DeleteOptions{})
			}
		}
	}

	nsGVR := schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	nsList, err := client.Resource(nsGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return
	}
	for _, item := range nsList.Items {
		if strings.HasPrefix(item.GetName(), "dc-test-") {
			_ = client.Resource(nsGVR).Delete(ctx, item.GetName(), metav1.DeleteOptions{})
		}
	}
}

// ── NOP providers (compute + cluster) so the router can be constructed ───────

type nopComputeProvider struct{}

func (n *nopComputeProvider) Name() string { return "nop" }
func (n *nopComputeProvider) CreateVM(_ context.Context, _, _ string, _ models.VMSpec) (*models.Resource, error) {
	return nil, fmt.Errorf("nop")
}
func (n *nopComputeProvider) GetVM(_ context.Context, _ string) (*models.Resource, error) {
	return nil, fmt.Errorf("nop")
}
func (n *nopComputeProvider) DeleteVM(_ context.Context, _ string) error { return fmt.Errorf("nop") }
func (n *nopComputeProvider) ListVMs(_ context.Context, _, _ string) ([]*models.Resource, error) {
	return nil, fmt.Errorf("nop")
}
func (n *nopComputeProvider) ListNetworks(_ context.Context) ([]*models.Network, error) {
	return nil, fmt.Errorf("nop")
}
func (n *nopComputeProvider) ListImages(_ context.Context) ([]*models.Image, error) {
	return nil, fmt.Errorf("nop")
}
func (n *nopComputeProvider) CreateImage(_ context.Context, _, _ string) (*models.Image, error) {
	return nil, fmt.Errorf("nop")
}

type nopClusterProvider struct{}

func (n *nopClusterProvider) Name() string { return "nop" }
func (n *nopClusterProvider) CreateCluster(_ context.Context, _, _ string, _ models.ClusterSpec) (*models.Resource, error) {
	return nil, fmt.Errorf("nop")
}
func (n *nopClusterProvider) GetCluster(_ context.Context, _ string) (*models.Resource, error) {
	return nil, fmt.Errorf("nop")
}
func (n *nopClusterProvider) DeleteCluster(_ context.Context, _ string) error {
	return fmt.Errorf("nop")
}
func (n *nopClusterProvider) GetKubeconfig(_ context.Context, _ string) (string, error) {
	return "", fmt.Errorf("nop")
}

// AKS-style node-pool methods. The integration twin of the contract-test fix in
// commit 6be7d15: the multi-pool refactor grew the ClusterProvider interface but
// this nop was missed (integration tests aren't in CI). All return the nop error
// — never invoked by the suites that use this provider.
func (n *nopClusterProvider) AddNodePool(_ context.Context, _ string, _ *models.NodePool, _, _, _, _ string) error {
	return fmt.Errorf("nop")
}
func (n *nopClusterProvider) ScaleNodePool(_ context.Context, _, _ string, _ int) error {
	return fmt.Errorf("nop")
}
func (n *nopClusterProvider) UpdateNodePoolTaintsLabels(_ context.Context, _, _ string, _ []models.NodePoolTaint, _ map[string]string) error {
	return fmt.Errorf("nop")
}
func (n *nopClusterProvider) RemoveNodePool(_ context.Context, _, _, _ string) error {
	return fmt.Errorf("nop")
}
func (n *nopClusterProvider) GetNodePoolStatuses(_ context.Context, _ string) (map[string]models.NodePoolStatus, error) {
	return nil, fmt.Errorf("nop")
}

// Compile-time interface checks.
var _ providers.ComputeProvider = &nopComputeProvider{}
var _ providers.ClusterProvider = &nopClusterProvider{}
