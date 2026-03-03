variable "harvester_kubeconfig" {
  type        = string
  description = "Content of the Harvester kubeconfig file"
  sensitive   = true
}

variable "harvester_cluster_name" {
  type        = string
  description = "Name for the Harvester cluster in Rancher"
  default     = "harvester-hci"
}
variable "rancher_hostname" {
  type        = string
  description = "The FQDN of the Rancher server"
}

variable "rancher_lb_ip" {
  type        = string
  description = "The IP address of the Rancher LoadBalancer"
}
