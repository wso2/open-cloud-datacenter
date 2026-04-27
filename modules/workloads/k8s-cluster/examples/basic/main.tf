# Example: Provision a tenant RKE2 Kubernetes cluster on Harvester
#
# This example provisions a 3-node RKE2 cluster for a tenant team using
# Rancher's machine provisioning API. Harvester acts as the infrastructure
# provider via a cloud credential created by the harvester-integration module.
#
# Prerequisites:
#   - Rancher deployed and Harvester integrated (harvester-integration module)
#   - A Harvester cloud credential present in Rancher (created by the
#     harvester-integration module or provided at onboarding)
#   - OS images and VLAN networks provisioned in Harvester (storage and
#     networking modules)
#   - The rancher2 provider configured with your Rancher URL and API token

terraform {
  required_version = ">= 1.7"

  required_providers {
    rancher2 = {
      source  = "rancher/rancher2"
      version = "~> 13.1"
    }
  }
}

provider "rancher2" {
  api_url   = var.rancher_url
  token_key = var.rancher_api_token
  insecure  = true
}

module "tenant_cluster" {
  source = "github.com/wso2/open-cloud-datacenter//modules/workloads/k8s-cluster?ref=v0.8.0"

  cluster_name        = local.cluster_name
  kubernetes_version  = "v1.32.13+rke2r1"
  cloud_credential_id = var.cloud_credential_id

  enable_harvester_cloud_provider = true
  cloud_provider_config_secret    = "harvesterconfig-${local.cluster_name}"

  machine_pools = [
    {
      name          = "control-plane"
      vm_namespace  = var.vm_namespace
      quantity      = 1
      cpu_count     = "2"
      memory_size   = "4"
      disk_size     = 50
      image_name    = var.image_name
      networks      = [var.network_name]
      control_plane = true
      etcd          = true
      worker        = false
    },
    {
      name          = "worker"
      vm_namespace  = var.vm_namespace
      quantity      = 2
      cpu_count     = "4"
      memory_size   = "8"
      disk_size     = 100
      image_name    = var.image_name
      networks      = [var.network_name]
      control_plane = false
      etcd          = false
      worker        = true
    }
  ]

  manage_rke_config = true
  user_data         = local.node_user_data
}

locals {
  cluster_name   = "tenant-alpha"
  node_user_data = <<-EOT
    #cloud-config
    packages:
      - qemu-guest-agent
    runcmd:
      - systemctl enable --now qemu-guest-agent
  EOT
}

