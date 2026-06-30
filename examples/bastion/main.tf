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

  name          = "prod-vpc"
  address_space = ["10.1.0.0/16"]
  region        = "lk-dev"
}

resource "dcapi_subnet" "example" {
  tenant_id  = dcapi_vnet.example.tenant_id
  project_id = dcapi_vnet.example.project_id
  vnet_id    = dcapi_vnet.example.vnet_uuid

  name = "bastion-subnet"
  cidr = "10.1.0.0/28"
}

resource "dcapi_bastion" "jump" {
  tenant_id  = dcapi_subnet.example.tenant_id
  project_id = dcapi_subnet.example.project_id

  name        = "jump-01"
  vnet_id     = dcapi_subnet.example.vnet_id
  subnet_id   = dcapi_subnet.example.subnet_uuid
  description = "SSH jump host for production VPC"
}

output "bastion_mgmt_ip" {
  value       = dcapi_bastion.jump.mgmt_ip
  description = "Management-plane IP for SSH access."
}

output "bastion_internal_ip" {
  value       = dcapi_bastion.jump.internal_ip
  description = "VPC-side IP address of the bastion."
}

# Retrieve with: terraform output -raw bastion_private_key
output "bastion_private_key" {
  value       = dcapi_bastion.jump.private_key
  sensitive   = true
  description = "SSH private key. SHOWN ONCE — if state is lost, delete and recreate."
}

output "bastion_console_password" {
  value       = dcapi_bastion.jump.console_password
  sensitive   = true
  description = "Web-console password. SHOWN ONCE — if state is lost, delete and recreate."
}
