// Package models — private_endpoint.go
//
// M3 chunk 2: Private Endpoint domain types.
//
// A Private Endpoint is a generic, per-(target, vnet) network attachment.
// It allocates a VIP from the tenant's subnet CIDR and stands up a dual-NIC
// nginx proxy pod (on Harvester, in dc-api-endpoints namespace) that forwards
// to the target service's in-cluster backend. The target itself never has a
// NIC in any tenant network — only the proxy bridges in.
//
// Generic by design: the table is keyed by (target_type, target_id) so the
// same primitive backs Key Vault today and Postgres / Valkey / Harbor later.
// Each managed service provides a BackendResolver that maps a target_id to a
// backend address (e.g. an OpenBao Service IP, a CNPG read-write Service)
// and uses the generic Provisioner to do the rest.
package models

import (
	"time"

	"github.com/google/uuid"
)

// PrivateEndpointTargetType identifies the kind of managed service an endpoint
// fronts. Stored as TEXT in the DB so adding new types needs no schema change.
type PrivateEndpointTargetType string

const (
	PrivateEndpointTargetKeyVault PrivateEndpointTargetType = "key_vault"
	// PrivateEndpointTargetDatabase identifies a DNS-only host record for a
	// managed Database. Unlike key_vault endpoints these do NOT spin up a
	// proxy pod — the DB VM is already in the tenant VPC, so the Corefile
	// just needs an A record pointing at the VM's data-NIC IP. The row's
	// `proxy_pod_name` stays NULL; `backend_addr` is informational (the VM's
	// own ip:port) and never consumed by any proxy.
	PrivateEndpointTargetDatabase PrivateEndpointTargetType = "database"
)

// PrivateEndpointSpec is the caller-supplied intent to create a Private
// Endpoint. The service-specific handler resolves target_type, target_id,
// and the backend address; the generic provisioner consumes this shape.
type PrivateEndpointSpec struct {
	TenantID    string                    // who owns this
	TargetType  PrivateEndpointTargetType // what kind of service the endpoint fronts
	TargetID    uuid.UUID                 // FK-like to the service row (key_vaults.id today)
	Name        string                    // tenant-supplied, also becomes the hostname prefix
	VNetID      uuid.UUID                 // which VPC this endpoint lives in
	SubnetID    uuid.UUID                 // which subnet provides the VIP
	BackendAddr string                    // resolved by the service's BackendResolver
	// ServiceClass becomes the DNS suffix segment: "<name>.<service-class>.dc.internal"
	ServiceClass string
}

// PrivateEndpoint is the persisted Private Endpoint record. Mirrors the
// private_endpoints table.
type PrivateEndpoint struct {
	ID          uuid.UUID `json:"id"`
	TenantID    string    `json:"tenant_id"`
	TenantUUID  uuid.UUID `json:"tenant_uuid"`
	ProjectID   string    `json:"project_id"`
	ProjectUUID uuid.UUID `json:"project_uuid"`
	TargetType   PrivateEndpointTargetType `json:"target_type"`
	TargetID     uuid.UUID                 `json:"target_id"`
	VNetID       uuid.UUID                 `json:"vnet_id"`
	SubnetID     uuid.UUID                 `json:"subnet_id"`
	Name         string                    `json:"name"`
	IPAddress    string                    `json:"ip_address,omitempty"`
	Hostname     string                    `json:"hostname,omitempty"`
	BackendAddr  string                    `json:"-"` // internal — not surfaced to tenants
	ProxyPodName string                    `json:"-"` // internal
	Status       ResourceStatus            `json:"status"`
	Message      string                    `json:"message,omitempty"`
	CreatedAt    time.Time                 `json:"created_at"`
	UpdatedAt    time.Time                 `json:"updated_at"`
}
