terraform {
  required_providers {
    dcapi = {
      source  = "registry.terraform.io/wso2/dcapi"
      version = "~> 0.1.0"
    }
  }
}

provider "dcapi" {}

resource "dcapi_key_vault" "prod_secrets" {
  tenant_id  = "tenant-s87"
  project_id = "kv-project-s87"

  name                 = "prod-secrets-s87"
  soft_delete_days     = 30
  credentials_rotation = "initial"
}

resource "dcapi_key_vault_secret" "db_password" {
  tenant_id  = dcapi_key_vault.prod_secrets.tenant_id
  project_id = dcapi_key_vault.prod_secrets.project_id

  # dcapi_key_vault.id is the composite state id ("tenant_id/project_id/kv_id") —
  # pull out just the kv_id, same pattern examples/private_endpoint uses.
  key_vault_id = element(split("/", dcapi_key_vault.prod_secrets.id), 2)

  key   = "db-password"
  value = "super-secret-value" # sensitive — updating this bumps `version` in place

  metadata = {
    env = "prod"
  }
}

output "secret_version" {
  value       = dcapi_key_vault_secret.db_password.version
  description = "Version number of the secret; increments on every write."
}
