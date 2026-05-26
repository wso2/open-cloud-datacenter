variable "cluster_id" {
  type        = string
  description = "Rancher cluster ID of the Harvester HCI cluster."
}

variable "project_name" {
  type        = string
  description = "Name of the Rancher project for this tenant."
}

variable "create_default_namespace" {
  type        = bool
  description = "When true (default), a namespace named after the project is always included alongside any explicitly listed namespaces. Set to false for brownfield projects where the project-name namespace was never created or is managed outside Terraform."
  default     = true
}

variable "namespaces" {
  type        = list(string)
  description = "Kubernetes namespace names to create within the project. Defaults to [project_name] — a single namespace matching the project. Pass additional names to create more."
  default     = null
  validation {
    condition = var.namespaces == null || (
      length(var.namespaces) > 0 &&
      length(var.namespaces) == length(toset(var.namespaces)) &&
      alltrue([for ns in var.namespaces :
        length(ns) <= 63 &&
        can(regex("^[a-z0-9]([a-z0-9-]*[a-z0-9])?$", ns))
      ])
    )
    error_message = "At least one namespace is required. All names must be unique, at most 63 characters, and match RFC 1123 DNS label format (lowercase alphanumeric and hyphens, must start and end with alphanumeric)."
  }
}

# Resource quotas — all optional. Omit entirely for projects with no quota enforcement.
# When set, the same limits apply at both project aggregate and per-namespace default
# level unless namespace_* overrides are provided.

variable "cpu_limit" {
  type        = string
  description = "Total CPU limit for the project (e.g. \"8\", \"500m\"). Null skips quota entirely."
  default     = null
  validation {
    condition     = var.cpu_limit == null ? true : trimspace(var.cpu_limit) != ""
    error_message = "cpu_limit must be null or a non-empty quantity string."
  }
}

variable "memory_limit" {
  type        = string
  description = "Total memory limit for the project (e.g. \"16Gi\", \"4096Mi\"). Only applied when cpu_limit is set."
  default     = null
  validation {
    condition     = var.memory_limit == null ? true : trimspace(var.memory_limit) != ""
    error_message = "memory_limit must be null or a non-empty quantity string."
  }
}

variable "storage_limit" {
  type        = string
  description = "Total persistent storage request limit for the project (e.g. \"200Gi\"). Only applied when cpu_limit is set."
  default     = null
  validation {
    condition     = var.storage_limit == null ? true : trimspace(var.storage_limit) != ""
    error_message = "storage_limit must be null or a non-empty quantity string."
  }
}

variable "namespace_cpu_limit" {
  type        = string
  description = "Per-namespace default CPU limit. Defaults to cpu_limit."
  default     = null
  validation {
    condition     = var.namespace_cpu_limit == null ? true : trimspace(var.namespace_cpu_limit) != ""
    error_message = "namespace_cpu_limit must be null or a non-empty quantity string."
  }
}

variable "namespace_memory_limit" {
  type        = string
  description = "Per-namespace default memory limit. Defaults to memory_limit."
  default     = null
  validation {
    condition     = var.namespace_memory_limit == null ? true : trimspace(var.namespace_memory_limit) != ""
    error_message = "namespace_memory_limit must be null or a non-empty quantity string."
  }
}

variable "namespace_storage_limit" {
  type        = string
  description = "Per-namespace default storage limit. Defaults to storage_limit."
  default     = null
  validation {
    condition     = var.namespace_storage_limit == null ? true : trimspace(var.namespace_storage_limit) != ""
    error_message = "namespace_storage_limit must be null or a non-empty quantity string."
  }
}

# ── Simple VM / storage network (new approach) ───────────────────────────────
# Pass a single VLAN ID for each traffic type. The module auto-creates the
# harvester_network with a deterministic name and the correct cluster NIC.
# Naming: vm → <project>-vlan<id>   storage → <project>-strg-vlan<id>
# Use vm_vlan_id / storage_vlan_id for new tenants. The legacy vlan_id list
# below remains for brownfield imports and multi-VLAN configurations.

variable "vm_network_vlan_id" {
  type        = number
  description = "VLAN ID for the primary VM network. Creates a harvester_network named '<project_name>-vlan<id>' attached to cluster_network_name (default 'vm-network'). Always auto-routed. Use this instead of the legacy vlan_id list for simple single-VLAN tenants."
  default     = null
  validation {
    condition     = var.vm_network_vlan_id == null || (var.vm_network_vlan_id >= 1 && var.vm_network_vlan_id <= 4094)
    error_message = "vm_network_vlan_id must be null or a valid 802.1Q VLAN ID (1–4094)."
  }
}

variable "vm_network_name" {
  type        = string
  description = "Override the harvester_network name for vm_network_vlan_id. Defaults to '<project_name>-vlan<id>'. Use when importing a brownfield VM network whose name differs from this convention."
  default     = null
  validation {
    condition     = var.vm_network_name == null ? true : trimspace(var.vm_network_name) != ""
    error_message = "vm_network_name must be null or a non-empty string."
  }
}

# ── VyOS network integration — all optional ───────────────────────────────────
# When vlan_id is set, the module additionally creates:
#   - A "<project_name>-net" namespace in the project (network namespace)
#   - A harvester_network for that VLAN
#   - VyOS vif sub-interface, DHCP server, and NAT rule via the vyos-tenant module
#
# Requires the vyos and harvester providers to be configured in the caller.

variable "create_network_namespace" {
  type        = bool
  description = "When true, creates a dedicated <project_name>-net namespace labelled platform.wso2.com/role=network-namespace. Also true implicitly when vlan_id is set. Use this flag to pre-provision the namespace before a VLAN is assigned."
  default     = false
}

variable "vlan_id" {
  type        = list(number)
  description = "List of VLAN IDs for this tenant's networks. Each entry creates a harvester_network in the network namespace. When non-empty, the network namespace is always created. VyOS path (vyos_endpoint set) requires exactly one VLAN ID — a deterministic /23 from 10.0.0.0/8 is computed and full VyOS vif/DHCP/NAT config is provisioned. Auto-route path (vyos_endpoint null) supports multiple VLANs — the upstream router handles routing. When null or empty, no network resources are created."
  default     = null
  validation {
    condition = var.vlan_id == null || (
      length(var.vlan_id) > 0 &&
      alltrue([for id in var.vlan_id : id >= 1 && id <= 4094])
    )
    error_message = "vlan_id must be null or a non-empty list of valid 802.1Q VLAN IDs (1–4094)."
  }
}

variable "network_namespace_name" {
  type        = string
  description = "Override the name of the network namespace. Defaults to <project_name>-net. Use this when importing a brownfield namespace whose name differs from the default."
  default     = null
  validation {
    condition     = var.network_namespace_name == null ? true : trimspace(var.network_namespace_name) != ""
    error_message = "network_namespace_name must be null or a non-empty string."
  }
}

variable "vlan_network_names" {
  type        = map(string)
  description = "Map of VLAN ID (as string) to harvester_network resource name override. Use when importing brownfield networks whose names differ from the default <project_name>-vlan<id> pattern. Example: { \"608\" = \"vm-subnet-008\" }."
  default     = {}
}

variable "cluster_network_name" {
  type        = string
  description = "Harvester cluster network carrying tenant VLANs. Defaults to 'vm-network' — override only if your datacenter uses a different cluster network name."
  default     = "vm-network"
}


# Role bindings — one binding is created per (group, role) pair.

variable "group_role_bindings" {
  type = list(object({
    role_template_id   = string
    group_principal_id = optional(string) # OIDC/LDAP group principal e.g. "genericoidc_group://my-group"
    group_id           = optional(string) # local Rancher group ID
    user_principal_id  = optional(string) # OIDC/LDAP user principal e.g. "genericoidc_user://user@example.com"
    user_id            = optional(string) # local Rancher user ID
  }))
  description = <<-EOT
    List of principal + role pairs to bind within this project. Each entry creates a
    rancher2_project_role_template_binding. Exactly one of group_principal_id,
    group_id, user_principal_id, or user_id must be set per entry.

    role_template_id: built-in role name ("project-member", "read-only") or the
      ID of a custom rancher2_role_template (from the cluster-roles module).

    Examples:
      group_role_bindings = [
        { group_principal_id = "genericoidc_group://my-group", role_template_id = "project-member" },
        { user_principal_id  = "genericoidc_user://uuid", role_template_id = "read-only" },
        { user_id            = "u-abc123", role_template_id = module.cluster_roles.vm_metrics_observer_role_id },
      ]
  EOT
  default     = []

  validation {
    condition = alltrue([
      for b in var.group_role_bindings :
      length(compact([b.group_principal_id, b.group_id, b.user_principal_id, b.user_id])) == 1
    ])
    error_message = "Each binding must specify exactly one of: group_principal_id, group_id, user_principal_id, or user_id."
  }
}

# ── Shared image access — all optional ───────────────────────────────────────
# When enabled, each unique group in group_role_bindings receives a read-only
# project-level binding to the shared images project. This uses Rancher-native
# project role template bindings (not raw Kubernetes RoleBindings) and avoids
# granting the tenant groups access to the images namespace via project-owner
# or project-member, which would silently include all other namespaces.
#
# Defaults to true so all tenant spaces get read-only image access out of the
# box. Set to false only for spaces that should not see the shared catalogue
# (e.g. the shared space itself).

variable "enable_shared_image_access" {
  type        = bool
  description = "When true, creates a read-only Rancher project role binding for each group in group_role_bindings to the shared images project. Set to false for tenant spaces that should not see the shared image catalogue (e.g. the shared space itself)."
  default     = true
}

variable "shared_image_project_name" {
  type        = string
  description = "Name of the Rancher project that holds shared VM images. The module looks this project up by name on the same cluster. Defaults to 'shared'. Only change if your environment uses a different project name for the image catalogue."
  default     = "shared"
  validation {
    condition     = trimspace(var.shared_image_project_name) != ""
    error_message = "shared_image_project_name must be a non-empty string."
  }
}

variable "shared_image_namespace" {
  type        = string
  description = "Name of the namespace within the shared images project that holds VM images. Informational — used in resource naming only; access is granted at the project level. Defaults to 'images'."
  default     = "images"
  validation {
    condition     = trimspace(var.shared_image_namespace) != ""
    error_message = "shared_image_namespace must be a non-empty string."
  }
}

variable "expose_vm_kubeconfig" {
  type        = bool
  description = "When true, reads the 'harvester-vm-kubeconfig' Secret created by the namespace-credential-provisioner and exposes it via the vm_access_kubeconfig output. Requires the kubernetes.harvester provider alias to be configured in the caller. The provisioner must have run before apply."
  default     = false
}

variable "vm_access_namespace" {
  type        = string
  description = "Namespace from which to read the 'harvester-vm-kubeconfig' Secret when expose_vm_kubeconfig = true. Defaults to the first resolved namespace (or project_name when no namespaces are configured). Set explicitly when the tenant has multiple namespaces and the kubeconfig should target a specific one."
  default     = null
  validation {
    condition     = var.vm_access_namespace == null ? true : trimspace(var.vm_access_namespace) != ""
    error_message = "vm_access_namespace must be null or a non-empty namespace name."
  }
}

variable "vyos_endpoint" {
  type        = string
  description = "VyOS HTTPS API endpoint (e.g. 'https://172.22.100.50'). Required when vlan_id is set."
  default     = null
}

variable "vyos_api_key" {
  type        = string
  description = "VyOS HTTPS API key. Required when vlan_id is set."
  sensitive   = true
  default     = null
}

# ── Storage network ───────────────────────────────────────────────────────────
# Creates a harvester_network attached to a dedicated storage cluster network
# (typically a separate physical NIC, e.g. enp2s0) rather than the VM network.
# Storage networks are always auto-routed — the upstream switch handles routing.
# Separate from vlan_id so callers can use different cluster networks per traffic type.

variable "storage_network_vlan_id" {
  type        = number
  description = "VLAN ID for the dedicated storage network. Creates a harvester_network named '<project_name>-strg-vlan<id>' attached to storage_cluster_network_name (default 'strg-network'). Storage is always auto-routed. When set, the network namespace is always created. Separate from vlan_id which targets cluster_network_name (default 'vm-network')."
  default     = null
  validation {
    condition     = var.storage_network_vlan_id == null || (var.storage_network_vlan_id >= 1 && var.storage_network_vlan_id <= 4094)
    error_message = "storage_network_vlan_id must be null or a valid 802.1Q VLAN ID (1–4094)."
  }
}

variable "storage_cluster_network_name" {
  type        = string
  description = "Harvester cluster network for the storage VLAN. Should map to the dedicated storage NIC (e.g. enp2s0). Defaults to 'strg-network' — override only if your datacenter uses a different cluster network name for storage traffic."
  default     = "strg-network"
  validation {
    condition     = trimspace(var.storage_cluster_network_name) != ""
    error_message = "storage_cluster_network_name must be a non-empty string."
  }
}

variable "storage_network_name" {
  type        = string
  description = "Override the harvester_network name for storage_network_vlan_id. Defaults to '<project_name>-strg-vlan<id>'. Use when importing a brownfield storage network whose name differs from this convention."
  default     = null
  validation {
    condition     = var.storage_network_name == null ? true : trimspace(var.storage_network_name) != ""
    error_message = "storage_network_name must be null or a non-empty string."
  }
}
