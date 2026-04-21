locals {
  namespace_cpu_limit     = var.namespace_cpu_limit != null ? var.namespace_cpu_limit : var.cpu_limit
  namespace_memory_limit  = var.namespace_memory_limit != null ? var.namespace_memory_limit : var.memory_limit
  namespace_storage_limit = var.namespace_storage_limit != null ? var.namespace_storage_limit : var.storage_limit
  namespaces              = var.namespaces != null ? (var.create_default_namespace ? distinct(concat([var.project_name], var.namespaces)) : var.namespaces) : (var.create_default_namespace ? [var.project_name] : [])

  # create_net_ns is true when explicitly requested OR when vlan_id has entries.
  # Keeping backward compat: callers already using vlan_id still get the namespace.
  create_net_ns     = var.create_network_namespace || (var.vlan_id != null && length(var.vlan_id) > 0)
  network_namespace = local.create_net_ns ? coalesce(var.network_namespace_name, "${var.project_name}-net") : null

  # VyOS path: compute a deterministic /23 subnet from 10.0.0.0/8 using the VLAN
  # index. Only relevant when vyos_endpoint is set and exactly one VLAN is given.
  # Auto-routed environments (physical switch / DigiOps-issued VLANs) use multiple
  # VLANs with route_mode=auto; no explicit subnets needed.
  use_vyos       = var.vlan_id != null && length(var.vlan_id) > 0 && var.vyos_endpoint != null
  tenant_subnet  = local.use_vyos ? cidrsubnet("10.0.0.0/8", 15, var.vlan_id[0] - 1000) : null
  tenant_gateway = local.use_vyos ? cidrhost(local.tenant_subnet, 1) : null
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
    precondition {
      condition     = !local.use_vyos || length(var.vlan_id) == 1
      error_message = "VyOS path requires exactly one VLAN ID. Set vyos_endpoint = null for multi-VLAN auto-route configurations."
    }
  }
}

# One namespace per entry. Each is a standard k8s namespace assigned to this project.
resource "rancher2_namespace" "this" {
  for_each         = toset(local.namespaces)
  name             = each.value
  project_id       = rancher2_project.this.id
  wait_for_cluster = false

  # resource_quota intentionally omitted — the project-level quota already
  # enforces the aggregate ceiling across all namespaces. A per-namespace
  # quota would block VM creation when Rancher auto-applies a zero-limit
  # ResourceQuota to namespaces created via the API.

  # description may be set manually in Rancher UI; ignore to avoid removing it.
  lifecycle {
    ignore_changes = [description]
  }
}

# ── Network namespace ─────────────────────────────────────────────────────────
# Created when create_network_namespace = true OR when vlan_id is set.
# Labelled so the credential reconciler skips it (no harvesterconfig needed).

resource "rancher2_namespace" "network" {
  count            = local.create_net_ns ? 1 : 0
  name             = local.network_namespace
  project_id       = rancher2_project.this.id
  wait_for_cluster = false

  labels = {
    "platform.wso2.com/role" = "network-namespace"
  }

  lifecycle {
    ignore_changes = [description]
  }
}

# ── Harvester network (whenever vlan_id is set, with or without VyOS) ─────────
# Created directly here so it exists regardless of whether VyOS is configured.
# Environments using physical switch VLAN assignment skip VyOS but still need
# the harvester_network resource to attach VMs to the correct VLAN.

resource "harvester_network" "tenant" {
  for_each             = var.vlan_id != null ? toset([for id in var.vlan_id : tostring(id)]) : toset([])
  name                 = lookup(var.vlan_network_names, each.value, "${var.project_name}-vlan${each.value}")
  namespace            = rancher2_namespace.network[0].name
  vlan_id              = tonumber(each.value)
  cluster_network_name = var.cluster_network_name

  # VyOS path: manual routing with a deterministic /23 from 10.0.0.0/8 (single VLAN only).
  # DigiOps / physical-switch path: auto routing — the upstream router
  # advertises the gateway; no explicit CIDR or gateway needed here.
  route_mode    = local.use_vyos ? "manual" : "auto"
  route_cidr    = local.use_vyos ? local.tenant_subnet : null
  route_gateway = local.use_vyos ? local.tenant_gateway : null

  # When VyOS is configured, wait for the vif/DHCP to be provisioned before
  # the network is visible to tenant VMs. for_each with empty set is a no-op.
  depends_on = [rancher2_namespace.network, module.vyos_tenant]
}

# ── VyOS configuration (only when vyos_endpoint is also set) ──────────────────
# Environments using physical switch VLAN assignment omit vyos_endpoint and
# only get the harvester_network above. Environments with VyOS get the full
# vif sub-interface, DHCP server, and NAT rule in addition.

module "vyos_tenant" {
  count  = local.use_vyos ? 1 : 0
  source = "../../network/vyos-tenant"

  tenant_name   = var.project_name
  vlan_id       = var.vlan_id[0]
  vyos_endpoint = var.vyos_endpoint
  vyos_api_key  = var.vyos_api_key
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
