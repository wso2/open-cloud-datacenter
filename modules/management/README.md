# Management Modules

These modules handle everything that a datacenter operator does **after** the Rancher server is
up (Phase 2 onward): integrating Harvester, configuring networks and storage, and onboarding
product teams with isolated project spaces and access controls.

## Module Map

```
modules/management/
├── harvester-integration/   # Register Harvester into Rancher — run once
├── networking/              # VLAN-backed tenant networks — run per VLAN
├── storage/                 # OS images available for VM provisioning — run per image set
├── cluster-roles/           # Custom Rancher role templates — run once
├── tenant-space/            # Full team onboarding (project + namespace + quotas + RBAC)
└── rbac/                    # Bulk project/namespace creation without role bindings
```

## Recommended Apply Order

Each module depends on state from the previous one. Apply in this sequence:

| Step | Module | Depends On |
|------|--------|------------|
| 1 | `harvester-integration` | Phase 0 bootstrap (Rancher up, Harvester kubeconfig available) |
| 2 | `networking` | Harvester registered in Rancher |
| 3 | `storage` | Harvester registered in Rancher |
| 4 | `cluster-roles` | Rancher API accessible (authenticated provider) |
| 5 | `tenant-space` | `cluster-roles` (for custom role IDs), `harvester-integration` (for cluster ID) |

Steps 2–4 are independent of each other and can be applied in any order or in parallel.

## Typical Operator Workflow

### Initial Setup (run once per datacenter)

```hcl
# 1. Register Harvester into Rancher
module "harvester_integration" {
  source = "github.com/wso2-enterprise/open-cloud-datacenter//modules/management/harvester-integration?ref=v0.1.x"
  harvester_kubeconfig   = file(var.harvester_kubeconfig_path)
  harvester_cluster_name = "harvester-hci"
  rancher_hostname       = "rancher.example.internal"
  rancher_lb_ip          = "192.168.10.200"
}

# 2. Define custom role templates
module "cluster_roles" {
  source = "github.com/wso2-enterprise/open-cloud-datacenter//modules/management/cluster-roles?ref=v0.1.x"
}

# 3. Register OS images
module "storage" {
  source = "github.com/wso2-enterprise/open-cloud-datacenter//modules/management/storage?ref=v0.1.x"
  managed_images = {
    "ubuntu-22-04" = {
      display_name = "Ubuntu 22.04 LTS"
      url          = "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img"
    }
  }
}
```

### Onboarding a Product Team

```hcl
module "tenant_space_iam" {
  source = "github.com/wso2-enterprise/open-cloud-datacenter//modules/management/tenant-space?ref=v0.1.x"

  cluster_id    = module.harvester_integration.harvester_cluster_id
  project_name  = "iam-team"
  cpu_limit     = "8"
  memory_limit  = "16Gi"
  storage_limit = "200Gi"

  group_role_bindings = [
    # The team owns their project
    { group_principal_id = var.iam_group_id, role_template_id = "project-member" },
    # The team can view VM metrics in the Harvester dashboard
    { group_principal_id = var.iam_group_id, role_template_id = module.cluster_roles.vm_metrics_observer_role_id },
  ]
}
```

### Adding a Read-Only Observer (no project ownership)

```hcl
# Re-use the same tenant-space module with only the observer role — no project-member
module "tenant_space_iam" {
  ...
  group_role_bindings = [
    { group_principal_id = var.observer_group_id, role_template_id = module.cluster_roles.vm_metrics_observer_role_id },
  ]
}
```

## Module Descriptions

| Module | One-liner |
|--------|-----------|
| [harvester-integration](harvester-integration/README.md) | Registers Harvester HCI into Rancher: feature flag, UI extension, cloud credential, CoreDNS patch, registration manifest |
| [networking](networking/README.md) | Creates VLAN-backed Harvester networks for tenant isolation |
| [storage](storage/README.md) | Downloads and registers OS images into Harvester for VM provisioning |
| [cluster-roles](cluster-roles/README.md) | Defines custom Rancher role templates (e.g. `vm-metrics-observer`) shared across all tenant spaces |
| [tenant-space](tenant-space/README.md) | Full team onboarding: Rancher project, namespace, resource quotas, and flexible role bindings |
| [rbac](rbac/README.md) | Lightweight bulk project/namespace creator — use `tenant-space` when role bindings are needed |
