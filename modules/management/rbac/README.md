# Module: management/rbac

Creates Rancher projects and namespaces with resource quotas for multi-tenant RBAC isolation on the Harvester cluster.

## Requirements

| Name | Version |
|------|---------|
| terraform | >= 1.3 |
| rancher/rancher2 | ~> 3.0 |

## Usage

```hcl
module "rbac" {
  source = "github.com/wso2-enterprise/open-cloud-datacenter//modules/management/rbac?ref=v0.1.0"

  harvester_cluster_name = "local"

  projects = {
    "team-alpha" = {
      cpu_limit     = "8000m"
      memory_limit  = "16Gi"
      storage_limit = "200Gi"
    }
    "team-beta" = {
      cpu_limit     = "4000m"
      memory_limit  = "8Gi"
      storage_limit = "100Gi"
    }
  }
}
```

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|----------|
| harvester_cluster_name | The name of the local Harvester cluster registered in Rancher | `string` | `"local"` | no |
| projects | A map of team names to their resource quotas | `map(object({ cpu_limit = string, memory_limit = string, storage_limit = string }))` | `{}` | no |

## Outputs

This module does not define outputs. The Rancher project and namespace IDs are accessible via `module.rbac.rancher2_project.team_projects` and `module.rbac.rancher2_namespace.team_namespaces`.
