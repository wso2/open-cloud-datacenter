// Package endpoints provides the generic Private Endpoint provisioner used by
// every M3 managed service (Key Vault today; Postgres, Valkey, Harbor later).
//
// The provisioner is target-agnostic: it knows only how to allocate a VIP from
// a tenant subnet, stand up a dual-NIC nginx proxy that forwards to a backend
// address, and write the matching record into the tenant's per-VPC CoreDNS.
// What sits behind the backend address (an OpenBao Service, a CNPG primary
// Service, a Harbor LoadBalancer, …) is the service's concern, not the
// provisioner's.
//
// Each managed service implements a BackendResolver that maps a target_id to
// a backend (addr, port), and the generic handler wires the rest. See
// docs/spike-m3-keyvault.md for the network primitive this code implements
// and `wso2-datacenter-project#169` for the chunk plan.
package endpoints

import (
	"context"

	"github.com/google/uuid"
)

// HostRecord is a single (hostname, IP) pair written into the per-VPC
// CoreDNS Corefile's hosts block.
type HostRecord struct {
	Hostname  string
	IPAddress string
}

// ProvisionInput is the data the generic provisioner needs to create a
// Private Endpoint. The service-specific handler resolves all of it before
// calling Provision.
type ProvisionInput struct {
	EndpointID uuid.UUID // pre-allocated DB row UUID (used to name kube-ovn resources)
	TenantID   string

	// Network placement
	VNetBackendUID   string // KubeOVN Vpc CRD name — used to find the per-VPC Corefile ConfigMap
	SubnetBackendUID string // KubeOVN Subnet CRD name — used to allocate the Vip + Multus NAD ref
	SubnetCIDR       string // e.g. "10.231.1.0/24" — used by the provisioner to pick an IP in the subnet
	TenantNS         string // dc-<tenantID> — namespace where the Subnet's NAD lives

	// What the proxy forwards to
	BackendAddr string // e.g. "openbao.dc-api-vault.svc.cluster.local"
	BackendPort int    // e.g. 8200

	// MountPath is the OpenBao mount path for this vault, used by the nginx
	// rewrite rule. Tenants address the vault using the default "secret/" mount
	// in their Vault-SDK requests; nginx rewrites `/v1/secret/...` →
	// `/v1/<MountPath>/...` before forwarding. Example value:
	// "tenants/<tid>/vaults/<vault-id>". Empty MountPath skips the rewrite
	// (raw pass-through, useful for non-vault services in the future).
	MountPath string

	// Tenant-visible naming
	Name         string // tenant-supplied; used as the hostname prefix
	ServiceClass string // "kv" | "db" | "cache" | "registry" → DNS suffix segment

	// SiblingHosts are the host records for OTHER endpoints in the same VPC
	// (excluding the one being created). The provisioner appends this endpoint's
	// (hostname, allocated-IP) after IP allocation and rewrites the per-VPC
	// Corefile with the full list. Pass an empty slice if this is the first
	// endpoint in the VPC.
	SiblingHosts []HostRecord
}

// ProvisionResult is the outcome of a successful Provision call.
type ProvisionResult struct {
	IPAddress    string // VIP allocated in the tenant subnet
	Hostname     string // "<name>.<service-class>.dc.internal"
	ProxyPodName string // Deployment name in the dc-api-endpoints namespace
	VipName      string // KubeOVN Vip CRD name (handler stores for teardown)
}

// TeardownInput is the data the generic provisioner needs to remove a
// Private Endpoint. The service-specific handler looks the row up first.
type TeardownInput struct {
	EndpointID     uuid.UUID
	VNetBackendUID string

	// RemainingHosts is the full host record list to write into the per-VPC
	// Corefile AFTER this endpoint is torn down (i.e. the sibling list, NOT
	// including this endpoint). Pass an empty slice to clear the hosts block.
	RemainingHosts []HostRecord

	// ProxyPodName + VipName recorded at provision time. Empty values are
	// tolerated for idempotent retries; the provisioner derives them from
	// EndpointID if missing.
	ProxyPodName string
	VipName      string
}

// Provisioner is the generic Private Endpoint provisioner contract. The
// KubeOVN implementation lives in this same package; tests can supply a
// mock.
type Provisioner interface {
	Provision(ctx context.Context, in ProvisionInput) (ProvisionResult, error)
	Teardown(ctx context.Context, in TeardownInput) error

	// SyncCorefile rewrites the per-VPC Corefile with the given full host-record
	// list, without creating or destroying any proxy resources. Used by callers
	// that own host-record state in their own tables (e.g. the DBaaS DNS
	// reconciler) and need to keep CoreDNS in sync without touching nginx pods.
	SyncCorefile(ctx context.Context, vnetBackendUID string, hosts []HostRecord) error
}

// BackendResolver is implemented by each managed-service handler to translate
// a target resource ID into the network address that the proxy should forward
// to. KeyVault's resolver returns the OpenBao Service today; Postgres will
// return the CNPG read-write Service when M3 chunk 4 lands.
type BackendResolver interface {
	Resolve(ctx context.Context, targetID uuid.UUID) (addr string, port int, err error)
}
