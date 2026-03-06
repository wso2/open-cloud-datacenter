# Module: management/cluster-roles

Defines custom Rancher role templates that are shared across the entire Rancher instance.
Apply this module **once** during initial datacenter setup. The role IDs it outputs are then
passed into `tenant-space` module calls to bind groups to those roles.

## When to Use

Apply this module before onboarding any product team. It creates role templates that any
number of `tenant-space` module instances can reference.

If you only need standard Rancher built-in roles (`project-member`, `read-only`, etc.), this
module is not required.

## Roles Created

| Role Name | Context | Purpose |
|-----------|---------|---------|
| `vm-metrics-observer` | project | Read-only access to VM status and metrics for the Harvester dashboard. No mutating verbs. |

### `vm-metrics-observer` Permissions

| API Group | Resources | Verbs |
|-----------|-----------|-------|
| `kubevirt.io` | `virtualmachines`, `virtualmachineinstances` | `get`, `list`, `watch` |
| `subresources.kubevirt.io` | `virtualmachineinstances/metrics` | `get` |
| `""` (core) | `services/proxy` | `get` |

This role intentionally **excludes** `update`, `patch`, `delete`, and subresources that
control VM power state (`start`, `stop`, `restart`, `migrate`).

## Requirements

| Name | Version |
|------|---------|
| terraform | >= 1.3 |
| rancher/rancher2 | ~> 13.1 |

## Usage

```hcl
module "cluster_roles" {
  source = "github.com/wso2-enterprise/open-cloud-datacenter//modules/management/cluster-roles?ref=v0.1.x"
}

# Pass the output to tenant-space modules
module "tenant_space_example" {
  source = "github.com/wso2-enterprise/open-cloud-datacenter//modules/management/tenant-space?ref=v0.1.x"
  ...
  group_role_bindings = [
    {
      group_principal_id = var.team_group_id
      role_template_id   = module.cluster_roles.vm_metrics_observer_role_id
    },
  ]
}
```

## Outputs

| Name | Description |
|------|-------------|
| `vm_metrics_observer_role_id` | Role template ID for `vm-metrics-observer`. Pass to `tenant-space` `group_role_bindings`. |

## Notes

- This module requires an authenticated `rancher2` provider configured at the root level.
- Role templates are global to the Rancher instance, not scoped to a cluster or project.
- Adding new roles in future: extend `main.tf` with additional `rancher2_role_template`
  resources and expose their IDs as outputs.
