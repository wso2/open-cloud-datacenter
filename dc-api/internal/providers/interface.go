// Package providers defines the CloudProvider interface and all concrete implementations.
//
// ── DESIGN PATTERN: Strategy Pattern ──────────────────────────────────────────
//
// The Strategy Pattern lets you define a family of algorithms (or, in our case,
// a family of "how to talk to a hypervisor"), encapsulate each one, and make them
// interchangeable. The API handlers are the "context" — they call the strategy
// through the interface without knowing which concrete provider is behind it.
//
// Analogy: Think of a power adapter. Your laptop (the handler) always has the same
// plug (the interface). The adapter (Harvester driver, Rancher driver) is swappable
// without modifying the laptop.
//
// In practice:
//   - VMHandler.provider is of type providers.ComputeProvider (interface)
//   - At startup, factory.go injects either &harvester.Client{} or &rancher.Client{}
//   - The handler code is identical regardless of which backend is active
//   - Adding a new hypervisor = writing a new struct that satisfies this interface
package providers

import (
	"context"
	"net"
	"time"

	"github.com/wso2/dc-api/internal/models"
)

// ComputeProvider is the contract that any VM backend must satisfy.
// Harvester implements this. In the future, OpenStack or Proxmox could too.
type ComputeProvider interface {
	// CreateVM provisions a virtual machine according to spec.
	// It returns a Resource with BackendUID populated (the provider's internal ID).
	// The resource may be in PENDING status; the caller is responsible for polling.
	// projectID is the human-readable project slug (e.g. "prod-infra"); together
	// with tenantID it determines the Kubernetes namespace: "dc-<tenant>-<project>".
	CreateVM(ctx context.Context, tenantID, projectID string, spec models.VMSpec) (*models.Resource, error)

	// GetVM retrieves the current state of a VM from the provider.
	// Used by the async reconciler to sync provider state → PostgreSQL.
	GetVM(ctx context.Context, backendUID string) (*models.Resource, error)

	// DeleteVM initiates VM deletion. Returns nil if the deletion was accepted.
	// Actual removal may be asynchronous; call GetVM to confirm.
	DeleteVM(ctx context.Context, backendUID string) error

	// ListVMs returns all VMs in the provider for a given tenant+project namespace.
	// projectID is the human-readable project slug.
	ListVMs(ctx context.Context, tenantID, projectID string) ([]*models.Resource, error)

	// ListImages returns all available VM images from the provider.
	// The Image.ID field is what callers pass to CreateVM as the image reference.
	ListImages(ctx context.Context) ([]*models.Image, error)

	// CreateImage registers a new image by instructing the provider to download
	// it from the given URL. Returns the image record with its ID populated.
	// The image may still be downloading; callers should poll ListImages for status.
	CreateImage(ctx context.Context, displayName, downloadURL string) (*models.Image, error)

	// ListNetworks returns all VM networks available in the provider.
	// The Network.ID field ("namespace/resource-name") is what callers pass to CreateVM.
	ListNetworks(ctx context.Context) ([]*models.Network, error)

	// Name returns a human-readable identifier for logging and metadata.
	Name() string
}

// ClusterProvider is the contract for any Kubernetes cluster backend.
// Rancher implements this. Future: vCluster, EKS Anywhere, etc.
type ClusterProvider interface {
	// CreateCluster provisions an RKE2 cluster via the provider.
	// projectID is the human-readable project slug; together with tenantID it
	// determines the Kubernetes namespace the cluster nodes land in.
	// spec.SystemPool must be populated by the handler (including HarvesterConfigName)
	// before calling; the provider uses it to build the initial machinePool entry.
	CreateCluster(ctx context.Context, tenantID, projectID string, spec models.ClusterSpec) (*models.Resource, error)

	// GetCluster retrieves the current state of a cluster.
	GetCluster(ctx context.Context, backendUID string) (*models.Resource, error)

	// DeleteCluster removes a cluster.
	DeleteCluster(ctx context.Context, backendUID string) error

	// GetKubeconfig returns the kubeconfig YAML for a provisioned cluster.
	// Only valid when the cluster is in ACTIVE status.
	GetKubeconfig(ctx context.Context, backendUID string) (string, error)

	// AddNodePool creates a HarvesterConfig CR and appends a machinePool entry to
	// the provisioning Cluster CR via GET-then-PUT (Steve does not reliably accept
	// JSON-merge PATCH on provisioning.cattle.io). The pool's HarvesterConfigName
	// must be pre-set by the handler. On success the pool status reflects
	// "provisioning"; the reconciler finalises it when Rancher reports Ready.
	// Pool name must be unique within the cluster.
	AddNodePool(ctx context.Context, clusterName string, pool *models.NodePool,
		mgmtNAD, tenantSubnetNAD, vmNamespace, nodeImage string) error

	// ScaleNodePool updates machinePools[i].quantity via GET-then-PUT. Rancher
	// converges via MachineDeployment. The pool status flips to "scaling" until
	// Rancher reports the new count. Returns a 409-conflict error when a
	// concurrent PUT changes the resourceVersion between our GET and PUT; the
	// caller may retry once.
	ScaleNodePool(ctx context.Context, clusterName, poolName string, newCount int) error

	// UpdateNodePoolTaintsLabels replaces machinePools[i].taints and labels
	// via GET-then-PUT. Refused at the handler layer for the system pool (R5);
	// the provider performs the operation without role-checking.
	UpdateNodePoolTaintsLabels(ctx context.Context, clusterName, poolName string,
		taints []models.NodePoolTaint, labels map[string]string) error

	// RemoveNodePool drops the pool from machinePools[] via GET-then-PUT, then
	// deletes the associated HarvesterConfig CR. The MachineDeployment drains
	// asynchronously (drainBeforeDelete=true on the pool entry). The handler
	// returns 202; the reconciler reflects deleting status until the
	// MachineDeployment is gone from Rancher.
	RemoveNodePool(ctx context.Context, clusterName, poolName, harvesterConfigName string) error

	// GetNodePoolStatuses returns per-pool status derived from the live Cluster
	// CR's status conditions, keyed by pool name. Used by the reconciler to sync
	// pool rows without a separate per-pool API call.
	//
	// Status mapping (conservative — defaults to provisioning when unclear):
	//   ready      — machinePool desired==current and Ready=True
	//   scaling    — mid-scale (desired≠current but no error)
	//   deleting   — pool entry absent from machinePools but MachineDeployment present
	//   failed     — Stalled=True on the cluster condition
	//   provisioning — all other states
	//
	// NOTE: full reconciler integration is deferred to a follow-up chunk (see
	// FOLLOWUPS.md). For R4 the provider sets initial statuses and the reconciler
	// hook is a stub that returns empty map + nil error.
	GetNodePoolStatuses(ctx context.Context, clusterName string) (map[string]models.NodePoolStatus, error)

	// Name returns the provider name.
	Name() string
}

// ── Why separate interfaces? ─────────────────────────────────────────────────
//
// We intentionally split ComputeProvider and ClusterProvider rather than putting
// both in one "CloudProvider" interface. This follows the Interface Segregation
// Principle (the "I" in SOLID): clients (handlers) should only depend on the
// methods they actually use.
//
// The VMHandler only needs ComputeProvider. It doesn't need to know that clusters
// exist. If we merge them, any mock for VM tests must implement all cluster methods
// too — friction for no benefit.

// NetworkProvider is the contract for any virtual-networking backend.
// The kubeovn driver (internal/providers/kubeovn) implements this for M2.
// A future swap to Harvester's bundled KubeOVN or another SDN reuses this
// exact interface; only the driver changes.
//
// Conventions that match ComputeProvider / ClusterProvider:
//   - First argument is always ctx context.Context.
//   - tenantID string scopes operations to a single tenant.
//   - backendUID string is the provider-internal CRD name (e.g. KubeOVN Vpc name).
//   - All errors should be wrapped by callers with fmt.Errorf("doing X: %w", err).
type NetworkProvider interface {
	// Name returns a human-readable identifier for logging and metadata.
	// Example: "kubeovn".
	Name() string

	// ── VNet ─────────────────────────────────────────────────────────────────

	// CreateVNet provisions a KubeOVN Vpc CRD for the tenant+project.
	// Returns a VNetResource with BackendUID populated on success.
	// The resource may still be PENDING; the reconciler polls GetVNet to confirm.
	// projectID is the human-readable project slug; it scopes the Vpc to the
	// project namespace "dc-<tenant>-<project>".
	CreateVNet(ctx context.Context, tenantID, projectID string, spec models.VNetSpec) (*models.VNetResource, error)

	// GetVNet returns the current provider state of a VNet.
	// Used by the reconciler to sync PENDING→ACTIVE / DELETING→(removed).
	GetVNet(ctx context.Context, backendUID string) (*models.VNetResource, error)

	// DeleteVNet removes the KubeOVN Vpc CRD.
	// The driver must first confirm no Subnet CRDs reference the VPC
	// (the KubeOVN finalizer blocks deletion if subnets remain).
	DeleteVNet(ctx context.Context, backendUID string) error

	// ── Subnet ───────────────────────────────────────────────────────────────

	// CreateSubnet provisions a KubeOVN Subnet CRD inside the parent Vpc.
	// Also creates the NetworkAttachmentDefinition (NAD) that Harvester VM
	// drivers reference when attaching a VM to this subnet.
	// vnetUID is the parent Vpc's backend_uid (KubeOVN Vpc CRD name).
	CreateSubnet(ctx context.Context, vnetUID string, spec models.SubnetSpec) (*models.SubnetResource, error)

	// GetSubnet returns the current provider state of a Subnet.
	GetSubnet(ctx context.Context, backendUID string) (*models.SubnetResource, error)

	// DeleteSubnet removes the KubeOVN Subnet CRD and its associated NAD.
	// Must use a PATCH to remove ACLs before deleting to avoid finalizer blocks
	// (see gotcha 4 in m2-network-api-mapping.md).
	DeleteSubnet(ctx context.Context, backendUID string) error

	// ── Route Table ──────────────────────────────────────────────────────────

	// CreateRouteTable appends the spec's routes to Vpc.spec.staticRoutes,
	// tagging each entry with the route-table UUID for later removal.
	// Route tables are synchronous (no reconciler loop needed).
	CreateRouteTable(ctx context.Context, vnetUID string, spec models.RouteTableSpec) (*models.RouteTableResource, error)

	// UpdateRouteTableRoutes replaces the set of routes owned by a specific
	// route table on the parent Vpc. The driver reads current staticRoutes,
	// removes entries tagged with backendUID (the route-table UUID), then
	// appends the new set, and PATCHes the Vpc — never deleting the CRD.
	UpdateRouteTableRoutes(ctx context.Context, backendUID string, routes []models.RouteRule) error

	// DeleteRouteTable removes only the entries owned by this route table from
	// Vpc.spec.staticRoutes. The Vpc CRD itself is NOT deleted.
	DeleteRouteTable(ctx context.Context, backendUID string) error

	// AssociateRouteTable records the subnet association. In M2 this is
	// informational only — all routes already apply at the VPC level.
	AssociateRouteTable(ctx context.Context, routeTableUID, subnetUID string) error

	// DisassociateRouteTable removes the association. No data-plane effect in M2.
	DisassociateRouteTable(ctx context.Context, routeTableUID, subnetUID string) error

	// ── NSG ──────────────────────────────────────────────────────────────────

	// CreateNSG creates the NSG record. Rule application happens via
	// AttachNSGToSubnet — the NSG itself has no backend CRD until it is attached.
	// projectID is the human-readable project slug; passed for labelling purposes.
	CreateNSG(ctx context.Context, tenantID, projectID string, spec models.NSGSpec) (*models.NSGResource, error)

	// UpdateNSGRules replaces the complete rule set on the NSG.
	// For subnet-attached NSGs, the driver re-writes Subnet.spec.acls.
	// This is always a PATCH on the Subnet CRD — never a delete (gotcha 4).
	UpdateNSGRules(ctx context.Context, backendUID string, rules []models.NSGRule) error

	// DeleteNSG removes the NSG. Only valid when no attachments exist.
	DeleteNSG(ctx context.Context, backendUID string) error

	// AttachNSGToSubnet writes the NSG's rules to the target Subnet CRD's
	// spec.acls field (stateless OVN ACLs). M2 only; see interface note below
	// for why AttachNSGToNIC is intentionally absent.
	AttachNSGToSubnet(ctx context.Context, nsgUID, subnetUID string) error

	// DetachNSGFromSubnet removes the NSG's ACL entries from the Subnet CRD
	// via a PATCH (never deletes the Subnet CRD).
	DetachNSGFromSubnet(ctx context.Context, nsgUID, subnetUID string) error

	// NSG-on-NIC (AttachNSGToNIC / DetachNSGFromNIC) is intentionally absent
	// from this interface. NIC is not a first-class resource in M2 (deferred to
	// M3 per §14 "Public IPs and NICs — deferred to M3"). When M3 ships, add
	// the two NIC methods here and implement them in the kubeovn driver using
	// the SecurityGroup CRD + pod annotation path.

	// ── Peering ───────────────────────────────────────────────────────────────

	// CreatePeering creates a KubeOVN VpcPeering CRD and adds reciprocal
	// staticRoutes entries to both Vpc CRDs (tagged with the peering UUID).
	// Both vnetUID and peerVnetUID are KubeOVN Vpc CRD names (backend_uid values).
	CreatePeering(ctx context.Context, vnetUID, peerVnetUID string, spec models.PeeringSpec) (*models.PeeringResource, error)

	// DeletePeering removes the peering entries from both Vpc CRDs' spec.vpcPeerings
	// and removes the reciprocal staticRoutes that were added for this peering.
	//
	// localCIDRs is the address-space of the VNet on whose behalf the delete was
	// initiated; peerCIDRs is the address-space of the other VNet.  Both are
	// needed to identify which staticRoutes to remove when the routeTable field
	// is the empty-string default (item 3 of the peering driver fix).
	DeletePeering(ctx context.Context, backendUID string, localCIDRs, peerCIDRs []string) error

	// ── Private DNS ───────────────────────────────────────────────────────────

	// CreatePrivateDnsZone creates a KubeOVN VpcDns CRD scoped to the parent Vpc.
	// vnetUID is the parent Vpc's backend_uid.
	CreatePrivateDnsZone(ctx context.Context, vnetUID string, spec models.DnsZoneSpec) (*models.DnsZoneResource, error)

	// DeletePrivateDnsZone removes the VpcDns CRD and its associated ConfigMap.
	DeletePrivateDnsZone(ctx context.Context, backendUID string) error

	// UpsertDnsRecord creates or replaces a DNS record in the zone's ConfigMap.
	// zoneUID is the VpcDns CRD name.
	UpsertDnsRecord(ctx context.Context, zoneUID string, record models.DnsRecord) error

	// DeleteDnsRecord removes a specific DNS record from the zone's ConfigMap.
	// recordID is the dc-api UUID of the dns_records row (used to identify the
	// entry in the ConfigMap, where entries are keyed by dc-api UUID).
	DeleteDnsRecord(ctx context.Context, zoneUID, recordID string) error
}

// ProjectNamespaceProvisioner is the optional interface for provisioning the
// Kubernetes namespace + ResourceQuota that backs a dc-api Project. The kubeovn
// driver implements this. Providers that don't support namespace management
// (e.g. test stubs) may return nil for this interface.
//
// Called synchronously (inside a goroutine) on successful project creation so
// the namespace is ready before the first VNet or VM create. Best-effort: a
// failure is logged but the project row remains committed.
type ProjectNamespaceProvisioner interface {
	// EnsureProjectNamespace idempotently creates the namespace
	// "dc-<tenantID>-<projectID>" with standard labels and a ResourceQuota
	// mirroring the project's capacity + volume guardrails.
	EnsureProjectNamespace(
		ctx context.Context,
		tenantID, projectID string,
		projectUUID interface{ String() string },
		cpuCores, memoryGB, storageGB, maxVolumes int,
	) error
}

// KVIProvisioner is the optional interface that drives the KVI operator's
// CRDs (KeyVaultBackend + KeyVaultInstance) installed on the cluster. dc-api
// is the *creator* of CRs; the KVI controller watches them and provisions
// the underlying OpenBao + AppRole.
//
// All operations are best-effort idempotent — re-applying the same Ensure
// call when the CR already exists is a no-op. Status reads return ("", nil)
// when the CR does not exist (callers treat that as "not yet provisioned").
//
// dc-api handlers depend on this interface — never on the dynamic K8s
// client directly — so the same handler code works against a fake in tests.
type KVIProvisioner interface {
	// BackendName returns the canonical KeyVaultBackend CR name for a
	// tenant slug. Exposed so handlers don't have to import the impl
	// package just to compute the name. Convention: "kvb-<slug>".
	BackendName(tenantSlug string) string

	// EnsureKeyVaultBackend creates the per-tenant Backend CR in
	// "dc-tenant-<tenantSlug>" if it doesn't already exist. spec carries
	// the capacity hints the controller renders into the OpenBao
	// StatefulSet. Returns nil if the CR was created OR already existed.
	EnsureKeyVaultBackend(
		ctx context.Context,
		tenantSlug string,
		tenantUUID interface{ String() string },
		spec KeyVaultBackendSpec,
	) error

	// GetKeyVaultBackendStatus returns the Backend's status.phase
	// (Pending / Provisioning / Ready / Failed / Terminating) plus
	// the human-readable status.message. Returns ("", "", nil) when
	// the CR does not exist.
	GetKeyVaultBackendStatus(ctx context.Context, tenantSlug string) (phase, message string, err error)

	// CreateKeyVaultInstance creates a KVI CR in the project namespace
	// with the seven standard dc-api.wso2.com/* labels stamped on it.
	// Returns an AlreadyExists error if a CR of the same name exists.
	CreateKeyVaultInstance(ctx context.Context, req KeyVaultInstanceCreateRequest) error

	// GetKeyVaultInstance returns the CR's status, or (nil, nil) when
	// the CR doesn't exist.
	GetKeyVaultInstance(ctx context.Context, namespace, name string) (*KeyVaultInstanceStatus, error)

	// GetCredentialsSecret returns the data map of the credentials Secret
	// (role_id, secret_id, mount_path, backend_address, backend_port).
	// Returns (nil, nil) when the Secret doesn't exist.
	GetCredentialsSecret(ctx context.Context, namespace, name string) (map[string][]byte, error)

	// DeleteKeyVaultInstance removes the KVI CR. The KVI controller's
	// finalizer runs the upstream cleanup (mount + AppRole + policy
	// disable on OpenBao); dc-api just deletes the CR.
	DeleteKeyVaultInstance(ctx context.Context, namespace, name string) error

	// ── Secret proxy operations (KV-v2 via root token) ───────────────────────
	//
	// These methods proxy KV-v2 read/write/list/delete calls to the tenant's
	// OpenBao StatefulSet through the Kubernetes pod-proxy mechanism.
	//
	// SECURITY: the root token is read from the k8s Secret kvb-<tenant>-keys
	// and MUST NEVER appear in any log line, error message returned to callers,
	// or HTTP response.

	// GetOpenBaoLeaderPod returns the name of an OpenBao pod in the tenant's
	// backend namespace that can service read/write requests. The returned
	// string is a pod name, not a DNS hostname.
	//
	// Returns an error when no backend namespace exists or no ready pods are
	// found. The tenantSlug is the human-readable tenant identifier (e.g.
	// "acme"), NOT the tenant UUID.
	GetOpenBaoLeaderPod(ctx context.Context, tenantSlug string) (podName string, err error)

	// ReadDCAPIToken returns a token authenticated to OpenBao with the
	// dc-api-admin scoped policy (CRUD on tenants/+/+/* paths only — no
	// sys/*, no auth/*, no token-create). Reads from the per-Backend
	// kvb-<tenantSlug>-dcapi-token Secret minted by the KVI operator at
	// Backend-bootstrap time.
	//
	// Falls back to the root token (kvb-<tenantSlug>-keys) when the
	// scoped-token Secret is not present, so that F50 secret CRUD keeps
	// working against Backends bootstrapped before the operator started
	// minting the scoped token. Implementations should log a warning when
	// the fallback path is taken.
	//
	// The returned token MUST NOT be logged or included in any user-visible
	// error.
	ReadDCAPIToken(ctx context.Context, tenantSlug string) (token string, err error)

	// WriteSecret writes a new KV-v2 version to <mount>/data/<key>.
	// Returns the new version number and whether this was the first write.
	WriteSecret(
		ctx context.Context,
		tenantSlug, podName, mount, key, token, value string,
		meta map[string]string,
	) (version int, isNew bool, err error)

	// ReadSecret reads the KV-v2 secret at <mount>/data/<key>. version=0 means
	// "latest". Returns a *SecretReadResult or an error (ErrSecretNotFound,
	// ErrOpenBaoUnavailable, or a generic error for unexpected statuses).
	ReadSecret(
		ctx context.Context,
		tenantSlug, podName, mount, key, token string,
		version int,
	) (*SecretReadResult, error)

	// ListSecretKeys returns the sorted list of keys in <mount>.
	ListSecretKeys(
		ctx context.Context,
		tenantSlug, podName, mount, token string,
	) ([]string, error)

	// GetSecretMetadata returns version metadata for a single key.
	GetSecretMetadata(
		ctx context.Context,
		tenantSlug, podName, mount, key, token string,
	) (*SecretKeyMetadata, error)

	// DeleteSecret soft-deletes the latest version of <key> in <mount>.
	DeleteSecret(
		ctx context.Context,
		tenantSlug, podName, mount, key, token string,
	) error

	// UndeleteSecretVersion reverses a soft-delete for the named version
	// (typically the latest deleted one — caller determines which).
	UndeleteSecretVersion(
		ctx context.Context,
		tenantSlug, podName, mount, key, token string,
		version int,
	) error

	// ListSecretIDAccessors returns every secret_id_accessor currently bound
	// to the named AppRole. Used to enumerate existing secret_ids before
	// rotation so they can be destroyed atomically.
	ListSecretIDAccessors(
		ctx context.Context,
		tenantSlug, podName, role, token string,
	) ([]string, error)

	// DestroySecretIDAccessor invalidates the secret_id identified by its
	// accessor. Idempotent: a 404 (already gone) is treated as success.
	DestroySecretIDAccessor(
		ctx context.Context,
		tenantSlug, podName, role, accessor, token string,
	) error

	// GenerateSecretID mints a fresh secret_id on the named AppRole. Returns
	// the secret_id (one-time-shown) and its accessor. Caller is responsible
	// for persisting the secret_id before letting it go out of scope.
	GenerateSecretID(
		ctx context.Context,
		tenantSlug, podName, role, token string,
	) (secretID, accessor string, err error)

	// PatchCredentialsSecret merges key/value pairs into the named Secret's
	// .data field. Used after rotation to update the AppRole credentials
	// Secret in the project namespace so downstream consumers reading the
	// Secret directly see the new value.
	PatchCredentialsSecret(
		ctx context.Context,
		namespace, name string,
		data map[string][]byte,
	) error
}

// SecretReadResult holds the data and version information for a KV-v2 read.
type SecretReadResult struct {
	Value       string
	Metadata    map[string]string
	Version     int
	CreatedTime string
	DeletionTime string // non-empty → soft-deleted
	Destroyed   bool
}

// SecretKeyMetadata holds the full version history metadata for a KV-v2 key.
type SecretKeyMetadata struct {
	CurrentVersion int
	CreatedTime    string // first-version creation time
	UpdatedTime    string // latest-version write time
	// VersionMeta maps string version number → per-version meta.
	VersionMeta map[string]SecretVersionMeta
}

// SecretVersionMeta is per-version metadata from the KV-v2 metadata endpoint.
type SecretVersionMeta struct {
	CreatedTime  string
	DeletionTime string
	Destroyed    bool
}

// KeyVaultBackendSpec is the capacity portion of the Backend CR's spec.
// Only the dc-api-driven fields are exposed; engine-specific defaults
// (HA replicas, audit on/off) are filled in by the controller.
type KeyVaultBackendSpec struct {
	// CPU is the total CPU budget for the StatefulSet (e.g. "1").
	// Empty string means "controller default".
	CPU string
	// MemoryGB is the total memory budget. 0 = controller default.
	MemoryGB int
	// StorageGB is the per-replica Raft PVC size. 0 = controller default.
	StorageGB int
}

// KeyVaultInstanceCreateRequest is what dc-api hands the provisioner when
// creating a user-facing KVI. The provisioner translates it into the CR's
// metadata + spec.
type KeyVaultInstanceCreateRequest struct {
	Name           string            // CR name (e.g. "kv-<8-char-uuid>")
	Namespace      string            // project ns (dc-<tenant>-<project>)
	Labels         map[string]string // dc-api.wso2.com/* labels for propagation
	BackendName    string            // KeyVaultBackend CR name
	BackendNS      string            // dc-tenant-<slug>
	SoftDeleteDays int               // 7..90
}

// KeyVaultInstanceStatus is the framework-friendly view of a KVI's status.
// Mirrors the contract's phase enum so handlers don't have to grub through
// the unstructured CR themselves.
type KeyVaultInstanceStatus struct {
	Phase           string // Pending|Provisioning|Ready|Failed|Terminating
	Message         string
	MountPath       string
	EndpointAddress string
	EndpointPort    int
	SecretRefName   string // empty until Ready
}

// TenantNamespaceProvisioner is the optional interface for provisioning the
// per-tenant Kubernetes namespace "dc-tenant-<tenantID>". This namespace
// hosts tenant-tier managed-service Backends (keyvault HA cluster, etc.)
// per docs/managed-services-integration.md §3.
//
// Called from POST /v1/admin/tenants right after the DB row is committed.
// Best-effort: a failure is logged but the tenant row remains; the next
// time the namespace is needed (e.g. first key vault create in that
// tenant), the responsible handler can call EnsureTenantNamespace again.
//
// Unlike ProjectNamespaceProvisioner there's no ResourceQuota here — the
// tenant cap is enforced at the application layer (handlers + project
// sums), and the workloads hosted in the tenant ns are operator-managed
// Backends with their own size knobs.
type TenantNamespaceProvisioner interface {
	// EnsureTenantNamespace idempotently creates "dc-tenant-<tenantID>"
	// with the standard tenant-tier labels:
	//   dc-api/managed=true
	//   dc-api.wso2.com/tenant=<slug>
	//   dc-api.wso2.com/tenant-uuid=<uuid>
	//   dc-api.wso2.com/scope=tenant-services
	EnsureTenantNamespace(
		ctx context.Context,
		tenantID string,
		tenantUUID interface{ String() string },
	) error
}

// VPCDNSProvisioner is the optional F20 interface for per-VPC CoreDNS provisioning.
// The kubeovn driver implements this; other drivers may return nil.
//
// Handlers receive this as an optional dependency: if nil, the DNS step is
// skipped (useful for integration tests without a full KubeOVN cluster).
type VPCDNSProvisioner interface {
	// EnsureVpcDNS idempotently provisions the per-VPC CoreDNS Deployment and
	// patches the subnet's dhcpV4Options to advertise the pinned DNS IP.
	// Returns the pinned IP so the caller can persist it on the VNet row.
	EnsureVpcDNS(ctx context.Context, vpcUID, subnetCIDR, subnetBackendUID, tenantNS string) (net.IP, error)

	// DeleteVpcDNS removes the CoreDNS Deployment for vpcUID. Idempotent.
	DeleteVpcDNS(ctx context.Context, vpcUID string) error

	// IsVpcDNSPresent returns true if the CoreDNS Deployment for vpcUID exists.
	IsVpcDNSPresent(ctx context.Context, vpcUID string) (bool, error)

	// WaitVpcDNSPodsGone blocks until the CoreDNS pods for vpcUID are fully
	// terminated (no pods with app=vpc-dns,vpc=<vpcUID> remain in kube-system),
	// or until timeout expires. Returns nil immediately if pods are already gone.
	// Called by the subnet-delete goroutine after DeleteVpcDNS so that KubeOVN
	// can release the LSP from the logical switch before the subnet CRD is deleted.
	WaitVpcDNSPodsGone(ctx context.Context, vpcUID string, timeout time.Duration) error
}

// VPCNATProvisioner is the optional F15 interface that a NetworkProvider may
// implement to support automatic per-VPC SNAT. The kubeovn driver implements
// this; other drivers (or test stubs) may return nil for this interface.
//
// Handlers receive this as an optional dependency: if nil, the SNAT step is
// skipped (useful for integration tests that don't run against a full KubeOVN
// cluster with the external network configured).
//
// NOTE: This interface is intentionally separate from NetworkProvider because:
//   (a) Not every NetworkProvider backend supports VpcNatGateway semantics.
//   (b) SNAT is infrastructure-level plumbing, not part of the user-facing API.
//   (c) Keeping it separate lets tests stub NetworkProvider without implementing NAT.
type VPCNATProvisioner interface {
	// EnsureVpcNAT idempotently provisions the per-VPC SNAT chain.
	// vpcName is the KubeOVN Vpc CRD name (BackendUID from the DB row).
	// tenantSubnetCIDR and tenantSubnetName describe the tenant's first subnet.
	// lanIP is an unused IP inside the tenant subnet used for the gateway's LAN NIC.
	//
	// Returns the EIP address that KubeOVN's IPAM auto-allocated, so the caller
	// can persist it on the VNet row for display. dc-api does NOT pick the EIP IP
	// — KubeOVN owns external-subnet allocation (see project_f15_redo_plan.md).
	EnsureVpcNAT(ctx context.Context, vpcName, tenantSubnetCIDR, tenantSubnetName string, lanIP net.IP) (net.IP, error)

	// DeleteVpcNAT removes all per-VPC NAT resources in reverse order and strips
	// the default route from the VPC. Idempotent.
	DeleteVpcNAT(ctx context.Context, vpcName string) error

	// IsVpcNATPresent returns true if the VpcNatGateway and IptablesEIP for
	// vpcName both exist and are present on the cluster.
	IsVpcNATPresent(ctx context.Context, vpcName string) (bool, error)

	// WaitVpcNATPodsGone blocks until the VpcNatGateway's StatefulSet pods for
	// vpcName are fully terminated (no pods with app=vpc-nat-gw-<gwName> remain
	// in kube-system), or until timeout expires. Returns nil immediately if pods
	// are already gone. Called by the subnet-delete goroutine after DeleteVpcNAT
	// so that KubeOVN can release the LSP before the subnet CRD is deleted.
	WaitVpcNATPodsGone(ctx context.Context, vpcUID string, timeout time.Duration) error
}

// DatabaseProvisioner is the optional interface that drives the dbaas
// operator's DBInstance CRD. dc-api creates / reads / deletes CRs of group
// dbaas.opencloud.wso2.com/v1alpha1 (kind: DBInstance) and reads the
// credentials Secret the controller writes. The dbaas controller (separate
// workload at github.com/wso2/open-cloud-datacenter) watches the CRs and
// provisions the underlying KubeVirt VM running PostgreSQL.
//
// Task 1 (D2): 1:1 model — one Database == one DBInstance CR == one VM.
// No DatabaseBackend (unlike KVIProvisioner). The interface is therefore
// strictly smaller than KVIProvisioner.
//
// dc-api handlers depend on this interface — never on the concrete dbaas
// adapter — so the same handler code works against a fake in tests.
type DatabaseProvisioner interface {
	// CreateDatabaseInstance creates a DBInstance CR in the project
	// namespace with the seven standard dc-api.wso2.com/* labels stamped
	// on it. Returns an AlreadyExists error if a CR of the same name
	// exists (handler maps to 409).
	CreateDatabaseInstance(ctx context.Context, req DatabaseInstanceCreateRequest) error

	// GetDatabaseInstance returns the CR's status, or (nil, nil) when the
	// CR does not exist (handler treats that as "not yet provisioned" —
	// no error).
	GetDatabaseInstance(ctx context.Context, namespace, name string) (*DatabaseInstanceStatus, error)

	// DeleteDatabaseInstance removes the DBInstance CR. The dbaas
	// controller's finalizer runs the upstream teardown (VM, DataVolumes,
	// Service, ServiceMonitor, Secret); dc-api just deletes the CR.
	// Idempotent: NotFound is treated as success.
	DeleteDatabaseInstance(ctx context.Context, namespace, name string) error

	// GetDatabaseCredentialsSecret returns the raw data map of the credentials
	// Secret the controller wrote. Returns (nil, nil) when the Secret
	// does not exist. Keys are dbaas-specific (admin_user, admin_password,
	// ca_cert, server_cert, ...); the handler picks the subset to surface
	// on GET .../credentials.
	GetDatabaseCredentialsSecret(ctx context.Context, namespace, name string) (map[string][]byte, error)

	// DeleteCredentialsSecret removes the credentials Secret from Kubernetes
	// after it has been read and returned to the caller once. This enforces
	// the shown-once guarantee at the Kubernetes layer as well as in the db
	// row. Idempotent: NotFound is treated as success.
	DeleteCredentialsSecret(ctx context.Context, namespace, name string) error
}

// DatabaseInstanceCreateRequest is what the handler hands the provisioner.
// The provisioner translates it into the DBInstance CR's metadata + spec.
// Field naming mirrors the dbaas controller's CRD (api/v1alpha1/
// dbinstance_types.go) so the adapter's translation is mechanical.
type DatabaseInstanceCreateRequest struct {
	// Name is the CR's metadata.name (e.g. "db-<8-char-uuid>"). Built by
	// the handler via common.NamespaceScopedName("db", dbID).
	Name string
	// Namespace is the project-tier namespace ("dc-<tenant>-<project>").
	Namespace string
	// Labels is the result of common.StandardLabels(...) — the seven
	// dc-api.wso2.com/* labels the operator must propagate to children
	// per docs/managed-services-integration.md §6.
	Labels map[string]string

	// InstanceClass maps to spec.dbInstanceClass (e.g. "db.t3.medium").
	// Validated by the handler against models.DatabaseInstanceClasses.
	InstanceClass string
	// AllocatedStorageGB maps to spec.allocatedStorage.
	AllocatedStorageGB int
	// EngineVersion is informational in v1 (the dbaas controller doesn't
	// act on it). Stored in the CR spec for future-compat (Task 2).
	EngineVersion string

	// OSImage is the Harvester VirtualMachineImage ("namespace/name") the
	// controller boots the VM from (spec.osImage). Operator-configured via
	// DCAPI_DBAAS_OS_IMAGE; empty leaves the controller's own default.
	OSImage string

	// NetworkRef is the resolved Multus NAD identity the controller
	// attaches the VM's data NIC to. The handler resolves it from either
	// VPC mode (subnet → NAD) or legacy mode (pass-through), so the
	// adapter never sees raw vnet/subnet/nad_ref fields.
	NetworkRef DatabaseNetworkRef
}

// DatabaseNetworkRef is the namespace/name pair identifying the Multus NAD
// the operator attaches the database VM to. Per
// docs/managed-services-integration.md §7.1 the operator does not see how
// the NAD was produced — only its identity.
type DatabaseNetworkRef struct {
	Namespace string
	Name      string
	// DNSServerIP is the per-VPC CoreDNS address for VPC-mode subnets, passed
	// to the operator so the VM's dnsConfig points at a resolver reachable
	// from the isolated VPC (defeats the KubeVirt-on-OVN DHCP DNS race that
	// otherwise breaks apt during cloud-init). Empty for legacy bridge NADs
	// (cluster-routable VLAN DNS works without it).
	DNSServerIP string
}

// DatabaseInstanceStatus is the framework-friendly view of a DBInstance.status.
// Mirrors the contract's five-phase enum so handlers don't have to grub
// through the unstructured CR themselves. The adapter is responsible for
// mapping the dbaas-specific status shape (RDS-style status.phase values
// like "available", and the credential Secret at status.masterUserSecret.name
// rather than the canonical status.endpoint.secretRef.name) into this
// canonical view.
type DatabaseInstanceStatus struct {
	Phase   string // Pending|Provisioning|Ready|Failed|Terminating
	Message string

	EndpointAddress string
	EndpointPort    int

	SecretRefName string // empty until Ready
}

