variable "vm_memory" {
  type        = string
  description = "Memory size for the Rancher VM (e.g. '8Gi')"
  default     = "8Gi"
}

variable "vm_name" {
  type        = string
  description = "Name of the Rancher server VM"
  default     = "rancher-bootstrap"
}

variable "node_count" {
  type        = number
  description = "Number of nodes in the bootstrap cluster"
  default     = 1
}

variable "harvester_namespace" {
  type        = string
  description = "Harvester namespace to deploy into"
  default     = "default"
}

variable "cluster_network_name" {
  type        = string
  description = "Name of the base cluster network in Harvester (e.g. 'mgmt')"
  default     = "mgmt"
}

variable "cluster_vlan_id" {
  type        = number
  description = "The VLAN tag ID for the bootstrap node network"
  default     = 100
}

variable "cluster_vlan_gateway" {
  type        = string
  description = "The gateway IP for the new VLAN (Optional)"
  default     = ""
}

variable "ubuntu_image_id" {
  type        = string
  description = "Harvester ID of the Ubuntu Cloud Image"
}

variable "vm_password" {
  type        = string
  description = "Default password for the ubuntu user"
  sensitive   = true
}

variable "rancher_hostname" {
  type        = string
  description = "FQDN for the Rancher UI"
}

variable "rancher_admin_password" {
  type        = string
  description = "Bootstrap password for Rancher Admin user"
  sensitive   = true
}

variable "ippool_subnet" {
  type        = string
  description = "Subnet for the IP pool (e.g. 192.168.10.1/24)"
}

variable "ippool_gateway" {
  type        = string
  description = "Gateway for the IP pool"
}

variable "ippool_start" {
  type        = string
  description = "Start of the IP range for the pool"
}

variable "ippool_end" {
  type        = string
  description = "End of the IP range for the pool"
}
