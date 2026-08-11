variable "project_id" {
  description = "The Rancher v2 project ID (format: <cluster_id>:<project_id>)"
  type        = string
}

variable "namespaces" {
  description = "Map of namespaces to create, where the key is the namespace name and the value contains its specific container resource limits and resource quotas."
  type = map(object({
    container_resource_limit = optional(object({
      cpu_limit      = optional(string)
      memory_limit   = optional(string)
      cpu_request    = optional(string)
      memory_request = optional(string)
    }))
    resource_quota = object({
      config_maps              = optional(string)
      cpu_limit                = string
      memory_limit             = string
      persistent_volume_claims = optional(string)
      pods                     = optional(string)
      replication_controllers  = optional(string)
      cpu_request              = optional(string)
      memory_request           = optional(string)
      storage_limit            = string
      secrets                  = optional(string)
      services_load_balancers  = optional(string)
      services_node_ports      = optional(string)
    })
  }))
  default = {}

  validation {
    condition = alltrue([
      for ns in keys(var.namespaces) : can(regex("^[a-z0-9]([-a-z0-9]*[a-z0-9])?$", ns))
    ])
    error_message = "Namespace names must consist of lowercase alphanumeric characters or '-', and must start and end with an alphanumeric character."
  }
}
variable "description" {
  description = "Optional description applied to all created namespaces."
  type        = string
  default     = null
}

variable "labels" {
  description = "Labels to apply to the namespaces."
  type        = map(string)
  default     = {}
}

variable "annotations" {
  description = "Annotations to apply to the namespaces."
  type        = map(string)
  default     = {}
}

variable "wait_for_cluster" {
  description = "Wait for cluster to become active before creating namespaces."
  type        = bool
  default     = false
}
