terraform {
    required_providers {
      dcapi = {
        source  = "registry.terraform.io/wso2/dcapi"
        version = "~> 0.1.0"        
      }
    }
}

provider "dcapi" {}

variable "admin_cidr" {
  type    = string
  default = "203.0.113.5/32"
}

resource "dcapi_project" "proj-s87" {

  tenant_id = "tenant-s87"
  project_id = "proj-s87"

  name        = "Testing Project"
  description = "Project for testing the DCAPI provider"

  cpu_cores  = 20
  memory_gb  = 64
  storage_gb = 500

  max_vnets      = 5
  max_clusters   = 2
  max_volumes    = 20
  max_public_ips = 3
}


resource "dcapi_vnet" "vnet-s87" {

  tenant_id = dcapi_project.proj-s87.tenant_id
  project_id = dcapi_project.proj-s87.project_id

  name        = "vnet-my-s87"
  description = "VNet for testing the DCAPI provider"
  address_space = ["10.1.0.0/16"]
  region = "lk"  
}

resource "dcapi_subnet" "subnet-s87" {

  tenant_id = dcapi_vnet.vnet-s87.tenant_id
  project_id = dcapi_vnet.vnet-s87.project_id
  vnet_id = dcapi_vnet.vnet-s87.vnet_uuid

  name        = "subnet-my-s87"
  cidr = "10.1.1.0/24"
  description = "Subnet for testing the DCAPI provider"

}

resource "dcapi_network_security_group" "nsg-s87" {
  tenant_id  = dcapi_project.proj-s87.tenant_id
  project_id = dcapi_project.proj-s87.project_id
  name       = "nsg-s87"

  rules {
      name                       = "allow-ssh"
      direction                  = "inbound"
      priority                   = 300
      protocol                   = "tcp"
      source_address_prefix      = var.admin_cidr
      source_port_range          = "*"
      destination_address_prefix = "*"
      destination_port_range     = "22"
      action                     = "allow"
  }

  rules {
      name                       = "allow-http"
      direction                  = "inbound"
      priority                   = 200
      protocol                   = "tcp"
      source_address_prefix      = var.admin_cidr
      source_port_range          = "*"
      destination_address_prefix = "*"
      destination_port_range     = "80"
      action                     = "allow"
  }


}

resource "dcapi_nsg_attachment" "nsg-attachment-s87" {
  tenant_id   = dcapi_project.proj-s87.tenant_id
  project_id  = dcapi_project.proj-s87.project_id
  sg_id       = dcapi_network_security_group.nsg-s87.sg_id
  target_type = "subnet"
  target_id   = dcapi_subnet.subnet-s87.subnet_uuid
}


output "subnet" {
  description = "All attributes of the subnet resource"
  value       = dcapi_subnet.subnet-s87
}

output "nsg" {
  description = "All attributes of the network security group resource"
  value       = dcapi_network_security_group.nsg-s87
}
