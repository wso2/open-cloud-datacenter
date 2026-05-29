variable "name" {
  type        = string
  description = "Bot user identifier used as the Rancher username and resource name prefix. Must be lowercase alphanumeric with hyphens, starting and ending with alphanumeric. Typically prefixed with the tenant project name by the caller (e.g. 'kasun-test-space-pipeline')."
  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9-]*[a-z0-9]$", var.name))
    error_message = "name must be lowercase alphanumeric with hyphens, starting and ending with alphanumeric."
  }
}

variable "password" {
  type        = string
  description = "Optional password for the bot user. When null (default), a 40-character random password is generated and stored only in Terraform state. The generated value is never surfaced as an output. Provide an explicit value only when the password is managed externally (e.g. pulled from Vault)."
  default     = null
  sensitive   = true
}

variable "cluster_id" {
  type        = string
  description = "Rancher cluster ID of the target cluster (e.g. 'c-xxxxx'). Required for cluster-level role bindings."
}

variable "project_id" {
  type        = string
  description = "Rancher project ID in the format '<cluster_id>:<short_project_id>'. Required for project-level role bindings."
}

variable "cluster_role_template_ids" {
  type        = list(string)
  description = "Role template IDs to bind for this bot at the cluster level. Each entry creates one rancher2_cluster_role_template_binding. Use IDs from the cluster-roles module (e.g. vm_creator_role_id) or built-in IDs like 'cluster-member'."
  default     = []
}

variable "project_role_template_ids" {
  type        = list(string)
  description = "Role template IDs to bind for this bot at the project level. Each entry creates one rancher2_project_role_template_binding. Use built-in IDs like 'project-member-restricted', 'project-member', or custom role IDs."
  default     = []
}

variable "can_provision_clusters" {
  type        = bool
  description = "When true, creates a custom rancher2_global_role granting the additional verbs on provisioning.cattle.io/clusters that are missing from the Standard User role. Required to provision rancher2_cluster_v2 (RKE2) resources via this bot's API token. See runbooks/tenants/bot-user-service-principal-design.md for background."
  default     = false
}

variable "token_ttl" {
  type        = number
  description = "Token TTL in seconds. Defaults to 7776000 (90 days), which matches the Rancher default auth-token-max-ttl-minutes ceiling. Combined with renew = true the token is automatically recreated on expiry. Set 0 only if your Rancher instance has no max-TTL configured."
  default     = 7776000
  validation {
    condition     = var.token_ttl >= 0
    error_message = "token_ttl must be a non-negative integer (seconds)."
  }
}

variable "token_rotation_version" {
  type        = number
  description = "Increment to rotate the API token without touching the user or password. The version is embedded in the token description, triggering ForceNew recreation of only the rancher2_custom_user_token resource."
  default     = 1
}

variable "password_rotation_version" {
  type        = number
  description = "Increment to generate a new random password. Cascades ForceNew through rancher2_user → all role bindings → token. Use only when the password itself may be compromised. Plan a short downtime window — the user is recreated and existing tokens are immediately invalidated."
  default     = 1
}

variable "enable_shared_image_access" {
  type        = bool
  description = "When true (default), grants this bot user a read-only project role binding to the shared images project so VirtualMachineImage lookups succeed during cluster/VM provisioning. Set to false only for bots that never provision infrastructure."
  default     = true
}

variable "shared_image_project_name" {
  type        = string
  description = "Name of the Rancher project that holds shared VM images. Looked up by name on the same cluster. Defaults to 'shared'. Change only if your environment uses a different project name for the image catalogue."
  default     = "shared"
  validation {
    condition     = trimspace(var.shared_image_project_name) != ""
    error_message = "shared_image_project_name must be a non-empty string."
  }
}
