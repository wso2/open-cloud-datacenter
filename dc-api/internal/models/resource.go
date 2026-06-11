// Package models defines the core domain types for DC-API.
//
// Design note: These structs are PURE Go — no database tags, no HTTP tags.
// They represent the "domain layer" (what things ARE), not the "persistence
// layer" (how they are stored) or the "API layer" (how they are serialised).
// This separation is intentional: if we change the DB schema or the JSON
// field names, these types stay stable.
package models

import (
	"time"

	"github.com/google/uuid"
)

// ResourceType is a string enum of the resource kinds DC-API manages.
type ResourceType string

const (
	ResourceTypeVM      ResourceType = "VIRTUAL_MACHINE"
	ResourceTypeCluster ResourceType = "CLUSTER"
	ResourceTypeVolume  ResourceType = "VOLUME"
	// ResourceTypeBastion is a per-VPC SSH bastion host (F10). Internally a
	// small KubeVirt VM with two NICs: tenant subnet (OVN) + mgmt VLAN.
	ResourceTypeBastion ResourceType = "BASTION"
)

// ResourceStatus represents the lifecycle state of a resource.
// Resources move through: PENDING → ACTIVE, or PENDING → FAILED.
// Deletion: ACTIVE → DELETING → (row removed on success).
type ResourceStatus string

const (
	StatusPending  ResourceStatus = "PENDING"
	StatusActive   ResourceStatus = "ACTIVE"
	StatusFailed   ResourceStatus = "FAILED"
	StatusDeleting ResourceStatus = "DELETING"
)

// VMSize defines a named instance size — CPU, memory, and a default root disk.
// Sizes are the only way to specify compute resources in the DC-API.
// Callers may override the default disk with disk_gb in the request.
type VMSize struct {
	CPU           int
	MemoryGB      int
	DefaultDiskGB int
}

// Sizes is the catalog of valid instance sizes, matching Azure/AWS naming style.
// The handler resolves a size name to these numbers before calling the provider.
var Sizes = map[string]VMSize{
	"small":  {CPU: 2, MemoryGB: 4, DefaultDiskGB: 40},
	"medium": {CPU: 4, MemoryGB: 8, DefaultDiskGB: 40},
	"large":  {CPU: 8, MemoryGB: 16, DefaultDiskGB: 80},
	"xlarge": {CPU: 16, MemoryGB: 32, DefaultDiskGB: 160},
}

// ValidSizeNames returns the ordered list of valid size names for error messages.
func ValidSizeNames() []string {
	return []string{"small", "medium", "large", "xlarge"}
}

// Resource is the canonical representation of any datacenter resource.
// Every VM, cluster, and volume managed by DC-API has exactly one Resource
// record in PostgreSQL.
type Resource struct {
	ID         uuid.UUID `json:"id"`
	TenantID   string    `json:"tenant_id"`
	// TenantUUID is the immutable identity for the owning tenant (Phase 6a).
	// DB queries that enforce tenant isolation filter on this column, not TenantID.
	// Populated from tenants.tenant_uuid on write; read back on every SELECT.
	TenantUUID  uuid.UUID `json:"tenant_uuid"`
	ProjectID   string    `json:"project_id"`
	ProjectUUID uuid.UUID `json:"project_uuid"`
	OwnerID      string         `json:"owner_id"`
	Name         string         `json:"name"`
	Type         ResourceType   `json:"type"`
	Size         string         `json:"size,omitempty"`   // "small" | "medium" | "large" | "xlarge"
	Status       ResourceStatus `json:"status"`
	ProviderType string         `json:"provider_type"`
	BackendUID   string         `json:"backend_uid"`
	IPAddress    string         `json:"ip_address,omitempty"`
	// MgmtIP is the operator-reachable IP on the mgmt VLAN. F10 bastions
	// populate this; other resource types leave it empty.
	MgmtIP string `json:"mgmt_ip,omitempty"`
	// VNetID and SubnetID record the tenant-VPC placement of this resource (F41).
	// Both are nil for resources created on the legacy bridge path (NetworkName);
	// both are set for resources created on a tenant VPC.
	VNetID    *uuid.UUID `json:"vnet_id,omitempty"`
	SubnetID  *uuid.UUID `json:"subnet_id,omitempty"`
	Message   string     `json:"message,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// VMSpec is the intent (desired state) for a virtual machine.
// The caller fills this in; DC-API resolves backend details from it.
type VMSpec struct {
	Name        string `json:"name"`
	CPU         int    `json:"cpu"`          // vCPU count
	MemoryGB    int    `json:"memory_gb"`    // RAM in GiB
	DiskGB      int    `json:"disk_gb"`      // Root volume size in GiB
	ImageName   string `json:"image_name"`   // Human-readable image name (e.g., "ubuntu-22.04")
	NetworkName string `json:"network_name"` // Legacy bridge network name (mutually exclusive with VNet/Subnet path)

	// VPC attachment (M2): set by the handler after resolving vnet_id / subnet_id
	// UUIDs to their KubeOVN CRD names.  The harvester driver uses these instead
	// of NetworkName when both are non-empty.  The handler ensures only ONE of
	// (NetworkName) or (VNetBackendUID + SubnetBackendUID) is set — never both.
	VNetBackendUID   string `json:"-"` // KubeOVN Vpc CRD name, e.g. "vnet-<tenant>-<name>"
	SubnetBackendUID string `json:"-"` // KubeOVN Subnet/NAD CRD name, e.g. "subnet-<tenant>-<name>"

	// SSHPublicKey and Password are populated by DC-API internally — the caller does not
	// provide them. DC-API generates a key pair and a random console password, injects
	// both via cloud-init, and returns them ONCE in the provisioning response.
	SSHPublicKey string `json:"-"`
	Password     string `json:"-"`
	// ResourceID is the DC-API UUID of the resource row.  Set by the Create handler
	// so the harvester driver can derive a stable MAC from it (gotcha 1).
	ResourceID string `json:"-"`

	// DNSServerIP is the per-VPC CoreDNS pod IP (F20). Set by the VM handler when
	// the VM's subnet belongs to a VPC that has F20 DNS provisioned. The harvester
	// driver injects this as dnsPolicy=None + dnsConfig.nameservers on the VM spec,
	// silencing KubeVirt's bridge-mode internal DHCP race. Empty on the legacy
	// bridge path (no VPC, no F20 DNS).
	DNSServerIP string `json:"-"`
	// DNSSearchDomain is the optional search domain injected alongside DNSServerIP.
	// Sourced from DCAPI_VPC_DNS_SEARCH_DOMAIN. Empty means no extra search domain.
	DNSSearchDomain string `json:"-"`

	// MgmtNAD is the Multus NAD reference (`namespace/name`) for an optional
	// second NIC on an operator-reachable VLAN. When set, the harvester driver
	// renders a dual-NIC VM (mgmt + OVN) and the cloud-init bootcmd brings up
	// both interfaces. Empty for single-NIC VMs. F10 bastions set this.
	MgmtNAD string `json:"-"`
}

// ClusterSpec is the intent for an RKE2 cluster.
// The system pool is described by SystemPool; optional worker pools may be
// specified at create time via WorkerPools (each must have a pre-generated
// HarvesterConfigName); additional pools can also be added post-creation via
// the node-pool API.
type ClusterSpec struct {
	Name       string `json:"name"`
	K8sVersion string `json:"k8s_version"`
	ImageName  string `json:"image_name"`
	// NetworkName is the legacy bridge network name.
	// Mutually exclusive with VNetBackendUID + SubnetBackendUID (F32).
	NetworkName string `json:"network_name"`

	// SystemPool describes the control-plane + etcd pool created at cluster
	// create time. Role is always NodePoolRoleSystem; Count must be 1, 3, or 5.
	// The handler populates HarvesterConfigName before passing the spec to the
	// provider so the provider can use a consistent name without regenerating it.
	SystemPool *NodePool `json:"-"`

	// WorkerPools are optional worker pools to provision alongside the cluster.
	// Each entry must have HarvesterConfigName pre-populated by the handler
	// (same naming convention as the system pool). May be nil or empty when no
	// worker pools are requested at create time.
	WorkerPools []*NodePool `json:"-"`

	// ── F32: RKE2-on-VPC fields ───────────────────────────────────────────────
	// VNetBackendUID is the KubeOVN Vpc CRD name for the tenant VPC.
	// Set by the handler after resolving vnet_id to the KubeOVN backend CRD name.
	// Only used when the cluster is created on a tenant VPC (not a bridge network).
	VNetBackendUID string `json:"-"`

	// SubnetBackendUID is the KubeOVN Subnet/NAD CRD name inside the VPC.
	// Combined with the tenant namespace to form the full NAD reference:
	// "dc-<tenantID>/<SubnetBackendUID>" passed into the HarvesterConfig.
	SubnetBackendUID string `json:"-"`

	// RancherUID is the management cluster ID assigned by Rancher (c-m-xxxxx).
	// Populated by the reconciler once the provisioning.cattle.io cluster has a
	// status.clusterName. Stored in the resources.metadata JSONB column.
	RancherUID string `json:"-"`
}

// AuditEvent records a state transition for compliance and debugging.
type AuditEvent struct {
	ID         uuid.UUID `json:"id"`
	ResourceID uuid.UUID `json:"resource_id"`
	ActorID    string    `json:"actor_id"`   // Asgardeo subject
	Action     string    `json:"action"`     // e.g., "CREATE", "DELETE", "STATUS_CHANGE"
	FromStatus ResourceStatus `json:"from_status,omitempty"`
	ToStatus   ResourceStatus `json:"to_status,omitempty"`
	Message    string    `json:"message,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// ActivityEntry is one row of a project's activity feed: an AuditEvent plus
// the owning resource's name and type, snapshotted onto the event at write
// time so clients can render a readable line ("vm-web-1 CREATE") even after
// the resource is deleted. ResourceID is uuid.Nil (omitted from JSON) once the
// resource is gone — clients use it for deep links while it lasts.
type ActivityEntry struct {
	ID           uuid.UUID      `json:"id"`
	ResourceID   uuid.UUID      `json:"resource_id,omitzero"`
	ResourceName string         `json:"resource_name"`
	ResourceType ResourceType   `json:"resource_type"`
	Action       string         `json:"action"`   // e.g., "CREATE", "DELETE", "STATUS_CHANGE"
	ActorID      string         `json:"actor_id"` // OIDC sub, service-account ID, or "system"
	FromStatus   ResourceStatus `json:"from_status,omitempty"`
	ToStatus     ResourceStatus `json:"to_status,omitempty"`
	Message      string         `json:"message,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
}

// Image represents a bootable VM image available in the compute provider.
// In Harvester these are VirtualMachineImage CRDs.
type Image struct {
	ID          string `json:"id"`           // "namespace/resource-name" — pass this to CreateVM
	DisplayName string `json:"display_name"` // human-readable name shown in the UI
	Namespace   string `json:"namespace"`
}

// Network represents a VM network available in the compute provider.
// In Harvester these are NetworkAttachmentDefinition CRDs.
type Network struct {
	ID          string `json:"id"`           // "namespace/resource-name" — pass this to --network
	DisplayName string `json:"display_name"` // human-readable label (falls back to resource name)
	Namespace   string `json:"namespace"`
}

// Quota defines the resource limits for a tenant.
type Quota struct {
	TenantID   string    `json:"tenant_id"`
	TenantUUID uuid.UUID `json:"tenant_uuid"`
	MaxVMs     int       `json:"max_vms"`
	MaxClusters int   `json:"max_clusters"`
	MaxCPU     int    `json:"max_cpu"`      // Total vCPU across all VMs
	MaxMemoryGB int   `json:"max_memory_gb"`
}
