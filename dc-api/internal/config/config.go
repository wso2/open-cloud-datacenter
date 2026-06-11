// Package config loads DC-API configuration from environment variables.
//
// We follow the 12-Factor App principle: configuration comes from the
// environment, not from files checked into the repo. This makes the binary
// portable across dev, staging, and prod without recompilation.
//
// Usage:
//
//	cfg, err := config.Load()
//	if err != nil { log.Fatal(err) }
package config

import (
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/kelseyhightower/envconfig"
)

// Config holds all runtime configuration for DC-API.
// Fields are populated from environment variables with the "DCAPI_" prefix.
// Example: DCAPI_DB_URL, DCAPI_LISTEN_ADDR, etc.
//
// The `required:"true"` tag causes envconfig to return an error at startup
// if the variable is missing — fail fast, never silently.
type Config struct {
	// ── HTTP server ───────────────────────────────────────────────────────────
	ListenAddr string `envconfig:"LISTEN_ADDR" default:":8080"`

	// ── PostgreSQL ────────────────────────────────────────────────────────────
	// Full DSN: postgres://user:pass@host:5432/dbname?sslmode=disable
	DBURL string `envconfig:"DB_URL" required:"true"`

	// ── OIDC / Auth ───────────────────────────────────────────────────────────
	// OIDCIssuer is the identity provider's issuer URL.
	// DC-API is provider-agnostic — this works with any OIDC-compliant IdP.
	//
	//   Asgardeo: https://api.asgardeo.io/t/<your-org>
	//   Keycloak: https://keycloak.example.com/realms/<realm>
	//   Okta:     https://<domain>.okta.com/oauth2/default
	//
	// go-oidc discovers all endpoints automatically from /.well-known/openid-configuration.
	OIDCIssuer string `envconfig:"OIDC_ISSUER" required:"true"`

	// OIDCAudience is the set of accepted "aud" claims in incoming JWTs.
	// Set this to a comma-separated list of every IdP client whose tokens
	// should be honoured by DC-API: the dcctl CLI client, the cloud-ui SPA
	// client, the future terraform-provider client, etc. A token is accepted
	// when at least one of its audience values matches one of these.
	//
	// Single-value backwards compatibility: a plain "client-id" with no
	// commas still works and behaves like a one-element list.
	OIDCAudience []string `envconfig:"OIDC_AUDIENCE" required:"true"`

	// TenantGroupPrefix: groups matching "<prefix><name>" map to tenant "<name>".
	// Override if your IdP uses a different group naming convention.
	TenantGroupPrefix string `envconfig:"TENANT_GROUP_PREFIX" default:"dc-tenant-"`

	// AdminGroup: members of this Asgardeo group get the "admin" role in
	// DC-API. Legacy mechanism — preferred way (Option D) is to list the
	// Asgardeo subs of platform admins in PlatformAdminSubs. AdminGroup
	// stays as a fallback during the transition; either path promotes.
	AdminGroup string `envconfig:"ADMIN_GROUP" default:"dc-admin"`

	// PlatformAdminSubs is a comma-separated list of Asgardeo `sub` values
	// for users who should bypass per-tenant RBAC. This is the Option D
	// preferred path because it decouples platform admin from any IdP-side
	// group machinery — useful when an operator wants admin status to
	// survive an IdP migration or to add an admin without console access.
	// Either this list OR membership in AdminGroup promotes a user.
	// Empty = no env-driven admins (rely solely on AdminGroup).
	PlatformAdminSubs []string `envconfig:"PLATFORM_ADMIN_SUBS"`

	// RBACAutoProvision: when true, the first time a user with a valid
	// dc-tenant-<x> group is seen, DC-API auto-inserts a 'member'
	// role_assignment row. Option D treats autoprovision as legacy — the
	// new default is `false`, which means new members must be explicitly
	// invited via POST /v1/tenants/{tid}/members. Set to true to restore
	// the M1.5 self-onboarding behaviour for environments that prefer it.
	RBACAutoProvision bool `envconfig:"RBAC_AUTOPROVISION" default:"false"`

	// ── Harvester ─────────────────────────────────────────────────────────────
	// HarvesterKubeconfig: base64-encoded kubeconfig for the Harvester cluster.
	// Generate with: base64 < ~/.kube/harvester.yaml
	HarvesterKubeconfig string `envconfig:"HARVESTER_KUBECONFIG" required:"true"`
	HarvesterNamespace  string `envconfig:"HARVESTER_NAMESPACE"  default:"default"`

	// ── Rancher ───────────────────────────────────────────────────────────────
	RancherURL   string `envconfig:"RANCHER_URL"   required:"true"`
	RancherToken string `envconfig:"RANCHER_TOKEN" required:"true"`
	// RancherInsecure: skip TLS verification. ONLY for dev (self-signed certs).
	RancherInsecure bool `envconfig:"RANCHER_INSECURE" default:"false"`

	// RancherHarvesterCredential is the name of the cloud credential secret
	// that already exists in Rancher for the Harvester cluster.
	// Create it once in Rancher UI: Cluster Management → Cloud Credentials → Create → Harvester.
	// The value is the secret name shown in the Rancher UI (e.g., "cattle-global-data:cc-xxxxx").
	RancherHarvesterCredential string `envconfig:"RANCHER_HARVESTER_CREDENTIAL" required:"true"`

	// ── Operator access (IaaS team break-glass) ──────────────────────────────
	// If set, every cluster node VM gets these credentials injected via cloud-init.
	// Stored in the dc-api Kubernetes Secret — never in ConfigMap or source control.
	// OperatorSSHKey: IaaS team's public key (~/.ssh/id_ed25519.pub format)
	// OperatorPassword: recovery console password for when SSH is unavailable
	OperatorSSHKey   string `envconfig:"OPERATOR_SSH_KEY"   default:""`
	OperatorPassword string `envconfig:"OPERATOR_PASSWORD"  default:""`

	// ── F32 cluster provisioning (RKE2-on-VPC) ───────────────────────────────
	// ClusterMgmtNAD is the Multus NetworkAttachmentDefinition used for the
	// management (outbound internet) NIC on every cluster node. Format:
	// "namespace/name", e.g. "iaas/vm-network-001".
	// Required for VPC-path cluster creates (vnet_id + subnet_id in the request).
	// Optional for legacy bridge-mode clusters (network_name in the request).
	ClusterMgmtNAD string `envconfig:"CLUSTER_MGMT_NAD" default:"iaas/vm-network-001"`

	// ClusterVMNamespace is the fallback Harvester namespace for cluster node VMs
	// when no tenantID-derived namespace is available. On the VPC path the
	// namespace is always "dc-<tenantID>" derived from the JWT — this value is
	// only used as a default for the provisioner's non-VPC path.
	ClusterVMNamespace string `envconfig:"CLUSTER_VM_NAMESPACE" default:"default"`

	// ── F10 bastion provisioning ─────────────────────────────────────────────
	// BastionImage is the VM image (`namespace/resource-name`) used for all
	// bastion hosts. Typically a small Ubuntu cloud image.
	BastionImage string `envconfig:"BASTION_IMAGE" default:"rancher-infra/ubuntu-22-04"`
	// BastionMgmtNAD is the Multus NAD reference for the bastion's
	// operator-reachable NIC. Same VLAN used for cluster nodes; reachable
	// from workstations via VPN.
	BastionMgmtNAD string `envconfig:"BASTION_MGMT_NAD" default:"iaas/vm-network-001"`

	// ── Task 1 DBaaS ─────────────────────────────────────────────────────────
	// DBaaSOSImage is the Harvester VirtualMachineImage (`namespace/resource-name`)
	// the dbaas controller boots database VMs from. dc-api stamps this into every
	// DBInstance CR's spec.osImage. Empty leaves the controller's own default,
	// which may not match a given cluster's image catalog. Set per-environment.
	DBaaSOSImage string `envconfig:"DBAAS_OS_IMAGE" default:"rancher-infra/ubuntu-22-04"`

	// ── F21 infra-reserved NADs ─────────────────────────────────────────────
	// Comma-separated list of `namespace/nad-name` references that tenant
	// VMs MUST NOT attach to (the legacy `network_name` path). These are
	// bridges claimed by KubeOVN's ProviderNetwork for VPC SNAT or otherwise
	// reserved for cluster infrastructure — attaching a tenant VM to one
	// triggers an OVS flow reconverge that disrupted Harvester/Rancher
	// VIPs in the 2026-05-11 outage. Default protects the known-bad NAD
	// (dc-api/dc-api-mgmt); operators extend via env var when they claim
	// additional bridges.
	InfraReservedNADs []string `envconfig:"INFRA_RESERVED_NADS" default:"dc-api/dc-api-mgmt"`

	// ── Provider selection ────────────────────────────────────────────────────
	VMProvider      string `envconfig:"VM_PROVIDER"      default:"harvester"`
	ClusterProvider string `envconfig:"CLUSTER_PROVIDER" default:"rancher"`
	// NetworkProvider selects the SDN backend. Only "kubeovn" is supported in M2.
	// The kubeovn driver uses the same DCAPI_HARVESTER_KUBECONFIG credential
	// since KubeOVN runs on the same Harvester cluster.
	NetworkProvider string `envconfig:"NETWORK_PROVIDER" default:"kubeovn"`

	// KubeOVNNamespace is the Kubernetes namespace where KubeOVN's daemon pods
	// live.  Used as a reference for NAD provider sync annotations.  The daemon
	// namespace is NOT where tenant CRDs are created — those go in "dc-<tenantID>".
	// Default matches the standard KubeOVN Helm install.
	KubeOVNNamespace string `envconfig:"KUBEOVN_NAMESPACE" default:"kube-ovn"`

	// ── F15 VPC external network / SNAT pool ─────────────────────────────────
	// These six variables define the external (physical) network that VpcNatGateway
	// pods use to SNAT outbound traffic from tenant VPCs to the internet.
	// All six are required when the network provider is "kubeovn".
	//
	// VPCExternalBridge: the Linux bridge on Harvester hosts that is connected to
	//   the upstream router (matches the Harvester host NIC / bond name, e.g. "mgmt-br").
	// VPCExternalCIDR: CIDR of the external network (e.g. "192.168.10.0/24").
	// VPCExternalGateway: the real upstream gateway IP (e.g. "192.168.10.254").
	// VPCExternalReservedIPs: comma-separated IPs that KubeOVN must NOT allocate
	//   (host nodes, ingress LB, anything already pinned on the external network).
	//   Optional — empty means KubeOVN can allocate any IP in the CIDR except .0,
	//   the gateway, and the broadcast.
	// VPCExternalVLANID: VLAN tag for the external network (0 = untagged).
	VPCExternalBridge      string `envconfig:"VPC_EXTERNAL_BRIDGE"        required:"true"`
	VPCExternalCIDR        string `envconfig:"VPC_EXTERNAL_CIDR"          required:"true"`
	VPCExternalGateway     string `envconfig:"VPC_EXTERNAL_GATEWAY"       required:"true"`
	VPCExternalReservedIPs string `envconfig:"VPC_EXTERNAL_RESERVED_IPS"  default:""`
	VPCExternalVLANID      int    `envconfig:"VPC_EXTERNAL_VLAN_ID"       default:"0"`

	// ── F20 Per-VPC CoreDNS ───────────────────────────────────────────────────
	// VPCDNSForwarders: comma-separated upstream DNS servers used in the
	//   per-VPC CoreDNS Corefile "forward ." directive.
	// VPCDNSImage: the CoreDNS container image to run. If empty at startup,
	//   dc-api auto-detects the image from the cluster's existing CoreDNS
	//   deployment with a fallback to "coredns/coredns:1.11.3".
	// VPCDNSSearchDomain: optional DNS search domain injected into every VPC VM
	//   (e.g. "lk.dc.internal"). Empty means no extra search domain.
	VPCDNSForwarders    string `envconfig:"VPC_DNS_FORWARDERS"     default:"1.1.1.1,8.8.8.8"`
	VPCDNSImage         string `envconfig:"VPC_DNS_IMAGE"          default:""`
	VPCDNSSearchDomain  string `envconfig:"VPC_DNS_SEARCH_DOMAIN"  default:""`

	// ── F7 BFF (Backend-for-Frontend) — cloud-ui Asgardeo session ────────────
	// Set BFFClientID to a non-empty value to enable. When enabled, dc-api
	// serves /v1/auth/{login,callback,logout,me} and accepts a session
	// cookie (dcapi_session) in addition to Bearer headers on /v1/*.
	//
	// BFFClientID + ClientSecret are a NEW confidential Asgardeo app
	// (distinct from the public cloud-ui SPA client). Get them from
	// 03-asgardeo-auth's TF outputs once the new resource lands.
	BFFClientID     string `envconfig:"BFF_CLIENT_ID"     default:""`
	BFFClientSecret string `envconfig:"BFF_CLIENT_SECRET" default:""`
	// BFFRedirectURL must match a callback_urls entry on the BFF app.
	// Example: https://dcapi.lk-dev.internal.wso2.com/v1/auth/callback
	BFFRedirectURL string `envconfig:"BFF_REDIRECT_URL" default:""`
	// BFFPostLoginRedirect is the cloud-ui origin (where the browser
	// lands after callback). Example: https://cloud.lk-dev.internal.wso2.com
	BFFPostLoginRedirect string `envconfig:"BFF_POST_LOGIN_REDIRECT" default:""`
	// BFFPostLogoutRedirect is where Asgardeo's end_session bounces the
	// browser back to. Usually the same as PostLoginRedirect.
	BFFPostLogoutRedirect string `envconfig:"BFF_POST_LOGOUT_REDIRECT" default:""`
	// BFFCookieDomain controls the Domain attribute on dcapi_session.
	// Empty = host-only cookie (safer; only works when dc-api and
	// cloud-ui share the exact hostname). For prod, set to the parent
	// domain both share (e.g. .lk-dev.internal.wso2.com).
	BFFCookieDomain string `envconfig:"BFF_COOKIE_DOMAIN" default:""`
	// BFFCookieSecure flips the Secure attribute on session+state
	// cookies. Default true; flip to false ONLY for local http://
	// dev where the browser refuses Secure-only cookies.
	BFFCookieSecure bool `envconfig:"BFF_COOKIE_SECURE" default:"true"`
	// BFFSessionSecret is a base64-encoded 32-byte AES-256 key. Generate
	// once: `openssl rand -base64 32`. Required when BFFClientID is set;
	// otherwise the BFF refuses to start (fail fast over silently
	// generating an ephemeral key that invalidates every session on
	// pod restart).
	BFFSessionSecret string `envconfig:"BFF_SESSION_SECRET" default:""`

	// ── M3 Key Vault backend (chunk 2) ────────────────────────────────────────
	// KeyVaultBackendAddr / Port locate the OpenBao Service that every Key
	// Vault Private Endpoint's nginx proxy forwards to. The address is the
	// in-cluster DNS name resolvable from Harvester pods (eth0 side of the
	// proxy). Defaults match the M3 chunk 2 "Option C" layout (OpenBao runs
	// as a Harvester pod in dc-api-vault); chunk-4 will flip to "Option B"
	// (dedicated service-keyvault RKE2 cluster) by updating these values.
	KeyVaultBackendAddr string `envconfig:"KV_BACKEND_ADDR" default:"openbao.dc-api-vault.svc.cluster.local"`
	KeyVaultBackendPort int    `envconfig:"KV_BACKEND_PORT" default:"8200"`

	// ── IdP SCIM2 directory (optional, read-only) ─────────────────────────────
	// Optional read-only SCIM2 directory integration: powers the invite picker
	// (GET /v1/tenants/{tid}/directory/{users,groups}) and invite-by-email
	// (user_email on POST .../role-assignments). When ALL FOUR are unset the
	// feature is dark — the directory endpoints answer 501 and invites work by
	// user_sub only. Setting SOME but not all four fails startup (see
	// ValidateDirectory) so a half-configured deployment is caught early.
	//
	// The OAuth2 client_credentials credential MUST hold user/group VIEW
	// scopes only — dc-api never writes to the IdP and a leaked read-only
	// credential cannot modify identities.
	//
	// Asgardeo examples:
	//   IDP_SCIM_BASE_URL = https://api.asgardeo.io/t/{org}/scim2
	//   IDP_TOKEN_URL     = https://api.asgardeo.io/t/{org}/oauth2/token
	IDPSCIMBaseURL  string `envconfig:"IDP_SCIM_BASE_URL" default:""`
	IDPTokenURL     string `envconfig:"IDP_TOKEN_URL"     default:""`
	IDPClientID     string `envconfig:"IDP_CLIENT_ID"     default:""`
	IDPClientSecret string `envconfig:"IDP_CLIENT_SECRET" default:""`

	// IDPScopes are the OAuth2 scopes requested with the client_credentials
	// grant when fetching the SCIM access token. Space- or comma-separated.
	//
	// Required by Asgardeo and WSO2 Identity Server: they only attach SCIM
	// permissions to a client_credentials token when the scopes are explicitly
	// requested. Without them every SCIM call returns 403 against a real org.
	// The Asgardeo read-only set is:
	//
	//   IDP_SCOPES = "internal_user_mgt_list internal_user_mgt_view internal_group_mgt_view"
	//
	// Optional: some IdPs grant SCIM permissions to the M2M app directly and
	// need no scopes, so this is NOT part of the all-or-nothing
	// ValidateDirectory check — it only matters when the other four are set.
	// Grant LIST/VIEW scopes only; dc-api never writes to the IdP.
	IDPScopes string `envconfig:"IDP_SCOPES" default:""`

	// IDPUserstoreDomain restricts directory reads to one userstore via the
	// WSO2 SCIM2 `domain` query parameter. Without it the IdP returns every
	// account in the organization — including console administrators and
	// collaborators, who are not invite candidates. The default "DEFAULT" is
	// Asgardeo's consumer-user store; on-prem WSO2 Identity Server typically
	// wants "PRIMARY". Set explicitly empty to list all stores (non-WSO2
	// SCIM servers ignore the parameter either way). Not part of the
	// all-or-nothing ValidateDirectory check.
	IDPUserstoreDomain string `envconfig:"IDP_USERSTORE_DOMAIN" default:"DEFAULT"`

	// ── Logging ───────────────────────────────────────────────────────────────
	LogLevel string `envconfig:"LOG_LEVEL" default:"info"`

	// ── Test mode (integration tests only — NEVER set in production) ──────────
	// TestMode enables a secondary JWT verifier that accepts tokens signed with
	// a per-test-run RSA key generated in memory. The production Asgardeo path
	// is unchanged. Never set this true in production — it defaults to false
	// and the test verifier is unreachable when false.
	TestMode         bool   `envconfig:"TEST_MODE" default:"false"`
	TestModeJWKSJSON string `envconfig:"TEST_MODE_JWKS" default:""`
}

// Load reads environment variables and returns a validated Config.
// Returns an error immediately if any required variable is missing.
func Load() (*Config, error) {
	var cfg Config
	if err := envconfig.Process("DCAPI", &cfg); err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return &cfg, nil
}

// DirectoryConfigured reports whether the optional IdP SCIM2 directory is
// fully configured (all four DCAPI_IDP_* variables set).
func (c *Config) DirectoryConfigured() bool {
	return c.IDPSCIMBaseURL != "" && c.IDPTokenURL != "" &&
		c.IDPClientID != "" && c.IDPClientSecret != ""
}

// ValidateDirectory fails fast on a half-configured IdP directory: either all
// four required DCAPI_IDP_* variables (SCIM_BASE_URL, TOKEN_URL, CLIENT_ID,
// CLIENT_SECRET) are set (feature on) or none are (feature dark). Anything in
// between is an operator mistake — e.g. the secret didn't make it into the
// deployment — and silently running with the feature dark would hide it until
// the first invite-by-email fails.
//
// DCAPI_IDP_SCOPES is intentionally excluded from this check: it is optional
// (some IdPs need no scopes) and only meaningful once the other four are set.
func (c *Config) ValidateDirectory() error {
	vars := map[string]string{
		"DCAPI_IDP_SCIM_BASE_URL": c.IDPSCIMBaseURL,
		"DCAPI_IDP_TOKEN_URL":     c.IDPTokenURL,
		"DCAPI_IDP_CLIENT_ID":     c.IDPClientID,
		"DCAPI_IDP_CLIENT_SECRET": c.IDPClientSecret,
	}
	var missing []string
	set := 0
	for name, v := range vars {
		if v == "" {
			missing = append(missing, name)
		} else {
			set++
		}
	}
	if set == 0 || set == len(vars) {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf(
		"IdP directory config is incomplete: %s not set — set all four required DCAPI_IDP_* variables (SCIM_BASE_URL, TOKEN_URL, CLIENT_ID, CLIENT_SECRET) to enable the directory, or none to disable it (DCAPI_IDP_SCOPES is optional)",
		strings.Join(missing, ", "))
}

// ValidateF20 performs fail-fast validation of the F20 per-VPC DNS config.
// Fails at startup when DCAPI_VPC_DNS_FORWARDERS contains invalid IPs.
// VPCDNSImage and VPCDNSSearchDomain are optional and not validated here.
func (c *Config) ValidateF20() error {
	forwarders := c.ParseDNSForwarders()
	for _, f := range forwarders {
		if net.ParseIP(f) == nil {
			return fmt.Errorf("DCAPI_VPC_DNS_FORWARDERS contains invalid IP %q", f)
		}
	}
	return nil
}

// ParseDNSForwarders splits VPCDNSForwarders on commas and trims whitespace.
// Defaults to ["1.1.1.1","8.8.8.8"] when the field is empty.
func (c *Config) ParseDNSForwarders() []string {
	if c.VPCDNSForwarders == "" {
		return []string{"1.1.1.1", "8.8.8.8"}
	}
	parts := strings.Split(c.VPCDNSForwarders, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// ValidateF15 performs fail-fast validation of the F15 external network config.
// Call this after Load(), before starting the server, when NetworkProvider == "kubeovn".
//
// KubeOVN's IPAM owns the external subnet — we don't allocate IPs ourselves.
// We just need a valid CIDR, a gateway inside it, and (optionally) a list of
// IPs to keep KubeOVN's IPAM away from (host nodes, ingress LB, etc.).
func (c *Config) ValidateF15() error {
	_, extNet, err := net.ParseCIDR(c.VPCExternalCIDR)
	if err != nil {
		return fmt.Errorf("DCAPI_VPC_EXTERNAL_CIDR %q is not a valid CIDR: %w", c.VPCExternalCIDR, err)
	}

	gw := net.ParseIP(c.VPCExternalGateway)
	if gw == nil {
		return fmt.Errorf("DCAPI_VPC_EXTERNAL_GATEWAY %q is not a valid IP", c.VPCExternalGateway)
	}
	if !extNet.Contains(gw) {
		return fmt.Errorf("DCAPI_VPC_EXTERNAL_GATEWAY %s is not inside DCAPI_VPC_EXTERNAL_CIDR %s",
			c.VPCExternalGateway, c.VPCExternalCIDR)
	}

	for _, ipStr := range c.ParseReservedIPs() {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			return fmt.Errorf("DCAPI_VPC_EXTERNAL_RESERVED_IPS contains invalid IP %q", ipStr)
		}
		if !extNet.Contains(ip) {
			return fmt.Errorf("DCAPI_VPC_EXTERNAL_RESERVED_IPS %s is not inside DCAPI_VPC_EXTERNAL_CIDR %s",
				ipStr, c.VPCExternalCIDR)
		}
	}

	return nil
}

// ParseReservedIPs splits VPCExternalReservedIPs on commas and trims whitespace.
// Empty input → empty slice.
func (c *Config) ParseReservedIPs() []string {
	if c.VPCExternalReservedIPs == "" {
		return nil
	}
	parts := strings.Split(c.VPCExternalReservedIPs, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
