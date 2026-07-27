terraform {
  required_providers {
    dcapi = {
      source  = "registry.terraform.io/wso2/dcapi"
      version = "~> 0.1.0"
    }
  }
}

provider "dcapi" {}

resource "dcapi_project" "peer-project-s87" {
  tenant_id  = "tenant-s87"
  project_id = "peer-project-s87"

  name        = "VNet Peering Testing Project"
  description = "Project for testing the VNetPeering resource"
}

# Two VNets with non-overlapping CIDRs in the same region — a peering requirement.
resource "dcapi_vnet" "peer-vnet-a-s87" {
  tenant_id  = dcapi_project.peer-project-s87.tenant_id
  project_id = dcapi_project.peer-project-s87.project_id

  name          = "peer-vnet-a-s87"
  address_space = ["10.6.0.0/16"]
  region        = "lk"
  description   = "First VNet in the peering pair"
}

resource "dcapi_vnet" "peer-vnet-b-s87" {
  tenant_id  = dcapi_project.peer-project-s87.tenant_id
  project_id = dcapi_project.peer-project-s87.project_id

  name          = "peer-vnet-b-s87"
  address_space = ["10.7.0.0/16"]
  region        = "lk"
  description   = "Second VNet in the peering pair"
}

# Direction 1: routes FROM vnet-a TOWARDS vnet-b.
resource "dcapi_vnet_peering" "a-to-b-s87" {
  tenant_id  = dcapi_vnet.peer-vnet-a-s87.tenant_id
  project_id = dcapi_vnet.peer-vnet-a-s87.project_id
  vnet_id    = dcapi_vnet.peer-vnet-a-s87.vnet_uuid

  name         = "a-to-b-s87"
  peer_vnet_id = dcapi_vnet.peer-vnet-b-s87.vnet_uuid

  allow_forwarded_traffic = false
}

# Direction 2: routes FROM vnet-b TOWARDS vnet-a — required for full bidirectional
# connectivity. Without this second resource, only vnet-a could reach vnet-b.
resource "dcapi_vnet_peering" "b-to-a-s87" {
  tenant_id  = dcapi_vnet.peer-vnet-b-s87.tenant_id
  project_id = dcapi_vnet.peer-vnet-b-s87.project_id
  vnet_id    = dcapi_vnet.peer-vnet-b-s87.vnet_uuid

  name         = "b-to-a-s87"
  peer_vnet_id = dcapi_vnet.peer-vnet-a-s87.vnet_uuid

  allow_forwarded_traffic = false
}

output "a_to_b_status" {
  value       = dcapi_vnet_peering.a-to-b-s87.status
  description = "Should be 'ACTIVE' after successful apply."
}

output "b_to_a_status" {
  value       = dcapi_vnet_peering.b-to-a-s87.status
  description = "Should be 'ACTIVE' after successful apply."
}
