variable "rancher_url" {
  type        = string
  description = "Rancher server URL (e.g. https://rancher.example.com)."
}

variable "rancher_api_token" {
  type        = string
  sensitive   = true
  description = "Rancher API token with permission to create provisioning clusters."
}

variable "cloud_credential_id" {
  type        = string
  sensitive   = true
  description = "Harvester cloud credential ID (e.g. cattle-global-data:cc-xxxx)."
}

variable "vm_namespace" {
  type        = string
  description = "Harvester namespace where the VMs for this cluster are created."
}

variable "image_name" {
  type        = string
  description = "Harvester VM image in namespace/name format (e.g. default/ubuntu-22-04)."
}

variable "network_name" {
  type        = string
  description = "Harvester network attachment in namespace/name format (e.g. my-team-ns/vm-net-100)."
}
