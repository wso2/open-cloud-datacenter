// Package api wires together the Chi router, middleware, and handlers.
//
// This file is the "composition root" of DC-API.
// The composition root is the single place where all dependencies are assembled.
// Think of it like the wiring diagram in an electrical schematic:
//   - Components (handlers, middleware) are defined elsewhere.
//   - The router is where the wires connect.
//
// Why keep this separate from main.go?
//
//	main.go handles OS concerns (signals, exit codes, env loading).
//	router.go handles HTTP concerns (routes, middleware order).
//	Testing router.go is easy: call NewRouter() with mock deps, send test requests.
//	You don't need to spin up the actual server process.
package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog"
	dcapi "github.com/wso2/dc-api"
	"github.com/wso2/dc-api/internal/api/auth"
	"github.com/wso2/dc-api/internal/api/handlers"
	"github.com/wso2/dc-api/internal/api/middleware"
	"github.com/wso2/dc-api/internal/db"
	"github.com/wso2/dc-api/internal/directory"
	"github.com/wso2/dc-api/internal/models"
	"github.com/wso2/dc-api/internal/providers"
	"github.com/wso2/dc-api/internal/providers/endpoints"
	"github.com/wso2/dc-api/internal/rbac"
)

// RouterDeps bundles the dependencies needed to build the router.
// This is a form of Dependency Injection at the router level.
// main.go constructs this struct and passes it to NewRouter.
type RouterDeps struct {
	Repo            *db.Repository
	ComputeProvider providers.ComputeProvider
	ClusterProvider providers.ClusterProvider
	NetworkProvider providers.NetworkProvider
	// NATProvisioner is optional (nil if the network provider doesn't support VPC NAT,
	// e.g. in integration tests that don't have the external network configured).
	// When non-nil, the VNet handler uses it to provision SNAT for every new VPC.
	NATProvisioner providers.VPCNATProvisioner
	// DNSProvisioner is optional (nil if F20 DNS is not configured).
	// When non-nil, the subnet handler uses it to provision per-VPC CoreDNS.
	DNSProvisioner providers.VPCDNSProvisioner
	// DNSSearchDomain is the optional search domain injected into VPC VMs (F20).
	// Sourced from DCAPI_VPC_DNS_SEARCH_DOMAIN. Empty = no extra search domain.
	DNSSearchDomain string
	// BastionImage / BastionMgmtNAD configure F10 bastion provisioning.
	// Sourced from DCAPI_BASTION_IMAGE and DCAPI_BASTION_MGMT_NAD.
	BastionImage   string
	BastionMgmtNAD string
	// InfraReservedNADs (F21) is the set of `namespace/nad-name` references
	// the VM handler refuses on the legacy bridge path. Built from
	// DCAPI_INFRA_RESERVED_NADS at startup.
	InfraReservedNADs map[string]bool
	// NSProvisioner provisions the Kubernetes namespace + ResourceQuota for new
	// projects. Nil skips namespace creation (acceptable in tests or when running
	// without a Kubernetes backend).
	NSProvisioner providers.ProjectNamespaceProvisioner
	// TenantNSProvisioner provisions the per-tenant Kubernetes namespace
	// "dc-tenant-<slug>" at admin-tenant-create time. Hosts tenant-tier
	// managed-service Backends (keyvault HA cluster etc.). Nil skips the
	// step — same fallback as NSProvisioner.
	TenantNSProvisioner providers.TenantNamespaceProvisioner
	// KVIProvisioner drives the KVI operator's CRDs (KeyVaultBackend +
	// KeyVaultInstance). Nil means dc-api falls back to the chunk-1+2
	// behaviour (logical CRUD only; no backing OpenBao integration).
	KVIProvisioner providers.KVIProvisioner
	// DatabaseProvisioner drives the dbaas operator's DBInstance CRD
	// (Task 1). Nil means dc-api falls back to DB-only synchronous CRUD —
	// tests + no-K8s deployments.
	DatabaseProvisioner providers.DatabaseProvisioner
	// DBaaSOSImage is the operator-configured Harvester VM image
	// ("namespace/name") database VMs boot from (DCAPI_DBAAS_OS_IMAGE).
	// Empty defers to the controller's own default.
	DBaaSOSImage string
	// EndpointProvisioner is the generic Private Endpoint provisioner used by
	// every M3 managed service (Key Vault today; Postgres / Valkey / Harbor
	// later). Nil disables /v1/<service>/{id}/private-endpoints routes.
	EndpointProvisioner endpoints.Provisioner
	// KeyVaultBackendAddr / Port locate the OpenBao backend the KV Private
	// Endpoint proxy forwards to. When EndpointProvisioner is non-nil these
	// must be set (sourced from DCAPI_KV_BACKEND_ADDR / DCAPI_KV_BACKEND_PORT).
	KeyVaultBackendAddr string
	KeyVaultBackendPort int
	// DirectoryProvider is the optional read-only IdP SCIM2 directory (invite
	// picker + invite-by-email). Nil when the deployment has no DCAPI_IDP_*
	// config: the /directory endpoints then answer 501 and role-assignment
	// creates accept user_sub only (user_email → 422).
	DirectoryProvider directory.Provider
	AuthMiddleware    middleware.AuthValidator
	// AuthService (F7 BFF) is optional. When non-nil, dc-api serves
	// /v1/auth/{login,callback,logout,me} for cloud-ui's session-cookie
	// auth path. When nil, those routes are not registered and the only
	// auth surface is the Bearer-header /v1/* dcctl uses.
	AuthService *auth.Service
	Log         zerolog.Logger
}

// NewRouter creates and returns the fully configured Chi router.
// It wires all middleware, handlers, and routes.
func NewRouter(deps RouterDeps) http.Handler {
	r := chi.NewRouter()

	// ── Global middleware (applied to ALL routes) ─────────────────────────────
	r.Use(chimiddleware.RealIP)
	r.Use(requestLogger(deps.Log))
	r.Use(chimiddleware.Recoverer)
	r.Use(chimiddleware.Timeout(60 * time.Second))

	// Override Chi's default 404 handler so unknown routes return the
	// spec's Error envelope as JSON. Without this Chi writes "404 page
	// not found\n" as text/plain, which violates every operation's
	// documented application/json response.
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte(`{"error":"method not allowed"}`))
	})

	// ── Health check (unauthenticated) ───────────────────────────────────────
	// Kubernetes liveness/readiness probes hit this endpoint.
	// It must NOT require auth — the probe has no JWT.
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// ── Spec serving (unauthenticated) ───────────────────────────────────────
	// /openapi.yaml — the raw embedded spec for tooling (Postman, dcctl
	// codegen via URL, partner SDK generators).
	// /docs        — Redoc HTML page that renders the spec for humans.
	// Both are deliberately public. The spec describes the API surface but
	// doesn't leak secrets, and exposing it is what makes the API
	// self-documenting for any new consumer.
	r.Get("/openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write(dcapi.OpenAPISpec)
	})
	r.Get("/docs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write([]byte(redocHTML))
	})

	// ── F7 BFF auth (unauthenticated, only registered when enabled) ─────────
	// /v1/auth/login + /v1/auth/callback are the OIDC handshake — no
	// session exists yet, so they can't sit behind the JWT middleware.
	// /v1/auth/logout + /v1/auth/me read the session cookie directly
	// (don't need the bearer middleware to inject a context).
	//
	// Mounted at /v1/auth so reverse proxies can route the whole /v1
	// prefix to dc-api without carving out exceptions.
	if deps.AuthService != nil {
		r.Get("/v1/auth/login", deps.AuthService.HandleLogin(deps.Log))
		r.Get("/v1/auth/callback", deps.AuthService.HandleCallback(deps.Log))
		r.Post("/v1/auth/logout", deps.AuthService.HandleLogout(deps.Log))
		r.Get("/v1/auth/me", deps.AuthService.HandleMe(deps.Log))
	}

	// ── dc-agent channel (bearer-token auth, NOT OIDC) ────────────────────────
	// GET /v1/agent/ws is the WebSocket a per-zone dc-agent dials outbound over
	// WSS/443. It authenticates with a "dcagent_" token from agent_tokens — not
	// an Asgardeo JWT — so it is mounted OUTSIDE the /v1 OIDC group (like
	// /healthz). The handler validates the bearer before upgrading.
	// The Registry holds the live agent Sessions. It is shared between the WS
	// handler (which registers a session per connected agent) and the v1 HTTP
	// handlers that Call those agents (e.g. zone inventory).
	agentRegistry := handlers.NewRegistry()
	agentWSHandler := handlers.NewAgentWSHandler(deps.Repo, agentRegistry, deps.Log)
	r.Get("/v1/agent/ws", agentWSHandler.ServeHTTP)

	// ── API v1 (authenticated) ────────────────────────────────────────────────
	// All /v1/* routes require a valid Asgardeo JWT.
	// The auth middleware is applied only inside this route group.
	//
	// Using route groups (r.Route) keeps auth scoped to /v1 only.
	// If we add a public endpoint later (/v1/versions, /v1/catalog),
	// we can do so without touching the auth middleware.
	r.Route("/v1", func(r chi.Router) {
		// Apply auth middleware to the entire /v1 group.
		r.Use(deps.AuthMiddleware.Validate)

		// gate wraps a handler in the RBAC v2 authorization check for `action`.
		// Every tenant/project/resource route below is mounted through it, so
		// enforcement lives in this one declarative map instead of being
		// scattered across the handlers.
		gate := func(action string, h http.HandlerFunc) http.Handler {
			return handlers.Gate(deps.Repo, action, h)
		}

		// Instantiate handlers with their dependencies.
		// Dependency Injection: we pass repo and provider INTO the handler.
		// The handler does not create these — it receives them.
		vmHandler := handlers.NewVMHandler(deps.Repo, deps.ComputeProvider, deps.DNSSearchDomain, deps.InfraReservedNADs, deps.Log)
		bastionHandler := handlers.NewBastionHandler(deps.Repo, deps.ComputeProvider, deps.BastionImage, deps.BastionMgmtNAD, deps.DNSSearchDomain, deps.Log)
		clusterHandler := handlers.NewClusterHandler(deps.Repo, deps.ClusterProvider, deps.Log)

		// M1.5 member management handler. The directory provider (possibly nil)
		// powers invite-by-email resolution.
		roleAssignmentHandler := handlers.NewRoleAssignmentsHandler(deps.Repo, deps.DirectoryProvider, deps.Log)

		// IdP directory proxy (optional SCIM2). The handler itself answers 501
		// when deps.DirectoryProvider is nil, so the routes always register —
		// 501 is the documented feature-detection signal, not a 404.
		directoryHandler := handlers.NewDirectoryHandler(deps.DirectoryProvider, deps.Log)

		// RBAC v2 capability probe — the UI asks "may I do these actions here?"
		// and gets booleans back, so it never re-implements the matcher.
		permissionsHandler := handlers.NewPermissionsHandler(deps.Repo, deps.Log)

		// Project activity feed — read-only, paginated audit-event listing.
		activityHandler := handlers.NewActivityHandler(deps.Repo, deps.Log)

		// M1.5 Chunk 7 — service account management handler.
		serviceAccountHandler := handlers.NewServiceAccountHandler(deps.Repo, deps.Log)

		// M2 network handlers — all share the same NetworkProvider.
		vnetHandler := handlers.NewVNetHandler(deps.Repo, deps.NetworkProvider, deps.NATProvisioner, deps.DNSProvisioner, deps.Log)
		subnetHandler := handlers.NewSubnetHandler(deps.Repo, deps.NetworkProvider, deps.NATProvisioner, deps.DNSProvisioner, deps.Log)
		rtHandler := handlers.NewRouteTableHandler(deps.Repo, deps.NetworkProvider, deps.Log)
		nsgHandler := handlers.NewNSGHandler(deps.Repo, deps.NetworkProvider, deps.Log)
		peeringHandler := handlers.NewPeeringHandler(deps.Repo, deps.NetworkProvider, deps.Log)
		dnsHandler := handlers.NewPrivateDnsZoneHandler(deps.Repo, deps.NetworkProvider, deps.Log)

		// Tenant list — used by cloud-ui's tenant switcher. Lives at the
		// /v1/ level (not under /v1/tenants/{tenant_id}) because it's how
		// the caller discovers which tenants exist for them in the first
		// place — no tenant_id is known yet.
		tenantHandler := handlers.NewTenantHandler(deps.Repo, deps.Log)
		r.Get("/tenants", tenantHandler.List) // GET /v1/tenants

		// RBAC v2 role catalog — the assignable built-in roles (and, later,
		// tenant custom roles). Global and authenticated: any caller may read it
		// to render the role picker and role-detail view. No tenant context —
		// the built-ins are universal.
		roleDefHandler := handlers.NewRoleDefinitionsHandler(deps.Log)
		r.Get("/role-definitions", roleDefHandler.List)      // GET /v1/role-definitions
		r.Get("/role-definitions/{key}", roleDefHandler.Get) // GET /v1/role-definitions/{key}

		// ── Multi-region foundation (phase 0) ──────────────────────────────
		// GET /v1/regions — region/zone health, derived from each zone's
		// dc-agent last_seen. Any authenticated caller (the dashboard reads
		// it); not tenant- or project-scoped. The admin token-mint route is
		// platform-admin-only (enforced inside the handler, like admin/tenants).
		regionsHandler := handlers.NewRegionsHandler(deps.Repo, deps.Log)
		r.Get("/regions", regionsHandler.List) // GET /v1/regions
		r.Post("/admin/regions/{region}/zones/{zone}/agent-token", regionsHandler.MintAgentToken)

		// Admin-only live zone inventory, fetched from the zone's dc-agent over
		// the command channel (shares the agentRegistry the WS handler populates).
		// Node/capacity figures are Harvester-internal — admin-gated in the handler.
		inventoryHandler := handlers.NewInventoryHandler(agentRegistry, deps.Log)
		r.Get("/admin/regions/{region}/zones/{zone}/inventory", inventoryHandler.Get)

		// Admin tenant registry — pre-register empty tenants so they're
		// visible to GET /v1/tenants before any member has logged in.
		// Platform-admin-only (enforced in the handler itself).
		adminTenantHandler := handlers.NewAdminTenantHandler(deps.Repo, deps.TenantNSProvisioner, deps.Log)
		r.Post("/admin/tenants", adminTenantHandler.Create) // POST /v1/admin/tenants

		// PATCH /v1/admin/tenants/{tenant_id} — adjust the tenant capacity cap.
		// Goes through TenantContext so the slug resolves to a tenant_uuid; the
		// handler enforces the platform-admin requirement itself.
		r.Route("/admin/tenants/{tenant_id}", func(r chi.Router) {
			r.Use(middleware.NewTenantContext(deps.Repo).Validate)
			r.Patch("/", adminTenantHandler.PatchCap) // PATCH /v1/admin/tenants/{tenant_id}
		})

		// ── M3 chunk 1+2: Key Vault handler + optional endpoint handler ────
		// Constructed once and reused inside the tenant-scoped route block
		// below.
		kvHandler := handlers.NewKeyVaultHandler(deps.Repo, deps.KVIProvisioner, deps.TenantNSProvisioner, deps.Log)
		var kvEpHandler *handlers.PrivateEndpointHandler
		if deps.EndpointProvisioner != nil {
			kvLookup := &handlers.KeyVaultTargetLookup{Repo: deps.Repo}
			kvResolver := &handlers.KeyVaultBackendResolver{
				Addr: deps.KeyVaultBackendAddr,
				Port: deps.KeyVaultBackendPort,
			}
			kvEpHandler = handlers.NewPrivateEndpointHandler(
				deps.Repo,
				deps.EndpointProvisioner,
				models.PrivateEndpointTargetKeyVault,
				"kv",
				"id",
				kvLookup,
				kvResolver,
				deps.Log,
			)
		}

		// ── Tenant-scoped routes ────────────────────────────────────────────
		// /v1/tenants/{tenant_id}/...
		// TenantContext middleware resolves the URL slug to the immutable
		// tenant_uuid and enforces that the caller has at least Viewer access.
		tenantCtx := middleware.NewTenantContext(deps.Repo)
		projectCtx := middleware.NewProjectContext(deps.Repo)
		projectHandler := handlers.NewProjectHandler(deps.Repo, deps.NSProvisioner, deps.Log)

		r.Route("/tenants/{tenant_id}", func(r chi.Router) {
			r.Use(tenantCtx.Validate)

			// RBAC v2 capability probe at tenant scope. The same handler serves
			// every scope; project/resource routes can mount it unchanged.
			r.Post("/permissions:check", permissionsHandler.Check) // POST /v1/tenants/{tid}/permissions:check

			// ── M2.5 Project management ─────────────────────────────────────
			// POST /projects is tenant-owner-only; GET /projects is any member.
			r.Route("/projects", func(r chi.Router) {
				r.Method(http.MethodPost, "/", gate(rbac.ActionProjectWrite, projectHandler.Create)) // POST /v1/tenants/{tenant_id}/projects
				r.Get("/", projectHandler.List)                                                      // GET  /v1/tenants/{tenant_id}/projects (navigation list; tenant-membership-gated)

				// ── Project-scoped subroutes ────────────────────────────────
				// ProjectContext validates access and injects project_id / project_uuid.
				r.Route("/{project_id}", func(r chi.Router) {
					r.Use(projectCtx.Validate)

					r.Get("/", projectHandler.Get)                                                          // GET    /v1/tenants/{tid}/projects/{pid} (navigation; project-context-gated)
					r.Method(http.MethodPatch, "/", gate(rbac.ActionProjectWrite, projectHandler.Patch))    // PATCH  /v1/tenants/{tid}/projects/{pid}
					r.Method(http.MethodDelete, "/", gate(rbac.ActionProjectDelete, projectHandler.Delete)) // DELETE /v1/tenants/{tid}/projects/{pid}

					// ── M1.5 Chunk 7 — Service accounts (project-scoped) ────
					r.Route("/service-accounts", func(r chi.Router) {
						r.Method(http.MethodPost, "/", gate(rbac.ActionServiceAccountWrite, serviceAccountHandler.Create))           // POST   .../service-accounts
						r.Method(http.MethodGet, "/", gate(rbac.ActionServiceAccountRead, serviceAccountHandler.List))               // GET    .../service-accounts
						r.Method(http.MethodGet, "/{sa_id}", gate(rbac.ActionServiceAccountRead, serviceAccountHandler.Get))         // GET    .../service-accounts/{sa_id}
						r.Method(http.MethodDelete, "/{sa_id}", gate(rbac.ActionServiceAccountDelete, serviceAccountHandler.Delete)) // DELETE .../service-accounts/{sa_id}
					})

					// ── M5: project-scope role assignments (access management) ──
					// Same handler as tenant scope; ProjectContext makes the active
					// scope the project (keyed on project_uuid).
					r.Route("/role-assignments", func(r chi.Router) {
						r.Method(http.MethodPost, "/", gate(rbac.ActionRoleAssignmentWrite, roleAssignmentHandler.Create))                  // POST   .../projects/{project_id}/role-assignments
						r.Method(http.MethodGet, "/", gate(rbac.ActionRoleAssignmentRead, roleAssignmentHandler.List))                      // GET    .../projects/{project_id}/role-assignments
						r.Method(http.MethodDelete, "/{principal_id}", gate(rbac.ActionRoleAssignmentDelete, roleAssignmentHandler.Remove)) // DELETE .../projects/{project_id}/role-assignments/{principal_id}
					})

					// Project-scope capability probe — same handler as tenant scope;
					// ProjectContext makes the active scope the project, so the engine
					// answers "may I do X in THIS project". Self-check, so ungated.
					r.Post("/permissions:check", permissionsHandler.Check) // POST .../projects/{project_id}/permissions:check

					// Project activity feed — newest-first audit events for every
					// resource in the project. Read-only; Reader's */read covers
					// the action, so any project member can see it.
					r.Method(http.MethodGet, "/activity", gate(rbac.ActionActivityRead, activityHandler.List)) // GET .../projects/{project_id}/activity

					// ── Virtual Machines ────────────────────────────────────
					r.Route("/virtual-machines", func(r chi.Router) {
						r.Method(http.MethodPost, "/", gate(rbac.ActionVMWrite, vmHandler.Create)) // POST   .../virtual-machines
						r.Method(http.MethodGet, "/", gate(rbac.ActionVMRead, vmHandler.List))     // GET    .../virtual-machines

						// Per-VM routes — ResourceScope injects {resource, vm uuid} into the
						// scope chain, so a role granted on THIS VM authorizes actions on it
						// (and only it). ProjectContext above still gates the project.
						r.Route("/{id}", func(r chi.Router) {
							r.Use(middleware.ResourceScope("id"))
							r.Method(http.MethodGet, "/", gate(rbac.ActionVMRead, vmHandler.Get))         // GET    .../virtual-machines/{id}
							r.Method(http.MethodDelete, "/", gate(rbac.ActionVMDelete, vmHandler.Delete)) // DELETE .../virtual-machines/{id}

							// M5b: resource-scope role assignments + capability probe.
							r.Route("/role-assignments", func(r chi.Router) {
								r.Method(http.MethodPost, "/", gate(rbac.ActionRoleAssignmentWrite, roleAssignmentHandler.Create))                  // POST   .../{id}/role-assignments
								r.Method(http.MethodGet, "/", gate(rbac.ActionRoleAssignmentRead, roleAssignmentHandler.List))                      // GET    .../{id}/role-assignments
								r.Method(http.MethodDelete, "/{principal_id}", gate(rbac.ActionRoleAssignmentDelete, roleAssignmentHandler.Remove)) // DELETE .../{id}/role-assignments/{principal_id}
							})
							r.Post("/permissions:check", permissionsHandler.Check) // POST .../{id}/permissions:check
						})
					})

					// ── Bastions (F10) ──────────────────────────────────────
					r.Route("/bastions", func(r chi.Router) {
						r.Method(http.MethodPost, "/", gate(rbac.ActionBastionWrite, bastionHandler.Create))        // POST   .../bastions
						r.Method(http.MethodGet, "/", gate(rbac.ActionBastionRead, bastionHandler.List))            // GET    .../bastions
						r.Method(http.MethodGet, "/{id}", gate(rbac.ActionBastionRead, bastionHandler.Get))         // GET    .../bastions/{id}
						r.Method(http.MethodDelete, "/{id}", gate(rbac.ActionBastionDelete, bastionHandler.Delete)) // DELETE .../bastions/{id}
					})

					// ── Clusters ────────────────────────────────────────────
					r.Route("/clusters", func(r chi.Router) {
						r.Method(http.MethodPost, "/", gate(rbac.ActionClusterWrite, clusterHandler.Create)) // POST   .../clusters
						r.Method(http.MethodGet, "/", gate(rbac.ActionClusterRead, clusterHandler.List))     // GET    .../clusters

						r.Route("/{id}", func(r chi.Router) {
							r.Use(middleware.ResourceScope("id"))                                                                         // resource-scope grants authorize actions on this cluster
							r.Method(http.MethodGet, "/", gate(rbac.ActionClusterRead, clusterHandler.Get))                               // GET    .../clusters/{id}
							r.Method(http.MethodDelete, "/", gate(rbac.ActionClusterDelete, clusterHandler.Delete))                       // DELETE .../clusters/{id}
							r.Method(http.MethodGet, "/kubeconfig", gate(rbac.ActionClusterKubeconfigRead, clusterHandler.GetKubeconfig)) // GET    .../clusters/{id}/kubeconfig

							// M5b: resource-scope role assignments + capability probe.
							r.Route("/role-assignments", func(r chi.Router) {
								r.Method(http.MethodPost, "/", gate(rbac.ActionRoleAssignmentWrite, roleAssignmentHandler.Create))
								r.Method(http.MethodGet, "/", gate(rbac.ActionRoleAssignmentRead, roleAssignmentHandler.List))
								r.Method(http.MethodDelete, "/{principal_id}", gate(rbac.ActionRoleAssignmentDelete, roleAssignmentHandler.Remove))
							})
							r.Post("/permissions:check", permissionsHandler.Check)

							// ── AKS-style node pool management (R5) ────────────
							r.Route("/node-pools", func(r chi.Router) {
								r.Method(http.MethodGet, "/", gate(rbac.ActionClusterRead, clusterHandler.ListNodePools)) // GET    .../node-pools
								r.Method(http.MethodPost, "/", gate(rbac.ActionClusterWrite, clusterHandler.AddNodePool)) // POST   .../node-pools

								r.Route("/{pool_name}", func(r chi.Router) {
									r.Method(http.MethodGet, "/", gate(rbac.ActionClusterRead, clusterHandler.GetNodePool))              // GET    .../node-pools/{pool_name}
									r.Method(http.MethodPatch, "/", gate(rbac.ActionClusterWrite, clusterHandler.ScaleOrUpdateNodePool)) // PATCH  .../node-pools/{pool_name}
									r.Method(http.MethodDelete, "/", gate(rbac.ActionClusterWrite, clusterHandler.RemoveNodePool))       // DELETE .../node-pools/{pool_name}
								})
							})
						})
					})

					// ── M2 Networking — VNets ────────────────────────────────
					r.Route("/vnets", func(r chi.Router) {
						r.Method(http.MethodPost, "/", gate(rbac.ActionVNetWrite, vnetHandler.Create)) // POST   .../vnets
						r.Method(http.MethodGet, "/", gate(rbac.ActionVNetRead, vnetHandler.List))     // GET    .../vnets

						r.Route("/{vnet_id}", func(r chi.Router) {
							r.Method(http.MethodGet, "/", gate(rbac.ActionVNetRead, vnetHandler.Get))         // GET    .../vnets/{vnet_id}
							r.Method(http.MethodDelete, "/", gate(rbac.ActionVNetDelete, vnetHandler.Delete)) // DELETE .../vnets/{vnet_id}

							// Subnets
							r.Route("/subnets", func(r chi.Router) {
								r.Method(http.MethodPost, "/", gate(rbac.ActionSubnetWrite, subnetHandler.Create))               // POST   .../subnets
								r.Method(http.MethodGet, "/", gate(rbac.ActionSubnetRead, subnetHandler.List))                   // GET    .../subnets
								r.Method(http.MethodGet, "/{subnet_id}", gate(rbac.ActionSubnetRead, subnetHandler.Get))         // GET    .../subnets/{subnet_id}
								r.Method(http.MethodDelete, "/{subnet_id}", gate(rbac.ActionSubnetDelete, subnetHandler.Delete)) // DELETE .../subnets/{subnet_id}
							})

							// Route Tables
							r.Route("/route-tables", func(r chi.Router) {
								r.Method(http.MethodPost, "/", gate(rbac.ActionRouteTableWrite, rtHandler.Create)) // POST   .../route-tables
								r.Method(http.MethodGet, "/", gate(rbac.ActionRouteTableRead, rtHandler.List))     // GET    .../route-tables

								r.Route("/{rt_id}", func(r chi.Router) {
									r.Method(http.MethodGet, "/", gate(rbac.ActionRouteTableRead, rtHandler.Get))           // GET    .../route-tables/{rt_id}
									r.Method(http.MethodPut, "/", gate(rbac.ActionRouteTableWrite, rtHandler.UpdateRoutes)) // PUT    .../route-tables/{rt_id}
									r.Method(http.MethodDelete, "/", gate(rbac.ActionRouteTableDelete, rtHandler.Delete))   // DELETE .../route-tables/{rt_id}

									r.Method(http.MethodPost, "/associations", gate(rbac.ActionRouteTableWrite, rtHandler.Associate))                 // POST   .../associations
									r.Method(http.MethodDelete, "/associations/{assoc_id}", gate(rbac.ActionRouteTableWrite, rtHandler.Disassociate)) // DELETE .../associations/{assoc_id}
								})
							})

							// VNet Peerings
							r.Route("/peerings", func(r chi.Router) {
								r.Method(http.MethodPost, "/", gate(rbac.ActionPeeringWrite, peeringHandler.Create))                // POST   .../peerings
								r.Method(http.MethodGet, "/", gate(rbac.ActionPeeringRead, peeringHandler.List))                    // GET    .../peerings
								r.Method(http.MethodGet, "/{peering_id}", gate(rbac.ActionPeeringRead, peeringHandler.Get))         // GET    .../peerings/{peering_id}
								r.Method(http.MethodDelete, "/{peering_id}", gate(rbac.ActionPeeringDelete, peeringHandler.Delete)) // DELETE .../peerings/{peering_id}
							})

							// Private DNS Zones
							r.Route("/dns-zones", func(r chi.Router) {
								r.Method(http.MethodPost, "/", gate(rbac.ActionDNSZoneWrite, dnsHandler.CreateZone)) // POST   .../dns-zones
								r.Method(http.MethodGet, "/", gate(rbac.ActionDNSZoneRead, dnsHandler.ListZones))    // GET    .../dns-zones

								r.Route("/{zone_id}", func(r chi.Router) {
									r.Method(http.MethodGet, "/", gate(rbac.ActionDNSZoneRead, dnsHandler.GetZone))         // GET    .../dns-zones/{zone_id}
									r.Method(http.MethodDelete, "/", gate(rbac.ActionDNSZoneDelete, dnsHandler.DeleteZone)) // DELETE .../dns-zones/{zone_id}

									r.Method(http.MethodPost, "/records", gate(rbac.ActionDNSZoneWrite, dnsHandler.UpsertRecord))               // POST   .../records
									r.Method(http.MethodGet, "/records", gate(rbac.ActionDNSZoneRead, dnsHandler.ListRecords))                  // GET    .../records
									r.Method(http.MethodGet, "/records/{record_id}", gate(rbac.ActionDNSZoneRead, dnsHandler.GetRecord))        // GET    .../records/{record_id}
									r.Method(http.MethodPut, "/records/{record_id}", gate(rbac.ActionDNSZoneWrite, dnsHandler.UpdateRecord))    // PUT    .../records/{record_id}
									r.Method(http.MethodDelete, "/records/{record_id}", gate(rbac.ActionDNSZoneWrite, dnsHandler.DeleteRecord)) // DELETE .../records/{record_id}
								})
							})
						})
					})

					// ── M2 Networking — NSGs ─────────────────────────────────
					r.Route("/security-groups", func(r chi.Router) {
						r.Method(http.MethodPost, "/", gate(rbac.ActionNSGWrite, nsgHandler.Create)) // POST   .../security-groups
						r.Method(http.MethodGet, "/", gate(rbac.ActionNSGRead, nsgHandler.List))     // GET    .../security-groups

						r.Route("/{sg_id}", func(r chi.Router) {
							r.Method(http.MethodGet, "/", gate(rbac.ActionNSGRead, nsgHandler.Get))         // GET    .../security-groups/{sg_id}
							r.Method(http.MethodDelete, "/", gate(rbac.ActionNSGDelete, nsgHandler.Delete)) // DELETE .../security-groups/{sg_id}

							r.Method(http.MethodPut, "/rules", gate(rbac.ActionNSGWrite, nsgHandler.UpdateRules)) // PUT    .../security-groups/{sg_id}/rules

							r.Method(http.MethodPost, "/attachments", gate(rbac.ActionNSGWrite, nsgHandler.Attach))                   // POST   .../attachments
							r.Method(http.MethodDelete, "/attachments/{attachment_id}", gate(rbac.ActionNSGWrite, nsgHandler.Detach)) // DELETE .../attachments/{attachment_id}
						})
					})

					// ── M3 Key Vaults ───────────────────────────────────────
					r.Route("/keyvaults", func(r chi.Router) {
						r.Use(middleware.ResourceScope("id"))                                                                           // resource-scope grants authorize actions on a vault; no-op for the {id}-less collection routes
						r.Method(http.MethodPost, "/", gate(rbac.ActionVaultWrite, kvHandler.Create))                                   // POST   .../keyvaults
						r.Method(http.MethodGet, "/", gate(rbac.ActionVaultRead, kvHandler.List))                                       // GET    .../keyvaults
						r.Method(http.MethodGet, "/{id}", gate(rbac.ActionVaultRead, kvHandler.Get))                                    // GET    .../keyvaults/{id}
						r.Method(http.MethodDelete, "/{id}", gate(rbac.ActionVaultDelete, kvHandler.Delete))                            // DELETE .../keyvaults/{id}
						r.Method(http.MethodGet, "/{id}/credentials", gate(rbac.ActionVaultCredentialsRead, kvHandler.Credentials))     // GET    .../keyvaults/{id}/credentials (shown-once)
						r.Method(http.MethodPost, "/{id}/credentials/rotate", gate(rbac.ActionVaultWrite, kvHandler.RotateCredentials)) // POST   .../keyvaults/{id}/credentials/rotate (atomic rotate, shown-once)

						// ── M3 chunk 3 — Secret CRUD (proxy to OpenBao) ─────
						// Routes registered only when the KVI operator is wired
						// (kvi != nil). When nil, requests to these paths return
						// the Chi 404 handler (JSON {"error":"not found"}).
						if deps.KVIProvisioner != nil {
							kvSecretsHandler := handlers.NewKeyVaultSecretsHandler(deps.Repo, deps.KVIProvisioner, deps.Log)
							r.Method(http.MethodGet, "/{id}/secrets", gate(rbac.ActionSecretReadMetadata, kvSecretsHandler.ListKeyVaultSecrets))           // GET    .../secrets
							r.Method(http.MethodGet, "/{id}/secrets/{key}", gate(rbac.ActionSecretRead, kvSecretsHandler.GetKeyVaultSecret))               // GET    .../secrets/{key}
							r.Method(http.MethodPut, "/{id}/secrets/{key}", gate(rbac.ActionSecretWrite, kvSecretsHandler.PutKeyVaultSecret))              // PUT    .../secrets/{key}
							r.Method(http.MethodDelete, "/{id}/secrets/{key}", gate(rbac.ActionSecretDelete, kvSecretsHandler.DeleteKeyVaultSecret))       // DELETE .../secrets/{key}
							r.Method(http.MethodPost, "/{id}/secrets/{key}/restore", gate(rbac.ActionSecretWrite, kvSecretsHandler.RestoreKeyVaultSecret)) // POST   .../secrets/{key}/restore
						}

						if kvEpHandler != nil {
							r.Route("/{id}/private-endpoints", func(r chi.Router) {
								r.Method(http.MethodPost, "/", gate(rbac.ActionPrivateEndpointWrite, kvEpHandler.Create))
								r.Method(http.MethodGet, "/", gate(rbac.ActionPrivateEndpointRead, kvEpHandler.List))
								r.Method(http.MethodGet, "/{ep_id}", gate(rbac.ActionPrivateEndpointRead, kvEpHandler.Get))
								r.Method(http.MethodDelete, "/{ep_id}", gate(rbac.ActionPrivateEndpointDelete, kvEpHandler.Delete))
							})
						}

						// M5b: resource-scope role assignments + capability probe.
						r.Route("/{id}/role-assignments", func(r chi.Router) {
							r.Method(http.MethodPost, "/", gate(rbac.ActionRoleAssignmentWrite, roleAssignmentHandler.Create))
							r.Method(http.MethodGet, "/", gate(rbac.ActionRoleAssignmentRead, roleAssignmentHandler.List))
							r.Method(http.MethodDelete, "/{principal_id}", gate(rbac.ActionRoleAssignmentDelete, roleAssignmentHandler.Remove))
						})
						r.Post("/{id}/permissions:check", permissionsHandler.Check)
					})

					// ── Task 1 — DBaaS Databases ────────────────────────────
					dbHandler := handlers.NewDatabaseHandler(deps.Repo, deps.DatabaseProvisioner, deps.DBaaSOSImage, deps.Log)
					r.Route("/databases", func(r chi.Router) {
						r.Use(middleware.ResourceScope("id"))                                                                    // resource-scope grants authorize actions on a database; no-op for the {id}-less collection routes
						r.Method(http.MethodPost, "/", gate(rbac.ActionDBServerWrite, dbHandler.Create))                         // POST   .../databases
						r.Method(http.MethodGet, "/", gate(rbac.ActionDBServerRead, dbHandler.List))                             // GET    .../databases
						r.Method(http.MethodGet, "/{id}", gate(rbac.ActionDBServerRead, dbHandler.Get))                          // GET    .../databases/{id}
						r.Method(http.MethodDelete, "/{id}", gate(rbac.ActionDBServerDelete, dbHandler.Delete))                  // DELETE .../databases/{id}
						r.Method(http.MethodGet, "/{id}/credentials", gate(rbac.ActionDBCredentialsRead, dbHandler.Credentials)) // GET    .../databases/{id}/credentials (shown-once)

						// M5b: resource-scope role assignments + capability probe.
						r.Route("/{id}/role-assignments", func(r chi.Router) {
							r.Method(http.MethodPost, "/", gate(rbac.ActionRoleAssignmentWrite, roleAssignmentHandler.Create))
							r.Method(http.MethodGet, "/", gate(rbac.ActionRoleAssignmentRead, roleAssignmentHandler.List))
							r.Method(http.MethodDelete, "/{principal_id}", gate(rbac.ActionRoleAssignmentDelete, roleAssignmentHandler.Remove))
						})
						r.Post("/{id}/permissions:check", permissionsHandler.Check)
					})
				})
			})

			// ── Tenant-level endpoints (no project scope required) ──────────

			// M1.5 / M5: tenant-scope role assignments (access management).
			r.Route("/role-assignments", func(r chi.Router) {
				r.Method(http.MethodPost, "/", gate(rbac.ActionRoleAssignmentWrite, roleAssignmentHandler.Create))                  // POST   /v1/tenants/{tenant_id}/role-assignments
				r.Method(http.MethodGet, "/", gate(rbac.ActionRoleAssignmentRead, roleAssignmentHandler.List))                      // GET    /v1/tenants/{tenant_id}/role-assignments
				r.Method(http.MethodDelete, "/{principal_id}", gate(rbac.ActionRoleAssignmentDelete, roleAssignmentHandler.Remove)) // DELETE /v1/tenants/{tenant_id}/role-assignments/{principal_id}
			})

			// IdP directory proxy — invite picker (users) + group listing.
			// BOTH gated with roleAssignments/write even though they are GETs:
			// intentional product guardrail — the directory is visible only to
			// principals who can perform invitations (Owner,
			// UserAccessAdministrator). Do NOT introduce a read action here.
			r.Route("/directory", func(r chi.Router) {
				r.Method(http.MethodGet, "/users", gate(rbac.ActionRoleAssignmentWrite, directoryHandler.ListUsers))   // GET /v1/tenants/{tenant_id}/directory/users
				r.Method(http.MethodGet, "/groups", gate(rbac.ActionRoleAssignmentWrite, directoryHandler.ListGroups)) // GET /v1/tenants/{tenant_id}/directory/groups
			})

			// Images and provider networks are tenant-shared catalog (not project
			// resources). Reads are membership-gated — any tenant member, including a
			// project-scoped service account, needs the catalog to provision — so they
			// sit on the router completeness-test allowlist rather than an action gate.
			// The privileged image upload IS action-gated.
			r.Get("/images", vmHandler.ListImages)                                                   // GET  /v1/tenants/{tenant_id}/images
			r.Method(http.MethodPost, "/images", gate(rbac.ActionImageWrite, vmHandler.CreateImage)) // POST /v1/tenants/{tenant_id}/images
			r.Get("/networks", vmHandler.ListNetworks)                                               // GET /v1/tenants/{tenant_id}/networks

			// Capacity cap + current allocation across projects, in one shot.
			// Used by the RegisterProjectDialog to show "X cpu available" inline
			// so the user knows what fits before submitting. Any tenant member
			// can read; mutation is admin-only via PATCH /v1/admin/tenants/{tid}.
			r.Get("/cap-usage", projectHandler.GetTenantCapUsage) // GET /v1/tenants/{tenant_id}/cap-usage (tenant-shared; membership-gated)

			// Resources the caller can reach via a resource-scope grant — lets a
			// resource-only user find and open what's been shared with them.
			// Self-scoped (only the caller's own grants), so no per-action gate.
			r.Get("/shared-resources", tenantHandler.SharedResources) // GET /v1/tenants/{tenant_id}/shared-resources
		})
	})

	return r
}

// requestLogger returns a middleware that logs each request as a structured
// zerolog JSON line, consistent with the rest of DC-API's log output.
//
// Example output:
//
//	{"level":"info","method":"POST","path":"/v1/virtual-machines","status":202,"bytes":312,"duration_ms":47,"remote":"10.0.0.1","time":"..."}
func requestLogger(log zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)

			status := ww.Status()
			log.Info().
				Str("method", r.Method).
				Str("path", r.URL.Path).
				Int("status", status).
				Int("bytes", ww.BytesWritten()).
				Int64("duration_ms", time.Since(start).Milliseconds()).
				Str("remote", r.RemoteAddr).
				Msg("request")
		})
	}
}
