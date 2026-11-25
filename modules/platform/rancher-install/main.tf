# Installs Rancher onto an existing RKE2 cluster (platform/rancher-bootstrap)
# and keeps the release in Terraform state, so hostname, replica count, chart
# version and every other value are planned and diffed like any other resource.
#
# Providers are declared, not configured, here: the caller points the helm and
# kubernetes providers at the cluster's admin kubeconfig.
#
# NOTE ON helm_release — the rest of this catalog installs charts with the helm
# CLI via local-exec, because the Terraform helm provider cannot post release
# Secrets through Rancher's downstream-cluster proxy ("request body too large").
# That constraint does not apply here: this module talks straight to the RKE2
# apiserver through the load balancer, with no Rancher proxy in the path. Using
# the provider is what puts the Rancher release under Terraform management,
# which is the entire point of splitting it out of cloud-init.

locals {
  chart_repository = (
    var.use_rancher_prime
    ? "https://charts.rancher.com/server-charts/prime"
    : "https://releases.rancher.com/server-charts/latest"
  )

  # A chart-managed TLS secret means cert-manager must be present; with
  # ingress.tls.source = "secret" the certificate is supplied here instead.
  tls_from_secret = var.tls_source == "secret"

  # Rendered as a single values document rather than many set{} blocks: nested
  # structures and lists survive intact and the whole thing stays diffable.
  rancher_values = merge(
    {
      hostname = var.rancher_hostname
      replicas = var.replicas

      # ingressClassName = "" makes the chart fall back to the cluster's default
      # IngressClass. That works, but it is implicit: it silently re-resolves if a
      # second controller is installed or the is-default-class annotation moves.
      # Name the class to pin it.
      #
      # extraAnnotations is the chart's documented path for non-nginx classes —
      # its ingress template only renders ingress-nginx annotations by default
      # (includeDefaultExtraAnnotations, deprecated since ingress-nginx's EOL).
      # A Traefik Middleware reference belongs here.
      ingress = {
        ingressClassName = var.ingress_class_name
        extraAnnotations = var.ingress_extra_annotations

        tls = {
          source = var.tls_source
        }
      }

      global = {
        cattle = {
          psp = {
            enabled = var.psp_enabled
          }
        }
      }

      extraEnv = var.extra_env
    },
    var.additional_values,
  )
}

# ── Input validation ──────────────────────────────────────────────────────────
# A misconfigured certificate leaves Rancher unreachable, so those two are
# preconditions on the release (they fail the plan). The replica check is
# advisory, and a check block only warns.
check "replicas_not_more_than_nodes" {
  assert {
    condition     = var.control_plane_node_count == 0 || var.replicas <= var.control_plane_node_count
    error_message = "replicas exceeds the control-plane node count; the surplus Rancher pods stay Pending."
  }
}

# ── Namespaces ────────────────────────────────────────────────────────────────
# Created explicitly rather than with the chart's create_namespace, because the
# TLS secret has to exist in it before the release is installed.
resource "kubernetes_namespace_v1" "cattle_system" {
  metadata {
    name = var.rancher_namespace
  }

  lifecycle {
    # Rancher labels and annotates its own namespace after install.
    ignore_changes = [metadata[0].labels, metadata[0].annotations]
  }
}

# ── Ingress certificate ───────────────────────────────────────────────────────
# Consumed by the chart under the fixed name tls-rancher-ingress when
# ingress.tls.source = "secret".
resource "kubernetes_secret_v1" "tls_rancher_ingress" {
  count = local.tls_from_secret ? 1 : 0

  metadata {
    name      = "tls-rancher-ingress"
    namespace = kubernetes_namespace_v1.cattle_system.metadata[0].name
  }

  type = "kubernetes.io/tls"

  data = {
    "tls.crt" = var.tls_cert
    "tls.key" = var.tls_key
  }
}

# The private CA bundle Rancher serves to agents, when the ingress certificate
# is signed by a CA the agents do not already trust.
resource "kubernetes_secret_v1" "tls_ca" {
  count = local.tls_from_secret && var.tls_ca_cert != "" ? 1 : 0

  metadata {
    name      = "tls-ca"
    namespace = kubernetes_namespace_v1.cattle_system.metadata[0].name
  }

  data = {
    "cacerts.pem" = var.tls_ca_cert
  }
}

# ── cert-manager ──────────────────────────────────────────────────────────────
# Only needed when Rancher issues its own certificate.
resource "helm_release" "cert_manager" {
  count = var.install_cert_manager ? 1 : 0

  name             = "cert-manager"
  repository       = "https://charts.jetstack.io"
  chart            = "cert-manager"
  version          = var.cert_manager_version
  namespace        = "cert-manager"
  create_namespace = true

  set = [
    {
      name  = "installCRDs"
      value = "true"
    },
  ]

  wait    = true
  timeout = var.helm_timeout_seconds
}

# ── Rancher ───────────────────────────────────────────────────────────────────
resource "helm_release" "rancher" {
  name       = "rancher"
  repository = local.chart_repository
  chart      = "rancher"
  version    = var.rancher_version
  namespace  = kubernetes_namespace_v1.cattle_system.metadata[0].name

  values = [yamlencode(local.rancher_values)]

  # Kept out of the values document so it is not rendered into plan output.
  # Only read by the chart on first install; later changes do not rotate the
  # admin password, which is why a change here must not force a replacement.
  set_sensitive = [
    {
      name  = "bootstrapPassword"
      value = var.bootstrap_password
    },
  ]

  wait          = true
  timeout       = var.helm_timeout_seconds
  wait_for_jobs = true

  lifecycle {
    precondition {
      condition     = !local.tls_from_secret || (var.tls_cert != "" && var.tls_key != "")
      error_message = "tls_cert and tls_key are required when tls_source = \"secret\"."
    }

    # Rancher issues its own certificate through cert-manager unless one is
    # supplied as a secret; with neither, the ingress never gets a certificate.
    precondition {
      condition     = local.tls_from_secret || var.install_cert_manager
      error_message = "tls_source = \"${var.tls_source}\" needs cert-manager; set install_cert_manager = true or switch to tls_source = \"secret\"."
    }

    # Rotating the bootstrap password in Terraform would not change the live
    # admin password anyway; ignoring it keeps re-applies diff-clean.
    ignore_changes = [set_sensitive]
  }

  depends_on = [
    helm_release.cert_manager,
    kubernetes_secret_v1.tls_rancher_ingress,
    kubernetes_secret_v1.tls_ca,
  ]
}
