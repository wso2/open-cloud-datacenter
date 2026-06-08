# ─────────────────────────────────────────────────────────────────────────────
# database (dbaas-operator) module — inputs
#
# Sibling of modules/operators/keyvault. Per-operator variables are prefixed
# (db_*) so the namespace stays clean when sibling operators compose in the
# same root module.
# ─────────────────────────────────────────────────────────────────────────────

# ── Dbaas operator ────────────────────────────────────────────────────────────

variable "db_namespace" {
  type        = string
  description = "Namespace the dbaas-operator controller runs in. Must exist or be created by this module (default: 'dbaas-system')."
  default     = "dbaas-system"
}

variable "db_image" {
  type        = string
  description = "Container image registry path for the dbaas-operator, without a tag (e.g. 'ghcr.io/wso2/dbaas-controller')."
  default     = "ghcr.io/wso2/dbaas-controller"
}

variable "db_image_tag" {
  type        = string
  description = "Pinned image tag for the dbaas-operator. Never 'latest' — a fixed tag ensures plan output is deterministic and rollback is possible."
  default     = "v0.2.15-split-secrets"
}

variable "db_mgmt_logical_switch" {
  type        = string
  description = "KubeOVN logical switch name the controller uses for the management NIC on each DB VM. Passed to the manager as --mgmt-logical-switch. Default 'ovn-default' matches the cluster-default KubeOVN switch; override when DB VMs must attach to a non-default management subnet."
  default     = "ovn-default"
}

# ── GHCR image pull credentials (optional) ────────────────────────────────────
# Both must be set together. If either is empty the pull secret is skipped and
# the Deployment has no imagePullSecrets — suitable for clusters that already
# have cluster-level registry credentials or when the image is public.

variable "ghcr_username" {
  type        = string
  description = "GitHub username for pulling images from ghcr.io. Set together with ghcr_pat to create a 'ghcr-pull-secret' in the operator namespace. Leave empty for public images or clusters with pre-existing registry credentials."
  default     = ""
}

variable "ghcr_pat" {
  type        = string
  sensitive   = true
  description = "GitHub Personal Access Token with read:packages scope. Required when ghcr_username is set. Stored as a kubernetes.io/dockerconfigjson Secret in the operator namespace."
  default     = ""
}

# ── Optional feature toggles ──────────────────────────────────────────────────

variable "enable_metrics_network_policy" {
  type        = bool
  description = "When true, creates a NetworkPolicy restricting /metrics (port 8443) ingress to namespaces labelled 'metrics: enabled'. Mirrors the commented config/network-policy resource. Off by default to match the kustomize/default baseline. Enable when the cluster has NetworkPolicy enforcement (Calico/Cilium)."
  default     = false
}

variable "enable_prometheus_servicemonitor" {
  type        = bool
  description = "When true, creates a ServiceMonitor (monitoring.coreos.com/v1) that points Prometheus at the controller-manager metrics Service. Requires the Prometheus Operator CRDs to be installed on the target cluster (e.g. via kube-prometheus-stack). Off by default to avoid a hard dependency on the Prometheus Operator."
  default     = false
}

variable "enable_cert_manager_metrics" {
  type        = bool
  description = "When true, mounts the cert-manager-issued 'metrics-server-cert' Secret into the manager container and adds --metrics-cert-path, enabling TLS on the /metrics endpoint. Also updates the ServiceMonitor tlsConfig when enable_prometheus_servicemonitor is true. Requires cert-manager to be installed and to have issued a Certificate named 'metrics-certs' in the dbaas namespace."
  default     = false
}
