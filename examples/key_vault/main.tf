terraform {
  required_providers {
    dcapi = {
      source  = "registry.terraform.io/wso2/dcapi"
      version = "~> 0.1.0"
    }
  }
}

provider "dcapi" {}

resource "dcapi_project" "kv-project-s87" {
  tenant_id  = "tenant-s87"
  project_id = "kv-project-s87"

  name        = "Infrastructure Team"
  description = "Core infrastructure resources: VNets, clusters, and shared VMs."

}


resource "dcapi_key_vault" "prod_secrets-s87" {
  tenant_id  = dcapi_project.kv-project-s87.tenant_id
  project_id = dcapi_project.kv-project-s87.project_id

  name              = "prod-secrets-s87"
  soft_delete_days  = 30

  # Bump this value (e.g. to a new date) to rotate the AppRole secret_id in place,
  # without destroying and recreating the KeyVault.
  credentials_rotation = "initial"
}

# secret_id is a sensitive, shown-once value. It is stored in Terraform state and never
# returned by subsequent API reads. To obtain a new one, change credentials_rotation above.
output "kv_role_id" {
  value       = dcapi_key_vault.prod_secrets-s87.role_id
  description = "Stable AppRole role_id for authenticating against this vault."
}

output "kv_secret_id" {
  value       = dcapi_key_vault.prod_secrets-s87.secret_id
  sensitive   = true
  description = "AppRole secret_id. Retrieve with: terraform output -raw kv_secret_id"
}

output "kv_mount_path" {
  value       = dcapi_key_vault.prod_secrets-s87.mount_path
  description = "OpenBao mount path for this vault, populated once status is ACTIVE."
}
