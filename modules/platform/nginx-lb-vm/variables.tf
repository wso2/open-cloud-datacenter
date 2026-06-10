variable "vm_name" {
  type        = string
  description = "Name of the nginx load balancer VM"
  default     = "rancher-lb"
}

variable "harvester_namespace" {
  type        = string
  description = "Harvester namespace to deploy into"
}

variable "vm_cpu" {
  type        = number
  description = "vCPU count"
  default     = 2
}

variable "vm_memory" {
  type        = string
  description = "Memory size (e.g. '4Gi')"
  default     = "4Gi"
}

variable "vm_disk_size" {
  type        = string
  description = "Root disk size"
  default     = "20Gi"
}

variable "vm_disk_auto_delete" {
  type    = bool
  default = true
}

variable "image_id" {
  type        = string
  description = "Harvester image ID (namespace/name) for the VM OS image"
}

variable "network_name" {
  type        = string
  description = "Full NAD reference (namespace/name) for the bridge network"
}

variable "network_interface_name" {
  type    = string
  default = "nic-1"
}

variable "vm_password" {
  type      = string
  sensitive = true
}

variable "ssh_key_ids" {
  type        = list(string)
  description = "Existing Harvester SSH key IDs to attach"
  default     = []
}

variable "ssh_authorized_keys" {
  type        = list(string)
  description = "Actual SSH public key strings to place in cloud-init ssh_authorized_keys. Harvester requires these to match any key referenced in ssh_key_ids."
  default     = []
}

variable "static_ip" {
  type        = string
  description = "Static IP address for the nginx LB VM"

  validation {
    condition     = var.static_ip != ""
    error_message = "static_ip is required for the nginx LB VM."
  }
}

variable "gateway" {
  type        = string
  description = "Default gateway for the static IP"
}

variable "subnet_prefix" {
  type        = number
  description = "CIDR prefix length (e.g. 25 for /25)"
  default     = 24
}

variable "dns_servers" {
  type        = list(string)
  description = "DNS servers written as nameservers in netplan. Defaults to [\"8.8.8.8\"] when empty."
  default     = []
}

variable "rke2_node_ips" {
  type        = list(string)
  description = "List of RKE2 node IPs to load-balance"

  validation {
    condition     = length(var.rke2_node_ips) > 0
    error_message = "At least one RKE2 node IP must be provided."
  }
}

variable "rancher_hostname" {
  type        = string
  description = "FQDN used as nginx server_name for the Rancher vhost"
}

variable "tls_cert" {
  type        = string
  sensitive   = true
  description = "PEM-encoded TLS certificate (full chain)"
}

variable "tls_key" {
  type        = string
  sensitive   = true
  description = "PEM-encoded TLS private key"
}

variable "enable_usb_tablet" {
  type    = bool
  default = true
}
