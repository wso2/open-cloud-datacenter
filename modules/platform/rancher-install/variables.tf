# ── Rancher identity ──────────────────────────────────────────────────────────
variable "rancher_hostname" {
  type        = string
  description = "FQDN Rancher is served on (chart value 'hostname'). Must resolve to the load balancer in front of the cluster."

  validation {
    condition     = can(regex("^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$", var.rancher_hostname))
    error_message = "rancher_hostname must be a lowercase DNS name, e.g. 'rancher.example.com'."
  }
}

variable "bootstrap_password" {
  type        = string
  description = "Initial admin password (chart value 'bootstrapPassword'). Only consumed by the chart on first install — changing it later does not rotate the live password."
  sensitive   = true
}

variable "rancher_namespace" {
  type        = string
  description = "Namespace Rancher is installed into. The chart assumes cattle-system."
  default     = "cattle-system"
}

# ── Chart ─────────────────────────────────────────────────────────────────────
variable "use_rancher_prime" {
  type        = bool
  description = "Install from the Rancher Prime chart repository (charts.rancher.com/server-charts/prime) instead of the community 'latest' channel (releases.rancher.com/server-charts/latest)."
  default     = false
}

variable "rancher_version" {
  type        = string
  description = "Rancher chart version to install (e.g. '2.9.3'). Pin it — leaving this empty resolves to whatever the repository currently serves, so the installed version depends on when apply ran."

  validation {
    condition     = var.rancher_version != ""
    error_message = "rancher_version must be pinned; an unpinned chart makes the release non-reproducible."
  }
}

variable "replicas" {
  type        = number
  description = "Rancher pod replicas (chart value 'replicas'). Use the control-plane node count — 3 for an HA cluster."
  default     = 3

  validation {
    condition     = var.replicas > 0
    error_message = "replicas must be at least 1."
  }
}

variable "control_plane_node_count" {
  type        = number
  description = "Control-plane node count, used only to validate replicas against the cluster. Pass platform/rancher-bootstrap's server_count; 0 disables the check."
  default     = 0
}

variable "psp_enabled" {
  type        = bool
  description = "Chart value 'global.cattle.psp.enabled'. PodSecurityPolicy was removed in Kubernetes 1.25, so this stays false on any current cluster."
  default     = false
}

variable "extra_env" {
  type = list(object({
    name  = string
    value = string
  }))
  description = "Environment variables for the Rancher pods (chart value 'extraEnv'). Defaults to the GODEBUG flag that re-enables acceptance of certificates with negative serial numbers."
  default = [
    {
      name  = "GODEBUG"
      value = "x509negativeserial=1"
    },
  ]
}

variable "additional_values" {
  type        = any
  description = "Extra chart values merged over the ones this module builds. Escape hatch for anything not exposed as a typed variable."
  default     = {}
}

variable "helm_timeout_seconds" {
  type        = number
  description = "How long to wait for a release to become ready."
  default     = 900
}

# ── TLS ───────────────────────────────────────────────────────────────────────
variable "ingress_class_name" {
  type        = string
  description = <<-EOT
    Chart value 'ingress.ingressClassName'. Empty (the default) leaves it blank,
    so the Ingress is admitted by whichever IngressClass carries
    ingressclass.kubernetes.io/is-default-class — convenient, but implicit.

    Name the class on any cluster where it matters, e.g. "traefik" on an RKE2
    cluster installed with ingress-controller: ["traefik"], or "nginx" on one
    still running the bundled ingress-nginx.
  EOT
  default     = ""
}

variable "ingress_extra_annotations" {
  type        = map(string)
  description = <<-EOT
    Chart value 'ingress.extraAnnotations', merged onto the Rancher Ingress.

    The chart renders ingress-nginx annotations only when
    includeDefaultExtraAnnotations is on, which is deprecated since
    ingress-nginx's March 2026 EOL — so annotations for any other controller go
    here. Note that Traefik drops unsupported nginx.ingress.kubernetes.io/*
    annotations silently, with no log line: test functionally rather than
    assuming an annotation took effect.

    Example — reference a Traefik Middleware:
      { "traefik.ingress.kubernetes.io/router.middlewares" = "cattle-system-body-limit@kubernetescrd" }
  EOT
  default     = {}
}

variable "tls_source" {
  type        = string
  description = "Chart value 'ingress.tls.source': 'secret' (certificate supplied via tls_cert/tls_key), 'rancher' (self-signed, needs cert-manager) or 'letsEncrypt' (needs cert-manager)."
  default     = "secret"

  validation {
    condition     = contains(["secret", "rancher", "letsEncrypt"], var.tls_source)
    error_message = "tls_source must be one of: secret, rancher, letsEncrypt."
  }
}

variable "tls_cert" {
  type        = string
  description = "PEM-encoded certificate chain (leaf + intermediates) for the Rancher ingress. Required when tls_source = 'secret'."
  sensitive   = true
  default     = ""
}

variable "tls_key" {
  type        = string
  description = "PEM-encoded private key for tls_cert. Required when tls_source = 'secret'."
  sensitive   = true
  default     = ""
}

variable "tls_ca_cert" {
  type        = string
  description = "PEM-encoded CA certificate published to agents as the tls-ca secret. Only needed when tls_cert is signed by a CA that downstream agents do not already trust; leave empty for a publicly-trusted certificate."
  sensitive   = true
  default     = ""
}

variable "install_cert_manager" {
  type        = bool
  description = "Install cert-manager alongside Rancher. Required when tls_source is 'rancher' or 'letsEncrypt'; unnecessary when the certificate comes from a secret."
  default     = false
}

variable "cert_manager_version" {
  type        = string
  description = "cert-manager chart version, used when install_cert_manager = true."
  default     = "v1.13.1"
}
