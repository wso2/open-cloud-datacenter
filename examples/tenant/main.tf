terraform {
  required_providers {
    dcapi = {
      source  = "registry.terraform.io/wso2/dcapi"
      version = "~> 0.1.0"
    }
  }
}


provider "dcapi" {}

resource "dcapi_tenant" "tenant-s87" {

  tenant_id = "tenant-s87"
  name = "tenant-s87"
  description = "Tenant for testing the sovereign cloud provider."
}


output "tenant_id" {
  value       = dcapi_tenant.tenant-s87.id
  description = "The tenant slug used in all DC-API API paths."
}

output "tenant_uuid" {
  value       = dcapi_tenant.tenant-s87.tenant_uuid
  description = "The API-generated UUID4 that permanently identifies this tenant."
}

output "asgardeo_group" {
  value       = dcapi_tenant.tenant-s87.asgardeo_group
  description = "The Asgardeo group name assigned to this tenant."
}

output "created_at" {
  value       = dcapi_tenant.tenant-s87.created_at
  description = "RFC3339 timestamp of when the tenant was created."
}
