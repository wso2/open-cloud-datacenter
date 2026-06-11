// Package rbac — actions.go
//
// The DC-API action taxonomy (RBAC v2). Each constant is a namespaced operation
// string of the form <provider>/<resourceType>[/<subType>]/<verb>. These strings
// are an API contract: once shipped they are never renamed or repurposed — only
// added or deprecated. See docs/rbac-v2.md §3.
//
// Control-plane actions describe operations on a resource's metadata/lifecycle.
// DataActions (flagged by IsDataAction) describe operations on a resource's
// *contents* — reading a secret value, pulling a kubeconfig, reading DB
// credentials — and are evaluated against a role's DataActions list, never its
// Actions list.
package rbac

// ── Compute provider ─────────────────────────────────────────────────────────
const (
	ActionVMRead    = "compute/virtualMachines/read"
	ActionVMWrite   = "compute/virtualMachines/write"
	ActionVMDelete  = "compute/virtualMachines/delete"
	ActionVMStart   = "compute/virtualMachines/start"
	ActionVMStop    = "compute/virtualMachines/stop"
	ActionVMRestart = "compute/virtualMachines/restart"

	ActionClusterRead           = "compute/clusters/read"
	ActionClusterWrite          = "compute/clusters/write"
	ActionClusterDelete         = "compute/clusters/delete"
	ActionClusterKubeconfigRead = "compute/clusters/kubeconfig/read" // DataAction

	ActionBastionRead   = "compute/bastions/read"
	ActionBastionWrite  = "compute/bastions/write"
	ActionBastionDelete = "compute/bastions/delete"

	ActionImageRead   = "compute/images/read"
	ActionImageWrite  = "compute/images/write"
	ActionImageDelete = "compute/images/delete"
)

// ── Network provider ─────────────────────────────────────────────────────────
const (
	ActionVNetRead   = "network/vnets/read"
	ActionVNetWrite  = "network/vnets/write"
	ActionVNetDelete = "network/vnets/delete"

	ActionSubnetRead   = "network/subnets/read"
	ActionSubnetWrite  = "network/subnets/write"
	ActionSubnetDelete = "network/subnets/delete"

	ActionNSGRead   = "network/nsgs/read"
	ActionNSGWrite  = "network/nsgs/write"
	ActionNSGDelete = "network/nsgs/delete"

	ActionPeeringRead   = "network/peerings/read"
	ActionPeeringWrite  = "network/peerings/write"
	ActionPeeringDelete = "network/peerings/delete"

	ActionRouteTableRead   = "network/routeTables/read"
	ActionRouteTableWrite  = "network/routeTables/write"
	ActionRouteTableDelete = "network/routeTables/delete"

	ActionDNSZoneRead   = "network/dnsZones/read"
	ActionDNSZoneWrite  = "network/dnsZones/write"
	ActionDNSZoneDelete = "network/dnsZones/delete"

	ActionPrivateEndpointRead   = "network/privateEndpoints/read"
	ActionPrivateEndpointWrite  = "network/privateEndpoints/write"
	ActionPrivateEndpointDelete = "network/privateEndpoints/delete"

	ActionProviderNetworkRead = "network/providerNetworks/read"
)

// ── Key Vault provider ───────────────────────────────────────────────────────
const (
	ActionVaultRead   = "keyvault/vaults/read"
	ActionVaultWrite  = "keyvault/vaults/write"
	ActionVaultDelete = "keyvault/vaults/delete"

	ActionSecretRead         = "keyvault/vaults/secrets/read"         // DataAction (value)
	ActionSecretWrite        = "keyvault/vaults/secrets/write"        // DataAction
	ActionSecretDelete       = "keyvault/vaults/secrets/delete"       // DataAction
	ActionSecretReadMetadata = "keyvault/vaults/secrets/readMetadata" // control (names, not values)

	ActionVaultCredentialsRead = "keyvault/vaults/credentials/read" // DataAction (vault connection creds)
)

// ── Database provider ────────────────────────────────────────────────────────
const (
	ActionDBServerRead   = "database/servers/read"
	ActionDBServerWrite  = "database/servers/write"
	ActionDBServerDelete = "database/servers/delete"

	ActionDBCredentialsRead = "database/servers/credentials/read" // DataAction
)

// ── Authorization provider (access management) ───────────────────────────────
const (
	ActionRoleAssignmentRead   = "authorization/roleAssignments/read"
	ActionRoleAssignmentWrite  = "authorization/roleAssignments/write"
	ActionRoleAssignmentDelete = "authorization/roleAssignments/delete"

	ActionRoleDefinitionRead   = "authorization/roleDefinitions/read"
	ActionRoleDefinitionWrite  = "authorization/roleDefinitions/write"
	ActionRoleDefinitionDelete = "authorization/roleDefinitions/delete"

	ActionServiceAccountRead   = "authorization/serviceAccounts/read"
	ActionServiceAccountWrite  = "authorization/serviceAccounts/write"
	ActionServiceAccountDelete = "authorization/serviceAccounts/delete"
)

// ── Resource manager provider (org hierarchy) ────────────────────────────────
const (
	ActionTenantRead   = "resourcemanager/tenants/read"
	ActionTenantWrite  = "resourcemanager/tenants/write"
	ActionTenantDelete = "resourcemanager/tenants/delete"

	ActionProjectRead   = "resourcemanager/projects/read"
	ActionProjectWrite  = "resourcemanager/projects/write"
	ActionProjectDelete = "resourcemanager/projects/delete"

	ActionQuotaRead  = "resourcemanager/quotas/read"
	ActionQuotaWrite = "resourcemanager/quotas/write"

	ActionCapUsageRead = "resourcemanager/capUsage/read"

	// ActionActivityRead gates the read-only project activity feed (audit
	// events). A plain control-plane read: Reader's `*/read` covers it, so any
	// project member can see the feed.
	ActionActivityRead = "resourcemanager/activity/read"
)

// dataActions is the set of concrete actions that touch resource *contents*
// (data plane). A request for one of these is evaluated against a role's
// DataActions list. Everything else is control-plane.
var dataActions = map[string]struct{}{
	ActionClusterKubeconfigRead: {},
	ActionSecretRead:            {},
	ActionSecretWrite:           {},
	ActionSecretDelete:          {},
	ActionVaultCredentialsRead:  {},
	ActionDBCredentialsRead:     {},
}

// IsDataAction reports whether the action is a data-plane action. Callers use it
// to decide which list (Actions vs DataActions) Authorize should evaluate.
func IsDataAction(action string) bool {
	_, ok := dataActions[action]
	return ok
}

// allActions is every concrete action DC-API knows about — the enumerable
// registry a future custom-role picker presents to users.
var allActions = []string{
	// compute
	ActionVMRead, ActionVMWrite, ActionVMDelete, ActionVMStart, ActionVMStop, ActionVMRestart,
	ActionClusterRead, ActionClusterWrite, ActionClusterDelete, ActionClusterKubeconfigRead,
	ActionBastionRead, ActionBastionWrite, ActionBastionDelete,
	ActionImageRead, ActionImageWrite, ActionImageDelete,
	// network
	ActionVNetRead, ActionVNetWrite, ActionVNetDelete,
	ActionSubnetRead, ActionSubnetWrite, ActionSubnetDelete,
	ActionNSGRead, ActionNSGWrite, ActionNSGDelete,
	ActionPeeringRead, ActionPeeringWrite, ActionPeeringDelete,
	ActionRouteTableRead, ActionRouteTableWrite, ActionRouteTableDelete,
	ActionDNSZoneRead, ActionDNSZoneWrite, ActionDNSZoneDelete,
	ActionPrivateEndpointRead, ActionPrivateEndpointWrite, ActionPrivateEndpointDelete,
	ActionProviderNetworkRead,
	// keyvault
	ActionVaultRead, ActionVaultWrite, ActionVaultDelete,
	ActionSecretRead, ActionSecretWrite, ActionSecretDelete, ActionSecretReadMetadata, ActionVaultCredentialsRead,
	// database
	ActionDBServerRead, ActionDBServerWrite, ActionDBServerDelete, ActionDBCredentialsRead,
	// authorization
	ActionRoleAssignmentRead, ActionRoleAssignmentWrite, ActionRoleAssignmentDelete,
	ActionRoleDefinitionRead, ActionRoleDefinitionWrite, ActionRoleDefinitionDelete,
	ActionServiceAccountRead, ActionServiceAccountWrite, ActionServiceAccountDelete,
	// resourcemanager
	ActionTenantRead, ActionTenantWrite, ActionTenantDelete,
	ActionProjectRead, ActionProjectWrite, ActionProjectDelete,
	ActionQuotaRead, ActionQuotaWrite,
	ActionCapUsageRead,
	ActionActivityRead,
}

// AllActions returns a copy of the full action registry.
func AllActions() []string {
	out := make([]string, len(allActions))
	copy(out, allActions)
	return out
}
