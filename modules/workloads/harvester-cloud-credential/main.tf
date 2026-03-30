# ── Harvester cluster CA cert ─────────────────────────────────────────────────
# kube-root-ca.crt is the Kubernetes API server CA — the correct certificate
# authority for connections to harvester_api_server (port 6443). It differs
# from the Rancher proxy CA that may be in the kubeconfig used by the provider.
data "kubernetes_config_map" "kube_root_ca" {
  provider = kubernetes.harvester
  metadata {
    name      = "kube-root-ca.crt"
    namespace = "kube-system"
  }
}

locals {
  # kube-root-ca.crt data["ca.crt"] is PEM; base64-encode for kubeconfig.
  ca_cert_b64 = base64encode(data.kubernetes_config_map.kube_root_ca.data["ca.crt"])
}

# ── ServiceAccount on Harvester ───────────────────────────────────────────────
# Mirrors what Rancher's provisioner creates automatically when a cluster is
# built via the UI with the Harvester cloud provider enabled.

resource "kubernetes_service_account" "csi" {
  provider = kubernetes.harvester
  metadata {
    name      = var.cluster_name
    namespace = var.vm_namespace
  }
}

# ClusterRole harvesterhci.io:csi-driver is pre-installed by Harvester.
resource "kubernetes_cluster_role_binding" "csi" {
  provider = kubernetes.harvester
  metadata {
    name = "${var.vm_namespace}-${var.cluster_name}"
  }
  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "ClusterRole"
    name      = "harvesterhci.io:csi-driver"
  }
  subject {
    kind      = "ServiceAccount"
    name      = var.cluster_name
    namespace = var.vm_namespace
  }
}

# Namespace-scoped binding to harvesterhci.io:cloudprovider (also pre-installed).
resource "kubernetes_role_binding" "cloud_provider" {
  provider = kubernetes.harvester
  metadata {
    name      = "${var.vm_namespace}-${var.cluster_name}"
    namespace = var.vm_namespace
  }
  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "ClusterRole"
    name      = "harvesterhci.io:cloudprovider"
  }
  subject {
    kind      = "ServiceAccount"
    name      = var.cluster_name
    namespace = var.vm_namespace
  }
}

# Long-lived SA token secret. kubernetes.io/service-account-token secrets are
# perpetually valid (no built-in expiry) — rotate by tainting this resource.
# Re-applying is fully idempotent: the token value stays stable in Terraform
# state and only changes if the secret is tainted or deleted externally.
resource "kubernetes_secret" "sa_token" {
  provider = kubernetes.harvester
  metadata {
    name      = "${var.cluster_name}-harvester-csi-token"
    namespace = var.vm_namespace
    annotations = {
      "kubernetes.io/service-account.name" = var.cluster_name
    }
  }
  type = "kubernetes.io/service-account-token"

  # Ensure SA exists before we request a token for it.
  depends_on = [kubernetes_service_account.csi]
}

# ── Build kubeconfig from SA token ────────────────────────────────────────────
locals {
  kubeconfig = yamlencode({
    apiVersion = "v1"
    kind       = "Config"
    clusters = [{
      name = "default"
      cluster = {
        certificate-authority-data = local.ca_cert_b64
        server                     = var.harvester_api_server
      }
    }]
    users = [{
      name = "default"
      user = { token = kubernetes_secret.sa_token.data["token"] }
    }]
    contexts = [{
      name = "default"
      context = {
        cluster   = "default"
        namespace = var.vm_namespace
        user      = "default"
      }
    }]
    current-context = "default"
  })
}

# ── harvesterconfig secret in Rancher fleet-default ──────────────────────────
# Rancher reads cloud-provider-config: secret://fleet-default:<name> from
# machineSelectorConfig and writes the credential to each node at
# /var/lib/rancher/rke2/etc/config-files/cloud-provider-config during bootstrap.
# Subsequent applies are idempotent — Kubernetes secrets are reconciled in place.

resource "kubernetes_secret" "harvesterconfig" {
  provider = kubernetes.rancher_local
  metadata {
    name      = "harvesterconfig-${var.cluster_name}"
    namespace = "fleet-default"
    annotations = {
      "v2prov-authorized-secret-deletes-on-cluster-removal" = "true"
      "v2prov-secret-authorized-for-cluster"                = var.cluster_name
    }
  }
  type = "secret"
  data = {
    credential = local.kubeconfig
  }
}
