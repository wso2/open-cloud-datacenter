variable "api_url" {
  type        = string
  description = "Harvester API URL, i.e. the cluster VIP over HTTPS (e.g. 'https://192.168.10.5')."
}

variable "initial_password" {
  type        = string
  description = "Admin password the Harvester installer set. Harvester's own default is 'admin'."
  sensitive   = true
  default     = "admin"
}

variable "password" {
  type        = string
  description = "New admin password to set on first login."
  sensitive   = true
}

variable "kubeconfig_path" {
  type        = string
  nullable    = false
  description = "File path the generated admin kubeconfig is written to. Deliberately has no default: the provider's own default (~/.kube/config) would overwrite the operator's kubeconfig."
}
