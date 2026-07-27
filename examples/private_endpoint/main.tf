# ── Example: expose a Key Vault via a Private Endpoint ────────────────────────────────
#
# A PrivateEndpoint nests under a KeyVault and exposes it inside a specific VNet/Subnet
# with a private VIP — the vault becomes reachable only from within that subnet, never
# over the public internet.
#
# Create/Delete are fully SYNCHRONOUS (201/204) — no polling required, unlike
# VNet/Subnet/Peering/DnsZone.
#
# NOTE: this route returns HTTP 501 Not Implemented if the endpoint provisioner is not
# enabled on the target DC-API instance — that will surface as a plain apply-time error.
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

resource "dcapi_project" "pe-project-s87" {
  tenant_id  = "tenant-s87"
  project_id = "pe-project-s87"

  name        = "Private Endpoint Testing Project"
  description = "Project for testing the PrivateEndpoint resource"
}

resource "dcapi_vnet" "pe-vnet-s87" {
  tenant_id  = dcapi_project.pe-project-s87.tenant_id
  project_id = dcapi_project.pe-project-s87.project_id

  name          = "pe-vnet-s87"
  address_space = ["10.5.0.0/16"]
  region        = "lk"
  description   = "VNet that will reach the key vault via a private endpoint"
}

resource "dcapi_subnet" "pe-subnet-s87" {
  tenant_id  = dcapi_vnet.pe-vnet-s87.tenant_id
  project_id = dcapi_vnet.pe-vnet-s87.project_id
  vnet_id    = dcapi_vnet.pe-vnet-s87.vnet_uuid

  name        = "pe-subnet-s87"
  cidr        = "10.5.1.0/24"
  description = "Subnet the private endpoint's VIP will be allocated from"
}

resource "dcapi_key_vault" "pe-secrets-s87" {
  tenant_id  = dcapi_project.pe-project-s87.tenant_id
  project_id = dcapi_project.pe-project-s87.project_id

  name             = "pe-secrets-s87"
  soft_delete_days = 30

  credentials_rotation = "initial"
}

resource "dcapi_private_endpoint" "kv-endpoint-s87" {
  tenant_id  = dcapi_key_vault.pe-secrets-s87.tenant_id
  project_id = dcapi_key_vault.pe-secrets-s87.project_id

  # dcapi_key_vault has no "kv_uuid"-style computed attribute (unlike dcapi_vnet's
  # vnet_uuid or dcapi_subnet's subnet_uuid) — its bare API UUID is only available as
  # the last segment of the composite state id ("tenant_id/project_id/kv_id"). Parse
  # it out with split() until the key_vault resource exposes it directly.
  kv_id = element(split("/", dcapi_key_vault.pe-secrets-s87.id), 2)

  name      = "kv-endpoint-s87"
  vnet_id   = dcapi_vnet.pe-vnet-s87.vnet_uuid
  subnet_id = dcapi_subnet.pe-subnet-s87.subnet_uuid
}

output "endpoint_ip_address" {
  value       = dcapi_private_endpoint.kv-endpoint-s87.ip_address
  description = "VIP assigned from the subnet CIDR — reachable only from within pe-subnet-s87."
}

output "endpoint_hostname" {
  value       = dcapi_private_endpoint.kv-endpoint-s87.hostname
  description = "DNS-resolvable hostname for the vault, reachable only within the VPC."
}
