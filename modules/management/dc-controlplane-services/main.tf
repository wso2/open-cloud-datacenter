# ─────────────────────────────────────────────────────────────────────────────
# DC-API Control Plane Services
#
# Manages everything that runs ON the dcapi-controlplane-rke2 cluster:
#   - dc-system namespace, ConfigMap, secrets
#   - PostgreSQL (Service + StatefulSet + PVC)
#   - DC-API Deployment + Service
#   - LoadBalancer exposing the nginx ingress + Ingress for the public hostname
#   - GitHub Actions Runner Controller (controller + runner scale-set) +
#     RBAC for the runner to deploy DC-API
#
# All provider-level config (kubernetes/helm host + token) lives in the
# calling environment layer. This module only contains resource definitions.
# ─────────────────────────────────────────────────────────────────────────────

# ── Namespaces ────────────────────────────────────────────────────────────────

resource "kubernetes_namespace" "dc_system" {
  metadata {
    name = "dc-system"
  }
  lifecycle {
    ignore_changes = [metadata[0].annotations]
  }
}

resource "kubernetes_namespace" "arc_systems" {
  metadata {
    name = "arc-systems"
  }
  lifecycle {
    ignore_changes = [metadata[0].annotations]
  }
}

resource "kubernetes_namespace" "arc_runners" {
  metadata {
    name = "arc-runners"
    labels = {
      # dind container mode requires privileged pods; declare that explicitly
      # so PSA admission controllers don't silently block runner pod startup.
      "pod-security.kubernetes.io/enforce" = "privileged"
    }
  }
  lifecycle {
    ignore_changes = [metadata[0].annotations]
  }
}

# ── Secrets ───────────────────────────────────────────────────────────────────

# Generated PostgreSQL password — internal only, never leaves this module.
# Length 24, no special chars to keep the DSN simple.
resource "random_password" "postgres" {
  length  = 24
  special = false
}

resource "kubernetes_secret" "dc_postgres" {
  metadata {
    name      = "dc-postgres-secret"
    namespace = kubernetes_namespace.dc_system.metadata[0].name
  }
  data = {
    password = random_password.postgres.result
  }
}

# DC-API server secrets. Consumed by the Deployment via individual envFrom keys.
resource "kubernetes_secret" "dc_api" {
  metadata {
    name      = "dc-api-secrets"
    namespace = kubernetes_namespace.dc_system.metadata[0].name
  }
  data = {
    DCAPI_DB_URL                       = "postgres://dc_api:${random_password.postgres.result}@dc-postgres.dc-system:5432/dc_api?sslmode=disable"
    DCAPI_OIDC_AUDIENCE                = var.oidc_audience
    DCAPI_HARVESTER_KUBECONFIG         = var.harvester_kubeconfig
    DCAPI_RANCHER_TOKEN                = var.rancher_token
    DCAPI_RANCHER_HARVESTER_CREDENTIAL = var.harvester_cloud_credential_id
    DCAPI_OPERATOR_SSH_KEY             = var.operator_ssh_key
    DCAPI_OPERATOR_PASSWORD            = var.operator_password
  }
}

# GHCR image pull secret — referenced as imagePullSecrets in the Deployment.
resource "kubernetes_secret" "ghcr_pull" {
  metadata {
    name      = "ghcr-pull-secret"
    namespace = kubernetes_namespace.dc_system.metadata[0].name
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

# GitHub PAT for the ARC runner scale-set listener. Lives in arc-runners
# namespace and is referenced by name from the dc-runner Helm release.
resource "kubernetes_secret" "github_runner_pat" {
  metadata {
    name      = "github-runner-pat"
    namespace = kubernetes_namespace.arc_runners.metadata[0].name
  }
  data = {
    github_token = var.github_runner_pat
  }
}

# ── Non-secret config ─────────────────────────────────────────────────────────

resource "kubernetes_config_map" "dc_api_config" {
  metadata {
    name      = "dc-api-config"
    namespace = kubernetes_namespace.dc_system.metadata[0].name
  }

  data = {
    DCAPI_OIDC_ISSUER         = var.oidc_issuer
    DCAPI_RANCHER_URL         = var.rancher_url
    DCAPI_RANCHER_INSECURE    = "false"
    DCAPI_HARVESTER_NAMESPACE = "default"
    DCAPI_VM_PROVIDER         = "harvester"
    DCAPI_CLUSTER_PROVIDER    = "rancher"
    DCAPI_TENANT_GROUP_PREFIX = var.tenant_group_prefix
    DCAPI_ADMIN_GROUP         = var.admin_group
    DCAPI_LOG_LEVEL           = var.log_level
    DCAPI_LISTEN_ADDR         = ":8080"
  }
}

# ── PostgreSQL ────────────────────────────────────────────────────────────────
# Single-instance StatefulSet on the harvester storage class. For production
# this becomes a CloudNativePG cluster in M3.

resource "kubernetes_service" "dc_postgres" {
  metadata {
    name      = "dc-postgres"
    namespace = kubernetes_namespace.dc_system.metadata[0].name
  }
  spec {
    cluster_ip = "None" # headless — StatefulSet pods get stable DNS names
    selector = {
      app = "dc-postgres"
    }
    port {
      port        = 5432
      target_port = 5432
    }
  }
}

resource "kubernetes_stateful_set_v1" "dc_postgres" {
  metadata {
    name      = "dc-postgres"
    namespace = kubernetes_namespace.dc_system.metadata[0].name
  }

  spec {
    service_name = kubernetes_service.dc_postgres.metadata[0].name
    replicas     = 1

    selector {
      match_labels = {
        app = "dc-postgres"
      }
    }

    template {
      metadata {
        labels = {
          app = "dc-postgres"
        }
      }

      spec {
        container {
          name  = "postgres"
          image = "postgres:16-alpine"

          env {
            name  = "POSTGRES_USER"
            value = "dc_api"
          }
          env {
            name = "POSTGRES_PASSWORD"
            value_from {
              secret_key_ref {
                name = kubernetes_secret.dc_postgres.metadata[0].name
                key  = "password"
              }
            }
          }
          env {
            name  = "POSTGRES_DB"
            value = "dc_api"
          }
          env {
            name  = "PGDATA"
            value = "/var/lib/postgresql/data/pgdata"
          }

          port {
            container_port = 5432
          }

          volume_mount {
            name       = "pgdata"
            mount_path = "/var/lib/postgresql/data"
          }

          readiness_probe {
            exec {
              command = ["pg_isready", "-U", "dc_api"]
            }
            initial_delay_seconds = 5
            period_seconds        = 5
          }

          liveness_probe {
            exec {
              command = ["pg_isready", "-U", "dc_api"]
            }
            initial_delay_seconds = 30
            period_seconds        = 15
            failure_threshold     = 3
          }
        }
      }
    }

    volume_claim_template {
      metadata {
        name = "pgdata"
      }
      spec {
        access_modes       = ["ReadWriteOnce"]
        storage_class_name = "harvester"
        resources {
          requests = {
            storage = "10Gi"
          }
        }
      }
    }
  }
}

# ── DC-API Deployment + Service ───────────────────────────────────────────────

resource "kubernetes_deployment" "dc_api" {
  metadata {
    name      = "dc-api"
    namespace = kubernetes_namespace.dc_system.metadata[0].name
    labels = {
      app = "dc-api"
    }
  }

  lifecycle {
    ignore_changes = [metadata[0].annotations]
  }

  spec {
    replicas = 1

    selector {
      match_labels = {
        app = "dc-api"
      }
    }

    template {
      metadata {
        labels = {
          app = "dc-api"
        }
      }

      spec {
        security_context {
          run_as_non_root = true
          run_as_user     = 65532
          run_as_group    = 65532
        }

        image_pull_secrets {
          name = kubernetes_secret.ghcr_pull.metadata[0].name
        }

        container {
          name              = "dc-api"
          image             = var.dc_api_image
          image_pull_policy = "Always"

          port {
            container_port = 8080
          }

          env_from {
            config_map_ref {
              name = kubernetes_config_map.dc_api_config.metadata[0].name
            }
          }

          dynamic "env" {
            for_each = toset([
              "DCAPI_DB_URL",
              "DCAPI_OIDC_AUDIENCE",
              "DCAPI_HARVESTER_KUBECONFIG",
              "DCAPI_RANCHER_TOKEN",
              "DCAPI_RANCHER_HARVESTER_CREDENTIAL",
              "DCAPI_OPERATOR_SSH_KEY",
              "DCAPI_OPERATOR_PASSWORD",
            ])
            content {
              name = env.value
              value_from {
                secret_key_ref {
                  name     = kubernetes_secret.dc_api.metadata[0].name
                  key      = env.value
                  optional = contains(["DCAPI_OPERATOR_SSH_KEY", "DCAPI_OPERATOR_PASSWORD"], env.value)
                }
              }
            }
          }

          liveness_probe {
            http_get {
              path = "/healthz"
              port = 8080
            }
            initial_delay_seconds = 10
            period_seconds        = 15
          }

          readiness_probe {
            http_get {
              path = "/healthz"
              port = 8080
            }
            initial_delay_seconds = 5
            period_seconds        = 10
          }

          resources {
            requests = {
              cpu    = "100m"
              memory = "64Mi"
            }
            limits = {
              cpu    = "500m"
              memory = "256Mi"
            }
          }
        }
      }
    }
  }

  depends_on = [kubernetes_stateful_set_v1.dc_postgres]
}

resource "kubernetes_service" "dc_api" {
  metadata {
    name      = "dc-api"
    namespace = kubernetes_namespace.dc_system.metadata[0].name
  }
  spec {
    type = "ClusterIP"
    selector = {
      app = "dc-api"
    }
    port {
      port        = 80
      target_port = 8080
    }
  }
}

# ── Ingress + LoadBalancer exposure ───────────────────────────────────────────

# LoadBalancer service in kube-system that gives the rke2-ingress-nginx
# controller a Harvester LB IP. The Harvester cloud provider fills in the
# EXTERNAL-IP from the dcapi-controlplane-lb IPPool.
resource "kubernetes_service" "ingress_lb" {
  metadata {
    name      = "ingress-expose"
    namespace = "kube-system"
  }
  lifecycle {
    ignore_changes = [metadata[0].annotations]
  }
  spec {
    type = "LoadBalancer"
    selector = {
      "app.kubernetes.io/name"      = "rke2-ingress-nginx"
      "app.kubernetes.io/component" = "controller"
    }
    port {
      name        = "http"
      port        = 80
      target_port = 80
    }
    port {
      name        = "https"
      port        = 443
      target_port = 443
    }
  }
}

resource "kubernetes_ingress_v1" "dc_api" {
  metadata {
    name      = "dc-api"
    namespace = kubernetes_namespace.dc_system.metadata[0].name
    annotations = {
      "nginx.ingress.kubernetes.io/proxy-read-timeout" = "3600"
      "nginx.ingress.kubernetes.io/proxy-send-timeout" = "3600"
    }
  }
  lifecycle {
    ignore_changes = [metadata[0].annotations]
  }
  spec {
    ingress_class_name = "nginx"
    rule {
      host = var.dcapi_hostname
      http {
        path {
          path      = "/"
          path_type = "Prefix"
          backend {
            service {
              name = kubernetes_service.dc_api.metadata[0].name
              port {
                number = 80
              }
            }
          }
        }
      }
    }
  }
}

# ── RBAC for the runner pods to deploy DC-API ─────────────────────────────────
# ServiceAccount lives in arc-runners (the pods' namespace), Role+RoleBinding
# live in dc-system (where the runner deploys things).

resource "kubernetes_service_account" "dc_api_deployer" {
  metadata {
    name      = "dc-api-deployer"
    namespace = kubernetes_namespace.arc_runners.metadata[0].name
  }
}

resource "kubernetes_role_v1" "dc_api_deployer" {
  metadata {
    name      = "dc-api-deployer"
    namespace = kubernetes_namespace.dc_system.metadata[0].name
  }

  rule {
    api_groups = [""]
    resources  = ["services", "configmaps", "persistentvolumeclaims"]
    verbs      = ["get", "list", "create", "update", "patch"]
  }
  rule {
    api_groups = ["apps"]
    resources  = ["deployments", "statefulsets"]
    verbs      = ["get", "list", "create", "update", "patch"]
  }
  rule {
    api_groups = ["apps"]
    resources  = ["deployments/status", "statefulsets/status"]
    verbs      = ["get"]
  }
  rule {
    api_groups = ["networking.k8s.io"]
    resources  = ["ingresses"]
    verbs      = ["get", "list", "create", "update", "patch"]
  }
}

resource "kubernetes_role_binding_v1" "dc_api_deployer" {
  metadata {
    name      = "dc-api-deployer"
    namespace = kubernetes_namespace.dc_system.metadata[0].name
  }

  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "Role"
    name      = kubernetes_role_v1.dc_api_deployer.metadata[0].name
  }

  subject {
    kind      = "ServiceAccount"
    name      = kubernetes_service_account.dc_api_deployer.metadata[0].name
    namespace = kubernetes_namespace.arc_runners.metadata[0].name
  }
}

# ── ARC controller + runner scale-set via helm CLI (local-exec) ───────────────
#
# These two releases used to be `helm_release` resources. The TF helm provider
# fails posting the ARC controller's release Secret through Rancher's proxy
# chain with "http: request body too large" (Rancher 2.14 path-specific issue
# we never fully pinned down). Helm CLI through the same Rancher proxy URL
# works fine — verified by direct test. So we shell out to helm CLI here.
#
# The consumer layer passes a full kubeconfig (var.helm_kubeconfig) pointing
# at the Rancher proxy URL with the admin token. We materialise it to a temp
# file at apply time, then run helm install/uninstall against that kubeconfig.

# Per-instance suffix for the temp files written below. Without this, two
# module instances (or two applies sharing the same module checkout) would
# collide on fixed filenames in path.module. Keyed on the arc-systems
# namespace so the suffix stays stable across plans for the same instance.
resource "random_id" "workdir_suffix" {
  byte_length = 4
  keepers = {
    arc_namespace = kubernetes_namespace.arc_systems.metadata[0].name
  }
}

locals {
  helm_kubeconfig_path  = "${path.module}/.helm-kubeconfig-${random_id.workdir_suffix.hex}.yaml"
  dc_runner_values_path = "${path.module}/.dc-runner-values-${random_id.workdir_suffix.hex}.yaml"
}

resource "local_sensitive_file" "helm_kubeconfig" {
  filename        = local.helm_kubeconfig_path
  content         = var.helm_kubeconfig
  file_permission = "0600"
}

resource "local_sensitive_file" "dc_runner_values" {
  filename = local.dc_runner_values_path
  content = yamlencode({
    githubConfigUrl    = var.github_repo_url
    githubConfigSecret = kubernetes_secret.github_runner_pat.metadata[0].name
    containerMode = {
      type = "dind"
    }
    template = {
      spec = {
        serviceAccountName = kubernetes_service_account.dc_api_deployer.metadata[0].name
      }
    }
  })
  file_permission = "0600"
}

resource "null_resource" "arc_controller" {
  triggers = {
    chart_version   = var.arc_chart_version
    namespace       = kubernetes_namespace.arc_systems.metadata[0].name
    kubeconfig_path = local.helm_kubeconfig_path
  }

  provisioner "local-exec" {
    interpreter = ["/usr/bin/env", "bash", "-c"]
    command     = <<-EOT
      set -euo pipefail
      export KUBECONFIG="${local_sensitive_file.helm_kubeconfig.filename}"
      helm upgrade --install arc \
        oci://ghcr.io/actions/actions-runner-controller-charts/gha-runner-scale-set-controller \
        --version "${var.arc_chart_version}" \
        --namespace "${kubernetes_namespace.arc_systems.metadata[0].name}" \
        --wait --timeout 10m
    EOT
  }

  provisioner "local-exec" {
    when        = destroy
    on_failure  = continue
    interpreter = ["/usr/bin/env", "bash", "-c"]
    command     = <<-EOT
      set -eu
      if [[ -z "${self.triggers.namespace}" ]]; then exit 0; fi
      export KUBECONFIG="${self.triggers.kubeconfig_path}"
      helm uninstall arc --namespace "${self.triggers.namespace}" --ignore-not-found || true
    EOT
  }

  depends_on = [
    kubernetes_namespace.arc_systems,
    local_sensitive_file.helm_kubeconfig,
  ]
}

resource "null_resource" "dc_runner" {
  triggers = {
    chart_version   = var.arc_chart_version
    namespace       = kubernetes_namespace.arc_runners.metadata[0].name
    values_sha      = sha256(local_sensitive_file.dc_runner_values.content)
    kubeconfig_path = local.helm_kubeconfig_path
  }

  provisioner "local-exec" {
    interpreter = ["/usr/bin/env", "bash", "-c"]
    command     = <<-EOT
      set -euo pipefail
      export KUBECONFIG="${local_sensitive_file.helm_kubeconfig.filename}"
      helm upgrade --install dc-runner \
        oci://ghcr.io/actions/actions-runner-controller-charts/gha-runner-scale-set \
        --version "${var.arc_chart_version}" \
        --namespace "${kubernetes_namespace.arc_runners.metadata[0].name}" \
        --values "${local_sensitive_file.dc_runner_values.filename}" \
        --wait --timeout 10m
    EOT
  }

  provisioner "local-exec" {
    when        = destroy
    on_failure  = continue
    interpreter = ["/usr/bin/env", "bash", "-c"]
    command     = <<-EOT
      set -eu
      if [[ -z "${self.triggers.namespace}" ]]; then exit 0; fi
      export KUBECONFIG="${self.triggers.kubeconfig_path}"
      helm uninstall dc-runner --namespace "${self.triggers.namespace}" --ignore-not-found || true
    EOT
  }

  depends_on = [
    null_resource.arc_controller,
    kubernetes_secret.github_runner_pat,
    kubernetes_role_binding_v1.dc_api_deployer,
    local_sensitive_file.dc_runner_values,
    local_sensitive_file.helm_kubeconfig,
  ]
}
