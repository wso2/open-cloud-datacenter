terraform {
  required_providers {
    dcapi = {
      source  = "registry.terraform.io/wso2/dcapi"
      version = "~> 0.1.0"
    }
  }
}

provider "dcapi" {}

resource "dcapi_vnet" "example" {
  tenant_id  = "my-org"
  project_id = "my-project"

  name          = "k8s-vpc"
  address_space = ["10.2.0.0/16"]
  region        = "lk-dev"
}

resource "dcapi_subnet" "example" {
  tenant_id  = dcapi_vnet.example.tenant_id
  project_id = dcapi_vnet.example.project_id
  vnet_id    = dcapi_vnet.example.vnet_uuid

  name = "k8s-subnet"
  cidr = "10.2.1.0/24"
}

resource "dcapi_cluster" "prod" {
  tenant_id  = dcapi_subnet.example.tenant_id
  project_id = dcapi_subnet.example.project_id

  name        = "prod-rke2"
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

  vnet_id   = dcapi_subnet.example.vnet_id
  subnet_id = dcapi_subnet.example.subnet_uuid
}

output "cluster_id" {
  value       = dcapi_cluster.prod.cluster_id
  description = "UUID of the cluster."
}

output "cluster_status" {
  value       = dcapi_cluster.prod.status
  description = "Current cluster status."
}

# Retrieve with: terraform output -raw kubeconfig > ~/.kube/prod.yaml
output "kubeconfig" {
  value       = dcapi_cluster.prod.kubeconfig
  sensitive   = true
  description = "Kubeconfig YAML for connecting to the cluster."
}
