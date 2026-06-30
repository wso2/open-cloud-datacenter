// Package models — network.go
//
// Domain types for M2 networking resources: VNet, Subnet, RouteTable,
// NetworkSecurityGroup, VNetPeering, PrivateDnsZone, and DnsRecord.
//
// These are PURE Go types — no DB tags, no JSON tags on internal-only fields.
// They represent the domain layer: what things ARE, not how they are stored
// or serialised. This matches the design of the existing resource.go.
//
// Status fields reuse models.ResourceStatus (PENDING/ACTIVE/FAILED/DELETING)
// so the reconciler and handlers share one enum for all resource kinds.
package models

import (
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────── VNet ───────────────────────────────────────────

// VNetSpec is the caller-supplied intent for creating a Virtual Network.
// The handler validates all fields before passing to the provider.
type VNetSpec struct {
	Name         string   // Unique within the tenant; [a-z0-9-], starts with a letter
	AddressSpace []string // One or more RFC1918 CIDRs; max 5 in M2
	Region       string   // Must exist in the regions table
	Description  string   // Free-text; max 256 chars
}

// VNetResource is the provider's view of a running VNet.
// Returned by CreateVNet and GetVNet; mirrors what the DB row stores.
type VNetResource struct {
	BackendUID string         // KubeOVN Vpc CRD name
	Status     ResourceStatus
	Message    string
}

// VNet is the full DC-API representation of a Virtual Network, as returned
// by list/get handlers and stored in the vnets table.
type VNet struct {
	ID          uuid.UUID `json:"id"`
	TenantID    string    `json:"tenant_id"`
	TenantUUID  uuid.UUID `json:"tenant_uuid"`
	ProjectID   string    `json:"project_id"`
	ProjectUUID uuid.UUID `json:"project_uuid"`
	Name         string         `json:"name"`
	Region       string         `json:"region"`
	// Zone is the user-facing availability-zone placement of the VNet, companion
	// to Region. A VNet is a root resource: callers MAY select a zone at create
	// time (validated against the regions/zones catalog); when omitted the zone
	// is stamped from DCAPI_LOCAL_ZONE in CreateVNet. Children (subnet/peering/
	// VM/cluster/etc.) inherit this zone. Surfaced read-only in vnetResponse.
	Zone         string         `json:"zone,omitempty"`
	AddressSpace []string       `json:"address_space"`
	Description  string         `json:"description,omitempty"`
	Status       ResourceStatus `json:"status"`
	BackendUID   string         `json:"-"` // internal — not surfaced in API responses
	ProviderType string         `json:"provider_type"`
	Message      string         `json:"message,omitempty"`
	// DNSServerIP is the per-VPC CoreDNS pod IP (F20). Empty until EnsureVpcDNS
	// runs for this VPC. Internal field — not serialised in API responses.
	DNSServerIP  string         `json:"-"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

// ─────────────────────────── Subnet ──────────────────────────────────────────

// SubnetSpec is the caller-supplied intent for creating a Subnet.
// CIDR must be contained in at least one of the parent VNet's AddressSpace entries.
// Gateway defaults to the first usable IP in the CIDR if omitted.
type SubnetSpec struct {
	Name        string // Unique within the VNet
	CIDR        string // IPv4 CIDR; must be within parent VNet's address_space
	Gateway     string // Optional; defaults to first usable IP in CIDR
	Description string
}

// SubnetResource is the provider's view of a running Subnet.
type SubnetResource struct {
	BackendUID string         // KubeOVN Subnet CRD name
	Status     ResourceStatus
	Message    string
}

// Subnet is the full DC-API representation of a Subnet row.
type Subnet struct {
	ID          uuid.UUID `json:"id"`
	VNetID      uuid.UUID `json:"vnet_id"`
	TenantID    string    `json:"tenant_id"`
	TenantUUID  uuid.UUID `json:"tenant_uuid"`
	ProjectID   string    `json:"project_id"`
	ProjectUUID uuid.UUID `json:"project_uuid"`
	Name         string         `json:"name"`
	CIDR         string         `json:"cidr"`
	Gateway      string         `json:"gateway,omitempty"`
	Description  string         `json:"description,omitempty"`
	Status       ResourceStatus `json:"status"`
	BackendUID   string         `json:"-"`
	ProviderType string         `json:"provider_type"`
	Message      string         `json:"message,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

// ─────────────────────────── Route Table ──────────────────────────────────────

// RouteRule is a single entry in a RouteTable.
// The KubeOVN driver translates these to Vpc.spec.staticRoutes entries,
// tagging each with the parent route-table UUID in a comment for clean removal.
type RouteRule struct {
	Name            string `json:"name"`             // Unique within the route table
	DestinationCIDR string `json:"destination_cidr"` // Valid CIDR or "0.0.0.0/0"
	// NextHopType: "vnet_local" | "internet" | "virtual_appliance" | "none"
	NextHopType string `json:"next_hop_type"`
	// NextHopIP: required when NextHopType is "virtual_appliance".
	// Must be a valid IP within a subnet of the parent VNet.
	NextHopIP string `json:"next_hop_ip,omitempty"`
}

// RouteTableSpec is the caller-supplied intent for a RouteTable.
type RouteTableSpec struct {
	Name        string      // Unique within the VNet
	Description string
	Routes      []RouteRule // Empty slice is valid (table with no rules)
}

// RouteTableResource is the provider's view of a RouteTable.
// Because routes are patched onto the Vpc CRD (not their own CRD), backend_uid
// stores the parent Vpc name so the driver can locate the right CRD.
type RouteTableResource struct {
	BackendUID string // KubeOVN Vpc CRD name (routes live on the VPC)
	Status     ResourceStatus
	Message    string
}

// RouteTable is the full DC-API representation of a route_tables row.
type RouteTable struct {
	ID          uuid.UUID `json:"id"`
	VNetID      uuid.UUID `json:"vnet_id"`
	TenantID    string    `json:"tenant_id"`
	TenantUUID  uuid.UUID `json:"tenant_uuid"`
	ProjectID   string    `json:"project_id"`
	ProjectUUID uuid.UUID `json:"project_uuid"`
	Name         string         `json:"name"`
	Description  string         `json:"description,omitempty"`
	Routes       []RouteRule    `json:"routes"`
	Status       ResourceStatus `json:"status"`
	BackendUID   string         `json:"-"`
	ProviderType string         `json:"provider_type"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

// ─────────────────────────── NSG ──────────────────────────────────────────────

// NSGRule is a single inbound or outbound security rule.
// Priority must be 100–4096; lower numbers have higher precedence.
// Uniqueness constraint: (direction, priority) within one NSG.
type NSGRule struct {
	Name                     string `json:"name"`
	Direction                string `json:"direction"`                  // "inbound" | "outbound"
	Priority                 int    `json:"priority"`                   // 100–4096
	Protocol                 string `json:"protocol"`                   // tcp | udp | icmp | *
	SourceAddressPrefix      string `json:"source_address_prefix"`      // CIDR, *, VnetLocal
	SourcePortRange          string `json:"source_port_range"`          // port, *, or range
	DestinationAddressPrefix string `json:"destination_address_prefix"` // same as source
	DestinationPortRange     string `json:"destination_port_range"`     // same as source_port_range
	Action                   string `json:"action"`                     // "allow" | "deny"
}

// NSGSpec is the caller-supplied intent for creating an NSG.
// Rules may be empty (a no-op NSG with no rules).
type NSGSpec struct {
	Name        string    // Unique within the tenant
	Description string
	Rules       []NSGRule // Replaced wholesale on PUT /rules
}

// NSGResource is the provider's view of an NSG.
// For subnet attachments, backend_uid is the target Subnet CRD name.
// For NIC attachments (M3), it will be the SecurityGroup CRD name.
type NSGResource struct {
	BackendUID string
	Status     ResourceStatus
	Message    string
}

// NSG is the full DC-API representation of a network_security_groups row,
// including its rules and attachments.
type NSG struct {
	ID          uuid.UUID `json:"id"`
	TenantID    string    `json:"tenant_id"`
	TenantUUID  uuid.UUID `json:"tenant_uuid"`
	ProjectID   string    `json:"project_id"`
	ProjectUUID uuid.UUID `json:"project_uuid"`
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	Rules        []NSGRule       `json:"rules"`
	Attachments  []NSGAttachment `json:"attachments"`
	Status       ResourceStatus  `json:"status"`
	BackendUID   string          `json:"-"`
	ProviderType string          `json:"provider_type"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

// NSGAttachment associates an NSG with a subnet (M2) or NIC (M3).
// The M2 handler rejects target_type="nic" with HTTP 400.
type NSGAttachment struct {
	ID          uuid.UUID `json:"id"`
	NSGiD       uuid.UUID `json:"sg_id"`
	TenantID    string    `json:"tenant_id"`
	TenantUUID  uuid.UUID `json:"tenant_uuid"`
	ProjectID   string    `json:"project_id"`
	ProjectUUID uuid.UUID `json:"project_uuid"`
	TargetType string    `json:"target_type"` // "subnet" | "nic"
	TargetID   uuid.UUID `json:"target_id"`
	CreatedAt  time.Time `json:"created_at"`
}

// ─────────────────────────── Peering ──────────────────────────────────────────

// PeeringSpec is the caller-supplied intent for a VNetPeering.
// PeerVNetID must belong to the same tenant (cross-tenant peering is 404 per §14).
// AllowForwardedTraffic is stored but not enforced in M2 (the response includes
// a warning field explaining this).
type PeeringSpec struct {
	Name                   string    // Unique within the requesting VNet
	PeerVNetID             uuid.UUID // UUID of the target VNet (same tenant only)
	AllowForwardedTraffic  bool      // No-op in M2; stored for M2.5
	// AddressSpace and PeerAddressSpace are the CIDRs of the local and remote
	// VNets respectively. Used at peering time to install reciprocal static
	// routes for the whole VNet (Azure-style semantics — entire peer
	// address-space is reachable post-peering). Without these, a peering
	// against a VNet with no subnets produces no routes.
	AddressSpace     []string
	PeerAddressSpace []string
	// TransitCIDR (F6) is the /24 the driver should use for OVN's transit
	// link between the two VPCs. Allocated by the peering handler via
	// db.Repository.AllocateTransitCIDR. Empty means "use legacy hash" —
	// kept as a fallback so pre-F6 peerings that were created without an
	// allocator row keep working on the existing localConnectIP they
	// already advertise on the Vpc CRD.
	TransitCIDR string
}

// PeeringResource is the provider's view of a peering.
// backend_uid is the KubeOVN VpcPeering CRD name.
type PeeringResource struct {
	BackendUID string
	Status     ResourceStatus
	Message    string
}

// Peering is the full DC-API representation of a peerings row.
type Peering struct {
	ID         uuid.UUID `json:"id"`
	VNetID     uuid.UUID `json:"vnet_id"`
	PeerVNetID uuid.UUID `json:"peer_vnet_id"`
	TenantID   string    `json:"tenant_id"`
	TenantUUID uuid.UUID `json:"tenant_uuid"`
	ProjectID   string    `json:"project_id"`
	ProjectUUID uuid.UUID `json:"project_uuid"`
	Name                   string         `json:"name"`
	AllowForwardedTraffic  bool           `json:"allow_forwarded_traffic"`
	Status                 ResourceStatus `json:"status"`
	BackendUID             string         `json:"-"`
	ProviderType           string         `json:"provider_type"`
	CreatedAt              time.Time      `json:"created_at"`
	UpdatedAt              time.Time      `json:"updated_at"`
}

// ─────────────────────────── DNS Zone ─────────────────────────────────────────

// DnsZoneSpec is the caller-supplied intent for a PrivateDnsZone.
// ZoneName must be a valid DNS name (e.g. "internal.lk.wso2.com").
// Cross-tenant collisions on zone name are explicitly allowed (§14).
type DnsZoneSpec struct {
	ZoneName    string // DNS zone name; validated as a valid DNS label sequence
	Description string
}

// DnsZoneResource is the provider's view of a DNS zone.
// backend_uid is the KubeOVN VpcDns CRD name.
type DnsZoneResource struct {
	BackendUID string
	Status     ResourceStatus
	Message    string
}

// PrivateDnsZone is the full DC-API representation of a private_dns_zones row.
type PrivateDnsZone struct {
	ID          uuid.UUID `json:"id"`
	VNetID      uuid.UUID `json:"vnet_id"`
	TenantID    string    `json:"tenant_id"`
	TenantUUID  uuid.UUID `json:"tenant_uuid"`
	ProjectID   string    `json:"project_id"`
	ProjectUUID uuid.UUID `json:"project_uuid"`
	ZoneName     string         `json:"name"`
	Description  string         `json:"description,omitempty"`
	Status       ResourceStatus `json:"status"`
	BackendUID   string         `json:"-"`
	ProviderType string         `json:"provider_type"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

// ─────────────────────────── DNS Record ──────────────────────────────────────

// DnsRecord represents a single DNS record within a PrivateDnsZone.
// Multi-value records (e.g. round-robin A records) use the Values slice.
// DNS record operations are synchronous — they patch a ConfigMap in KubeOVN.
type DnsRecord struct {
	ID          uuid.UUID `json:"id"`
	ZoneID      uuid.UUID `json:"zone_id"`
	TenantID    string    `json:"tenant_id"`
	TenantUUID  uuid.UUID `json:"tenant_uuid"`
	ProjectID   string    `json:"project_id"`
	ProjectUUID uuid.UUID `json:"project_uuid"`
	RecordType string    `json:"type"`   // A | AAAA | CNAME | SRV | TXT | MX
	Name       string    `json:"name"`   // Relative within the zone (e.g. "api")
	Values     []string  `json:"values"` // One or more record values
	TTL        int       `json:"ttl"`    // Seconds; default 300; min 30; max 86400
	CreatedAt  time.Time `json:"created_at"`
}

// ─────────────────────────── Quota extensions ────────────────────────────────

// NetworkQuota extends the base Quota with M2 networking limits.
// The handler checks these before allowing new VNet / Subnet creates.
// The DB stores these as columns on the existing quotas table.
type NetworkQuota struct {
	TenantID           string    `json:"tenant_id"`
	TenantUUID         uuid.UUID `json:"tenant_uuid"`
	MaxVNets           int       `json:"max_vnets"`
	MaxPublicIPs       int    `json:"max_public_ips"`
	MaxSubnetsPerVNet  int    `json:"max_subnets_per_vnet"`
}
