# Example: Register Harvester HCI into Rancher
#
# This example integrates a Harvester cluster into an existing Rancher server
# by enabling the Harvester feature flag, installing the UI extension, creating
# a cloud credential, patching CoreDNS so Harvester can resolve the internal
# Rancher FQDN, and applying the registration manifest.
#
# Prerequisites:
#   - Rancher deployed and accessible (e.g. via the bootstrap module)
#   - The Harvester kubeconfig available locally
#   - kubectl available in PATH (used by local-exec provisioners)
#   - The rancher2 provider configured with your Rancher URL and access key
#   - The harvester provider configured with your Harvester kubeconfig
#   - The kubernetes provider configured against the Harvester cluster

terraform {
  required_version = ">= 1.3"

  required_providers {
    rancher2 = {
      source  = "rancher/rancher2"
      version = "~> 8.0.0"
    }
    harvester = {
      source  = "harvester/harvester"
      version = "~> 0.6.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.30.0"
    }
  }
}

provider "rancher2" {
  api_url  = "https://rancher.example.internal"
  # Provide credentials via CATTLE_ACCESS_KEY / CATTLE_SECRET_KEY env vars
  insecure = true
}

provider "harvester" {
  kubeconfig = file("~/.kube/harvester-config")
}

provider "kubernetes" {
  config_path = "~/.kube/harvester-config"
}

module "harvester_integration" {
  source = "github.com/wso2-enterprise/open-cloud-datacenter//modules/management/harvester-integration?ref=v0.1.0"

  harvester_kubeconfig   = file("~/.kube/harvester-config")
  harvester_cluster_name = "harvester-hci"
  rancher_hostname       = "rancher.example.internal"
  rancher_lb_ip          = "192.168.10.10"
}
