// Package rbac — builtins.go
//
// The built-in role catalog (RBAC v2). Defined in code — versioned with the
// handlers, immutable, assignable at any scope. Keys are reserved PascalCase
// identifiers; custom roles use UUID keys so they can never collide. See
// docs/rbac-v2.md §4.
package rbac

import "sort"

// Built-in role keys. Use these constants when assigning built-in roles so
// typos surface at compile time.
const (
	RoleOwner                     = "Owner"
	RoleContributor               = "Contributor"
	RoleReader                    = "Reader"
	RoleUserAccessAdministrator   = "UserAccessAdministrator"
	RoleVirtualMachineContributor = "VirtualMachineContributor"
	RoleClusterContributor        = "ClusterContributor"
	RoleNetworkContributor        = "NetworkContributor"
	RoleKeyVaultAdministrator     = "KeyVaultAdministrator"
	RoleKeyVaultSecretsOfficer    = "KeyVaultSecretsOfficer"
	RoleKeyVaultSecretsUser       = "KeyVaultSecretsUser"
	RoleKeyVaultReader            = "KeyVaultReader"
	RoleDatabaseContributor       = "DatabaseContributor"
	RoleDatabaseReader            = "DatabaseReader"
)

// builtinRoles is the system catalog, keyed by RoleDefinition.Key.
var builtinRoles = map[string]RoleDefinition{
	RoleOwner: {
		Key:         RoleOwner,
		DisplayName: "Owner",
		Description: "Full control over everything in scope, including granting access and reading data.",
		Actions:     []string{"*"},
		DataActions: []string{"*"},
		Builtin:     true,
	},
	RoleContributor: {
		Key:         RoleContributor,
		DisplayName: "Contributor",
		Description: "Create, update, and delete all resources. Cannot manage access or service accounts, create/delete projects, change quotas, or read secret/credential data.",
		Actions:     []string{"*"},
		NotActions: []string{
			// No access management (role assignments / definitions) or service
			// accounts — that is Owner / User Access Administrator territory.
			"authorization/*/write",
			"authorization/*/delete",
			// No project lifecycle or quota changes — Owner only.
			"resourcemanager/projects/write",
			"resourcemanager/projects/delete",
			"resourcemanager/quotas/write",
		},
		Builtin: true,
	},
	RoleReader: {
		Key:         RoleReader,
		DisplayName: "Reader",
		Description: "Read all resource metadata. No writes, no data-plane access.",
		Actions:     []string{"*/read"},
		Builtin:     true,
	},
	RoleUserAccessAdministrator: {
		Key:         RoleUserAccessAdministrator,
		DisplayName: "User Access Administrator",
		Description: "Manage role assignments and definitions (plus read everything). No resource management.",
		Actions:     []string{"*/read", "authorization/*"},
		Builtin:     true,
	},
	RoleVirtualMachineContributor: {
		Key:         RoleVirtualMachineContributor,
		DisplayName: "Virtual Machine Contributor",
		Description: "Full control of virtual machines, plus read access to the images and networks they depend on.",
		Actions:     []string{"compute/virtualMachines/*", "compute/images/read", "network/*/read"},
		Builtin:     true,
	},
	RoleClusterContributor: {
		Key:         RoleClusterContributor,
		DisplayName: "Cluster Contributor",
		Description: "Manage RKE2 clusters and pull their kubeconfig, plus read images and networks.",
		Actions:     []string{"compute/clusters/*", "compute/images/read", "network/*/read"},
		DataActions: []string{"compute/clusters/kubeconfig/read"},
		Builtin:     true,
	},
	RoleNetworkContributor: {
		Key:         RoleNetworkContributor,
		DisplayName: "Network Contributor",
		Description: "Full control of all networking resources (vnets, subnets, NSGs, peerings, routes, DNS, private endpoints).",
		Actions:     []string{"network/*"},
		Builtin:     true,
	},
	RoleKeyVaultAdministrator: {
		Key:         RoleKeyVaultAdministrator,
		DisplayName: "Key Vault Administrator",
		Description: "Manage key vaults and their secret data.",
		Actions:     []string{"keyvault/*"},
		DataActions: []string{"keyvault/*"},
		Builtin:     true,
	},
	RoleKeyVaultSecretsOfficer: {
		Key:         RoleKeyVaultSecretsOfficer,
		DisplayName: "Key Vault Secrets Officer",
		Description: "Create, read, and delete secret values. Cannot manage the vault lifecycle.",
		Actions:     []string{"keyvault/vaults/read"},
		DataActions: []string{"keyvault/vaults/secrets/*"},
		Builtin:     true,
	},
	RoleKeyVaultSecretsUser: {
		Key:         RoleKeyVaultSecretsUser,
		DisplayName: "Key Vault Secrets User",
		Description: "Read secret values only.",
		Actions:     []string{"keyvault/vaults/read"},
		DataActions: []string{"keyvault/vaults/secrets/read"},
		Builtin:     true,
	},
	RoleKeyVaultReader: {
		Key:         RoleKeyVaultReader,
		DisplayName: "Key Vault Reader",
		Description: "Read vault and secret metadata. Cannot read secret values.",
		Actions:     []string{"keyvault/vaults/read", "keyvault/vaults/secrets/readMetadata"},
		Builtin:     true,
	},
	RoleDatabaseContributor: {
		Key:         RoleDatabaseContributor,
		DisplayName: "Database Contributor",
		Description: "Full control of managed database servers.",
		Actions:     []string{"database/*"},
		Builtin:     true,
	},
	RoleDatabaseReader: {
		Key:         RoleDatabaseReader,
		DisplayName: "Database Reader",
		Description: "Read managed database server metadata.",
		Actions:     []string{"database/servers/read"},
		Builtin:     true,
	},
}

// BuiltinRole returns the built-in role definition for key, or (zero, false) if
// no built-in has that key.
func BuiltinRole(key string) (RoleDefinition, bool) {
	d, ok := builtinRoles[key]
	return d, ok
}

// BuiltinRoles returns the full built-in catalog, sorted by Key for stable
// output (listings, tests, API responses).
func BuiltinRoles() []RoleDefinition {
	out := make([]RoleDefinition, 0, len(builtinRoles))
	for _, d := range builtinRoles {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// IsBuiltinKey reports whether key names a reserved built-in role. Custom-role
// authoring uses this to refuse keys that would shadow the catalog.
func IsBuiltinKey(key string) bool {
	_, ok := builtinRoles[key]
	return ok
}
