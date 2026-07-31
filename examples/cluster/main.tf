terraform {
  required_providers {
    dcapi = {
      source  = "registry.terraform.io/wso2/dcapi"
      version = "~> 0.1.0"
    }
  }
}

provider "dcapi" {}

resource "dcapi_project" "project-s87" {
  tenant_id  = "tenant-s87"
  project_id = "project-s87"

  name        = "Infrastructure Team"
  description = "Core infrastructure resources: VNets, clusters, and shared VMs."

}

resource "dcapi_vnet" "vnet-s87" {
  tenant_id  = dcapi_project.project-s87.tenant_id
  project_id = dcapi_project.project-s87.project_id

  name          = "cluster-vnet-s87"
  address_space = ["10.2.0.0/16"]
  region        = "lk"
}

resource "dcapi_subnet" "subnet-s87" {
  tenant_id  = dcapi_vnet.vnet-s87.tenant_id
  project_id = dcapi_vnet.vnet-s87.project_id
  vnet_id    = dcapi_vnet.vnet-s87.vnet_uuid

  name = "cluster-subnet-s87"
  cidr = "10.2.1.0/24"
}

resource "dcapi_cluster" "cluster-s87" {
  tenant_id  = dcapi_subnet.subnet-s87.tenant_id
  project_id = dcapi_subnet.subnet-s87.project_id

  name        = "cluster-s87-rke2"
  k8s_version = "v1.33.10+rke2r3"
  image_name  = "rancher-infra/rke2-ubuntu-22-04"

  system_pool {
    size    = "large"
    count   = 3
    disk_gb = 100
  }

  worker_pools {
    name  = "app-pool"
    size  = "medium"
    count = 2
    labels = {
      "pool-type" = "app"
    }
  }

  vnet_id   = dcapi_subnet.subnet-s87.vnet_id
  subnet_id = dcapi_subnet.subnet-s87.subnet_uuid
}

output "cluster_id" {
  value       = dcapi_cluster.cluster-s87.cluster_id
  description = "UUID of the cluster."
}

output "cluster_status" {
  value       = dcapi_cluster.cluster-s87.status
  description = "Current cluster status."
}

# Retrieve with: (umask 077; terraform output -raw kubeconfig > ~/.kube/prod.yaml)
output "kubeconfig" {
  value       = dcapi_cluster.cluster-s87.kubeconfig
  sensitive   = true
  description = "Kubeconfig YAML for connecting to the cluster."
}
