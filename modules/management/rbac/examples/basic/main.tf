# Example: Create Rancher projects and namespaces with resource quotas
#
# This example provisions two team projects on the Harvester cluster managed
# by Rancher, each with enforced CPU, memory, and storage limits.
#
# Prerequisites:
#   - Rancher deployed and accessible (e.g. via the bootstrap module)
#   - The Harvester cluster imported into Rancher (e.g. via the
#     management/harvester-integration module)
#   - The rancher2 provider configured with your Rancher URL and access key

terraform {
  required_version = ">= 1.3"

  required_providers {
    rancher2 = {
      source  = "rancher/rancher2"
      version = "~> 3.0"
    }
  }
}

provider "rancher2" {
  api_url   = "https://rancher.example.internal"
  # Provide credentials via environment variables:
  # CATTLE_ACCESS_KEY / CATTLE_SECRET_KEY
  # or via the access_key / secret_key arguments.
  insecure = true
}

module "rbac" {
  source = "github.com/wso2-enterprise/open-cloud-datacenter//modules/management/rbac?ref=v0.1.0"

  # "local" refers to the Harvester cluster as seen by Rancher
  harvester_cluster_name = "local"

  projects = {
    "team-alpha" = {
      cpu_limit     = "8000m"
      memory_limit  = "16Gi"
      storage_limit = "200Gi"
    }
    "team-beta" = {
      cpu_limit     = "4000m"
      memory_limit  = "8Gi"
      storage_limit = "100Gi"
    }
  }
}
