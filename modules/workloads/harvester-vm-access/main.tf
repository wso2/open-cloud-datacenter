# Harvester Kubernetes API CA cert — used in the output kubeconfig so the
# consumer's Harvester provider validates the server certificate correctly.
data "kubernetes_config_map" "kube_root_ca" {
  provider = kubernetes.harvester
  metadata {
    name      = "kube-root-ca.crt"
    namespace = "kube-system"
  }
}

locals {
  sa_name     = "harvester-vm-user-${var.consumer_name}"
  ca_cert_b64 = base64encode(data.kubernetes_config_map.kube_root_ca.data["ca.crt"])
}

# ── ServiceAccount ─────────────────────────────────────────────────────────────

resource "kubernetes_service_account_v1" "consumer" {
  provider = kubernetes.harvester
  metadata {
    name      = local.sa_name
    namespace = var.vm_namespace
    labels = {
      "platform.wso2.com/managed-by" = "harvester-vm-access"
      "platform.wso2.com/consumer"   = var.consumer_name
    }
  }
}

# ── RoleBinding: kubevirt VM lifecycle (edit ClusterRole, namespace-scoped) ────
# Grants create/update/delete on VirtualMachines and VirtualMachineInstances,
# PersistentVolumeClaims, Secrets, and ConfigMaps within the consumer namespace.

resource "kubernetes_role_binding_v1" "edit" {
  provider = kubernetes.harvester
  metadata {
    name      = "${local.sa_name}-edit"
    namespace = var.vm_namespace
  }
  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "ClusterRole"
    name      = "edit"
  }
  subject {
    kind      = "ServiceAccount"
    name      = kubernetes_service_account_v1.consumer.metadata[0].name
    namespace = var.vm_namespace
  }
}

# ── RoleBinding: Harvester HCI resources (keypairs, VM images, backups) ────────
# harvesterhci.io:edit covers keypairs, virtualmachineimages, virtualmachinetemplates,
# virtualmachinebackups, virtualmachinerestores, and k8s.cni.cncf.io NADs.

resource "kubernetes_role_binding_v1" "harvester_edit" {
  provider = kubernetes.harvester
  metadata {
    name      = "${local.sa_name}-harvester-edit"
    namespace = var.vm_namespace
  }
  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "ClusterRole"
    name      = "harvesterhci.io:edit"
  }
  subject {
    kind      = "ServiceAccount"
    name      = kubernetes_service_account_v1.consumer.metadata[0].name
    namespace = var.vm_namespace
  }
}

# ── RoleBinding: read OS images from default namespace ─────────────────────────
# Shared OS images downloaded by management/storage live in the "default"
# namespace. The consumer's harvester provider needs read access to reference
# them by "default/<image-name>".

resource "kubernetes_role_binding_v1" "image_read_default" {
  provider = kubernetes.harvester
  metadata {
    name      = "${local.sa_name}-default-image-read"
    namespace = "default"
  }
  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "ClusterRole"
    name      = "harvesterhci.io:view"
  }
  subject {
    kind      = "ServiceAccount"
    name      = kubernetes_service_account_v1.consumer.metadata[0].name
    namespace = var.vm_namespace
  }
}

# ── RoleBinding: read public OS images from harvester-public namespace ─────────

resource "kubernetes_role_binding_v1" "image_read_public" {
  provider = kubernetes.harvester
  metadata {
    name      = "${local.sa_name}-public-image-read"
    namespace = "harvester-public"
  }
  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "ClusterRole"
    name      = "harvesterhci.io:view"
  }
  subject {
    kind      = "ServiceAccount"
    name      = kubernetes_service_account_v1.consumer.metadata[0].name
    namespace = var.vm_namespace
  }
}

# ── Long-lived SA token ────────────────────────────────────────────────────────

resource "kubernetes_secret_v1" "token" {
  provider = kubernetes.harvester
  metadata {
    name      = "${local.sa_name}-token"
    namespace = var.vm_namespace
    annotations = {
      "kubernetes.io/service-account.name" = kubernetes_service_account_v1.consumer.metadata[0].name
    }
  }
  type = "kubernetes.io/service-account-token"
}

# ── Kubeconfig output ──────────────────────────────────────────────────────────

locals {
  token_decoded = base64decode(kubernetes_secret_v1.token.data["token"])

  kubeconfig = <<-EOT
    apiVersion: v1
    kind: Config
    clusters:
    - name: harvester
      cluster:
        certificate-authority-data: ${local.ca_cert_b64}
        server: ${var.harvester_api_server}
    users:
    - name: ${var.consumer_name}
      user:
        token: ${local.token_decoded}
    contexts:
    - name: ${var.consumer_name}@harvester
      context:
        cluster: harvester
        namespace: ${var.vm_namespace}
        user: ${var.consumer_name}
    current-context: ${var.consumer_name}@harvester
  EOT
}
