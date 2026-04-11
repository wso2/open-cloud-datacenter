locals {
  namespace_cpu_limit     = var.namespace_cpu_limit != null ? var.namespace_cpu_limit : var.cpu_limit
  namespace_memory_limit  = var.namespace_memory_limit != null ? var.namespace_memory_limit : var.memory_limit
  namespace_storage_limit = var.namespace_storage_limit != null ? var.namespace_storage_limit : var.storage_limit
  namespaces              = var.namespaces != null ? var.namespaces : [var.project_name]
  network_namespace       = var.vlan_id != null ? "${var.project_name}-net" : null
}

resource "rancher2_project" "this" {
  name             = var.project_name
  cluster_id       = var.cluster_id
  wait_for_cluster = false

  # resource_quota is optional — only set when cpu_limit is provided.
  # Existing projects without quotas can be imported cleanly by omitting these vars.
  dynamic "resource_quota" {
    for_each = var.cpu_limit != null ? [1] : []
    content {
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

  # container_resource_limit is an empty block set by Rancher on project creation.
  # Ignoring it prevents spurious diffs on brownfield-imported projects.
  lifecycle {
    ignore_changes = [container_resource_limit]
    precondition {
      condition = var.cpu_limit != null || alltrue([
        var.memory_limit == null,
        var.storage_limit == null,
        var.namespace_cpu_limit == null,
        var.namespace_memory_limit == null,
        var.namespace_storage_limit == null,
      ])
      error_message = "Quota variables (memory_limit, storage_limit, namespace_*_limit) are only applied when cpu_limit is set. Either set cpu_limit or remove the other quota variables."
    }
  }
}

# One namespace per entry. Each is a standard k8s namespace assigned to this project.
resource "rancher2_namespace" "this" {
  for_each         = toset(local.namespaces)
  name             = each.value
  project_id       = rancher2_project.this.id
  wait_for_cluster = false

  # description may be set manually in Rancher UI; ignore to avoid removing it.
  lifecycle {
    ignore_changes = [description]
  }
}

# ── Network namespace (only when vlan_id is set) ──────────────────────────────

resource "rancher2_namespace" "network" {
  count            = var.vlan_id != null ? 1 : 0
  name             = local.network_namespace
  project_id       = rancher2_project.this.id
  wait_for_cluster = false

  lifecycle {
    ignore_changes = [description]
  }
}

# ── VyOS tenant network (only when vlan_id is set) ────────────────────────────

module "vyos_tenant" {
  count  = var.vlan_id != null ? 1 : 0
  source = "../../network/vyos-tenant"

  tenant_name          = var.project_name
  vlan_id              = var.vlan_id
  network_namespace    = rancher2_namespace.network[0].name
  cluster_network_name = var.cluster_network_name
}

# ── One binding per (group, role) pair. ───────────────────────────────────────
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
