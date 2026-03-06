variable "cluster_id" {
  type        = string
  description = "Rancher cluster ID of the Harvester HCI cluster."
}

variable "project_name" {
  type        = string
  description = "Name of the Rancher project for this tenant (lowercase, hyphen-separated)."
}

variable "namespace_name" {
  type        = string
  description = "Namespace to create within the project. Defaults to <project_name>-ns."
  default     = null
}

# Resource quotas — applied to both the project aggregate and the per-namespace default.
# Adjust namespace_* overrides if the project spans multiple namespaces.

variable "cpu_limit" {
  type        = string
  description = "Total CPU limit for the project (e.g. \"8\", \"500m\")."
}

variable "memory_limit" {
  type        = string
  description = "Total memory limit for the project (e.g. \"16Gi\", \"4096Mi\")."
}

variable "storage_limit" {
  type        = string
  description = "Total persistent storage request limit for the project (e.g. \"200Gi\")."
}

variable "namespace_cpu_limit" {
  type        = string
  description = "Per-namespace default CPU limit. Defaults to cpu_limit."
  default     = null
}

variable "namespace_memory_limit" {
  type        = string
  description = "Per-namespace default memory limit. Defaults to memory_limit."
  default     = null
}

variable "namespace_storage_limit" {
  type        = string
  description = "Per-namespace default storage limit. Defaults to storage_limit."
  default     = null
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
  default = []
}
