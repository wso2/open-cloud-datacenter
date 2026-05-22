locals {
  namespace_cpu_limit     = var.namespace_cpu_limit != null ? var.namespace_cpu_limit : var.cpu_limit
  namespace_memory_limit  = var.namespace_memory_limit != null ? var.namespace_memory_limit : var.memory_limit
  namespace_storage_limit = var.namespace_storage_limit != null ? var.namespace_storage_limit : var.storage_limit
  namespaces              = var.namespaces != null ? (var.create_default_namespace ? distinct(concat([var.project_name], var.namespaces)) : var.namespaces) : (var.create_default_namespace ? [var.project_name] : [])

  # create_net_ns is true when explicitly requested, when any VLAN variable is set
  # (all NADs live in the network namespace regardless of traffic type).
  create_net_ns     = var.create_network_namespace || (var.vlan_id != null && length(var.vlan_id) > 0) || var.vm_network_vlan_id != null || var.storage_network_vlan_id != null
  network_namespace = local.create_net_ns ? coalesce(var.network_namespace_name, "${var.project_name}-net") : null

  # VyOS path: compute a deterministic /23 subnet from 10.0.0.0/8 using the VLAN
  # index. Only relevant when vyos_endpoint is set and exactly one VLAN is given.
  # Auto-routed environments (physical switch / DigiOps-issued VLANs) use multiple
  # VLANs with route_mode=auto; no explicit subnets needed.
  use_vyos = var.vlan_id != null && length(var.vlan_id) > 0 && var.vyos_endpoint != null
  # vlan_id[0] must be >= 1000 when VyOS is used — enforced by the precondition below.
  # max(..., 0) prevents a negative cidrsubnet index from causing a plan-time panic
  # before the precondition fires.
  tenant_subnet  = local.use_vyos ? cidrsubnet("10.0.0.0/8", 15, max(var.vlan_id[0] - 1000, 0)) : null
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
    precondition {
      condition     = !local.use_vyos || var.vlan_id[0] >= 1000
      error_message = "VyOS IPAM uses VLAN IDs >= 1000 (index = vlan_id - 1000). Set vyos_endpoint = null for VLANs below 1000."
    }
    precondition {
      condition     = var.vlan_id == null || length(var.vlan_id) == 0 || local.create_net_ns
      error_message = "vlan_id requires the network namespace to exist. This should never happen since create_net_ns is always true when vlan_id is set — if you see this, do not set create_network_namespace = false alongside vlan_id."
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

  # field.cattle.io/projectId is required for the Harvester UI to show
  # resource quota information correctly for namespaces in this project.
  labels = {
    "field.cattle.io/projectId" = split(":", rancher2_project.this.id)[1]
  }

  # description may be set manually in Rancher UI; ignore to avoid removing it.
  lifecycle {
    ignore_changes = [description, labels["kubernetes.io/metadata.name"]]
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

  # Zero-limit quota prevents any VMs from being created in the network namespace.
  resource_quota {
    limit {
      limits_cpu       = "0"
      limits_memory    = "0Mi"
      requests_storage = "0Gi"
    }
  }

  labels = {
    "field.cattle.io/projectId" = split(":", rancher2_project.this.id)[1]
    "platform.wso2.com/role"    = "network-namespace"
  }

  lifecycle {
    ignore_changes = [description, labels["kubernetes.io/metadata.name"]]
  }

  depends_on = [rancher2_namespace.this]
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

# ── VM network (simple path: vm_vlan_id) ──────────────────────────────────────
# Created when vm_vlan_id is set. Attached to cluster_network_name (default 'vm-network').
# Named <project_name>-vlan<id> by default. Always auto-routed.
# For multi-VLAN or VyOS scenarios use the legacy vlan_id list below instead.

resource "harvester_network" "vm" {
  count                = var.vm_network_vlan_id != null ? 1 : 0
  name                 = coalesce(var.vm_network_name, "${var.project_name}-vlan${var.vm_network_vlan_id}")
  namespace            = rancher2_namespace.network[0].name
  vlan_id              = var.vm_network_vlan_id
  cluster_network_name = var.cluster_network_name
  route_mode           = "auto"

  depends_on = [rancher2_namespace.network]
}

# ── Storage network (only when storage_vlan_id is set) ────────────────────────
# Attached to storage_cluster_network_name (default 'storage-network'), which maps
# to the dedicated storage NIC (e.g. enp2s0) on the Harvester nodes.
# Always auto-routed — the upstream switch advertises the storage gateway.
# Route and DHCP settings for this interface are handled by VM cloud-init
# (use-routes: false on enp2s0 ensures no default route is stolen from the VM NIC).

resource "harvester_network" "storage" {
  count                = var.storage_network_vlan_id != null ? 1 : 0
  name                 = coalesce(var.storage_network_name, "${var.project_name}-strg-vlan${var.storage_network_vlan_id}")
  namespace            = rancher2_namespace.network[0].name
  vlan_id              = var.storage_network_vlan_id
  cluster_network_name = var.storage_cluster_network_name
  route_mode           = "auto"

  depends_on = [rancher2_namespace.network]
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

# ── Consumer VM access kubeconfig (read from provisioner-created secret) ───────
# The namespace-credential-provisioner automatically creates a "harvester-vm-kubeconfig"
# Secret in each tenant namespace containing a namespace-scoped Harvester kubeconfig.
# This data source surfaces it as a Terraform output so the platform team can
# retrieve it once at onboarding and hand it to the tenant team:
#
#   terraform output -raw <tenant>_vm_kubeconfig > <team>.harvester.kubeconfig.secret
#
# Requires the kubernetes.harvester provider to be passed in (set expose_vm_kubeconfig = true
# and configure kubernetes.harvester in the caller's provider block).
#
# IMPORTANT — two-pass apply required:
# This data source races with the out-of-band reconciler. On a fresh namespace the
# provisioner may not have created the secret yet when Terraform runs the data read.
# Apply in two steps:
#   1. terraform apply                  # creates namespaces; provisioner reconciles
#   2. terraform apply                  # reads the now-present secret
# Alternatively, run `terraform apply -target=rancher2_namespace.this` first, wait
# for the provisioner, then run the full apply.

locals {
  # Primary workload namespace for the VM kubeconfig.
  # Uses var.vm_access_namespace when explicitly set; falls back to the first
  # resolved namespace (or project_name when no namespaces are configured).
  vm_access_ns = var.vm_access_namespace != null ? var.vm_access_namespace : (
    length(local.namespaces) > 0 ? local.namespaces[0] : var.project_name
  )
}

data "kubernetes_secret_v1" "vm_access_kubeconfig" {
  count    = var.expose_vm_kubeconfig ? 1 : 0
  provider = kubernetes.harvester
  metadata {
    name      = "harvester-vm-kubeconfig"
    namespace = local.vm_access_ns
  }
  depends_on = [rancher2_namespace.this]
}

locals {
  vm_access_kubeconfig = var.expose_vm_kubeconfig ? data.kubernetes_secret_v1.vm_access_kubeconfig[0].data["kubeconfig"] : null
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

# ── Shared image access ────────────────────────────────────────────────────────
# Grants each unique group in group_role_bindings a read-only binding to the
# shared images project. Using rancher2_project_role_template_binding (Rancher-
# native) rather than kubernetes_role_binding_v1. Groups are deduplicated and
# sorted so the idx-based key is stable across plan/apply runs.
#
# The shared project is discovered by name (shared_image_project_name, default
# "shared") so callers do not need to pass a project ID. The data source is
# guarded by both enable_shared_image_access AND the presence of at least one
# group binding — this prevents a spurious lookup when the module itself IS the
# shared space (no group_role_bindings configured) or when access is disabled.
#
# Callers should ensure the shared project exists before this module runs by
# adding depends_on = [module.shared_space] at the module call site.

locals {
  # Sort unique group principals for a stable idx → group mapping.
  _sorted_image_groups = var.enable_shared_image_access ? sort(distinct([
    for b in var.group_role_bindings : b.group_principal_id
  ])) : []

  # Map: "<project_name>-img-<idx>" => group_principal_id
  # e.g. "kasun-test-img-0" => "genericoidc_group://testgroup"
  shared_image_bindings = {
    for idx, group in local._sorted_image_groups :
    "${var.project_name}-img-${idx}" => group
  }
}

data "rancher2_project" "shared_images" {
  count      = var.enable_shared_image_access && length(local._sorted_image_groups) > 0 ? 1 : 0
  cluster_id = var.cluster_id
  name       = var.shared_image_project_name
}

resource "rancher2_project_role_template_binding" "shared_image_access" {
  for_each = local.shared_image_bindings

  name               = each.key
  project_id         = data.rancher2_project.shared_images[0].id
  role_template_id   = "read-only"
  group_principal_id = each.value
}
