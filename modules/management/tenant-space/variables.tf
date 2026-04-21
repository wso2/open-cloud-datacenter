variable "cluster_id" {
  type        = string
  description = "Rancher cluster ID of the Harvester HCI cluster."
}

variable "project_name" {
  type        = string
  description = "Name of the Rancher project for this tenant."
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
  type        = number
  description = "VLAN ID for this tenant's network (>= 1000). When set, always creates the network namespace and a harvester_network. Routing mode depends on vyos_endpoint: if set, route_mode=manual with a deterministic /23 from 10.0.0.0/8 plus full VyOS vif/DHCP/NAT config; if null, route_mode=auto (upstream router / DigiOps-issued VLAN handles routing). When vlan_id is null, no network resources are created."
  default     = null
  validation {
    condition     = var.vlan_id == null || (var.vlan_id >= 1 && var.vlan_id <= 4094)
    error_message = "vlan_id must be a valid 802.1Q VLAN ID (1–4094)."
  }
}

variable "cluster_network_name" {
  type        = string
  description = "Harvester cluster network carrying tenant VLANs. Defaults to 'vm-network' — override only if your datacenter uses a different cluster network name."
  default     = "vm-network"
}


# Role bindings — one binding is created per (group, role) pair.

variable "group_role_bindings" {
  type = list(object({
    group_principal_id = string
    role_template_id   = string
  }))
  description = <<-EOT
    List of group + role pairs to bind within this project. Each entry creates a
    rancher2_project_role_template_binding.

    group_principal_id: Rancher principal ID for the group
      (e.g. "local://group-id", or the OIDC/LDAP principal returned by Rancher).
    role_template_id: built-in role name ("project-member", "read-only") or the
      ID of a custom rancher2_role_template (from the cluster-roles module).

    Example:
      group_role_bindings = [
        { group_principal_id = "openid://my-oidc-group", role_template_id = "project-member" },
        { group_principal_id = "openid://my-oidc-group", role_template_id = module.cluster_roles.vm_metrics_observer_role_id },
      ]
  EOT
  default     = []
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
