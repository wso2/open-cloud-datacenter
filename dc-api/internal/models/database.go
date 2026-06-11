// Package models — database.go
//
// Task 1 v1: Database (DBaaS) domain types. A Database is a project-scoped
// managed PostgreSQL instance backed by the dbaas controller (one Resource =
// one KubeVirt VM, 1:1 model — no shared Backend).
//
// The dbaas controller is the operator at github.com/wso2/open-cloud-datacenter
// (the dbaas/ active fork during development). dc-api creates DBInstance CRs
// of group dbaas.opencloud.wso2.com/v1alpha1 and watches their status —
// it never imports the operator's Go types directly.
//
// Forward-compat reservations (accepted on POST today, behaviour deferred):
//   - EngineVersion: stored but not acted on by the controller until the
//     multi-version work in Task 2 lands.
//   - Backups, BYOK, SSL toggling: Task 2 scope.
package models

import (
	"time"

	"github.com/google/uuid"
)

// DatabaseEngine is the family of DB engines the platform supports.
// v1 is Postgres-only; Task 2 adds MySQL/MSSQL.
type DatabaseEngine string

const (
	DatabaseEnginePostgres DatabaseEngine = "postgres"
)

// DatabaseNetworkMode tells dc-api how to resolve the controller's
// spec.networkRef. The choice is per-instance, not per-tenant — a project
// can have some Databases on KubeOVN VPCs and others on legacy bridge NADs.
type DatabaseNetworkMode string

const (
	// DatabaseNetworkModeVPC resolves to a KubeOVN-managed NAD via
	// (VNetID, SubnetID). The NAD already exists — KubeOVN provisions it at
	// subnet create time. dc-api looks up the subnet row and derives the NAD
	// identity as (project-namespace, subnet.BackendUID).
	DatabaseNetworkModeVPC DatabaseNetworkMode = "vpc"
	// DatabaseNetworkModeLegacy passes the caller-supplied NadRef ("ns/name")
	// straight through to the controller. Used today by lk prod where VMs
	// attach to a pre-provisioned Multus bridge NAD on a VLAN.
	DatabaseNetworkModeLegacy DatabaseNetworkMode = "legacy"
)

// Database is the persisted dc-api record. Mirrors the databases table.
// dbaas controller-side state (VM phase, endpoint IP allocated by Multus,
// credentials secret name) lives on the DBInstance CR; the handler overlays
// it onto the response on GET.
type Database struct {
	ID          uuid.UUID `json:"id"`
	TenantID    string    `json:"tenant_id"`
	TenantUUID  uuid.UUID `json:"tenant_uuid"`
	ProjectID   string    `json:"project_id"`
	ProjectUUID uuid.UUID `json:"project_uuid"`

	Name               string         `json:"name"`
	Engine             DatabaseEngine `json:"engine"`
	EngineVersion      string         `json:"engine_version,omitempty"` // informational in v1
	InstanceClass      string         `json:"instance_class"`           // dbaas RDS-style class, e.g. "db.t3.medium"
	AllocatedStorageGB int            `json:"allocated_storage_gb"`

	// Network selection. Exactly one of (VNetID+SubnetID) or NadRef is set;
	// the handler enforces this on create. NetworkMode says which.
	NetworkMode DatabaseNetworkMode `json:"network_mode"`
	VNetID      *uuid.UUID          `json:"vnet_id,omitempty"`
	SubnetID    *uuid.UUID          `json:"subnet_id,omitempty"`
	NadRef      string              `json:"nad_ref,omitempty"`

	Status  ResourceStatus `json:"status"`
	Message string         `json:"message,omitempty"`

	// EndpointAddress / EndpointPort are populated from the live CR status
	// once the controller has assigned an IP on the data NIC. Empty until
	// status=ACTIVE.
	EndpointAddress string `json:"endpoint_address,omitempty"`
	EndpointPort    int    `json:"endpoint_port,omitempty"`

	// EndpointHostname is the DNS name registered in the tenant's per-VPC
	// CoreDNS once the DatabaseDNSReconciler has run for this DB. Resolves
	// to EndpointAddress inside the VPC. Empty for legacy-mode (non-VPC)
	// databases and until the reconciler has registered the record.
	// Customers should prefer this over EndpointAddress for client config.
	EndpointHostname string `json:"endpoint_hostname,omitempty"`

	// CredentialsConsumedAt is nil until GET .../credentials has been called
	// once; subsequent calls return 410 Gone (shown-once — same as KeyVault,
	// per managed-services contract §8).
	CredentialsConsumedAt *time.Time `json:"credentials_consumed_at,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DatabaseInstanceClasses is the catalog of RDS-style sizes the dbaas
// controller accepts on spec.dbInstanceClass. dc-api validates the caller's
// instance_class against this set before forwarding to the operator —
// rejecting unknown classes with a 400 instead of leaving the row PENDING
// while the controller fails admission.
//
// Values mirror api/v1alpha1/dbinstance_types.go in the dbaas repo. Keep in
// sync when the catalog grows.
var DatabaseInstanceClasses = map[string]struct{}{
	"db.t3.micro":   {},
	"db.t3.small":   {},
	"db.t3.medium":  {},
	"db.t3.large":   {},
	"db.t3.xlarge":  {},
	"db.t3.2xlarge": {},
	"db.m5.large":   {},
	"db.m5.xlarge":  {},
	"db.m5.2xlarge": {},
	"db.r5.large":   {},
	"db.r5.xlarge":  {},
	"db.r5.2xlarge": {},
}

// ValidDatabaseInstanceClass reports whether s is one of the catalog entries.
func ValidDatabaseInstanceClass(s string) bool {
	_, ok := DatabaseInstanceClasses[s]
	return ok
}
