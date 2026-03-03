# Example: Import OS images into Harvester
#
# This example registers Ubuntu 22.04 and Ubuntu 20.04 cloud images into
# Harvester so they can be used when provisioning VMs or tenant clusters.
#
# Prerequisites:
#   - A running Harvester HCI cluster
#   - The harvester provider configured with your Harvester kubeconfig
#   - Outbound internet access from the Harvester nodes (images are downloaded
#     directly by Harvester from the provided URLs)

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

module "storage" {
  source = "github.com/wso2-enterprise/open-cloud-datacenter//modules/management/storage?ref=v0.1.0"

  harvester_namespace = "default"

  managed_images = {
    "ubuntu-22-04" = {
      display_name = "Ubuntu 22.04 LTS"
      url          = "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img"
    }
    "ubuntu-20-04" = {
      display_name = "Ubuntu 20.04 LTS"
      url          = "https://cloud-images.ubuntu.com/focal/current/focal-server-cloudimg-amd64.img"
    }
  }
}
