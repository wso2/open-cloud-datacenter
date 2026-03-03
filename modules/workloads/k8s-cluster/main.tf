terraform {
  required_providers {
    rancher2 = {
      source  = "rancher/rancher2"
      version = "~> 3.0"
    }
  }
}

# 1. Fetch the Cloud Credential for Harvester
data "rancher2_cloud_credential" "harvester" {
  name = var.cloud_credential_name
}

# 2. Define the Machine Config (VM sizing and settings on Harvester)
resource "rancher2_machine_config_v2" "harvester_nodes" {
  generate_name = "${var.cluster_name}-machine-config"

  harvester_config {
    vm_namespace = var.harvester_namespace
    cpu_count    = var.node_cpu
    memory_size  = var.node_memory
    disk_size    = var.node_disk_size
    disk_bus     = "virtio"
    image_name   = var.harvester_image_name
    network_name = var.harvester_network_name
    ssh_user     = var.ssh_user
  }
}

# 3. Provision the RKE2 Cluster using the Machine Config
resource "rancher2_cluster_v2" "tenant_cluster" {
  name               = var.cluster_name
  kubernetes_version = var.k8s_version

  rke_config {
    machine_pools {
      name                         = "pool1"
      cloud_credential_secret_name = data.rancher2_cloud_credential.harvester.id
      control_plane_role           = true
      etcd_role                    = true
      worker_role                  = true
      quantity                     = var.node_count

      machine_config {
        kind = rancher2_machine_config_v2.harvester_nodes.kind
        name = rancher2_machine_config_v2.harvester_nodes.name
      }
    }
  }
}
