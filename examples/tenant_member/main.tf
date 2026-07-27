terraform {
  required_providers {
    dcapi = {
      source  = "registry.terraform.io/wso2/dcapi"
      version = "~> 0.1.0"
    }
  }
}

provider "dcapi" {}

resource "dcapi_tenant_member" "alice" {
  tenant_id = "tenant-s87"

  user_sub      = "auth0|abc123"
  role          = "member" 
  display_alias = "Alice"
}

output "member_id" {
  value       = dcapi_tenant_member.alice.member_id
  description = "UUID of the role_assignment row."
}
