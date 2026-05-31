// Package rbac — engine_test.go
//
// Unit tests for the RBAC v2 pure engine: wildcard matching, per-role grant/deny
// semantics for the built-in catalog, scope-chain inheritance, control/data
// separation, and the platform-admin short-circuit.
package rbac

import (
	"testing"

	"github.com/google/uuid"

	"github.com/wso2/dc-api/internal/models"
)

func TestMatchAction(t *testing.T) {
	cases := []struct {
		pattern, action string
		want            bool
	}{
		{"*", "compute/virtualMachines/write", true},
		{"*", "", true},
		{"compute/virtualMachines/write", "compute/virtualMachines/write", true},
		{"compute/virtualMachines/write", "compute/virtualMachines/read", false},
		{"*/read", "compute/virtualMachines/read", true},
		{"*/read", "compute/clusters/kubeconfig/read", true},
		{"*/read", "compute/virtualMachines/write", false},
		{"*/read", "read", false}, // needs the leading "/" before read
		{"compute/*", "compute/virtualMachines/write", true},
		{"compute/*", "computex", false}, // must not match across the slash boundary
		{"compute/*", "network/vnets/read", false},
		{"keyvault/*", "keyvault/vaults/secrets/read", true},
		{"network/*/read", "network/vnets/read", true},
		{"network/*/read", "network/vnets/write", false},
		{"a*b*c", "axxbyyc", true},
		{"a*b*c", "ac", false},
		{"a*b*c", "abc", true},
		{"keyvault/vaults/secrets/*", "keyvault/vaults/secrets/read", true},
		{"keyvault/vaults/secrets/*", "keyvault/vaults/read", false},
	}
	for _, c := range cases {
		if got := MatchAction(c.pattern, c.action); got != c.want {
			t.Errorf("MatchAction(%q, %q) = %v, want %v", c.pattern, c.action, got, c.want)
		}
	}
}

// permits is a small helper that resolves a built-in role and asks whether it
// grants the action on the given plane.
func permits(t *testing.T, roleKey, action string, isData bool) bool {
	t.Helper()
	d, ok := BuiltinRole(roleKey)
	if !ok {
		t.Fatalf("built-in role %q not found", roleKey)
	}
	return d.Permits(action, isData)
}

func TestBuiltinRolePermissions(t *testing.T) {
	cases := []struct {
		name    string
		role    string
		action  string
		isData  bool
		want    bool
	}{
		// Owner: everything, both planes.
		{"owner control any", RoleOwner, ActionRoleAssignmentWrite, false, true},
		{"owner vm delete", RoleOwner, ActionVMDelete, false, true},
		{"owner data secret", RoleOwner, ActionSecretRead, true, true},

		// Contributor: all control CRUD except access management; no data.
		{"contrib vm delete", RoleContributor, ActionVMDelete, false, true},
		{"contrib db delete", RoleContributor, ActionDBServerDelete, false, true},
		{"contrib cannot grant access", RoleContributor, ActionRoleAssignmentWrite, false, false},
		{"contrib cannot write roledefs", RoleContributor, ActionRoleDefinitionWrite, false, false},
		{"contrib can read assignments", RoleContributor, ActionRoleAssignmentRead, false, true},
		{"contrib no secret data", RoleContributor, ActionSecretRead, true, false},
		{"contrib cannot create SA", RoleContributor, ActionServiceAccountWrite, false, false},
		{"contrib cannot create project", RoleContributor, ActionProjectWrite, false, false},
		{"contrib cannot delete project", RoleContributor, ActionProjectDelete, false, false},
		{"contrib can read project", RoleContributor, ActionProjectRead, false, true},
		{"contrib cannot change quota", RoleContributor, ActionQuotaWrite, false, false},

		// Reader: */read control only; no writes; no data.
		{"reader vm read", RoleReader, ActionVMRead, false, true},
		{"reader vm write denied", RoleReader, ActionVMWrite, false, false},
		{"reader no kubeconfig data", RoleReader, ActionClusterKubeconfigRead, true, false},
		{"reader no secret data", RoleReader, ActionSecretRead, true, false},

		// User Access Administrator: manage authorization + read all; no resource writes.
		{"uaa grant access", RoleUserAccessAdministrator, ActionRoleAssignmentWrite, false, true},
		{"uaa read vm", RoleUserAccessAdministrator, ActionVMRead, false, true},
		{"uaa cannot write vm", RoleUserAccessAdministrator, ActionVMWrite, false, false},

		// VM Contributor: VMs fully; read images/networks; nothing else.
		{"vmc vm delete", RoleVirtualMachineContributor, ActionVMDelete, false, true},
		{"vmc vm start", RoleVirtualMachineContributor, ActionVMStart, false, true},
		{"vmc net read", RoleVirtualMachineContributor, ActionVNetRead, false, true},
		{"vmc net write denied", RoleVirtualMachineContributor, ActionVNetWrite, false, false},
		{"vmc vault read denied", RoleVirtualMachineContributor, ActionVaultRead, false, false},
		{"vmc image read", RoleVirtualMachineContributor, ActionImageRead, false, true},

		// Cluster Contributor: clusters + kubeconfig data.
		{"cc cluster write", RoleClusterContributor, ActionClusterWrite, false, true},
		{"cc kubeconfig data", RoleClusterContributor, ActionClusterKubeconfigRead, true, true},
		{"cc vm write denied", RoleClusterContributor, ActionVMWrite, false, false},

		// Network Contributor.
		{"nc subnet delete", RoleNetworkContributor, ActionSubnetDelete, false, true},
		{"nc vm read denied", RoleNetworkContributor, ActionVMRead, false, false},

		// Key Vault roles — the control/data split.
		{"kv admin vault write", RoleKeyVaultAdministrator, ActionVaultWrite, false, true},
		{"kv admin secret data", RoleKeyVaultAdministrator, ActionSecretWrite, true, true},
		{"kv secrets user reads value", RoleKeyVaultSecretsUser, ActionSecretRead, true, true},
		{"kv secrets user no write value", RoleKeyVaultSecretsUser, ActionSecretWrite, true, false},
		{"kv secrets user no vault control write", RoleKeyVaultSecretsUser, ActionVaultWrite, false, false},
		{"kv secrets user can read vault meta", RoleKeyVaultSecretsUser, ActionVaultRead, false, true},
		{"kv officer writes value", RoleKeyVaultSecretsOfficer, ActionSecretWrite, true, true},
		{"kv reader meta control", RoleKeyVaultReader, ActionSecretReadMetadata, false, true},
		{"kv reader no value data", RoleKeyVaultReader, ActionSecretRead, true, false},

		// Database roles.
		{"db contrib delete", RoleDatabaseContributor, ActionDBServerDelete, false, true},
		{"db reader read", RoleDatabaseReader, ActionDBServerRead, false, true},
		{"db reader write denied", RoleDatabaseReader, ActionDBServerWrite, false, false},
	}
	for _, c := range cases {
		if got := permits(t, c.role, c.action, c.isData); got != c.want {
			t.Errorf("%s: %s.Permits(%q, data=%v) = %v, want %v",
				c.name, c.role, c.action, c.isData, got, c.want)
		}
	}
}

func TestAuthorizeInheritance(t *testing.T) {
	tenantID := uuid.New()
	projectID := uuid.New()
	vmID := uuid.New()
	otherVMID := uuid.New()
	otherProjectID := uuid.New()

	tenantScope := ScopeRef{Type: models.ScopeTypeTenant, UUID: tenantID}
	projectScope := ScopeRef{Type: models.ScopeTypeProject, UUID: projectID}
	vmScope := ScopeRef{Type: models.ScopeTypeResource, UUID: vmID}

	// Chain for an op on an existing VM: resource → project → tenant.
	vmChain := []ScopeRef{vmScope, projectScope, tenantScope}
	// Chain for creating a VM in the project: project → tenant.
	createChain := []ScopeRef{projectScope, tenantScope}

	t.Run("tenant grant inherits to project and resource", func(t *testing.T) {
		a := []Assignment{{RoleDefKey: RoleVirtualMachineContributor, ScopeType: models.ScopeTypeTenant, ScopeUUID: tenantID}}
		if !Authorize(a, BuiltinResolver, ActionVMDelete, false, vmChain, false) {
			t.Error("tenant VMContributor should authorize deleting a VM beneath it")
		}
		if !Authorize(a, BuiltinResolver, ActionVMWrite, false, createChain, false) {
			t.Error("tenant VMContributor should authorize creating a VM in the project")
		}
	})

	t.Run("resource grant applies only to that resource", func(t *testing.T) {
		a := []Assignment{{RoleDefKey: RoleVirtualMachineContributor, ScopeType: models.ScopeTypeResource, ScopeUUID: vmID}}
		if !Authorize(a, BuiltinResolver, ActionVMDelete, false, vmChain, false) {
			t.Error("resource-scoped grant should authorize the target VM")
		}
		// A sibling VM in the same project must NOT be authorized.
		otherChain := []ScopeRef{{Type: models.ScopeTypeResource, UUID: otherVMID}, projectScope, tenantScope}
		if Authorize(a, BuiltinResolver, ActionVMDelete, false, otherChain, false) {
			t.Error("resource-scoped grant must not leak to a sibling VM")
		}
	})

	t.Run("project grant does not apply to a different project", func(t *testing.T) {
		a := []Assignment{{RoleDefKey: RoleNetworkContributor, ScopeType: models.ScopeTypeProject, ScopeUUID: projectID}}
		otherChain := []ScopeRef{{Type: models.ScopeTypeProject, UUID: otherProjectID}, tenantScope}
		if Authorize(a, BuiltinResolver, ActionVNetWrite, false, otherChain, false) {
			t.Error("project grant must not apply to a sibling project")
		}
	})

	t.Run("control role does not grant data action", func(t *testing.T) {
		a := []Assignment{{RoleDefKey: RoleReader, ScopeType: models.ScopeTypeTenant, ScopeUUID: tenantID}}
		if Authorize(a, BuiltinResolver, ActionClusterKubeconfigRead, true, vmChain, false) {
			t.Error("Reader (control */read) must not grant the kubeconfig DataAction")
		}
	})

	t.Run("additive: two roles union", func(t *testing.T) {
		a := []Assignment{
			{RoleDefKey: RoleVirtualMachineContributor, ScopeType: models.ScopeTypeTenant, ScopeUUID: tenantID},
			{RoleDefKey: RoleNetworkContributor, ScopeType: models.ScopeTypeProject, ScopeUUID: projectID},
		}
		if !Authorize(a, BuiltinResolver, ActionVMDelete, false, vmChain, false) {
			t.Error("VMContributor should still grant VM delete")
		}
		if !Authorize(a, BuiltinResolver, ActionVNetWrite, false, createChain, false) {
			t.Error("project NetworkContributor should grant vnet write in the project")
		}
	})

	t.Run("admin short-circuits", func(t *testing.T) {
		if !Authorize(nil, BuiltinResolver, ActionTenantDelete, false, vmChain, true) {
			t.Error("isAdmin must authorize regardless of assignments")
		}
	})

	t.Run("no assignments denies", func(t *testing.T) {
		if Authorize(nil, BuiltinResolver, ActionVMRead, false, vmChain, false) {
			t.Error("empty assignments must deny")
		}
	})

	t.Run("unknown role definition grants nothing", func(t *testing.T) {
		a := []Assignment{{RoleDefKey: "NoSuchRole", ScopeType: models.ScopeTypeTenant, ScopeUUID: tenantID}}
		if Authorize(a, BuiltinResolver, ActionVMRead, false, vmChain, false) {
			t.Error("unresolvable role key must grant nothing")
		}
	})
}

func TestBuiltinCatalogShape(t *testing.T) {
	roles := BuiltinRoles()
	if len(roles) != 13 {
		t.Fatalf("expected 13 built-in roles, got %d", len(roles))
	}
	for _, r := range roles {
		if !r.Builtin {
			t.Errorf("role %q should be marked Builtin", r.Key)
		}
		if !IsBuiltinKey(r.Key) {
			t.Errorf("IsBuiltinKey(%q) should be true", r.Key)
		}
	}
	if IsBuiltinKey("SomeCustomRole") {
		t.Error("IsBuiltinKey must be false for non-catalog keys")
	}
}

// TestRoleDefinitionBridgeMatchesBuiltins guards the transitional db-layer
// bridge: models.RoleDef* (the keys persisted by chunk-2) must equal the rbac
// built-in keys and resolve to real built-in roles. If someone renames a
// built-in key without updating the models bridge, this fails loudly.
func TestRoleDefinitionBridgeMatchesBuiltins(t *testing.T) {
	if models.RoleDefOwner != RoleOwner ||
		models.RoleDefContributor != RoleContributor ||
		models.RoleDefReader != RoleReader {
		t.Fatalf("models RBAC v2 bridge keys drifted from rbac built-in keys: %q/%q/%q vs %q/%q/%q",
			models.RoleDefOwner, models.RoleDefContributor, models.RoleDefReader,
			RoleOwner, RoleContributor, RoleReader)
	}
	for _, k := range []string{models.RoleDefOwner, models.RoleDefContributor, models.RoleDefReader} {
		if _, ok := BuiltinRole(k); !ok {
			t.Errorf("bridge key %q has no built-in role", k)
		}
	}
}
