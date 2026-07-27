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

}

resource "dcapi_vnet" "vnet-s87" {

  tenant_id = dcapi_project.proj-s87.tenant_id
  project_id = dcapi_project.proj-s87.project_id

  name        = "vnet-my-s87"
  description = "VNet for testing the DCAPI provider"

  address_space = ["10.1.0.0/16"]

  region = "lk"  
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

output "route_table_id" {
  value = dcapi_route_table.s87-with-routes.route_table_id
}

output "status" {
  value = dcapi_route_table.s87-with-routes.status
}

output "routes" {
  value = dcapi_route_table.s87-with-routes.routes
}

# resource "dcapi_vnet" "vnet-s87" {

#   tenant_id = dcapi_project.proj-s87-rt.tenant_id
#   project_id = dcapi_project.proj-s87-rt.project_id

#   name        = "vnet-my-s87"
#   description = "VNet for testing the DCAPI provider"

#   address_space = ["10.1.0.0/16"]

#   region = "lk"  
# }


# resource "dcapi_route_table" "s87-no_routes" {
#   tenant_id  = dcapi_vnet.vnet-s87.tenant_id
#   project_id = dcapi_vnet.vnet-s87.project_id
#   vnet_id    = dcapi_vnet.vnet-s87.vnet_uuid
#   name       = "s87-rt-no-routes"
# }

# output "route_table_id" {
#   value = dcapi_route_table.s87-no_routes.route_table_id
# }

# output "status" {
#   value = dcapi_route_table.s87-no_routes.status
# }

# output "provider_type" {
#   value = dcapi_route_table.s87-no_routes.provider_type
# }

# output "created_at" {
#   value = dcapi_route_table.s87-no_routes.created_at
# }

# output "routes" {
#   value = dcapi_route_table.s87-no_routes.routes
# }