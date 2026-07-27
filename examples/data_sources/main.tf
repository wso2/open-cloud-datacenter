# ── Example: look up existing DC-API objects with data sources ───────────────────────
#
# Every "data \"dcapi_*\"" block below performs a READ-ONLY lookup — Terraform never
# creates, updates, or deletes the object behind it. Use these when an object's lifecycle
# is owned by a different Terraform config (or by platform admins directly), and you only
# need to reference its attributes.
#
# This example is self-contained for demonstration: it first creates a small set of
# resources, then immediately looks each one up via its matching data source by name.
# In a real setup, the "data" blocks would point at objects created elsewhere.
#
# HOW TO USE THIS EXAMPLE:
#   1. Copy this file into your Terraform working directory.
#   2. Set DCAPI_ENDPOINT and DCAPI_TOKEN environment variables.
#   3. Run: terraform init && terraform plan && terraform apply

terraform {
  required_providers {
    dcapi = {
      source  = "registry.terraform.io/wso2/dcapi"
      version = "~> 0.1.0"
    }
  }
}

provider "dcapi" {}

# ── Resources to look up ──────────────────────────────────────────────────────────────

resource "dcapi_project" "ds-project-s87" {
  tenant_id  = "tenant-s87"
  project_id = "ds-project-s87"

  name        = "Data Source Testing Project"
  description = "Project used to exercise every dcapi_* data source"
}

resource "dcapi_vnet" "ds-vnet-s87" {
  tenant_id  = dcapi_project.ds-project-s87.tenant_id
  project_id = dcapi_project.ds-project-s87.project_id

  name          = "ds-vnet-s87"
  address_space = ["10.9.0.0/16"]
  region        = "lk"
  description   = "VNet looked up by the dcapi_vnet data source"
}

resource "dcapi_subnet" "ds-subnet-s87" {
  tenant_id  = dcapi_vnet.ds-vnet-s87.tenant_id
  project_id = dcapi_vnet.ds-vnet-s87.project_id
  vnet_id    = dcapi_vnet.ds-vnet-s87.vnet_uuid

  name = "ds-subnet-s87"
  cidr = "10.9.1.0/24"
}

resource "dcapi_route_table" "ds-rt-s87" {
  tenant_id  = dcapi_vnet.ds-vnet-s87.tenant_id
  project_id = dcapi_vnet.ds-vnet-s87.project_id
  vnet_id    = dcapi_vnet.ds-vnet-s87.vnet_uuid

  name = "ds-rt-s87"
  routes {
    name             = "default-internet"
    destination_cidr = "0.0.0.0/0"
    next_hop_type    = "internet"
  }
}

resource "dcapi_network_security_group" "ds-nsg-s87" {
  tenant_id  = dcapi_project.ds-project-s87.tenant_id
  project_id = dcapi_project.ds-project-s87.project_id
  name       = "ds-nsg-s87"

  rules {
    name                       = "allow-ssh"
    direction                  = "inbound"
    priority                   = 100
    protocol                   = "tcp"
    source_address_prefix      = "*"
    source_port_range          = "*"
    destination_address_prefix = "*"
    destination_port_range     = "22"
    action                     = "allow"
  }
}

resource "dcapi_key_vault" "ds-kv-s87" {
  tenant_id  = dcapi_project.ds-project-s87.tenant_id
  project_id = dcapi_project.ds-project-s87.project_id
  name       = "ds-kv-s87"
}

resource "dcapi_private_dns_zone" "ds-zone-s87" {
  tenant_id  = dcapi_vnet.ds-vnet-s87.tenant_id
  project_id = dcapi_vnet.ds-vnet-s87.project_id
  vnet_id    = dcapi_vnet.ds-vnet-s87.vnet_uuid

  name = "ds-zone-s87.internal"
}

resource "dcapi_dns_record" "ds-record-s87" {
  tenant_id  = dcapi_private_dns_zone.ds-zone-s87.tenant_id
  project_id = dcapi_private_dns_zone.ds-zone-s87.project_id
  vnet_id    = dcapi_private_dns_zone.ds-zone-s87.vnet_id
  zone_id    = dcapi_private_dns_zone.ds-zone-s87.zone_id

  name   = "app"
  type   = "A"
  values = ["10.9.1.10"]
}

resource "dcapi_vnet" "ds-vnet-peer-s87" {
  tenant_id  = dcapi_project.ds-project-s87.tenant_id
  project_id = dcapi_project.ds-project-s87.project_id

  name          = "ds-vnet-peer-s87"
  address_space = ["10.10.0.0/16"]
  region        = "lk"
}

resource "dcapi_vnet_peering" "ds-peering-s87" {
  tenant_id  = dcapi_vnet.ds-vnet-s87.tenant_id
  project_id = dcapi_vnet.ds-vnet-s87.project_id
  vnet_id    = dcapi_vnet.ds-vnet-s87.vnet_uuid

  name         = "ds-peering-s87"
  peer_vnet_id = dcapi_vnet.ds-vnet-peer-s87.vnet_uuid
}

# ── Data source lookups ────────────────────────────────────────────────────────────────

data "dcapi_tenant" "lookup" {
  id = dcapi_project.ds-project-s87.tenant_id
}

data "dcapi_project" "lookup" {
  tenant_id = dcapi_project.ds-project-s87.tenant_id
  id        = dcapi_project.ds-project-s87.project_id
}

data "dcapi_vnet" "lookup" {
  tenant_id  = dcapi_vnet.ds-vnet-s87.tenant_id
  project_id = dcapi_vnet.ds-vnet-s87.project_id
  name       = dcapi_vnet.ds-vnet-s87.name
}

data "dcapi_subnet" "lookup" {
  tenant_id  = dcapi_subnet.ds-subnet-s87.tenant_id
  project_id = dcapi_subnet.ds-subnet-s87.project_id
  vnet_id    = dcapi_subnet.ds-subnet-s87.vnet_id
  name       = dcapi_subnet.ds-subnet-s87.name
}

data "dcapi_route_table" "lookup" {
  tenant_id  = dcapi_route_table.ds-rt-s87.tenant_id
  project_id = dcapi_route_table.ds-rt-s87.project_id
  vnet_id    = dcapi_route_table.ds-rt-s87.vnet_id
  name       = dcapi_route_table.ds-rt-s87.name
}

data "dcapi_network_security_group" "lookup" {
  tenant_id  = dcapi_network_security_group.ds-nsg-s87.tenant_id
  project_id = dcapi_network_security_group.ds-nsg-s87.project_id
  name       = dcapi_network_security_group.ds-nsg-s87.name
}

data "dcapi_key_vault" "lookup" {
  tenant_id  = dcapi_key_vault.ds-kv-s87.tenant_id
  project_id = dcapi_key_vault.ds-kv-s87.project_id
  name       = dcapi_key_vault.ds-kv-s87.name
}

data "dcapi_private_dns_zone" "lookup" {
  tenant_id  = dcapi_private_dns_zone.ds-zone-s87.tenant_id
  project_id = dcapi_private_dns_zone.ds-zone-s87.project_id
  vnet_id    = dcapi_private_dns_zone.ds-zone-s87.vnet_id
  name       = dcapi_private_dns_zone.ds-zone-s87.name
}

data "dcapi_dns_record" "lookup" {
  tenant_id  = dcapi_dns_record.ds-record-s87.tenant_id
  project_id = dcapi_dns_record.ds-record-s87.project_id
  vnet_id    = dcapi_dns_record.ds-record-s87.vnet_id
  zone_id    = dcapi_dns_record.ds-record-s87.zone_id
  name       = dcapi_dns_record.ds-record-s87.name
  type       = dcapi_dns_record.ds-record-s87.type
}

data "dcapi_vnet_peering" "lookup" {
  tenant_id  = dcapi_vnet_peering.ds-peering-s87.tenant_id
  project_id = dcapi_vnet_peering.ds-peering-s87.project_id
  vnet_id    = dcapi_vnet_peering.ds-peering-s87.vnet_id
  name       = dcapi_vnet_peering.ds-peering-s87.name
}

# Platform-wide lookups — not tied to any resource created above.

data "dcapi_region" "lk" {
  name = "lk"
}

data "dcapi_image" "ubuntu" {
  tenant_id    = dcapi_project.ds-project-s87.tenant_id
  display_name = "Ubuntu 22.04"
}

# ── Outputs ─────────────────────────────────────────────────────────────────────────────

output "looked_up_vnet_uuid" {
  value       = data.dcapi_vnet.lookup.vnet_uuid
  description = "UUID resolved by looking up the VNet by name."
}

output "looked_up_key_vault_uuid" {
  value       = data.dcapi_key_vault.lookup.kv_uuid
  description = "Bare UUID resolved by looking up the Key Vault by name — use this for dcapi_private_endpoint.kv_id."
}

output "region_status" {
  value       = data.dcapi_region.lk.status
  description = "Current derived health of the \"lk\" region."
}
