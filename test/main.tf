terraform {
  required_providers {
    dcapi = {
      source  = "registry.terraform.io/wso2/dcapi"
      version = "~> 0.1.0"
    }
  }
}

provider "dcapi" {}

resource "dcapi_tenant" "test" {
  id          = "example-tenant"
  name        = "Example Tenant"
  description = "Testing the Terraform provider"
}

output "tenant_id" {
  value = dcapi_tenant.test.id
}

output "tenant_uuid" {
  value = dcapi_tenant.test.tenant_uuid
}

output "asgardeo_group" {
  value = dcapi_tenant.test.asgardeo_group
}

output "created_at" {
  value = dcapi_tenant.test.created_at
}
