# Example: Register Harvester HCI into Rancher
#
# This example integrates a Harvester cluster into an existing Rancher server
# by enabling the Harvester feature flag, installing the UI extension, creating
# a cloud credential, and applying the registration manifest.
#
# Prerequisites:
#   - Rancher deployed and accessible (e.g. via the bootstrap module)
#   - The Harvester kubeconfig available locally
#   - kubectl available in PATH (used by local-exec provisioners)
#   - The rancher2 provider configured with your Rancher URL and access key
#   - The harvester provider configured with your Harvester kubeconfig

terraform {
  required_version = ">= 1.3"

  required_providers {
    rancher2 = {
      source  = "rancher/rancher2"
      version = "~> 13.1"
    }
    harvester = {
      source  = "harvester/harvester"
      version = "~> 1.7"
    }
  }
}

provider "rancher2" {
  api_url = "https://rancher.example.internal"
  # Provide credentials via CATTLE_ACCESS_KEY / CATTLE_SECRET_KEY env vars
  insecure = true
}

provider "harvester" {
  kubeconfig = file(pathexpand("~/.kube/harvester-config"))
}

module "harvester_integration" {
  source = "github.com/wso2/open-cloud-datacenter//modules/management/harvester-integration?ref=v0.2.0"

  harvester_kubeconfig   = file(pathexpand("~/.kube/harvester-config"))
  harvester_cluster_name = "harvester-hci"
  cloud_credential_name  = "harvester-local-creds"
  cluster_labels         = {}
}
