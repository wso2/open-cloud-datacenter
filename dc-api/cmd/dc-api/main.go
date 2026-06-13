// Command dc-api is the DC-API server entry point.
//
// main.go has ONE responsibility: start the program.
//  1. Load configuration from environment.
//  2. Connect to dependencies (PostgreSQL, OIDC, providers).
//  3. Build the router with all dependencies wired in.
//  4. Start the HTTP server.
//  5. Wait for OS signals (Ctrl-C, SIGTERM from Kubernetes) and shut down gracefully.
//
// It does NOT contain business logic. Business logic belongs in handlers and providers.
package main

import (
	"context"
	"encoding/base64"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/wso2/dc-api/internal/api"
	"github.com/wso2/dc-api/internal/api/auth"
	"github.com/wso2/dc-api/internal/api/middleware"
	"github.com/wso2/dc-api/internal/config"
	"github.com/wso2/dc-api/internal/db"
	"github.com/wso2/dc-api/internal/directory"
	"github.com/wso2/dc-api/internal/providers"
	"github.com/wso2/dc-api/internal/providers/common"
	"github.com/wso2/dc-api/internal/providers/dbaas"
	"github.com/wso2/dc-api/internal/providers/endpoints"
	"github.com/wso2/dc-api/internal/providers/harvester"
	"github.com/wso2/dc-api/internal/providers/kubeovn"
	"github.com/wso2/dc-api/internal/providers/kvi"
	"github.com/wso2/dc-api/internal/providers/rancher"
	"github.com/wso2/dc-api/internal/reconciler"
)

func main() {
	// ── Configuration ─────────────────────────────────────────────────────────
	// config.Load() reads all DCAPI_* environment variables.
	// If any required variable is missing, we exit immediately with a clear error.
	cfg, err := config.Load()
	if err != nil {
		l := zerolog.New(os.Stdout).With().Timestamp().Logger()
		l.Fatal().Err(err).Msg("failed to load configuration — check DCAPI_* environment variables")
	}

	// ── Logging ───────────────────────────────────────────────────────────────
	// Apply log level from config before any other log calls.
	level, err := zerolog.ParseLevel(cfg.LogLevel)
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)
	log.Logger = zerolog.New(os.Stdout).With().Timestamp().Logger()
	log.Info().Str("listen", cfg.ListenAddr).Str("log_level", level.String()).Msg("DC-API starting")

	// ── Background context with signal handling ────────────────────────────────
	// We use a context that is cancelled when SIGINT or SIGTERM is received.
	// This is how Kubernetes signals a pod to shut down gracefully.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// ── PostgreSQL connection pool ────────────────────────────────────────────
	// The pool is shared across all requests. pgxpool is safe for concurrent use.
	pool, err := db.Connect(ctx, cfg.DBURL)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to PostgreSQL")
	}
	defer pool.Close()
	log.Info().Msg("connected to PostgreSQL")

	// ── Database migration ────────────────────────────────────────────────────
	// Applies schema.sql if the tables don't exist yet. Safe to run on every
	// startup — it checks first and skips if already applied.
	if err := db.Migrate(ctx, pool); err != nil {
		log.Fatal().Err(err).Msg("database migration failed")
	}

	repo := db.NewRepository(pool)
	// Multi-region phase 0: stamp every resource this control plane creates with
	// its local region (default "lk"). GET /v1/regions and the dc-agent channel
	// build on the same regions/zones catalog.
	repo.SetLocalRegion(cfg.LocalRegion)

	// ── Provider instantiation (Factory Pattern) ──────────────────────────────
	// The factory reads cfg.VMProvider and returns the right implementation.
	// If an unknown provider is configured, we exit here — not on first request.
	computeProvider, err := providers.NewComputeProvider(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialise compute provider")
	}
	log.Info().Str("provider", computeProvider.Name()).Msg("compute provider ready")

	clusterProvider, err := providers.NewClusterProvider(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialise cluster provider")
	}
	log.Info().Str("provider", clusterProvider.Name()).Msg("cluster provider ready")

	// ── F32: wire cloud-provider SA bootstrap into the cluster provisioner ─────
	// Both the harvester client (for SA creation + API info) and the rancher
	// client (for the Steve provisioner) must be ready before we can call
	// WithHarvesterProviders. This is the only place both exist at the same time.
	if rancherClient, ok := clusterProvider.(*rancher.Client); ok {
		if harvesterClient, ok := computeProvider.(*harvester.Client); ok {
			rancherClient.WithHarvesterProviders(harvesterClient, harvesterClient)
			log.Info().Msg("F32: cluster provisioner wired with Harvester SA bootstrap")
		}
	}

	networkProvider, err := providers.NewNetworkProvider(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialise network provider")
	}
	log.Info().Str("provider", networkProvider.Name()).Msg("network provider ready")

	// ── F15 VPC SNAT + F20 Per-VPC DNS bootstrap ────────────────────────────
	//
	// Ordering invariant (F29 fix): ALL bootstrap calls (cheap, idempotent
	// SA/ConfigMap/CRD creation) MUST complete before any backfill loop starts.
	// Backfill loops contain per-VPC waits that can exhaust a shared context
	// budget if a NAT gateway pod is slow to start; if that kills the context,
	// any bootstrap step that runs afterwards sees "context canceled" on its
	// first k8s write and crashes the process. The correct ordering is:
	//
	//   1. EnsureExternalNetworkBootstrap  (F15, cheap, 2 min budget)
	//   2. EnsureVpcDNSBootstrap           (F20, cheap, 1 min budget)
	//   3. runNATBackfill                  (F15, per-VPC loop, 10 min budget)
	//   4. runDNSBackfill                  (F20, per-VPC loop, 10 min budget)
	//
	// Each step uses its OWN context.WithTimeout so a slow VPC in step 3 cannot
	// poison steps 1/2 (which have already finished) or step 4 (which has its
	// own independent budget). The process-root context is NOT consumed here.
	var natProvisioner providers.VPCNATProvisioner
	var dnsProvisioner providers.VPCDNSProvisioner
	if kvClient, ok := networkProvider.(*kubeovn.Client); ok {
		if err := cfg.ValidateF15(); err != nil {
			log.Fatal().Err(err).Msg("F15 VPC external network config is invalid — check DCAPI_VPC_EXTERNAL_* vars")
		}

		kvClient.WithExternalNetwork(kubeovn.ExternalNetworkConfig{
			Bridge:      cfg.VPCExternalBridge,
			CIDR:        cfg.VPCExternalCIDR,
			Gateway:     cfg.VPCExternalGateway,
			ReservedIPs: cfg.ParseReservedIPs(),
			VLANID:      cfg.VPCExternalVLANID,
		})
		natProvisioner = kvClient

		// ── Step 1: F15 bootstrap (2 min budget) ──────────────────────────────
		{
			bsCtx, bsCancel := context.WithTimeout(ctx, 2*time.Minute)
			err := kvClient.EnsureExternalNetworkBootstrap(bsCtx)
			bsCancel()
			if err != nil {
				log.Fatal().Err(err).Msg("failed to bootstrap external network resources (ProviderNetwork/Vlan/Subnet/NAD)")
			}
		}
		log.Info().
			Str("cidr", cfg.VPCExternalCIDR).
			Strs("reserved_ips", cfg.ParseReservedIPs()).
			Msg("kubeovn: F15 external network bootstrap verified")

		// ── Step 2: F20 bootstrap (1 min budget) ──────────────────────────────
		// Bootstrap BEFORE backfill so the SA/ConfigMap are present even if
		// runNATBackfill below is slow or times out on its own context.
		if err := cfg.ValidateF20(); err != nil {
			log.Fatal().Err(err).Msg("F20 per-VPC DNS config is invalid — check DCAPI_VPC_DNS_FORWARDERS")
		}

		dnsImage := cfg.VPCDNSImage
		if dnsImage == "" {
			dnsImage = kvClient.AutoDetectCoreDNSImage(ctx)
		}

		kvClient.WithDNSConfig(kubeovn.DNSConfig{
			Forwarders:   cfg.ParseDNSForwarders(),
			Image:        dnsImage,
			SearchDomain: cfg.VPCDNSSearchDomain,
		})
		dnsProvisioner = kvClient

		{
			bsCtx, bsCancel := context.WithTimeout(ctx, 1*time.Minute)
			err := kvClient.EnsureVpcDNSBootstrap(bsCtx)
			bsCancel()
			if err != nil {
				log.Fatal().Err(err).Msg("failed to bootstrap F20 DNS resources (ServiceAccount/ConfigMap)")
			}
		}
		log.Info().
			Strs("forwarders", cfg.ParseDNSForwarders()).
			Str("image", dnsImage).
			Msg("kubeovn: F20 per-VPC DNS bootstrap verified")

		// ── Step 3: F15 backfill (10 min budget) ──────────────────────────────
		// Per-VPC loop; individual EnsureVpcNAT calls can wait up to ~90s for
		// the NAT gateway pod. Budget must be generous enough for all existing
		// VPCs to be processed. Don't fatal on failure — a transient
		// kube-ovn-controller hiccup must not prevent the API from starting.
		{
			bfCtx, bfCancel := context.WithTimeout(ctx, 10*time.Minute)
			runNATBackfill(bfCtx, repo, kvClient)
			bfCancel()
		}

		// ── Step 4: F20 backfill (10 min budget) ──────────────────────────────
		{
			bfCtx, bfCancel := context.WithTimeout(ctx, 10*time.Minute)
			runDNSBackfill(bfCtx, repo, kvClient)
			bfCancel()
		}
	}

	// ── OIDC Auth Middleware ──────────────────────────────────────────────────
	// This performs OIDC discovery (HTTP call to Asgardeo) at startup.
	// After this, JWKS keys are cached; JWT validation is fast and local.
	//
	// repo is passed so the middleware can run the M1.5 autoprovision flow
	// (insert 'member' role_assignment on first login with a valid tenant group).
	platformAdminSubs := make(map[string]struct{}, len(cfg.PlatformAdminSubs))
	for _, s := range cfg.PlatformAdminSubs {
		if s != "" {
			platformAdminSubs[s] = struct{}{}
		}
	}
	oidcAuth, err := middleware.NewAuth(ctx, cfg.OIDCIssuer, cfg.OIDCAudience, middleware.AuthConfig{
		AdminGroup:        cfg.AdminGroup,
		PlatformAdminSubs: platformAdminSubs,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialise OIDC auth middleware")
	}
	log.Info().
		Str("issuer", cfg.OIDCIssuer).
		Int("platform_admin_subs", len(platformAdminSubs)).
		Msg("OIDC middleware ready")

	// ── Service Account Auth + Composite chain (M1.5 Chunk 5) ───────────────
	// SA auth runs FIRST. If the bearer token starts with "dcapi_sa_" it is
	// validated here (lookup_id index + bcrypt). Otherwise it falls through to
	// OIDC. This keeps both auth paths on exactly the same handler chain with
	// the same context keys and role enforcement.
	saAuth := middleware.NewServiceAccountAuth(repo, log.Logger)

	// ── F7 BFF service (cloud-ui session-cookie auth) ───────────────────────
	// Enabled when DCAPI_BFF_CLIENT_ID is set. When enabled, dc-api serves
	// /v1/auth/{login,callback,logout,me} and the OIDC middleware accepts a
	// dcapi_session cookie as an alternative token source. When disabled,
	// the only auth surface is the Bearer-header /v1/* dcctl already uses.
	var bffSvc *auth.Service
	if cfg.BFFClientID != "" {
		sessionKey, err := base64.StdEncoding.DecodeString(cfg.BFFSessionSecret)
		if err != nil || len(sessionKey) != 32 {
			log.Fatal().Err(err).
				Msg("DCAPI_BFF_SESSION_SECRET must be a base64-encoded 32-byte key (generate: openssl rand -base64 32)")
		}
		bffIssuer := cfg.OIDCIssuer
		bffSvc, err = auth.NewService(ctx, auth.Config{
			Issuer:             bffIssuer,
			ClientID:           cfg.BFFClientID,
			ClientSecret:       cfg.BFFClientSecret,
			RedirectURL:        cfg.BFFRedirectURL,
			PostLoginRedirect:  cfg.BFFPostLoginRedirect,
			PostLogoutRedirect: cfg.BFFPostLogoutRedirect,
			CookieDomain:       cfg.BFFCookieDomain,
			CookieSecure:       cfg.BFFCookieSecure,
			SessionKey:         sessionKey,
			AdminGroup:         cfg.AdminGroup,
			PlatformAdminSubs:  platformAdminSubs,
		})
		if err != nil {
			log.Fatal().Err(err).Msg("failed to initialise BFF auth service")
		}
		// Wire the session cookie into the OIDC middleware so /v1/* requests
		// from cloud-ui (which carry the cookie, not a Bearer header) hit the
		// same JWT verification path dcctl uses.
		oidcAuth = oidcAuth.WithCookieAccessToken(bffSvc.AccessTokenFromCookieReq)
		log.Info().
			Str("client_id", cfg.BFFClientID).
			Str("redirect_url", cfg.BFFRedirectURL).
			Str("post_login_redirect", cfg.BFFPostLoginRedirect).
			Msg("BFF auth service ready — /v1/auth/* endpoints active and session cookies accepted on /v1/*")
	} else {
		log.Info().Msg("BFF auth disabled (DCAPI_BFF_CLIENT_ID unset) — Bearer header only")
	}

	authMiddleware := middleware.NewCompositeAuth(saAuth, oidcAuth)

	// ── Reconciler ───────────────────────────────────────────────────────────
	// Polls PENDING/DELETING resources every 60s and syncs their status from
	// the provider back into PostgreSQL. Runs as a background goroutine and
	// exits cleanly when ctx is cancelled (SIGTERM).
	go reconciler.New(repo, computeProvider, clusterProvider, log.Logger).Run(ctx)

	// ── F21: build infra-reserved-NAD set + sanity-check operator config ─────
	// Build a set from DCAPI_INFRA_RESERVED_NADS, then assert that the
	// operator hasn't pointed the bastion or cluster mgmt NIC at a NAD they
	// just told us is reserved. Catches "added the reserve list, forgot to
	// move bastion off mgmt-br" foot-shoots before the first request.
	reservedNADs := make(map[string]bool, len(cfg.InfraReservedNADs))
	for _, n := range cfg.InfraReservedNADs {
		n = strings.TrimSpace(n)
		if n != "" {
			reservedNADs[n] = true
		}
	}
	if reservedNADs[cfg.BastionMgmtNAD] {
		log.Fatal().Str("nad", cfg.BastionMgmtNAD).
			Msg("DCAPI_BASTION_MGMT_NAD is in DCAPI_INFRA_RESERVED_NADS — bastions would attach to an infra-claimed bridge. Refusing to start.")
	}
	if reservedNADs[cfg.ClusterMgmtNAD] {
		log.Fatal().Str("nad", cfg.ClusterMgmtNAD).
			Msg("DCAPI_CLUSTER_MGMT_NAD is in DCAPI_INFRA_RESERVED_NADS — RKE2 node mgmt NICs would attach to an infra-claimed bridge. Refusing to start.")
	}

	// ── M3 chunk 2: Private Endpoint provisioner ─────────────────────────────
	// One generic provisioner backs every managed-service Private Endpoint
	// (Key Vault today; Postgres / Valkey / Harbor later). Constructed only
	// when KubeOVN is the network provider — without kube-ovn we have no Vip
	// CRD to allocate from and no per-VPC Corefile to patch.
	var endpointProvisioner endpoints.Provisioner
	var nsProvisioner providers.ProjectNamespaceProvisioner
	var tenantNSProvisioner providers.TenantNamespaceProvisioner
	var kviProvisioner providers.KVIProvisioner
	var dbaasProvisioner providers.DatabaseProvisioner
	if kvClient, ok := networkProvider.(*kubeovn.Client); ok {
		endpointProvisioner = endpoints.NewKubeOVNProvisioner(kvClient.Dynamic(), endpoints.KubeOVNProvisionerOptions{
			DNSForwarders: cfg.ParseDNSForwarders(),
		})
		nsProvisioner = kvClient
		tenantNSProvisioner = kvClient
		// KVI provisioner reuses the kubeovn client's dynamic client and
		// REST config — same K8s API server, no point in two connection pools.
		// The REST config is needed for the OpenBao pod-proxy path (secret CRUD).
		kviProvisioner = kvi.NewClient(kvClient.Dynamic(), kvClient.RESTConfig())
		// Task 1 — DBaaS adapter. Only needs the dynamic client; doesn't
		// talk to the dbaas REST gateway, only the DBInstance CRD. Pre-req:
		// dbaas controller + CRD installed on the same K8s API server.
		dbaasProvisioner = dbaas.NewClient(kvClient.Dynamic())
	}

	// ── IdP SCIM2 directory (optional, read-only) ────────────────────────────
	// Fail fast on a half-configured DCAPI_IDP_* set; build the provider only
	// when all four variables are present. A nil provider keeps the feature
	// dark (directory endpoints → 501, invite-by-email → 422).
	if err := cfg.ValidateDirectory(); err != nil {
		log.Fatal().Err(err).Msg("IdP directory config is incomplete — set all DCAPI_IDP_* variables or none")
	}
	var directoryProvider directory.Provider
	if cfg.DirectoryConfigured() {
		directoryProvider = directory.NewSCIM2Client(
			cfg.IDPSCIMBaseURL, cfg.IDPTokenURL, cfg.IDPClientID, cfg.IDPClientSecret,
			cfg.IDPScopes, cfg.IDPUserstoreDomain, nil)
		log.Info().
			Str("scim_base_url", cfg.IDPSCIMBaseURL).
			Str("client_id", cfg.IDPClientID).
			Str("scopes", cfg.IDPScopes).
			Str("userstore_domain", cfg.IDPUserstoreDomain).
			Msg("IdP directory enabled (SCIM2) — invite picker and invite-by-email active")
	} else {
		log.Info().Msg("IdP directory disabled (DCAPI_IDP_* unset) — invite by user_sub only")
	}

	// ── Router ────────────────────────────────────────────────────────────────
	// All wiring happens in NewRouter. main.go does not know about individual routes.
	router := api.NewRouter(api.RouterDeps{
		Repo:                repo,
		ComputeProvider:     computeProvider,
		ClusterProvider:     clusterProvider,
		NetworkProvider:     networkProvider,
		NATProvisioner:      natProvisioner,
		DNSProvisioner:      dnsProvisioner,
		DNSSearchDomain:     cfg.VPCDNSSearchDomain,
		NSProvisioner:       nsProvisioner,
		TenantNSProvisioner: tenantNSProvisioner,
		KVIProvisioner:      kviProvisioner,
		DatabaseProvisioner: dbaasProvisioner,
		DBaaSOSImage:        cfg.DBaaSOSImage,
		BastionImage:        cfg.BastionImage,
		BastionMgmtNAD:      cfg.BastionMgmtNAD,
		InfraReservedNADs:   reservedNADs,
		EndpointProvisioner: endpointProvisioner,
		KeyVaultBackendAddr: cfg.KeyVaultBackendAddr,
		KeyVaultBackendPort: cfg.KeyVaultBackendPort,
		DirectoryProvider:   directoryProvider,
		AuthMiddleware:      authMiddleware,
		AuthService:         bffSvc,
		Log:                 log.Logger,
	})

	// ── HTTP Server ───────────────────────────────────────────────────────────
	// We set explicit timeouts. Without timeouts, a slow client can hold a goroutine
	// open indefinitely, eventually exhausting the server.
	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second, // 60s to allow long-running list calls
		IdleTimeout:  120 * time.Second,
	}

	// Start the server in a goroutine so we can wait for signals below.
	go func() {
		log.Info().Str("addr", cfg.ListenAddr).Msg("HTTP server listening")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("HTTP server failed")
		}
	}()

	// ── Graceful shutdown ─────────────────────────────────────────────────────
	// Wait for Ctrl-C or SIGTERM.
	<-ctx.Done()
	log.Info().Msg("shutdown signal received — draining connections")

	// Give in-flight requests 30 seconds to complete before forcefully closing.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("graceful shutdown timed out")
	}
	log.Info().Msg("DC-API stopped cleanly")
}

// runNATBackfill iterates over all ACTIVE/PENDING VNets and ensures each has
// a working VpcNatGateway + IptablesEIP + IptablesSnatRule. Runs synchronously
// at startup before the HTTP server begins accepting requests.
//
// Error handling: individual VPC failures are logged and skipped so a single
// broken VPC doesn't prevent the API from starting.
func runNATBackfill(ctx context.Context, repo *db.Repository, kvClient *kubeovn.Client) {
	vnets, err := repo.ListAllActiveVNets(ctx)
	if err != nil {
		log.Error().Err(err).Msg("NAT backfill: failed to list VNets — skipping backfill")
		return
	}
	if len(vnets) == 0 {
		log.Info().Msg("NAT backfill: no VNets found — nothing to backfill")
		return
	}
	log.Info().Int("count", len(vnets)).Msg("NAT backfill: checking VNets for missing NAT")

	for _, vnet := range vnets {
		if vnet.BackendUID == "" {
			log.Warn().Str("vnet_id", vnet.ID.String()).Msg("NAT backfill: skipping VNet with no backend_uid (still PENDING?)")
			continue
		}

		present, err := kvClient.IsVpcNATPresent(ctx, vnet.BackendUID)
		if err != nil {
			log.Warn().Err(err).Str("vpc", vnet.BackendUID).Msg("NAT backfill: IsVpcNATPresent check failed — skipping")
			continue
		}
		if present {
			log.Debug().Str("vpc", vnet.BackendUID).Msg("NAT backfill: NAT already present — skipping")
			continue
		}

		log.Info().Str("vpc", vnet.BackendUID).Str("tenant", vnet.TenantID).Msg("NAT backfill: provisioning NAT for VNet")

		// Look up the first subnet for this VPC.
		subnets, err := repo.ListSubnetsByVNetBackfill(ctx, vnet.ID)
		if err != nil || len(subnets) == 0 {
			log.Warn().Err(err).Str("vpc", vnet.BackendUID).Msg("NAT backfill: no subnet found — cannot provision NAT, will retry on next restart")
			continue
		}
		subnet := subnets[0]
		subnetName := subnet.BackendUID
		if subnetName == "" {
			subnetName = subnet.Name
		}

		lanIP, err := kubeovn.ComputeLanIP(subnet.CIDR)
		if err != nil {
			log.Warn().Err(err).Str("vpc", vnet.BackendUID).Str("cidr", subnet.CIDR).Msg("NAT backfill: ComputeLanIP failed — skipping")
			continue
		}

		assignedEIP, err := kvClient.EnsureVpcNAT(ctx, vnet.BackendUID, subnet.CIDR, subnetName, lanIP)
		if err != nil {
			log.Error().Err(err).Str("vpc", vnet.BackendUID).Msg("NAT backfill: EnsureVpcNAT failed — will retry on next restart")
			continue
		}
		if err := repo.SetVNetOutboundIP(ctx, vnet.ID, assignedEIP); err != nil {
			log.Warn().Err(err).Str("vpc", vnet.BackendUID).Stringer("eip", assignedEIP).Msg("NAT backfill: failed to cache outbound_ip — NAT is up but the IP isn't visible on the VNet row")
		}

		log.Info().
			Str("vpc", vnet.BackendUID).
			Stringer("eip", assignedEIP).
			Msg("NAT backfill: VPC NAT provisioned successfully")
	}
	log.Info().Msg("NAT backfill complete")
}

// runDNSBackfill iterates over all ACTIVE VNets and ensures each has a running
// CoreDNS Deployment (F20). Mirrors runNATBackfill exactly.
//
// Error handling: individual VPC failures are logged and skipped.
func runDNSBackfill(ctx context.Context, repo *db.Repository, kvClient *kubeovn.Client) {
	vnets, err := repo.ListAllActiveVNets(ctx)
	if err != nil {
		log.Error().Err(err).Msg("DNS backfill: failed to list VNets — skipping backfill")
		return
	}
	if len(vnets) == 0 {
		log.Info().Msg("DNS backfill: no VNets found — nothing to backfill")
		return
	}
	log.Info().Int("count", len(vnets)).Msg("DNS backfill: checking VNets for missing CoreDNS")

	for _, vnet := range vnets {
		if vnet.BackendUID == "" {
			log.Warn().Str("vnet_id", vnet.ID.String()).Msg("DNS backfill: skipping VNet with no backend_uid (still PENDING?)")
			continue
		}

		present, err := kvClient.IsVpcDNSPresent(ctx, vnet.BackendUID)
		if err != nil {
			log.Warn().Err(err).Str("vpc", vnet.BackendUID).Msg("DNS backfill: IsVpcDNSPresent check failed — skipping")
			continue
		}
		if present {
			log.Debug().Str("vpc", vnet.BackendUID).Msg("DNS backfill: CoreDNS already present — skipping")
			continue
		}

		log.Info().Str("vpc", vnet.BackendUID).Str("tenant", vnet.TenantID).Msg("DNS backfill: provisioning CoreDNS for VNet")

		// Look up the first subnet for this VPC.
		subnets, err := repo.ListSubnetsByVNetBackfill(ctx, vnet.ID)
		if err != nil || len(subnets) == 0 {
			log.Warn().Err(err).Str("vpc", vnet.BackendUID).Msg("DNS backfill: no subnet found — cannot provision DNS, will retry on next restart")
			continue
		}
		subnet := subnets[0]
		subnetBackendUID := subnet.BackendUID
		if subnetBackendUID == "" {
			subnetBackendUID = subnet.Name
		}

		// Use the project namespace if available; fall back to the tenant-only
		// namespace for VNets created pre-M2.5 (where ProjectID is empty).
		tenantNS := "dc-" + vnet.TenantID
		if vnet.ProjectID != "" {
			tenantNS = common.NamespaceForProject(vnet.TenantID, vnet.ProjectID)
		}
		dnsIP, err := kvClient.EnsureVpcDNS(ctx, vnet.BackendUID, subnet.CIDR, subnetBackendUID, tenantNS)
		if err != nil {
			log.Error().Err(err).Str("vpc", vnet.BackendUID).Msg("DNS backfill: EnsureVpcDNS failed — will retry on next restart")
			continue
		}
		if err := repo.SetVNetDNSServerIP(ctx, vnet.ID, dnsIP); err != nil {
			log.Warn().Err(err).Str("vpc", vnet.BackendUID).Stringer("dns_ip", dnsIP).Msg("DNS backfill: failed to cache dns_server_ip — CoreDNS is up but IP isn't visible on the VNet row")
		}

		log.Info().
			Str("vpc", vnet.BackendUID).
			Stringer("dns_ip", dnsIP).
			Msg("DNS backfill: VPC CoreDNS provisioned successfully")
	}
	log.Info().Msg("DNS backfill complete")
}
