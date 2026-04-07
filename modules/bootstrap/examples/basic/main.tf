# Example: Bootstrap Rancher on Harvester HCI
#
# This example provisions a single-node RKE2 cluster inside Harvester and
# installs Rancher on top of it, exposing the UI via a Harvester LoadBalancer.
#
# Prerequisites:
#   - A running Harvester HCI cluster
#   - The harvester provider configured with your Harvester kubeconfig
#   - An Ubuntu cloud image already imported into Harvester (or use the
#     management/storage module to import one first)
#
# After apply, point rancher.example.internal in your /etc/hosts to the
# output rancher_lb_ip and open https://rancher.example.internal in a browser.

terraform {
  required_version = ">= 1.3"

  required_providers {
    harvester = {
      source  = "harvester/harvester"
      version = "~> 1.7"
    }
    tls = {
      source  = "hashicorp/tls"
      version = "~> 4.0"
    }
  }
}

provider "harvester" {
  # Configure via HARVESTER_KUBECONFIG env variable or the kubeconfig argument:
  # kubeconfig = file("~/.kube/harvester-config")
}

provider "tls" {}

module "bootstrap" {
  source = "../.."

  vm_name              = "rancher-bootstrap"
  node_count           = 1
  harvester_namespace  = "default"
  cluster_network_name = "mgmt"

  # The Harvester internal image ID of an Ubuntu 22.04 cloud image
  ubuntu_image_id = "default/ubuntu-22-04"

  # Credentials – supply via tfvars or environment variables; never hardcode
  vm_password        = var.vm_password
  bootstrap_password = var.bootstrap_password

  rancher_hostname = "rancher.example.internal"

  # IP pool for the Harvester LoadBalancer that exposes Rancher
  ippool_subnet  = "192.168.10.0/24"
  ippool_gateway = "192.168.10.1"
  ippool_start   = "192.168.10.10"
  ippool_end     = "192.168.10.10"
}

variable "vm_password" {
  type      = string
  sensitive = true
}

variable "bootstrap_password" {
  type      = string
  sensitive = true
}

output "rancher_url" {
  value       = "https://${module.bootstrap.rancher_hostname}"
  description = "URL of the bootstrapped Rancher server"
}

output "rancher_lb_ip" {
  value       = module.bootstrap.rancher_lb_ip
  description = "Add this IP to /etc/hosts pointing to rancher.example.internal"
}
