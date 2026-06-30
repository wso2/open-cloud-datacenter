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

# VPC mode: supply vnet_id + subnet_id.
# Legacy bridge mode: supply network_name instead (mutually exclusive with vnet_id/subnet_id).
resource "dcapi_virtual_machine" "web" {
  tenant_id  = dcapi_subnet.app.tenant_id
  project_id = dcapi_subnet.app.project_id

  name       = "web-01"
  size       = "medium"
  image_name = "rancher-infra/ubuntu-22-04"

  vnet_id   = dcapi_subnet.app.vnet_id
  subnet_id = dcapi_subnet.app.subnet_uuid
}

output "vm_ip" {
  value       = dcapi_virtual_machine.web.ip_address
  description = "IP address assigned to the VM."
}

# Retrieve with: terraform output -raw vm_private_key
# The private key is shown once at creation — store it securely.
output "vm_private_key" {
  value       = dcapi_virtual_machine.web.private_key
  sensitive   = true
  description = "SSH private key. Available immediately after apply; not recoverable if state is lost."
}

output "vm_console_password" {
  value       = dcapi_virtual_machine.web.console_password
  sensitive   = true
  description = "Web-console password. Available immediately after apply; not recoverable if state is lost."
}

output "subnet_id" {
  value       = dcapi_subnet.app.id
  description = "UUID of the created Subnet."
}

output "subnet_gateway" {
  value       = dcapi_subnet.app.gateway
  description = "Gateway IP assigned by the API."
}
