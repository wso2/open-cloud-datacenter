terraform {
  required_providers {
    dcapi = {
      source  = "registry.terraform.io/wso2/dcapi"
      version = "~> 0.1.0"
    }
  }
}

provider "dcapi" {}

resource "dcapi_project" "project-s87" {

  tenant_id = "tenant-s87"
  project_id = "testing-project-s87"
  description = "Testing project"

  cpu_cores  = 20
  memory_gb  = 64
  storage_gb = 500
  
  max_vnets      = 5
  max_clusters   = 2
  max_volumes    = 20
  max_public_ips = 3
}

output "tenant_id"    { value = dcapi_project.project-s87.tenant_id }
output "project_id"   { value = dcapi_project.project-s87.project_id }
output "project_uuid" { value = dcapi_project.project-s87.project_uuid }
