# ── Example: create a Private DNS Zone ───────────────────────────────────────────────
#
# A PrivateDnsZone is nested under a VNet — it provides name resolution for resources
# inside that VNet only (not resolvable from the public internet or other VNets).
# dcapi_dns_record resources nest under a PrivateDnsZone (see examples/dns_record).
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

resource "dcapi_project" "dns-project-s87" {
  tenant_id  = "tenant-s87"
  project_id = "dns-project-s87"

  name        = "DNS Testing Project"
  description = "Project for testing PrivateDnsZone and DnsRecord resources"
}

resource "dcapi_vnet" "dns-vnet-s87" {
  tenant_id  = dcapi_project.dns-project-s87.tenant_id
  project_id = dcapi_project.dns-project-s87.project_id

  name          = "dns-vnet-s87"
  address_space = ["10.4.0.0/16"]
  region        = "lk"
  description   = "VNet that hosts the private DNS zone"
}

# Create is async (202) — the provider polls GET .../dns-zones/{zone_id} every 15s
# until status reaches "ACTIVE" before returning control to Terraform.
resource "dcapi_private_dns_zone" "internal-zone-s87" {
  tenant_id  = dcapi_vnet.dns-vnet-s87.tenant_id
  project_id = dcapi_vnet.dns-vnet-s87.project_id
  vnet_id    = dcapi_vnet.dns-vnet-s87.vnet_uuid

  name        = "internal.dns-vnet-s87.wso2.com"
  description = "Private zone for internal service discovery within this VNet"
}

output "zone_id" {
  value       = dcapi_private_dns_zone.internal-zone-s87.zone_id
  description = "UUID of the DNS zone. Use this as zone_id in dcapi_dns_record resources."
}

output "zone_status" {
  value       = dcapi_private_dns_zone.internal-zone-s87.status
  description = "Should be 'ACTIVE' after successful apply."
}
