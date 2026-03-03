variable "harvester_namespace" {
  type        = string
  description = "Harvester namespace to manage networks within"
  default     = "default"
}

variable "cluster_network_name" {
  type        = string
  description = "The name of the cluster network in Harvester to attach these VLANs"
  default     = "mgmt"
}

variable "vlans" {
  type = map(object({
    vlan_id = number
    cidr    = string
    gateway = string
  }))
  description = "A map of VLAN names to their configuration"
  default     = {}
}
