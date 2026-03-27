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
| `vm-manager` | project | Full lifecycle management of VMs: create, configure, start/stop/restart, console, and delete. |
| `network-manager` | cluster | Manage Harvester VLAN infrastructure and NetworkAttachmentDefinitions. Bind only via `rancher2_cluster_role_template_binding`. |
| `vm-metrics-observer` | project | Read-only access to VM status and metrics. No mutating verbs. |

### `vm-manager` Permissions

| API Group | Resources | Verbs |
|-----------|-----------|-------|
| `kubevirt.io` | `virtualmachines`, `virtualmachineinstances`, `virtualmachineinstancepresets`, `virtualmachineinstancereplicasets` | `get`, `list`, `watch`, `create`, `update`, `patch`, `delete` |
| `subresources.kubevirt.io` | `virtualmachines/start`, `virtualmachines/stop`, `virtualmachines/restart`, `virtualmachines/migrate`, `virtualmachineinstances/vnc`, `virtualmachineinstances/console`, `virtualmachineinstances/portforward`, `virtualmachineinstances/pause`, `virtualmachineinstances/unpause` | `get`, `update` |
| `subresources.kubevirt.io` | `virtualmachineinstances/metrics` | `get` |
| `cdi.kubevirt.io` | `datavolumes` | `get`, `list`, `watch`, `create`, `update`, `patch`, `delete` |
| `harvesterhci.io` | `virtualmachineimages` | `get`, `list`, `watch` |
| `harvesterhci.io` | `keypairs` | `get`, `list`, `watch`, `create`, `delete` |
| `""` (core) | `secrets`, `configmaps` | `get`, `list`, `watch`, `create`, `update`, `patch`, `delete` |
| `""` (core) | `services/proxy` | `get` |

This role is intended for product team members who own their VMs end-to-end within the
quota and namespace boundaries imposed by the `tenant-space` module.

### `vm-metrics-observer` Permissions

| API Group | Resources | Verbs |
|-----------|-----------|-------|
| `kubevirt.io` | `virtualmachines`, `virtualmachineinstances` | `get`, `list`, `watch` |
| `subresources.kubevirt.io` | `virtualmachineinstances/metrics` | `get` |
| `""` (core) | `services/proxy` | `get` |

This role intentionally **excludes** `update`, `patch`, `delete`, and subresources that
control VM power state (`start`, `stop`, `restart`, `migrate`). Use it for users who
need Harvester dashboard visibility only (e.g. on-call monitoring access).

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

module "tenant_space_iam" {
  source = "github.com/wso2-enterprise/open-cloud-datacenter//modules/management/tenant-space?ref=v0.1.x"
  ...
  group_role_bindings = [
    {
      group_principal_id = var.iam_team_group_id
      role_template_id   = "project-member"
    },
    {
      group_principal_id = var.iam_team_group_id
      role_template_id   = module.cluster_roles.vm_manager_role_id
    },
    # Add the observer group to the same tenant space rather than creating a
    # second module block — a second call would collide on project creation.
    {
      group_principal_id = var.sre_group_id
      role_template_id   = module.cluster_roles.vm_metrics_observer_role_id
    },
  ]
}
```

## Outputs

| Name | Description |
|------|-------------|
| `vm_manager_role_id` | Role template ID for `vm-manager`. Pass to `tenant-space` `group_role_bindings`. |
| `network_manager_role_id` | Role template ID for `network-manager` (cluster-scoped). Pass to `rancher2_cluster_role_template_binding`. |
| `vm_metrics_observer_role_id` | Role template ID for `vm-metrics-observer`. Pass to `tenant-space` `group_role_bindings`. |

## Notes

- This module requires an authenticated `rancher2` provider configured at the root level.
- Role templates are global to the Rancher instance, not scoped to a cluster or project.
- Adding new roles in future: extend `main.tf` with additional `rancher2_role_template`
  resources and expose their IDs as outputs.
