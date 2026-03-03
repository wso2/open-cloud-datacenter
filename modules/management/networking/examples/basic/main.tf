# Example: Create VLAN networks in Harvester
#
# This example creates two VLAN-backed networks inside Harvester HCI —
# one for management workloads and one for tenant workloads.
#
# Prerequisites:
#   - A running Harvester HCI cluster with a cluster network named "mgmt"
#   - The harvester provider configured with your Harvester kubeconfig

terraform {
  required_version = ">= 1.3"

  required_providers {
    harvester = {
      source  = "harvester/harvester"
      version = "~> 0.6.0"
    }
  }
}

provider "harvester" {
  # Configure via HARVESTER_KUBECONFIG env variable or the kubeconfig argument:
  # kubeconfig = file("~/.kube/harvester-config")
}

module "networking" {
  source = "github.com/wso2-enterprise/open-cloud-datacenter//modules/management/networking?ref=v0.1.0"

  cluster_network_name = "mgmt"
  harvester_namespace  = "default"

  vlans = {
    "vlan-mgmt" = {
      vlan_id = 100
      cidr    = "192.168.100.0/24"
      gateway = "192.168.100.1"
    }
    "vlan-tenants" = {
      vlan_id = 200
      cidr    = "192.168.200.0/24"
      gateway = "192.168.200.1"
    }
  }
}
