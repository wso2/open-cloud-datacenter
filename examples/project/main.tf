terraform {
  required_providers {
    dcapi = {
      source  = "registry.terraform.io/wso2/dcapi"
      version = "~> 0.1.0"
    }
  }
}

provider "dcapi" {}

resource "dcapi_project" "example" {
  tenant_id  = "my-org"
  project_id = "my-project"

  name        = "Infrastructure Team"
  description = "Core infrastructure resources: VNets, clusters, and shared VMs."

  # Quota fields — updatable after creation.
  cpu_cores  = 20
  memory_gb  = 64
  storage_gb = 500

  # Limit fields — immutable after creation.
  max_vnets      = 5
  max_clusters   = 2
  max_volumes    = 20
  max_public_ips = 3
}

output "tenant_id"    { value = dcapi_project.example.tenant_id }
output "project_id"   { value = dcapi_project.example.project_id }
output "project_uuid" { value = dcapi_project.example.project_uuid }
