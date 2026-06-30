terraform {
  required_providers {
    dcapi = {
      source  = "registry.terraform.io/wso2/dcapi"
      version = "~> 0.1.0"
    }
  }
}

provider "dcapi" {}

resource "dcapi_service_account" "ci_pipeline" {
  tenant_id  = "my-org"
  project_id = "my-project"

  name        = "ci-pipeline"
  role        = "member"
  description = "GitHub Actions service account for CI/CD pipeline"
}

# Retrieve with: terraform output -raw sa_token
# The token is shown once at creation — store it securely.
output "sa_token" {
  value       = dcapi_service_account.ci_pipeline.token
  sensitive   = true
  description = "Bearer token for the service account. Not recoverable if state is lost."
}

output "sa_id" {
  value       = dcapi_service_account.ci_pipeline.sa_id
  description = "UUID of the created service account."
}
