locals {
  namespace_name          = var.namespace_name != null ? var.namespace_name : "${var.project_name}-ns"
  namespace_cpu_limit     = var.namespace_cpu_limit != null ? var.namespace_cpu_limit : var.cpu_limit
  namespace_memory_limit  = var.namespace_memory_limit != null ? var.namespace_memory_limit : var.memory_limit
  namespace_storage_limit = var.namespace_storage_limit != null ? var.namespace_storage_limit : var.storage_limit
}

resource "rancher2_project" "this" {
  name       = var.project_name
  cluster_id = var.cluster_id

  resource_quota {
    project_limit {
      limits_cpu       = var.cpu_limit
      limits_memory    = var.memory_limit
      requests_storage = var.storage_limit
    }
    namespace_default_limit {
      limits_cpu       = local.namespace_cpu_limit
      limits_memory    = local.namespace_memory_limit
      requests_storage = local.namespace_storage_limit
    }
  }
}

resource "rancher2_namespace" "this" {
  name       = local.namespace_name
  project_id = rancher2_project.this.id
}

# One binding per (group, role) pair. The binding name is derived from the
# project name and a hash of the pair to stay within Kubernetes name limits.
resource "rancher2_project_role_template_binding" "this" {
  for_each = {
    for idx, b in var.group_role_bindings :
    "${var.project_name}-${idx}" => b
  }

  name               = each.key
  project_id         = rancher2_project.this.id
  role_template_id   = each.value.role_template_id
  group_principal_id = each.value.group_principal_id
}
