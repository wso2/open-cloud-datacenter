variable "name" {
  type        = string
  description = "Name for the virtual machine."
}

variable "namespace" {
  type        = string
  description = "Harvester namespace (tenant project namespace) to create the VM in."
}

variable "cpu" {
  type        = number
  description = "Number of vCPUs."
  default     = 2
}

variable "memory" {
  type        = string
  description = "RAM in Gi (e.g. \"4Gi\")."
  default     = "4Gi"
}

variable "disk_size" {
  type        = string
  description = "Root disk size (e.g. \"40Gi\")."
  default     = "40Gi"
}

variable "image_name" {
  type        = string
  description = "Harvester image reference in namespace/name format (e.g. \"default/ubuntu-22-04\"). Use the image_ids output from the management/storage module."
}

variable "network_name" {
  type        = string
  description = "Harvester network attachment name (e.g. \"iam-team-vlan\"). Must exist in the same namespace or cluster."
}

variable "run_strategy" {
  type        = string
  description = "VM run strategy: RerunOnFailure, Always, Halted, or Manual."
  default     = "RerunOnFailure"
}

# --- SSH access ---

variable "ssh_public_key" {
  type        = string
  description = "SSH public key content to inject into the VM. When set, a harvester_ssh_key resource is created and attached. Leave null to omit."
  default     = null
  sensitive   = true
}

# --- Cloud-init ---

variable "default_user" {
  type        = string
  description = "OS username for generated cloud-init (e.g. ubuntu, debian, rocky). Only used when user_data is null and password or ssh_authorized_keys are set."
  default     = "ubuntu"
}

variable "password" {
  type        = string
  description = "Password for the default_user, injected via chpasswd.list. Only used when user_data is null. Leave null to disable password auth."
  default     = null
  sensitive   = true
}

variable "ssh_authorized_keys" {
  type        = list(string)
  description = "SSH public keys to inject into the default_user. Only used when user_data is null."
  default     = []
}

variable "user_data" {
  type        = string
  description = "Full cloud-init user-data override (plain YAML, not base64). When set, password/ssh_authorized_keys/default_user are ignored and this is passed through unchanged."
  default     = null
}

variable "network_data" {
  type        = string
  description = "Cloud-init network-data config. Ignored if user_data is null."
  default     = ""
}

variable "create_ssh_key" {
  type        = bool
  description = "When true, create a harvester_ssh_key from ssh_public_key and attach it to the VM. Must be true for ssh_public_key to have any effect."
  default     = false
}

variable "wait_for_lease" {
  type        = bool
  description = "Whether Terraform should wait for an IP lease on the primary NIC. Set to false when using static IPs via cloud-init network_data without qemu-guest-agent."
  default     = true
}

variable "additional_disks" {
  type = list(object({
    name        = string
    size        = string
    image       = optional(string)
    auto_delete = optional(bool, true)
  }))
  description = "List of additional disks to attach to the VM."
  default     = []
}

# --- Scheduled backups ---

variable "backup_schedule" {
  type        = string
  description = "Cron schedule for VM backups in UTC (e.g. \"0 2 * * *\" for daily at 2 AM). Set to null to disable scheduled backups."
  default     = null
}

variable "backup_retain" {
  type        = number
  description = "Number of backups to retain when scheduled backups are enabled."
  default     = 5
}

variable "backup_enabled" {
  type        = bool
  description = "Whether the backup schedule is active. Only applies when backup_schedule is set."
  default     = true
}

variable "backup_max_failure" {
  type        = number
  description = "Maximum consecutive failed backup attempts before suspending the schedule."
  default     = 4
}
