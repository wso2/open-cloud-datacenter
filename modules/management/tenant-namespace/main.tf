/**
 * @module management/tenant-namespace
 * @description Provisions Kubernetes namespaces within a specified Rancher v2 project.
 * 
 * ### Features
 * - Dynamically creates multiple namespaces from a provided list.
 * - Applies custom labels and annotations for organizational tagging.
 * - Configures optional container resource limits (CPU/Memory).
 * - Enforces optional resource quotas (Pods, Storage, LoadBalancers, etc.).
 * 
 * ### Prerequisites
 * 1. **Provider**: Requires the `rancher2` provider authenticated to your Rancher server.
 * 2. **Project**: You must have a pre-existing Rancher `project_id` to pass as a variable.
 *
 */

locals {
  namespaces = toset(var.namespaces != null ? var.namespaces : [])
  container_resource_limit = var.container_resource_limit != null ? [var.container_resource_limit] : []
  resource_quota = var.resource_quota != null ? [var.resource_quota] : []
}

resource "rancher2_namespace" "this" {
  for_each = local.namespaces

  name        = each.value
  project_id  = var.project_id
  description = var.description

  labels = merge(var.labels, {
    "field.cattle.io/projectId" = split(":", var.project_id)[1]
  })
  annotations      = var.annotations
  wait_for_cluster = var.wait_for_cluster

  dynamic "container_resource_limit" {
    for_each = local.container_resource_limit
    content {
      limits_cpu      = container_resource_limit.value.cpu_limit
      limits_memory   = container_resource_limit.value.memory_limit
      requests_cpu    = container_resource_limit.value.cpu_request
      requests_memory = container_resource_limit.value.memory_request
    }
  }

  dynamic "resource_quota" {
    for_each = local.resource_quota
    content {
      limit {
        config_maps              = resource_quota.value.config_maps
        limits_cpu               = resource_quota.value.cpu_limit
        limits_memory            = resource_quota.value.memory_limit
        persistent_volume_claims = resource_quota.value.persistent_volume_claims
        pods                     = resource_quota.value.pods
        replication_controllers  = resource_quota.value.replication_controllers
        requests_cpu             = resource_quota.value.cpu_request
        requests_memory          = resource_quota.value.memory_request
        requests_storage         = resource_quota.value.storage_request
        secrets                  = resource_quota.value.secrets
        services_load_balancers  = resource_quota.value.services_load_balancers
        services_node_ports      = resource_quota.value.services_node_ports
      }
    }
  }
}
