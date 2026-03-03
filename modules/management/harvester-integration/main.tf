terraform {
  required_providers {
    rancher2 = {
      source  = "rancher/rancher2"
      version = "~> 8.0.0"
    }
    harvester = {
      source  = "harvester/harvester"
      version = "~> 0.6.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.30.0"
    }
  }
}

# 1. Enable Harvester Feature Flag
resource "rancher2_setting" "harvester_enabled" {
  name  = "harvester"
  value = "true"
}

# 2. (Optional) Enable Harvester Baremetal Container Workload (Experimental)
resource "rancher2_setting" "harvester_baremetal" {
  name  = "harvester-baremetal-container-workload"
  value = "true"
}

# 3. Add Harvester UI Extension Catalog
resource "rancher2_catalog_v2" "harvester_extensions" {
  cluster_id = "local"
  name       = "harvester-extensions"
  git_repo   = "https://github.com/harvester/harvester-ui-extension"
  git_branch = "gh-pages"
}

# 4. Find System Project in local cluster for the extension
data "rancher2_project" "local_system" {
  cluster_id = "local"
  name       = "System"
}

# 6. Install Harvester UI Extension App
resource "rancher2_app_v2" "harvester" {
  cluster_id    = "local"
  name          = "harvester" # User confirmed this name
  namespace     = "cattle-ui-plugin-system"
  repo_name     = rancher2_catalog_v2.harvester_extensions.name
  chart_name    = "harvester"
  chart_version = "1.7.1" # User confirmed this version
  project_id    = data.rancher2_project.local_system.id
  wait          = true

  # Ensure feature flag and catalogs are ready
  depends_on = [
    rancher2_setting.harvester_enabled,
    rancher2_catalog_v2.harvester_extensions
  ]
}

# 7. Create Cloud Credential for Harvester Import
resource "rancher2_cloud_credential" "harvester" {
  name = "harvester-local-creds"
  harvester_credential_config {
    cluster_id         = "local"
    cluster_type       = "imported"
    kubeconfig_content = var.harvester_kubeconfig
  }
}


# 8. Create Imported Cluster for Harvester HCI (Norman API)
# This registers the cluster in "Virtualization Management" using the legacy cluster resource
resource "rancher2_cluster" "harvester_hci" {
  name        = var.harvester_cluster_name
  description = "Harvester HCI"

  labels = {
    "provider.cattle.io" = "harvester"
  }

  depends_on = [rancher2_app_v2.harvester]
}

# 9. Apply Registration Command to Harvester
# We use a temporary file for the kubeconfig to apply the registration manifest
resource "null_resource" "apply_harvester_registration" {
  triggers = {
    registration_command = rancher2_cluster.harvester_hci.cluster_registration_token[0].command
    kubeconfig           = var.harvester_kubeconfig
  }

  provisioner "local-exec" {
    command = <<EOT
      echo "$KUBECONFIG_CONTENT" > harvester_kubeconfig.yaml
      ${rancher2_cluster.harvester_hci.cluster_registration_token[0].command} --kubeconfig harvester_kubeconfig.yaml
      rm harvester_kubeconfig.yaml
    EOT

    environment = {
      KUBECONFIG_CONTENT = var.harvester_kubeconfig
    }
  }

  # On destroy: remove the cattle-cluster-agent from Harvester so re-create is clean
  provisioner "local-exec" {
    when    = destroy
    command = <<EOT
      echo "$KUBECONFIG_CONTENT" > harvester_kubeconfig.yaml
      kubectl --kubeconfig harvester_kubeconfig.yaml -n cattle-system delete deployment cattle-cluster-agent --ignore-not-found
      kubectl --kubeconfig harvester_kubeconfig.yaml -n cattle-system delete secret cattle-credentials --ignore-not-found || true
      rm harvester_kubeconfig.yaml
    EOT

    environment = {
      KUBECONFIG_CONTENT = lookup(self.triggers, "kubeconfig", "")
    }
  }

  depends_on = [
    rancher2_cluster.harvester_hci,
    kubernetes_config_map_v1_data.harvester_coredns_patch,
  ]
}

# 11. Patch Harvester CoreDNS (Direct ConfigMap Fix)
# This allows Harvester nodes and pods to resolve the internal Rancher URL by
# prepending a specific hosts block to the Corefile.
# Must run BEFORE registration so Harvester can reach rancher.lk.internal.
resource "kubernetes_config_map_v1_data" "harvester_coredns_patch" {
  metadata {
    name      = "rke2-coredns-rke2-coredns"
    namespace = "kube-system"
  }

  data = {
    Corefile = <<-EOT
      .:53 {
          errors
          health {
              lameduck 10s
          }
          ready
          hosts {
              ${var.rancher_lb_ip} ${var.rancher_hostname}
              fallthrough
          }
          kubernetes  cluster.local  cluster.local in-addr.arpa ip6.arpa {
              pods insecure
              fallthrough in-addr.arpa ip6.arpa
              ttl 30
          }
          prometheus  0.0.0.0:9153
          forward  . /etc/resolv.conf
          cache  30
          loop
          reload
          loadbalance
      }
    EOT
  }

  force = true # Ensures we overwrite the manual/helm-generated Corefile

  depends_on = [rancher2_cluster.harvester_hci]
}

# 10. Configure Harvester Registration URL
# This is a critical step for Harvester to reach back to Rancher.
# Depends on the CoreDNS patch so rancher.lk.internal resolves before Harvester connects.
resource "harvester_setting" "registration_url" {
  name  = "cluster-registration-url"
  value = rancher2_cluster.harvester_hci.cluster_registration_token[0].manifest_url

  depends_on = [kubernetes_config_map_v1_data.harvester_coredns_patch]
}

resource "harvester_setting" "rancher_cluster" {
  name = "rancher-cluster"
  value = jsonencode({
    clusterId   = rancher2_cluster.harvester_hci.id
    clusterName = rancher2_cluster.harvester_hci.name
  })

  depends_on = [kubernetes_config_map_v1_data.harvester_coredns_patch]
}
