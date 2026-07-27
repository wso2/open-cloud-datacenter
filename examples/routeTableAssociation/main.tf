terraform {
    required_providers {
      dcapi = {
        source  = "registry.terraform.io/wso2/dcapi"
        version = "~> 0.1.0"        
      }
    }
}

provider "dcapi" {}

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

resource "dcapi_route_table" "s87-with-routes" {
  tenant_id  = dcapi_project.proj-s87.tenant_id
  project_id = dcapi_project.proj-s87.project_id
  vnet_id    = dcapi_vnet.vnet-s87.vnet_uuid
  name       = "rt-with-routes"

  routes {
    name             = "route-vnet-local"
    destination_cidr = "10.0.0.0/8"
    next_hop_type    = "vnet_local"
  }

  routes {
    name             = "route-internet"
    destination_cidr = "0.0.0.0/0"
    next_hop_type    = "internet"
  }

  routes {
    name             = "route-virtual-appliance"
    destination_cidr = "192.168.1.0/24"
    next_hop_type    = "virtual_appliance"
    next_hop_ip      = "10.0.1.5"
  }
}

resource "dcapi_route_table_association" "s87-rt-assoc" {
  tenant_id      = dcapi_route_table.s87-with-routes.tenant_id
  project_id     = dcapi_route_table.s87-with-routes.project_id
  vnet_id        = dcapi_route_table.s87-with-routes.vnet_id
  route_table_id = dcapi_route_table.s87-with-routes.route_table_id
  subnet_id      = dcapi_subnet.subnet-s87.subnet_uuid
}

output "association_id" {
  value = dcapi_route_table_association.s87-rt-assoc.association_id
}

output "created_at" {
  value = dcapi_route_table_association.s87-rt-assoc.created_at
}

output "warning" {
  value = dcapi_route_table_association.s87-rt-assoc.warning
}