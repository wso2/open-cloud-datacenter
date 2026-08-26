variable "rancher_url" {
  description = "HTTPS URL of the Rancher server managing the Target."
  type        = string

  validation {
    condition     = can(regex("^https://", var.rancher_url))
    error_message = "rancher_url must use HTTPS."
  }
}

variable "rancher_api_token" {
  description = "Rancher API token supplied through TF_VAR_rancher_api_token."
  type        = string
  sensitive   = true
}

variable "harvester_kubeconfig_path" {
  description = "Path to the read-only mounted Target Harvester kubeconfig."
  type        = string
  default     = "/credentials/harvester/kubeconfig"
}

variable "harvester_cluster_id" {
  description = "Rancher cluster ID of the Target Harvester cluster."
  type        = string
}

variable "project_name" {
  description = "Name of the tenant project managed by this workflow."
  type        = string

  validation {
    condition     = can(regex("^cap002-[a-z0-9]([a-z0-9-]*[a-z0-9])?$", var.project_name))
    error_message = "project_name must start with cap002- and use lowercase DNS-label characters."
  }
}

variable "cpu_limit" {
  description = "Aggregate tenant CPU quota."
  type        = string
  default     = "24"
}

variable "memory_limit" {
  description = "Aggregate tenant memory quota."
  type        = string
  default     = "32Gi"
}

variable "storage_limit" {
  description = "Aggregate tenant persistent storage quota."
  type        = string
  default     = "256Gi"
}

variable "vm_network_vlan_id" {
  description = "VLAN ID used by the tenant VM network."
  type        = number
  default     = 700

  validation {
    condition     = var.vm_network_vlan_id >= 1 && var.vm_network_vlan_id <= 4094
    error_message = "vm_network_vlan_id must be between 1 and 4094."
  }
}

variable "group_role_bindings" {
  description = "Rancher principal and project role pairs for the tenant."
  type = list(object({
    role_template_id   = string
    group_principal_id = optional(string)
    group_id           = optional(string)
    user_principal_id  = optional(string)
    user_id            = optional(string)
  }))
  default = []

  validation {
    condition = alltrue([
      for binding in var.group_role_bindings :
      length(compact([
        binding.group_principal_id,
        binding.group_id,
        binding.user_principal_id,
        binding.user_id,
      ])) == 1
    ])
    error_message = "Each role binding must select exactly one principal field."
  }
}
