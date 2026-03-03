# Example: Provision a tenant RKE2 Kubernetes cluster on Harvester
#
# This example provisions a 3-node RKE2 cluster for a tenant team using
# Rancher's machine provisioning API. Harvester acts as the infrastructure
# provider via a cloud credential created by the harvester-integration module.
#
# Prerequisites:
#   - Rancher deployed and Harvester integrated (harvester-integration module)
#   - A Harvester cloud credential named "harvester-local-creds" present in
#     Rancher (created automatically by the harvester-integration module)
#   - OS images and VLAN networks provisioned in Harvester (storage and
#     networking modules)
#   - The rancher2 provider configured with your Rancher URL and access key

terraform {
  required_version = ">= 1.3"

  required_providers {
    rancher2 = {
      source  = "rancher/rancher2"
      version = "~> 3.0"
    }
  }
}

provider "rancher2" {
  api_url  = "https://rancher.example.internal"
  # Provide credentials via CATTLE_ACCESS_KEY / CATTLE_SECRET_KEY env vars
  insecure = true
}

module "tenant_cluster" {
  source = "github.com/wso2-enterprise/open-cloud-datacenter//modules/workloads/k8s-cluster?ref=v0.1.0"

  cluster_name           = "tenant-alpha"
  k8s_version            = "v1.27.6+rke2r1"
  node_count             = 3
  cloud_credential_name  = "harvester-local-creds"
  harvester_namespace    = "default"

  # Reference the image and network created by the storage and networking modules
  harvester_image_name   = "default/ubuntu-22-04"
  harvester_network_name = "default/vlan-tenants"

  node_cpu       = "4"
  node_memory    = "16"
  node_disk_size = "100"
  ssh_user       = "ubuntu"
}
