# Module: management/tenant-space

Provisions a complete isolated workspace for a product team within the Harvester cluster:
a Rancher project with resource quotas, a primary namespace, and flexible role bindings for
any combination of groups and roles.

Use this module once per product team. Each call is independent — teams do not share state.

## When to Use

Use `tenant-space` when onboarding a product team that needs:

- A dedicated Rancher project with enforced CPU/memory/storage quotas
- An initial namespace to deploy workloads into
- Specific Rancher roles assigned to an LDAP/OIDC group (e.g. full ownership, or read-only metrics access)

For lightweight bulk project creation without role bindings, see [`rbac`](../rbac/README.md).

## Prerequisites

| Prerequisite | Where it comes from |
|---|---|
| Rancher cluster ID | `module.harvester_integration.harvester_cluster_id` |
| Custom role template IDs | `module.cluster_roles.vm_metrics_observer_role_id` (or other custom roles) |
| Group principal ID | Rancher's representation of an OIDC/LDAP group (visible in Rancher UI under Authentication) |

## Requirements

| Name | Version |
|------|---------|
| terraform | >= 1.3 |
| rancher/rancher2 | ~> 13.1 |

## Usage

### Full Team Onboarding

The team owns the project and can also view VM metrics:

```hcl
module "cluster_roles" {
  source = "github.com/wso2-enterprise/open-cloud-datacenter//modules/management/cluster-roles?ref=v0.1.x"
}

module "tenant_space_iam" {
  source = "github.com/wso2-enterprise/open-cloud-datacenter//modules/management/tenant-space?ref=v0.1.x"

  cluster_id    = module.harvester_integration.harvester_cluster_id
  project_name  = "iam-team"
  cpu_limit     = "8"
  memory_limit  = "16Gi"
  storage_limit = "200Gi"

  group_role_bindings = [
    {
      group_principal_id = "openid://iam-engineers"   # OIDC group claim value
      role_template_id   = "project-member"           # built-in: can create/manage resources
    },
    {
      group_principal_id = "openid://iam-engineers"
      role_template_id   = module.cluster_roles.vm_metrics_observer_role_id  # see VM metrics
    },
  ]
}
```

### Read-Only Observer (no project ownership)

A user or group that should only see VM metrics in the Harvester dashboard, nothing else:

```hcl
module "tenant_space_iam" {
  source = "github.com/wso2-enterprise/open-cloud-datacenter//modules/management/tenant-space?ref=v0.1.x"

  cluster_id    = module.harvester_integration.harvester_cluster_id
  project_name  = "iam-team"
  cpu_limit     = "8"
  memory_limit  = "16Gi"
  storage_limit = "200Gi"

  group_role_bindings = [
    # project-member intentionally omitted — observer only
    {
      group_principal_id = "openid://iam-observers"
      role_template_id   = module.cluster_roles.vm_metrics_observer_role_id
    },
  ]
}
```

### Multiple Groups with Different Roles

Bind an engineering group as owners and a separate ops group as observers:

```hcl
group_role_bindings = [
  { group_principal_id = "openid://iam-engineers", role_template_id = "project-member" },
  { group_principal_id = "openid://iam-engineers", role_template_id = module.cluster_roles.vm_metrics_observer_role_id },
  { group_principal_id = "openid://dc-ops",        role_template_id = module.cluster_roles.vm_metrics_observer_role_id },
]
```

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|----------|
| `cluster_id` | Rancher cluster ID of the Harvester HCI cluster | `string` | — | yes |
| `project_name` | Project name (lowercase, hyphen-separated) | `string` | — | yes |
| `namespace_name` | Primary namespace name. Defaults to `<project_name>-ns` | `string` | `null` | no |
| `cpu_limit` | Total CPU limit for the project (e.g. `"8"`, `"500m"`) | `string` | — | yes |
| `memory_limit` | Total memory limit for the project (e.g. `"16Gi"`) | `string` | — | yes |
| `storage_limit` | Total persistent storage limit (e.g. `"200Gi"`) | `string` | — | yes |
| `namespace_cpu_limit` | Per-namespace default CPU limit. Defaults to `cpu_limit` | `string` | `null` | no |
| `namespace_memory_limit` | Per-namespace default memory limit. Defaults to `memory_limit` | `string` | `null` | no |
| `namespace_storage_limit` | Per-namespace default storage limit. Defaults to `storage_limit` | `string` | `null` | no |
| `group_role_bindings` | List of `{ group_principal_id, role_template_id }` pairs | `list(object)` | `[]` | no |

### Resource Quota Notes

- `cpu_limit` / `memory_limit` / `storage_limit` apply to the **project aggregate** — the
  total across all namespaces in the project.
- `namespace_*` variants set the **per-namespace default** that Rancher enforces when a new
  namespace is created inside the project. If not set, they default to the project limits.
- When the project has a single namespace (the default), setting project and namespace limits
  to the same value is correct.
- When multiple namespaces are expected, set `namespace_*` limits to a fraction of the
  project total (e.g. project `cpu_limit = "16"`, `namespace_cpu_limit = "8"` for two namespaces).

### Group Principal ID Format

The `group_principal_id` format depends on your auth provider configured in Rancher:

| Auth Provider | Format example |
|---|---|
| Local | `local://group-id` |
| Generic OIDC (Asgardeo) | `openid://group-claim-value` |
| LDAP / AD | `ldap://cn=team,ou=groups,dc=example,dc=com` |

The exact value is visible in Rancher UI under **Users & Auth → Groups** after the group
has signed in at least once.

## Outputs

| Name | Description |
|------|-------------|
| `project_id` | Rancher project ID — use as input to workload cluster modules |
| `project_name` | Project name |
| `namespace_id` | ID of the primary namespace |
| `namespace_name` | Name of the primary namespace |

## Notes

- Each `tenant-space` module call is independent. Adding a new team = new module block.
- Removing a module block will destroy the project, namespace, and all role bindings.
  Ensure workloads are migrated before removing.
- The `group_role_bindings` list is stable across plan/apply — changing the order of entries
  will cause bindings to be destroyed and recreated. Add new entries at the end.
