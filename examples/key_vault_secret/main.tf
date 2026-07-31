terraform {
  required_providers {
    dcapi = {
      source  = "registry.terraform.io/wso2/dcapi"
      version = "~> 0.1.0"
    }
  }

  # State contains the secret value in plaintext — never use the local
  # backend for this config. Encryption (below) protects data at rest but
  # does not provide per-deployment isolation or access control on its own;
  # each deployment MUST supply its own bucket (with a restrictive bucket
  # policy) and a unique key, e.g. via:
  #   terraform init \
  #     -backend-config="bucket=<your-tfstate-bucket>" \
  #     -backend-config="key=key_vault_secret/<deployment>/terraform.tfstate" \
  #     -backend-config="region=<your-region>"
  backend "s3" {
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
