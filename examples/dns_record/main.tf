# ── Example: create DNS records inside a Private DNS Zone ────────────────────────────
#
# A DnsRecord nests under a PrivateDnsZone, which itself nests under a VNet.
# Unlike VNet/Subnet/PrivateDnsZone, create/update/delete are all SYNCHRONOUS
# (201/200/204) — no polling is needed here.
#
# name+type form the record's upsert identity (ForceNew — changing either destroys and
# recreates the record). "values" and "ttl" are updatable in place via PUT; the DC-API
# treats "values" as a full-replace, so Terraform always sends the complete desired list.
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

resource "dcapi_private_dns_zone" "internal-zone-s87" {
  tenant_id  = dcapi_vnet.dns-vnet-s87.tenant_id
  project_id = dcapi_vnet.dns-vnet-s87.project_id
  vnet_id    = dcapi_vnet.dns-vnet-s87.vnet_uuid

  name        = "internal.dns-vnet-s87.wso2.com"
  description = "Private zone for internal service discovery within this VNet"
}

# An "A" record — resolves a hostname to an internal IP address.
resource "dcapi_dns_record" "app-a-record-s87" {
  tenant_id  = dcapi_private_dns_zone.internal-zone-s87.tenant_id
  project_id = dcapi_private_dns_zone.internal-zone-s87.project_id
  vnet_id    = dcapi_private_dns_zone.internal-zone-s87.vnet_id
  zone_id    = dcapi_private_dns_zone.internal-zone-s87.zone_id

  name   = "app"
  type   = "A"
  values = ["10.4.1.10"]
  ttl    = 300
}

# A "CNAME" record — aliases one name to another within the same zone.
resource "dcapi_dns_record" "app-alias-record-s87" {
  tenant_id  = dcapi_private_dns_zone.internal-zone-s87.tenant_id
  project_id = dcapi_private_dns_zone.internal-zone-s87.project_id
  vnet_id    = dcapi_private_dns_zone.internal-zone-s87.vnet_id
  zone_id    = dcapi_private_dns_zone.internal-zone-s87.zone_id

  name   = "app-alias"
  type   = "CNAME"
  values = ["app.internal.dns-vnet-s87.wso2.com"]
  ttl    = 600
}

output "app_record_id" {
  value       = dcapi_dns_record.app-a-record-s87.record_id
  description = "API-generated UUID4 for the 'app' A record."
}

output "app_alias_values" {
  value       = dcapi_dns_record.app-alias-record-s87.values
  description = "Current values list for the CNAME record — full-replace on every update."
}
