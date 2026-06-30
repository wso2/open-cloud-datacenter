terraform {
  required_providers {
    dcapi = {
      source  = "registry.terraform.io/wso2/dcapi"
      version = "~> 0.1.0"
    }
  }
}

provider "dcapi" {}

resource "dcapi_tenant" "example" {
  tenant_id = "my-org"

  name        = "My Organisation"
  description = "Production tenant"

  # Quota caps — updatable after creation without recreating the tenant.
  # Set to 0 to use the platform default (80 cores / 256 GB RAM / 2000 GB storage).
  cpu_cores_cap  = 160
  memory_gb_cap  = 512
  storage_gb_cap = 4000
}

output "tenant_id" {
  value       = dcapi_tenant.example.id
  description = "The tenant slug used in all DC-API paths."
}

output "tenant_uuid" {
  value       = dcapi_tenant.example.tenant_uuid
  description = "API-generated UUID that permanently identifies this tenant."
}

output "asgardeo_group" {
  value       = dcapi_tenant.example.asgardeo_group
  description = "Asgardeo group name assigned to this tenant."
}

output "created_at" {
  value       = dcapi_tenant.example.created_at
  description = "RFC3339 timestamp of when the tenant was created."
}
