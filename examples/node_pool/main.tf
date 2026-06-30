terraform {
  required_providers {
    dcapi = {
      source  = "registry.terraform.io/wso2/dcapi"
      version = "~> 0.1.0"
    }
  }
}

provider "dcapi" {}

resource "dcapi_node_pool" "gpu" {
  tenant_id  = "my-org"
  project_id = "my-project"
  # Use dcapi_cluster.prod.cluster_id — not dcapi_cluster.prod.id.
  cluster_id = "880e8400-e29b-41d4-a716-446655440000"

  name    = "gpu-pool"
  size    = "xlarge"
  count   = 2
  disk_gb = 200

  taints = [
    {
      key    = "nvidia.com/gpu"
      value  = "true"
      effect = "NoSchedule"
    }
  ]

  labels = {
    "pool-type" = "gpu"
  }
}

output "node_pool_id" {
  value       = dcapi_node_pool.gpu.node_pool_id
  description = "Internal UUID of the node pool."
}

output "node_pool_status" {
  value       = dcapi_node_pool.gpu.status
  description = "Current lifecycle status of the node pool."
}
