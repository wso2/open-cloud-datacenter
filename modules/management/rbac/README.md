# Module: management/rbac

Bulk-creates Rancher projects and namespaces with resource quotas. Accepts a map of team
names so multiple teams can be declared in one module call.

> **When to use `rbac` vs `tenant-space`**
>
> Use `rbac` when you only need projects and namespaces with quotas and no role bindings
> (e.g. initial cluster setup, or when RBAC is managed outside Terraform).
>
> Use [`tenant-space`](../tenant-space/README.md) when you also need to bind LDAP/OIDC groups
> to roles within the project — it handles quotas, namespace, and role bindings in one call.

## Requirements

| Name | Version |
|------|---------|
| terraform | >= 1.3 |
| rancher/rancher2 | ~> 13.1 |

## Prerequisites

- Harvester cluster registered in Rancher (`harvester-integration` applied)
- Authenticated `rancher2` provider at the root level

## Usage

```hcl
module "rbac" {
  source = "github.com/wso2-enterprise/open-cloud-datacenter//modules/management/rbac?ref=v0.1.x"

  harvester_cluster_name = "harvester-hci"

  projects = {
    "iam-team" = {
      cpu_limit     = "8"
      memory_limit  = "16Gi"
      storage_limit = "200Gi"
    }
    "middleware-team" = {
      cpu_limit     = "4"
      memory_limit  = "8Gi"
      storage_limit = "100Gi"
    }
  }
}
```

Each entry in `projects` creates:
- A `rancher2_project` named after the map key, with the specified resource quotas
- A `rancher2_namespace` named `<key>-ns` scoped to that project

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|----------|
| `harvester_cluster_name` | Name of the Harvester cluster as registered in Rancher | `string` | `"local"` | no |
| `projects` | Map of team names to CPU/memory/storage quota objects | `map(object({ cpu_limit, memory_limit, storage_limit }))` | `{}` | no |

## Outputs

This module does not expose outputs. Resources are accessible via Terraform state as
`module.rbac.rancher2_project.team_projects["<team-name>"]`.

## Notes

- Quota values follow Kubernetes quantity format: `"8"` (cores), `"500m"` (millicores),
  `"16Gi"` (memory), `"200Gi"` (storage).
- Both the project aggregate limit and the per-namespace default are set to the same values.
  For fine-grained control, use `tenant-space` which exposes separate namespace quota inputs.
