# ─────────────────────────────────────────────────────────────────────────────
# DC Managed-Service Operators — database (dbaas-operator)
#
# Deploys the controller tier for the dbaas-operator (RDS-style managed
# PostgreSQL via Harvester KubeVirt VMs) onto a Harvester RKE2 cluster.
# Sibling of modules/operators/keyvault — same shape, different operator.
#
# All provider config (kubernetes host + credentials) lives in the calling
# environment layer. This module only contains resource definitions.
#
# Source: the dbaas-operator's `config/` directory (kubebuilder layout) on
#   the `operators` branch of github.com/wso2/open-cloud-datacenter. The
#   canonical deployment command is `kustomize build config/default |
#   kubectl apply`. This module re-expresses that output as typed Terraform
#   resources so the same cluster state is managed idiomatically without
#   requiring kustomize at apply time.
#
# How to refresh from upstream:
#   1. Check out the `operators` branch of open-cloud-datacenter
#   2. Run: kubectl kustomize database/config/default
#   3. Diff each resource kind+name against the corresponding TF resource below.
#   4. Reconcile any structural changes (new RBAC rules, new container args,
#      changed limits, new ports/mounts).
#   5. Bump crds/crd-dbinstances.yaml alongside the operator image tag bump —
#      schema changes must travel with the image version.
# ─────────────────────────────────────────────────────────────────────────────

locals {
  # Emitted on every resource so the label set is consistent. Canonical
  # kustomize labels are app.kubernetes.io/name=dbaas +
  # app.kubernetes.io/managed-by=kustomize; we swap managed-by to terraform.
  db_labels = {
    "app.kubernetes.io/name"       = "dbaas"
    "app.kubernetes.io/managed-by" = "terraform"
  }

  # True only when both credentials are provided. Drives the pull-secret
  # count and the Deployment's imagePullSecrets block.
  db_pull_secret_enabled = var.ghcr_username != "" && var.ghcr_pat != ""
}

# ── Dbaas-operator: Namespace ─────────────────────────────────────────────────

resource "kubernetes_namespace" "dbaas_system" {
  metadata {
    name = var.db_namespace
    labels = merge(local.db_labels, {
      "control-plane" = "controller-manager"
    })
  }
  lifecycle {
    # Rancher and other cluster-level controllers add annotations that TF
    # should not fight on every plan.
    ignore_changes = [metadata[0].annotations]
  }
}

# ── Dbaas-operator: GHCR pull secret (optional) ──────────────────────────────

resource "kubernetes_secret" "db_ghcr_pull" {
  count = local.db_pull_secret_enabled ? 1 : 0

  metadata {
    name      = "ghcr-pull-secret"
    namespace = kubernetes_namespace.dbaas_system.metadata[0].name
    labels    = local.db_labels
  }
  type = "kubernetes.io/dockerconfigjson"
  data = {
    ".dockerconfigjson" = jsonencode({
      auths = {
        "ghcr.io" = {
          username = var.ghcr_username
          password = var.ghcr_pat
          auth     = base64encode("${var.ghcr_username}:${var.ghcr_pat}")
        }
      }
    })
  }
}

# ── Dbaas-operator: CRDs ──────────────────────────────────────────────────────
# kubernetes_manifest is used here because there is no typed Terraform resource
# for CustomResourceDefinitions. The manifest content is sourced verbatim from
# the operator's kubebuilder-generated CRD file — bump along with operator
# version bumps.

resource "kubernetes_manifest" "crd_dbinstances" {
  manifest = yamldecode(file("${path.module}/crds/crd-dbinstances.yaml"))

  # CRD updates are additive in apiextensions.k8s.io — new versions are
  # appended, old versions kept until explicitly removed. Structural schema
  # changes require a delete + re-apply (handled by destroy + re-apply of
  # this resource).
  field_manager {
    # Use server-side apply so Kubernetes handles list-map merges in the
    # openAPIV3Schema correctly. Without this, nested list fields in CRD
    # schemas produce noisy in-place diffs on every plan.
    force_conflicts = true
  }
}

# ── Dbaas-operator: ServiceAccount ───────────────────────────────────────────
# kustomize namePrefix "dbaas-" yields name "dbaas-controller-manager".

resource "kubernetes_service_account" "dbaas_controller_manager" {
  metadata {
    name      = "dbaas-controller-manager"
    namespace = kubernetes_namespace.dbaas_system.metadata[0].name
    labels    = local.db_labels
  }
}

# ── Dbaas-operator: Leader-election Role + RoleBinding ───────────────────────
# Scoped to dbaas-system. The controller uses standard kubebuilder leader
# election via ConfigMaps/Leases in its own namespace.

resource "kubernetes_role_v1" "db_leader_election" {
  metadata {
    name      = "dbaas-leader-election-role"
    namespace = kubernetes_namespace.dbaas_system.metadata[0].name
    labels    = local.db_labels
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

resource "kubernetes_role_binding_v1" "db_leader_election" {
  metadata {
    name      = "dbaas-leader-election-rolebinding"
    namespace = kubernetes_namespace.dbaas_system.metadata[0].name
    labels    = local.db_labels
  }
  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "Role"
    name      = kubernetes_role_v1.db_leader_election.metadata[0].name
  }
  subject {
    kind      = "ServiceAccount"
    name      = kubernetes_service_account.dbaas_controller_manager.metadata[0].name
    namespace = kubernetes_namespace.dbaas_system.metadata[0].name
  }
}

# ── Dbaas-operator: manager ClusterRole + ClusterRoleBinding ─────────────────
# Cluster-scoped so the controller can manage child Harvester resources
# (VMs, DataVolumes), Secrets, Services, and ServiceMonitors across every
# tenant namespace where a DBInstance is created.
#
# Rules mirror the +kubebuilder:rbac markers in
# database/internal/controller/dbinstance_controller.go (operators branch).

resource "kubernetes_cluster_role_v1" "db_manager" {
  metadata {
    name = "dbaas-manager-role"
  }

  rule {
    api_groups = [""]
    resources  = ["endpoints", "services"]
    verbs      = ["create", "delete", "get", "update"]
  }
  rule {
    api_groups = [""]
    resources  = ["pods"]
    verbs      = ["get", "list"]
  }
  rule {
    api_groups = [""]
    resources  = ["secrets"]
    verbs      = ["create", "delete", "get"]
  }
  rule {
    api_groups = ["cdi.kubevirt.io"]
    resources  = ["datavolumes"]
    verbs      = ["create", "delete", "get", "update"]
  }
  rule {
    api_groups = ["dbaas.opencloud.wso2.com"]
    resources  = ["dbinstances"]
    verbs      = ["create", "delete", "get", "list", "patch", "update", "watch"]
  }
  rule {
    api_groups = ["dbaas.opencloud.wso2.com"]
    resources  = ["dbinstances/finalizers"]
    verbs      = ["update"]
  }
  rule {
    api_groups = ["dbaas.opencloud.wso2.com"]
    resources  = ["dbinstances/status"]
    verbs      = ["get", "patch", "update"]
  }
  rule {
    api_groups = ["harvesterhci.io"]
    resources  = ["virtualmachineimages"]
    verbs      = ["get", "list"]
  }
  rule {
    api_groups = ["kubevirt.io"]
    resources  = ["virtualmachineinstances"]
    verbs      = ["get"]
  }
  rule {
    api_groups = ["kubevirt.io"]
    resources  = ["virtualmachines"]
    verbs      = ["create", "delete", "get", "update"]
  }
  rule {
    api_groups = ["monitoring.coreos.com"]
    resources  = ["servicemonitors"]
    verbs      = ["create", "delete", "get", "update"]
  }
}

resource "kubernetes_cluster_role_binding_v1" "db_manager" {
  metadata {
    name   = "dbaas-manager-rolebinding"
    labels = local.db_labels
  }
  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "ClusterRole"
    name      = kubernetes_cluster_role_v1.db_manager.metadata[0].name
  }
  subject {
    kind      = "ServiceAccount"
    name      = kubernetes_service_account.dbaas_controller_manager.metadata[0].name
    namespace = kubernetes_namespace.dbaas_system.metadata[0].name
  }
}

# ── Dbaas-operator: metrics-auth ClusterRole + ClusterRoleBinding ────────────
# Allows the metrics endpoint to perform TokenReview + SubjectAccessReview so
# Prometheus scrape jobs can authenticate to the protected /metrics endpoint.

resource "kubernetes_cluster_role_v1" "db_metrics_auth" {
  metadata {
    name = "dbaas-metrics-auth-role"
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

resource "kubernetes_cluster_role_binding_v1" "db_metrics_auth" {
  metadata {
    name = "dbaas-metrics-auth-rolebinding"
  }
  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "ClusterRole"
    name      = kubernetes_cluster_role_v1.db_metrics_auth.metadata[0].name
  }
  subject {
    kind      = "ServiceAccount"
    name      = kubernetes_service_account.dbaas_controller_manager.metadata[0].name
    namespace = kubernetes_namespace.dbaas_system.metadata[0].name
  }
}

# ── Dbaas-operator: metrics-reader ClusterRole ───────────────────────────────
# Grants non-resource URL /metrics access. Bind to Prometheus ServiceAccounts
# in the consumer layer if you want scrape-authenticated Prometheus.

resource "kubernetes_cluster_role_v1" "db_metrics_reader" {
  metadata {
    name = "dbaas-metrics-reader"
  }
  rule {
    non_resource_urls = ["/metrics"]
    verbs             = ["get"]
  }
}

# ── Dbaas-operator: helper ClusterRoles (admin / editor / viewer) ────────────
# Kubebuilder scaffolds these for cluster admins to delegate object-level
# access to DBInstance CRs. The operator itself does not use them — they are
# scaffolding aids for human operators.
#
# Carry the rbac.authorization.k8s.io/aggregate-to-{admin,edit,view}=true
# labels so the cluster's default admin/edit/view ClusterRoles aggregate
# DBInstance verbs automatically (matches kustomize default output).

resource "kubernetes_cluster_role_v1" "db_dbinstance_admin" {
  metadata {
    name = "dbaas-dbinstance-admin-role"
    labels = merge(local.db_labels, {
      "rbac.authorization.k8s.io/aggregate-to-admin" = "true"
    })
  }
  rule {
    api_groups = ["dbaas.opencloud.wso2.com"]
    resources  = ["dbinstances"]
    verbs      = ["*"]
  }
  rule {
    api_groups = ["dbaas.opencloud.wso2.com"]
    resources  = ["dbinstances/status"]
    verbs      = ["get"]
  }
}

resource "kubernetes_cluster_role_v1" "db_dbinstance_editor" {
  metadata {
    name = "dbaas-dbinstance-editor-role"
    labels = merge(local.db_labels, {
      "rbac.authorization.k8s.io/aggregate-to-edit" = "true"
    })
  }
  rule {
    api_groups = ["dbaas.opencloud.wso2.com"]
    resources  = ["dbinstances"]
    verbs      = ["create", "delete", "get", "list", "patch", "update", "watch"]
  }
  rule {
    api_groups = ["dbaas.opencloud.wso2.com"]
    resources  = ["dbinstances/status"]
    verbs      = ["get"]
  }
}

resource "kubernetes_cluster_role_v1" "db_dbinstance_viewer" {
  metadata {
    name = "dbaas-dbinstance-viewer-role"
    labels = merge(local.db_labels, {
      "rbac.authorization.k8s.io/aggregate-to-view" = "true"
    })
  }
  rule {
    api_groups = ["dbaas.opencloud.wso2.com"]
    resources  = ["dbinstances"]
    verbs      = ["get", "list", "watch"]
  }
  rule {
    api_groups = ["dbaas.opencloud.wso2.com"]
    resources  = ["dbinstances/status"]
    verbs      = ["get"]
  }
}

# ── Dbaas-operator: Metrics Service ──────────────────────────────────────────
# Exposes port 8443 (the controller's metrics HTTPS port). The controller's
# health probes run on 8081; this Service is for Prometheus scraping only.
# Port 8443 is wired by the manager_metrics_patch applied in kustomize/default.

resource "kubernetes_service" "db_metrics" {
  metadata {
    name      = "dbaas-controller-manager-metrics-service"
    namespace = kubernetes_namespace.dbaas_system.metadata[0].name
    labels = merge(local.db_labels, {
      "control-plane" = "controller-manager"
    })
  }
  spec {
    selector = {
      "control-plane"          = "controller-manager"
      "app.kubernetes.io/name" = "dbaas"
    }
    port {
      name        = "https"
      port        = 8443
      protocol    = "TCP"
      target_port = 8443
    }
  }
}

# ── Dbaas-operator: NetworkPolicy (optional) ─────────────────────────────────
# Gates ingress to the metrics endpoint (8443) from namespaces labelled
# `metrics: enabled`. Off by default — the canonical kustomize/default has the
# network-policy resource commented out. Enable when the cluster has
# NetworkPolicy enforcement and you want to restrict who can scrape /metrics.

resource "kubernetes_network_policy_v1" "db_allow_metrics" {
  count = var.enable_metrics_network_policy ? 1 : 0

  metadata {
    name      = "dbaas-allow-metrics-traffic"
    namespace = kubernetes_namespace.dbaas_system.metadata[0].name
    labels    = local.db_labels
  }

  spec {
    pod_selector {
      match_labels = {
        "control-plane"          = "controller-manager"
        "app.kubernetes.io/name" = "dbaas"
      }
    }
    policy_types = ["Ingress"]
    ingress {
      from {
        namespace_selector {
          match_labels = {
            "metrics" = "enabled"
          }
        }
      }
      ports {
        port     = "8443"
        protocol = "TCP"
      }
    }
  }
}

# ── Dbaas-operator: Deployment ───────────────────────────────────────────────

resource "kubernetes_deployment" "dbaas_controller_manager" {
  metadata {
    name      = "dbaas-controller-manager"
    namespace = kubernetes_namespace.dbaas_system.metadata[0].name
    labels = merge(local.db_labels, {
      "control-plane" = "controller-manager"
    })
  }

  # Same CI-ownership pattern as the keyvault module: TF seeds the initial
  # image; after the first apply, CI rolls it forward via
  # `kubectl set image`. Ignoring the image here prevents noisy revert diffs
  # on every plan after a CI deploy. Change the tag by updating db_image_tag
  # + running apply only when you want TF to pin a specific release.
  lifecycle {
    ignore_changes = [
      metadata[0].annotations,
      spec[0].template[0].spec[0].container[0].image,
    ]
  }

  spec {
    replicas = 1

    selector {
      match_labels = {
        "control-plane"          = "controller-manager"
        "app.kubernetes.io/name" = "dbaas"
      }
    }

    template {
      metadata {
        labels = {
          "control-plane"          = "controller-manager"
          "app.kubernetes.io/name" = "dbaas"
        }
        annotations = {
          # Marks the primary container for `kubectl logs` / `kubectl exec`
          # auto-selection — standard kubebuilder convention.
          "kubectl.kubernetes.io/default-container" = "manager"
        }
      }

      spec {
        service_account_name             = kubernetes_service_account.dbaas_controller_manager.metadata[0].name
        termination_grace_period_seconds = 10

        security_context {
          run_as_non_root = true
          seccomp_profile {
            type = "RuntimeDefault"
          }
        }

        dynamic "image_pull_secrets" {
          for_each = local.db_pull_secret_enabled ? [1] : []
          content {
            name = kubernetes_secret.db_ghcr_pull[0].metadata[0].name
          }
        }

        container {
          name              = "manager"
          image             = "${var.db_image}:${var.db_image_tag}"
          image_pull_policy = "IfNotPresent"
          command           = ["/manager"]

          # --metrics-bind-address is added by manager_metrics_patch in
          # kustomize/default. It binds the HTTPS metrics server on :8443.
          # --mgmt-logical-switch is dbaas-specific (KubeOVN management
          # subnet for the VM mgmt NIC) — see db_mgmt_logical_switch.
          # --metrics-cert-path is only added when enable_cert_manager_metrics
          # is true (cert_metrics_manager_patch effect).
          args = concat(
            [
              "--metrics-bind-address=:8443",
              "--leader-elect",
              "--health-probe-bind-address=:8081",
              "--mgmt-logical-switch=${var.db_mgmt_logical_switch}",
            ],
            var.enable_cert_manager_metrics ? ["--metrics-cert-path=/tmp/k8s-metrics-server/metrics-certs"] : [],
          )

          security_context {
            read_only_root_filesystem  = true
            allow_privilege_escalation = false
            capabilities {
              drop = ["ALL"]
            }
          }

          port {
            name           = "health"
            container_port = 8081
            protocol       = "TCP"
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
            requests = {
              cpu    = "10m"
              memory = "64Mi"
            }
            limits = {
              cpu    = "500m"
              memory = "128Mi"
            }
          }

          # cert_metrics_manager_patch mounts the cert-manager-issued Secret
          # at /tmp/k8s-metrics-server/metrics-certs. Only present when TLS
          # cert rotation is enabled for the metrics endpoint.
          dynamic "volume_mount" {
            for_each = var.enable_cert_manager_metrics ? [1] : []
            content {
              name       = "metrics-certs"
              mount_path = "/tmp/k8s-metrics-server/metrics-certs"
              read_only  = true
            }
          }
        }

        dynamic "volume" {
          for_each = var.enable_cert_manager_metrics ? [1] : []
          content {
            name = "metrics-certs"
            secret {
              secret_name = "metrics-server-cert"
              optional    = false
              items {
                key  = "ca.crt"
                path = "ca.crt"
              }
              items {
                key  = "tls.crt"
                path = "tls.crt"
              }
              items {
                key  = "tls.key"
                path = "tls.key"
              }
            }
          }
        }
      }
    }
  }

  depends_on = [
    kubernetes_manifest.crd_dbinstances,
    kubernetes_cluster_role_binding_v1.db_manager,
  ]
}

# ── Dbaas-operator: ServiceMonitor (optional) ────────────────────────────────
# Requires the Prometheus Operator CRDs (monitoring.coreos.com/v1) to be
# installed on the target cluster. Off by default — enable only when the
# cluster runs kube-prometheus-stack or another Prometheus Operator deployment.
# Mirrors the commented config/prometheus/monitor.yaml in kustomize/default.
#
# When enable_cert_manager_metrics is also true, the tlsConfig is updated to
# use the cert-manager-issued metrics-server-cert Secret instead of
# insecureSkipVerify. This mirrors the effect of monitor_tls_patch.yaml.

resource "kubernetes_manifest" "db_service_monitor" {
  count = var.enable_prometheus_servicemonitor ? 1 : 0

  manifest = {
    apiVersion = "monitoring.coreos.com/v1"
    kind       = "ServiceMonitor"
    metadata = {
      name      = "dbaas-controller-manager-metrics-monitor"
      namespace = kubernetes_namespace.dbaas_system.metadata[0].name
      labels = merge(local.db_labels, {
        "control-plane" = "controller-manager"
      })
    }
    spec = {
      selector = {
        matchLabels = {
          "control-plane"          = "controller-manager"
          "app.kubernetes.io/name" = "dbaas"
        }
      }
      endpoints = [
        var.enable_cert_manager_metrics ? {
          path            = "/metrics"
          port            = "https"
          scheme          = "https"
          bearerTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"
          tlsConfig = {
            serverName         = "dbaas-controller-manager-metrics-service.${kubernetes_namespace.dbaas_system.metadata[0].name}.svc"
            insecureSkipVerify = false
            ca = {
              secret = {
                name = "metrics-server-cert"
                key  = "ca.crt"
              }
            }
            cert = {
              secret = {
                name = "metrics-server-cert"
                key  = "tls.crt"
              }
            }
            keySecret = {
              name = "metrics-server-cert"
              key  = "tls.key"
            }
          }
          } : {
          path            = "/metrics"
          port            = "https"
          scheme          = "https"
          bearerTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"
          tlsConfig = {
            serverName         = null
            insecureSkipVerify = true
            ca                 = null
            cert               = null
            keySecret          = null
          }
        }
      ]
    }
  }
}
