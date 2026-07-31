terraform {
  required_providers {
    dcapi = {
      source  = "registry.terraform.io/wso2/dcapi"
      version = "~> 0.1.0"
    }
  }

  # State contains the secret value in plaintext — never use the local
  # backend for this config. Point this at a backend with encryption at
  # rest and access controls (e.g. an S3 bucket with SSE + a restrictive
  # bucket policy, or Terraform Cloud/Enterprise).
  backend "s3" {
    bucket  = "wso2-dcapi-tfstate"
    key     = "key_vault_secret/terraform.tfstate"
    region  = "us-east-1"
    encrypt = true
  }
}

provider "dcapi" {}

variable "db_password" {
  description = "Value stored in the key vault secret. Pass via TF_VAR_db_password or a secret-backed .tfvars file — never commit a value here."
  type        = string
  sensitive   = true
}

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
  value = var.db_password # sensitive — updating this bumps `version` in place

  metadata = {
    env = "prod"
  }
}

output "secret_version" {
  value       = dcapi_key_vault_secret.db_password.version
  description = "Version number of the secret; increments on every write."
}
