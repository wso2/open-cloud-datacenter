module "my_cluster" {
  source = "github.com/wso2/open-cloud-datacenter//modules/tenancy/k8s-cluster?ref=terraform/v0.1.7"

  # ── Cluster identity ────────────────────────────────────────────────────────
  cluster_name       = "my-cluster"        # REPLACE: your cluster name
  kubernetes_version = "v1.34.9+rke2r1"

  # ── Rancher / Harvester connection ──────────────────────────────────────────
  create_cloud_credential         = true
  enable_harvester_cloud_provider = true
  harvester_cluster_name          = var.harvester_cluster_name
  rancher_api_url                 = var.rancher_api_url
  rancher_api_token               = var.rancher_api_token

  # REPLACE: the VM namespace from your tenant space
  harvester_vm_namespace = "my-team"

  # ── Networking ──────────────────────────────────────────────────────────────
  cni                = "cilium"        # options: cilium (default), calico, canal
  ingress_controller = "ingress-nginx" # options: ingress-nginx (default), traefik, none

  # ── Node configuration ──────────────────────────────────────────────────────
  node_password       = var.node_password
  ssh_authorized_keys = var.ssh_authorized_keys
  ntp_server          = var.ntp_server
  manage_rke_config   = true

  # ── Machine pools ───────────────────────────────────────────────────────────
  machine_pools = [
    {
      name         = "controlplane-worker"
      vm_namespace = "my-team"                               # REPLACE: VM namespace
      quantity     = 3
      cpu_count    = "4"
      memory_size  = "8"
      disk_size    = 50
      image_name   = "images/ubuntu-24-04"

      # Primary NIC — gets the default route.
      # Format: "<network-namespace>/<nad-name>"
      vm_network = "my-team-net/my-team-vlan601"            # REPLACE

      # Storage NIC — optional, remove if not allocated.
      storage_network = "my-team-net/my-team-strg-vlan698"  # REPLACE or remove

      control_plane = true
      etcd          = true
      worker        = true
    }
  ]
}

output "cluster_id" {
  description = "Rancher v2 cluster ID"
  value       = module.my_cluster.cluster_id
}

output "cluster_name" {
  description = "Downstream cluster name"
  value       = module.my_cluster.cluster_name
}
