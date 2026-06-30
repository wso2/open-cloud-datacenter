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
  tenant_id  = dcapi_project.example.tenant_id
  project_id = dcapi_project.example.project_id

  name          = "prod-vpc"
  address_space = ["10.1.0.0/16"]
  region        = "lk"
  description   = "Production VPC"
}

resource "dcapi_subnet" "app" {
  tenant_id  = dcapi_vnet.example.tenant_id
  project_id = dcapi_vnet.example.project_id
  vnet_id    = dcapi_vnet.example.vnet_uuid

  name        = "app-subnet"
  cidr        = "10.1.1.0/24"
  description = "Application tier subnet"
}

output "subnet_id" {
  value       = dcapi_subnet.app.id
  description = "UUID of the created Subnet."
}

output "subnet_gateway" {
  value       = dcapi_subnet.app.gateway
  description = "Gateway IP assigned by the API."
}
