# ─────────────────────────────────────────────────────────────────────────────
# Key Vault Operator (KVI)
#
# Deploys the keyvault-operator manager onto a Kubernetes cluster. The operator
# watches KeyVaultBackend and KeyVaultInstance custom resources and reconciles
# OpenBao backends + KV-v2 mounts + AppRoles per the contract described at
# https://github.com/wso2/open-cloud-datacenter/blob/main/modules/management/keyvault-operator/README.md
#
# Resources created:
#   - Namespace (operator's own; CRDs are cluster-scoped)
#   - 2x CustomResourceDefinition (KeyVaultBackend, KeyVaultInstance)
#   - ServiceAccount for the manager
#   - Leader-election Role + RoleBinding (namespaced)
#   - manager ClusterRole + ClusterRoleBinding (watches own CRDs;
#     creates/updates the StatefulSet + RBAC + Service that back each
#     KeyVaultBackend)
#   - metrics-auth ClusterRole + ClusterRoleBinding (TokenReview /
#     SubjectAccessReview for the metrics endpoint)
#   - metrics-reader ClusterRole (granted by the operator to anything
#     scraping its /metrics endpoint, e.g. Prometheus)
#   - 6x stub ClusterRoles for end-user kubectl access (admin/editor/viewer
#     × backend/instance) — emitted by kubebuilder defaults, harmless;
#     consumers can ignore them.
#   - metrics Service (ClusterIP, port 8443)
#   - manager Deployment
#   - Optional image-pull Secret (when image_pull_secret is set)
#
# Provider config (kubernetes) is expected to live in the caller layer.
# ─────────────────────────────────────────────────────────────────────────────

locals {
  manager_name        = "${var.name_prefix}controller-manager"
  manager_role_name   = "${var.name_prefix}manager-role"
  leader_role_name    = "${var.name_prefix}leader-election-role"
  metrics_auth_name   = "${var.name_prefix}metrics-auth-role"
  metrics_reader_name = "${var.name_prefix}metrics-reader"
  metrics_svc_name    = "${var.name_prefix}controller-manager-metrics-service"
  image_pull_secret   = var.image_pull_secret != null ? "${var.name_prefix}image-pull" : null

  base_labels = merge(
    {
      "app.kubernetes.io/name"       = "keyvault-operator"
      "app.kubernetes.io/managed-by" = "terraform"
      "control-plane"                = "controller-manager"
    },
    var.common_labels,
  )
}

# ── Namespace ────────────────────────────────────────────────────────────────

resource "kubernetes_namespace" "operator" {
  metadata {
    name   = var.namespace
    labels = local.base_labels
  }
}

# ── CRDs (cluster-scoped; not bound to the namespace above) ──────────────────

resource "kubernetes_manifest" "crd_backends" {
  manifest = yamldecode(file("${path.module}/crds/keyvaultbackends.yaml"))
}

resource "kubernetes_manifest" "crd_instances" {
  manifest = yamldecode(file("${path.module}/crds/keyvaultinstances.yaml"))
}

# ── ServiceAccount ───────────────────────────────────────────────────────────

resource "kubernetes_service_account" "manager" {
  metadata {
    name      = local.manager_name
    namespace = kubernetes_namespace.operator.metadata[0].name
    labels    = local.base_labels
  }
}

# ── Image-pull Secret (optional) ─────────────────────────────────────────────

resource "kubernetes_secret" "image_pull" {
  count = var.image_pull_secret != null ? 1 : 0

  metadata {
    name      = local.image_pull_secret
    namespace = kubernetes_namespace.operator.metadata[0].name
    labels    = local.base_labels
  }

  type = "kubernetes.io/dockerconfigjson"

  data = {
    ".dockerconfigjson" = jsonencode({
      auths = {
        (var.image_pull_secret.server) = {
          username = var.image_pull_secret.username
          password = var.image_pull_secret.password
          email    = var.image_pull_secret.email
          auth     = base64encode("${var.image_pull_secret.username}:${var.image_pull_secret.password}")
        }
      }
    })
  }
}

# ── Leader-election Role + RoleBinding (namespaced) ──────────────────────────

resource "kubernetes_role" "leader_election" {
  metadata {
    name      = local.leader_role_name
    namespace = kubernetes_namespace.operator.metadata[0].name
    labels    = local.base_labels
  }

  rule {
    api_groups = [""]
    resources  = ["configmaps"]
    verbs      = ["get", "list", "watch", "create", "update", "patch", "delete"]
  }
  rule {
    api_groups = ["coordination.k8s.io"]
    resources  = ["leases"]
    verbs      = ["get", "list", "watch", "create", "update", "patch", "delete"]
  }
  rule {
    api_groups = [""]
    resources  = ["events"]
    verbs      = ["create", "patch"]
  }
}

resource "kubernetes_role_binding" "leader_election" {
  metadata {
    name      = "${var.name_prefix}leader-election-rolebinding"
    namespace = kubernetes_namespace.operator.metadata[0].name
    labels    = local.base_labels
  }

  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "Role"
    name      = kubernetes_role.leader_election.metadata[0].name
  }

  subject {
    kind      = "ServiceAccount"
    name      = kubernetes_service_account.manager.metadata[0].name
    namespace = kubernetes_namespace.operator.metadata[0].name
  }
}

# ── Manager ClusterRole + ClusterRoleBinding ─────────────────────────────────
# These rules MUST stay in sync with the +kubebuilder:rbac markers in
# crds/keyvault/internal/controller/*.go upstream. The operator's
# `make manifests` regenerates them into the operator's own
# config/rbac/role.yaml; the YAML below is a verbatim port from that file
# at the time of writing. Bump along with operator version bumps.

resource "kubernetes_cluster_role" "manager" {
  metadata {
    name   = local.manager_role_name
    labels = local.base_labels
  }

  rule {
    api_groups = [""]
    resources  = ["namespaces", "configmaps", "services", "serviceaccounts"]
    verbs      = ["get", "list", "watch", "create", "update", "patch", "delete"]
  }
  rule {
    api_groups = [""]
    resources  = ["secrets"]
    verbs      = ["get", "list", "watch", "create"]
  }
  rule {
    api_groups = [""]
    resources  = ["pods"]
    verbs      = ["get", "list", "watch", "update", "patch"]
  }
  rule {
    api_groups = [""]
    resources  = ["pods/proxy"]
    verbs      = ["get", "create", "update"]
  }
  rule {
    api_groups = [""]
    resources  = ["persistentvolumeclaims"]
    verbs      = ["get", "list", "watch", "delete"]
  }
  rule {
    api_groups = [""]
    resources  = ["events"]
    verbs      = ["create", "patch"]
  }
  rule {
    api_groups = ["apps"]
    resources  = ["statefulsets"]
    verbs      = ["get", "list", "watch", "create", "update", "patch", "delete"]
  }
  rule {
    api_groups = ["rbac.authorization.k8s.io"]
    resources  = ["roles", "rolebindings"]
    verbs      = ["get", "list", "watch", "create", "update", "patch", "delete"]
  }
  rule {
    api_groups = ["keyvault.opencloud.wso2.com"]
    resources  = ["keyvaultbackends", "keyvaultinstances"]
    verbs      = ["get", "list", "watch", "create", "update", "patch", "delete"]
  }
  rule {
    api_groups = ["keyvault.opencloud.wso2.com"]
    resources  = ["keyvaultbackends/finalizers", "keyvaultinstances/finalizers"]
    verbs      = ["update"]
  }
  rule {
    api_groups = ["keyvault.opencloud.wso2.com"]
    resources  = ["keyvaultbackends/status", "keyvaultinstances/status"]
    verbs      = ["get", "update", "patch"]
  }
}

resource "kubernetes_cluster_role_binding" "manager" {
  metadata {
    name   = "${var.name_prefix}manager-rolebinding"
    labels = local.base_labels
  }

  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "ClusterRole"
    name      = kubernetes_cluster_role.manager.metadata[0].name
  }

  subject {
    kind      = "ServiceAccount"
    name      = kubernetes_service_account.manager.metadata[0].name
    namespace = kubernetes_namespace.operator.metadata[0].name
  }
}

# ── Metrics-auth ClusterRole + ClusterRoleBinding ────────────────────────────
# Manager-side authentication of metrics-endpoint callers.

resource "kubernetes_cluster_role" "metrics_auth" {
  metadata {
    name   = local.metrics_auth_name
    labels = local.base_labels
  }

  rule {
    api_groups = ["authentication.k8s.io"]
    resources  = ["tokenreviews"]
    verbs      = ["create"]
  }
  rule {
    api_groups = ["authorization.k8s.io"]
    resources  = ["subjectaccessreviews"]
    verbs      = ["create"]
  }
}

resource "kubernetes_cluster_role_binding" "metrics_auth" {
  metadata {
    name   = "${var.name_prefix}metrics-auth-rolebinding"
    labels = local.base_labels
  }

  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "ClusterRole"
    name      = kubernetes_cluster_role.metrics_auth.metadata[0].name
  }

  subject {
    kind      = "ServiceAccount"
    name      = kubernetes_service_account.manager.metadata[0].name
    namespace = kubernetes_namespace.operator.metadata[0].name
  }
}

# ── Metrics-reader ClusterRole ───────────────────────────────────────────────
# Granted by the operator to anything authorised to scrape /metrics
# (e.g. a Prometheus scrape SA). Not bound to any subject by this module.

resource "kubernetes_cluster_role" "metrics_reader" {
  metadata {
    name   = local.metrics_reader_name
    labels = local.base_labels
  }

  rule {
    non_resource_urls = ["/metrics"]
    verbs             = ["get"]
  }
}

# ── End-user-facing stub ClusterRoles (kubebuilder default scaffolding) ──────
# These aren't bound by this module; they exist as standardised
# aggregate-cluster-role anchor points so kubectl users with admin/editor/viewer
# privileges on the CRs can be granted via a single rolebinding elsewhere.

locals {
  user_cr_specs = {
    "${var.name_prefix}keyvaultbackend-admin-role"   = { plural = "keyvaultbackends", verbs = ["*"] }
    "${var.name_prefix}keyvaultbackend-editor-role"  = { plural = "keyvaultbackends", verbs = ["get", "list", "watch", "create", "update", "patch", "delete"] }
    "${var.name_prefix}keyvaultbackend-viewer-role"  = { plural = "keyvaultbackends", verbs = ["get", "list", "watch"] }
    "${var.name_prefix}keyvaultinstance-admin-role"  = { plural = "keyvaultinstances", verbs = ["*"] }
    "${var.name_prefix}keyvaultinstance-editor-role" = { plural = "keyvaultinstances", verbs = ["get", "list", "watch", "create", "update", "patch", "delete"] }
    "${var.name_prefix}keyvaultinstance-viewer-role" = { plural = "keyvaultinstances", verbs = ["get", "list", "watch"] }
  }
}

resource "kubernetes_cluster_role" "user_stubs" {
  for_each = local.user_cr_specs

  metadata {
    name   = each.key
    labels = local.base_labels
  }

  rule {
    api_groups = ["keyvault.opencloud.wso2.com"]
    resources  = [each.value.plural]
    verbs      = each.value.verbs
  }

  rule {
    api_groups = ["keyvault.opencloud.wso2.com"]
    resources  = ["${each.value.plural}/status"]
    verbs      = ["get"]
  }
}

# ── Metrics Service ──────────────────────────────────────────────────────────

resource "kubernetes_service" "metrics" {
  metadata {
    name      = local.metrics_svc_name
    namespace = kubernetes_namespace.operator.metadata[0].name
    labels    = local.base_labels
  }

  spec {
    selector = {
      "control-plane"          = "controller-manager"
      "app.kubernetes.io/name" = "keyvault-operator"
    }
    port {
      name        = "https"
      port        = 8443
      protocol    = "TCP"
      target_port = "8443"
    }
  }
}

# ── Manager Deployment ───────────────────────────────────────────────────────

resource "kubernetes_deployment" "manager" {
  metadata {
    name      = local.manager_name
    namespace = kubernetes_namespace.operator.metadata[0].name
    labels    = local.base_labels
  }

  spec {
    replicas = var.replicas

    selector {
      match_labels = {
        "control-plane"          = "controller-manager"
        "app.kubernetes.io/name" = "keyvault-operator"
      }
    }

    template {
      metadata {
        labels = merge(local.base_labels, {
          "control-plane"          = "controller-manager"
          "app.kubernetes.io/name" = "keyvault-operator"
        })
        annotations = {
          "kubectl.kubernetes.io/default-container" = "manager"
        }
      }

      spec {
        service_account_name             = kubernetes_service_account.manager.metadata[0].name
        termination_grace_period_seconds = 10

        security_context {
          run_as_non_root = true
          seccomp_profile {
            type = "RuntimeDefault"
          }
        }

        dynamic "image_pull_secrets" {
          for_each = var.image_pull_secret != null ? [1] : []
          content {
            name = local.image_pull_secret
          }
        }

        container {
          name              = "manager"
          image             = var.image
          image_pull_policy = "IfNotPresent"

          command = ["/manager"]
          args = compact([
            var.leader_elect ? "--leader-elect" : "",
            "--health-probe-bind-address=:8081",
          ])

          port {
            name           = "health"
            container_port = 8081
            protocol       = "TCP"
          }

          security_context {
            read_only_root_filesystem  = true
            allow_privilege_escalation = false
            capabilities {
              drop = ["ALL"]
            }
          }

          liveness_probe {
            http_get {
              path = "/healthz"
              port = 8081
            }
            initial_delay_seconds = 15
            period_seconds        = 20
          }

          readiness_probe {
            http_get {
              path = "/readyz"
              port = 8081
            }
            initial_delay_seconds = 5
            period_seconds        = 10
          }

          resources {
            limits   = var.resources.limits
            requests = var.resources.requests
          }
        }
      }
    }
  }

  # Make sure the CRDs exist before the manager starts watching them.
  depends_on = [
    kubernetes_manifest.crd_backends,
    kubernetes_manifest.crd_instances,
    kubernetes_cluster_role_binding.manager,
    kubernetes_role_binding.leader_election,
  ]
}
