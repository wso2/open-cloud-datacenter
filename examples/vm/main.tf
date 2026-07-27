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
  project_id = "vm-project-s87"

  name        = "project-s87"
  description = "Vm created to test the vm vpc mode"

}

resource "dcapi_vnet" "vnet-s87" {
  tenant_id  = dcapi_project.project-s87.tenant_id   
  project_id = dcapi_project.project-s87.project_id  

  name          = "vnet-s87"
  address_space = ["10.1.0.0/16"]
  region        = "lk"
  description   = "vnet created to test the vm vpc mode"
}

resource "dcapi_subnet" "subnet-s87" {
  tenant_id  = dcapi_vnet.vnet-s87.tenant_id   
  project_id = dcapi_vnet.vnet-s87.project_id  
  vnet_id    = dcapi_vnet.vnet-s87.vnet_uuid   

  name        = "subnet-s87"
  cidr        = "10.1.1.0/24"
  description = "subnet created to test the vm vpc mode"
}

resource "dcapi_virtual_machine" "vm-s87" {

  tenant_id  = dcapi_subnet.subnet-s87.tenant_id   
  project_id = dcapi_subnet.subnet-s87.project_id  

  name = "vm-s87"
  size = "medium"
  image_name = "rancher-infra/ubuntu-22-04"

  vnet_id   = dcapi_subnet.subnet-s87.vnet_id    
  subnet_id = dcapi_subnet.subnet-s87.subnet_uuid     
}

output "vm_ip" {
  value       = dcapi_virtual_machine.vm-s87.ip_address
  description = "IP address assigned to the vm-s87 VM by DC-API."
}

output "vm_private_key" {
  value       = dcapi_virtual_machine.vm-s87.private_key
  sensitive   = true
  description = "SSH private key for vm-s87 VM. Only available right after terraform apply."
}

output "vm_console_password" {
  value       = dcapi_virtual_machine.vm-s87.console_password
  sensitive   = true
  description = "Web-console password for vm-s87 VM. Only available right after terraform apply."
}
