terraform {
  required_providers {
    dcapi = {
      source  = "registry.terraform.io/wso2/dcapi"
      version = "~> 0.1.0"
    }
  }
}

provider "dcapi" {}

resource "dcapi_project" "example" {
  tenant_id  = "my-org"
  project_id = "my-project"

  name        = "Infrastructure Team"
  description = "Core infrastructure resources: VNets, clusters, and shared VMs."

  cpu_cores  = 20
  memory_gb  = 64
  storage_gb = 500

  max_vnets      = 5
  max_clusters   = 2
  max_volumes    = 20
  max_public_ips = 3
}

resource "dcapi_vnet" "example" {
  tenant_id  = "my-org"
  project_id = dcapi_project.example.project_id

  name          = "my-vpc"
  address_space = ["10.1.0.0/16"]
  region        = "lk"
  description   = "Production VPC"
}

output "vnet_id" {
  value       = dcapi_vnet.example.id
  description = "UUID of the created VNet."
}

output "vnet_status" {
  value       = dcapi_vnet.example.status
  description = "Provisioning status — ACTIVE after a successful apply."
}
