terraform {
  required_version = ">= 1.3"
  required_providers {
    rancher2 = {
      source  = "rancher/rancher2"
      version = "~> 13.1"
    }
    harvester = {
      source  = "harvester/harvester"
      version = "~> 1.7"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.30"
    }
  }
}

# 1. Enable Harvester Feature Flag
# These are Rancher feature flags (at /v3/features), not settings (/v3/settings).
# rancher2_feature does not support import — set manage_feature_flags = false for brownfield.
resource "rancher2_feature" "harvester" {
  count = var.manage_feature_flags ? 1 : 0
  name  = "harvester"
  value = true
}

# 2. (Optional) Enable Harvester Baremetal Container Workload (Experimental)
resource "rancher2_feature" "harvester_baremetal" {
  count = var.manage_feature_flags ? 1 : 0
  name  = "harvester-baremetal-container-workload"
  value = true
}

# 3. Add Harvester UI Extension Catalog
# Rancher names this repo "harvester" by default when installed via Harvester integration.
resource "rancher2_catalog_v2" "harvester_extensions" {
  cluster_id = "local"
  name       = "harvester"
  git_repo   = "https://github.com/harvester/harvester-ui-extension"
  git_branch = "gh-pages"
}

# 4. Find System Project in local cluster for the extension
data "rancher2_project" "local_system" {
  cluster_id = "local"
  name       = "System"
}

# 6. Install Harvester UI Extension App
# Set manage_app = false for brownfield (app already installed; rancher2_app_v2 import
# does not populate name/namespace, which forces a destroy+recreate).
resource "rancher2_app_v2" "harvester" {
  count         = var.manage_app ? 1 : 0
  cluster_id    = "local"
  name          = "harvester"
  namespace     = "cattle-ui-plugin-system"
  repo_name     = rancher2_catalog_v2.harvester_extensions.name
  chart_name    = "harvester"
  chart_version = var.harvester_chart_version
  project_id    = data.rancher2_project.local_system.id
  wait          = true

  depends_on = [
    rancher2_feature.harvester,
    rancher2_catalog_v2.harvester_extensions,
  ]
}

# 7a. Create a dedicated ServiceAccount + ClusterRoleBinding on Harvester for the cloud credential.
# The kubeconfig passed by the operator uses a Harvester-embedded Rancher token which is unknown
# to a standalone Rancher instance. We instead generate a long-lived SA token so the standalone
# Rancher can validate it against the Harvester API directly.
resource "kubernetes_service_account" "rancher_credential" {
  count = var.create_cloud_credential ? 1 : 0
  metadata {
    name      = "rancher-cloud-credential"
    namespace = "default"
  }
}

resource "kubernetes_cluster_role_binding" "rancher_credential" {
  count = var.create_cloud_credential ? 1 : 0
  metadata {
    name = "rancher-cloud-credential"
  }
  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "ClusterRole"
    name      = "cluster-admin"
  }
  subject {
    kind      = "ServiceAccount"
    name      = kubernetes_service_account.rancher_credential[0].metadata[0].name
    namespace = kubernetes_service_account.rancher_credential[0].metadata[0].namespace
  }
}

resource "kubernetes_secret" "rancher_credential_token" {
  count = var.create_cloud_credential ? 1 : 0
  metadata {
    name      = "rancher-cloud-credential-token"
    namespace = "default"
    annotations = {
      "kubernetes.io/service-account.name" = kubernetes_service_account.rancher_credential[0].metadata[0].name
    }
  }
  type = "kubernetes.io/service-account-token"

  depends_on = [kubernetes_service_account.rancher_credential]
}

locals {
  # Build a minimal kubeconfig using the SA token — no Rancher-embedded token involved.
  harvester_sa_kubeconfig = var.create_cloud_credential ? yamlencode({
    apiVersion = "v1"
    kind       = "Config"
    clusters = [{
      name = "harvester"
      cluster = {
        server                     = yamldecode(var.harvester_kubeconfig).clusters[0].cluster.server
        certificate-authority-data = yamldecode(var.harvester_kubeconfig).clusters[0].cluster["certificate-authority-data"]
      }
    }]
    users = [{
      name = "rancher-cloud-credential"
      user = {
        token = kubernetes_secret.rancher_credential_token[0].data["token"]
      }
    }]
    contexts = [{
      name = "harvester"
      context = {
        cluster = "harvester"
        user    = "rancher-cloud-credential"
      }
    }]
    current-context = "harvester"
  }) : ""
}

# 7b. Create Cloud Credential for Harvester Import
# Set create_cloud_credential = false for brownfield (rancher2_cloud_credential does not
# support import for the harvester driver; the credential already exists in production).
resource "rancher2_cloud_credential" "harvester" {
  count = var.create_cloud_credential ? 1 : 0
  name  = var.cloud_credential_name
  harvester_credential_config {
    cluster_id         = "local"
    cluster_type       = "imported"
    kubeconfig_content = local.harvester_sa_kubeconfig
  }

  depends_on = [kubernetes_secret.rancher_credential_token]
}


# 8. Create Imported Cluster for Harvester HCI (Norman API)
# This registers the cluster in "Virtualization Management" using the legacy cluster resource
resource "rancher2_cluster" "harvester_hci" {
  name        = var.harvester_cluster_name
  description = "Harvester HCI"

  labels = merge(
    { "provider.cattle.io" = "harvester" },
    var.cluster_labels,
  )

  depends_on = [rancher2_app_v2.harvester]
}

# 9. Apply Registration Command to Harvester
# Set apply_registration = false for brownfield (cluster already registered and active).
resource "null_resource" "apply_harvester_registration" {
  count = var.apply_registration ? 1 : 0
  triggers = {
    registration_command = rancher2_cluster.harvester_hci.cluster_registration_token[0].command
    kubeconfig           = var.harvester_kubeconfig
  }

  provisioner "local-exec" {
    command = <<EOT
      tmpkubeconfig=$(mktemp)
      trap "rm -f $tmpkubeconfig" EXIT
      printf '%s' "$KUBECONFIG_CONTENT" > "$tmpkubeconfig"
      ${rancher2_cluster.harvester_hci.cluster_registration_token[0].command} --kubeconfig "$tmpkubeconfig"
    EOT

    environment = {
      KUBECONFIG_CONTENT = var.harvester_kubeconfig
    }
  }

  # On destroy: remove the cattle-cluster-agent from Harvester so re-create is clean
  provisioner "local-exec" {
    when    = destroy
    command = <<EOT
      tmpkubeconfig=$(mktemp)
      trap "rm -f $tmpkubeconfig" EXIT
      printf '%s' "$KUBECONFIG_CONTENT" > "$tmpkubeconfig"
      kubectl --kubeconfig "$tmpkubeconfig" -n cattle-system delete deployment cattle-cluster-agent --ignore-not-found
      kubectl --kubeconfig "$tmpkubeconfig" -n cattle-system delete secret cattle-credentials --ignore-not-found || true
    EOT

    environment = {
      KUBECONFIG_CONTENT = lookup(self.triggers, "kubeconfig", "")
    }
  }

  # CoreDNS must be patched before registration so Harvester nodes can resolve
  # the Rancher hostname during the agent bootstrap.
  depends_on = [
    rancher2_cluster.harvester_hci,
    kubernetes_config_map_v1_data.harvester_coredns_patch,
  ]
}

# 10. Configure Harvester Registration URL
# Ensures Harvester has the correct manifest URL to connect back to Rancher.
# CoreDNS patch (if enabled) must be applied first so Harvester can reach Rancher.
resource "harvester_setting" "registration_url" {
  name  = "cluster-registration-url"
  value = rancher2_cluster.harvester_hci.cluster_registration_token[0].manifest_url

  depends_on = [
    rancher2_cluster.harvester_hci,
    kubernetes_config_map_v1_data.harvester_coredns_patch,
  ]
}

# 11. (Optional) Patch Harvester CoreDNS for private Rancher hostname resolution
# When Rancher has no public DNS record, Harvester cluster nodes cannot resolve the
# hostname. This replaces the Corefile with one that includes a hosts entry mapping
# rancher_lb_ip → rancher_hostname, allowing nodes to reach Rancher during bootstrap.
# Set patch_coredns = false (default) when using a publicly resolvable domain.
resource "kubernetes_config_map_v1_data" "harvester_coredns_patch" {
  count = var.patch_coredns ? 1 : 0

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
          kubernetes cluster.local in-addr.arpa ip6.arpa {
              pods insecure
              fallthrough in-addr.arpa ip6.arpa
              ttl 30
          }
          prometheus 0.0.0.0:9153
          forward . /etc/resolv.conf
          cache 30
          loop
          reload
          loadbalance
      }
    EOT
  }

  # Overwrite the ConfigMap data managed by the rke2-coredns Helm chart.
  force = true

  depends_on = [rancher2_cluster.harvester_hci]
}
