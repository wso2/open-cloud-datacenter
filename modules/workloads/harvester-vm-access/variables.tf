variable "vm_namespace" {
  description = "Harvester namespace the consumer team uses for their workloads."
  type        = string
}

variable "consumer_name" {
  description = "Short identifier for the consumer team (lowercase, hyphen-separated). Used in SA and RoleBinding names. Must be unique per namespace."
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9-]*[a-z0-9]$", var.consumer_name))
    error_message = "consumer_name must be lowercase alphanumeric with hyphens (DNS-1123 subdomain)."
  }
}

variable "harvester_api_server" {
  description = "Direct Harvester Kubernetes API server URL (port 6443, e.g. https://192.168.10.100:6443). Do NOT use the Rancher proxy URL (/k8s/clusters/local)."
  type        = string
}
