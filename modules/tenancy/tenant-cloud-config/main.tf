resource "random_password" "node_password" {
  length  = 20
  special = false
}

resource "kubernetes_config_map_v1" "k8s_node_with_storage" {

  metadata {
    name      = "k8s-cluster-node-with-storage-network"
    namespace = var.namespace
    labels = {
      "harvesterhci.io/cloud-init-template" = "user"
    }
  }

  data = {
    cloudInit = templatefile("${path.module}/templates/k8s-cluster-node-with-storage-network.tpl", {
      node_password       = random_password.node_password.result
      ntp_server          = var.ntp_server
      ssh_authorized_keys = var.ssh_authorized_keys
    })
  }
}
