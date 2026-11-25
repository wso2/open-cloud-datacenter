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

  # Whether this module publishes a private CA bundle as the tls-ca secret.
  # Drives both that secret's count and the chart's privateCA value: with
  # privateCA = false the chart passes --no-cacerts and never mounts tls-ca, so
  # creating the secret without the flag leaves it inert and Rancher clears its
  # CA certs instead of serving the bundle to downstream agents.
  publishes_private_ca = var.tls_source == "secret" && var.tls_ca_cert != ""

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

      privateCA = local.publishes_private_ca
    }
  )

  # additional_values is NOT merged in above. Terraform's merge() is shallow, so
  # { ingress = { pathType = "Prefix" } } would replace the whole ingress object
  # and silently drop ingressClassName, extraAnnotations and tls.source — the
  # chart would then default to a Rancher-issued certificate and demand
  # cert-manager, while the preconditions below still passed because they read
  # var.tls_source rather than the effective values.
  #
  # Handing Helm a second values document instead gets a deep merge, so a nested
  # override replaces only the leaf it names. Note Helm still replaces lists
  # wholesale (extraEnv, for example) — that is normal chart behaviour.
  additional_values_yaml = (
    try(length(var.additional_values), 0) > 0
    ? [yamlencode(var.additional_values)]
    : []
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
  count = local.publishes_private_ca ? 1 : 0

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

  # Two documents, merged deeply by Helm in order. See additional_values_yaml.
  values = concat([yamlencode(local.rancher_values)], local.additional_values_yaml)

  # Kept out of the values document so it is not rendered into plan output.
  # Only read by the chart on first install; later changes do not rotate the
  # admin password, which is why a change here must not force a replacement.
  # type = "literal" + jsonencode, because neither alone is safe:
  #   "auto"    — strvals parsing; a comma splits the password into a second
  #               entry with no "=", failing the release, and a digits-only
  #               password becomes an integer.
  #   "string"  — forces a string but still splits on commas.
  #   "literal" — the provider YAML-parses "name: value", so commas survive but
  #               a digits-only password is still read as a number.
  # jsonencode emits a quoted YAML scalar, so the unmarshal always yields the
  # exact string, commas, quotes, digits and all.
  #
  # "literal" is accepted by the provider's validator but the registry docs list
  # only "auto" and "string"; verified against helm ~> 3.3.
  set_sensitive = [
    {
      name  = "bootstrapPassword"
      type  = "literal"
      value = jsonencode(var.bootstrap_password)
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

    # The module owns the TLS wiring: tls_source decides whether the
    # tls-rancher-ingress secret is created and which preconditions apply.
    # Overriding it from additional_values would desync the chart from those
    # decisions, so reject it rather than silently disagreeing.
    precondition {
      condition     = try(var.additional_values.ingress.tls.source, null) == null
      error_message = "Set the TLS source with the tls_source variable, not additional_values.ingress.tls.source — the module keys secret creation and validation off it."
    }

    # Likewise the hostname. It is the module's identity: the rancher_url and
    # rancher_hostname outputs are built from it and read by downstream layers
    # over remote state, and tls_cert/tls_key are issued for it. An override
    # here would point those outputs and that certificate at the wrong name.
    precondition {
      condition     = try(var.additional_values.hostname, null) == null
      error_message = "Set the hostname with the rancher_hostname variable, not additional_values.hostname — the module's outputs and TLS secret are derived from it."
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
