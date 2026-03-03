variable "cluster_name" {
  type        = string
  description = "The name of the K8s cluster to provision"
}

variable "k8s_version" {
  type        = string
  description = "The RKE2 Kubernetes version (e.g., v1.27.6+rke2r1)"
  default     = "v1.27.6+rke2r1"
}

variable "node_count" {
  type        = number
  description = "Number of control-plane/worker hybrid nodes"
  default     = 3
}

variable "cloud_credential_name" {
  type        = string
  description = "Name of the Harvester cloud credential in Rancher"
  default     = "harvester-creds"
}

variable "harvester_namespace" {
  type        = string
  description = "Namespace in Harvester to deploy the VMs"
  default     = "default"
}

variable "harvester_image_name" {
  type        = string
  description = "Harvester image name for the base OS (e.g., default/ubuntu-22.04)"
}

variable "harvester_network_name" {
  type        = string
  description = "Harvester network name (e.g., default/vlan-100)"
}

variable "node_cpu" {
  type        = string
  description = "CPU string (e.g., '4')"
  default     = "4"
}

variable "node_memory" {
  type        = string
  description = "Memory string (e.g., '16Gi')"
  default     = "16"
}

variable "node_disk_size" {
  type        = string
  description = "Disk size string (e.g., '100Gi')"
  default     = "100"
}

variable "ssh_user" {
  type        = string
  description = "SSH username for the VM OS"
  default     = "ubuntu"
}
