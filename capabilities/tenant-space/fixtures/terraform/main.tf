module "tenant_space" {
  source = "github.com/wso2/open-cloud-datacenter//modules/tenancy/tenant-space?ref=terraform/v0.2.0"

  providers = {
    kubernetes.harvester = kubernetes.harvester
    harvester            = harvester
  }

  cluster_id   = var.harvester_cluster_id
  project_name = var.project_name

  cpu_limit     = var.cpu_limit
  memory_limit  = var.memory_limit
  storage_limit = var.storage_limit

  # Rancher applies the namespace default when creating the dedicated network
  # namespace, even though the module requests a zero resource quota for it.
  # Keep the project aggregate for the workload namespace while ensuring the
  # network namespace does not consume a second copy of that quota.
  namespace_cpu_limit     = "0"
  namespace_memory_limit  = "0Mi"
  namespace_storage_limit = "0Gi"


  group_role_bindings = var.group_role_bindings
  vm_network_vlan_id  = var.vm_network_vlan_id
}
