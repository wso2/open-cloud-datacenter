# ── Identity / auth ──────────────────────────────────────────────────────────

variable "oidc_issuer" {
  type        = string
  description = "OIDC issuer URL for Asgardeo (e.g. https://api.asgardeo.io/t/wso2). Passed to DC-API as DCAPI_OIDC_ISSUER."
}

variable "oidc_audience" {
  type        = string
  description = "OIDC client ID / audience for the DC-API application in Asgardeo. Passed to DC-API as DCAPI_OIDC_AUDIENCE."
}

# ── Rancher ───────────────────────────────────────────────────────────────────

variable "rancher_url" {
  type        = string
  description = "Rancher API URL (e.g. https://rancher.internal.wso2.com). Passed to DC-API as DCAPI_RANCHER_URL."
}

variable "rancher_token" {
  type        = string
  sensitive   = true
  description = "Rancher admin token for DC-API to call the Rancher REST API. Passed to DC-API as DCAPI_RANCHER_TOKEN."
}

# ── Harvester ─────────────────────────────────────────────────────────────────

variable "harvester_cloud_credential_id" {
  type        = string
  sensitive   = true
  description = "Rancher cloud credential ID for Harvester. Passed to DC-API as DCAPI_RANCHER_HARVESTER_CREDENTIAL."
}

variable "harvester_kubeconfig" {
  type        = string
  sensitive   = true
  description = "Full contents of the Harvester kubeconfig (not a path). Passed to DC-API as DCAPI_HARVESTER_KUBECONFIG."
}

# ── DC-API workload ───────────────────────────────────────────────────────────

variable "dc_api_image" {
  type        = string
  description = "Container image for the DC-API server (full ref including tag/digest)."
  default     = "ghcr.io/hiranadikari/dc-api:latest"
}

variable "dcapi_hostname" {
  type        = string
  description = "Public hostname for the DC-API ingress. Must resolve to the LoadBalancer IP (via /etc/hosts on dev machines or via internal DNS in production)."
  default     = "dcapi.lk.internal.wso2.com"
}

variable "tenant_group_prefix" {
  type        = string
  description = "Asgardeo group prefix that identifies tenants (e.g. 'dc-tenant-' → group 'dc-tenant-teamalpha' maps to tenant 'teamalpha')."
  default     = "dc-tenant-"
}

variable "admin_group" {
  type        = string
  description = "Asgardeo group name for platform admins."
  default     = "dc-admin"
}

variable "log_level" {
  type        = string
  description = "DC-API log level (debug | info | warn | error)."
  default     = "info"
  validation {
    condition     = contains(["debug", "info", "warn", "error"], var.log_level)
    error_message = "log_level must be one of: debug, info, warn, error."
  }
}

variable "operator_ssh_key" {
  type        = string
  sensitive   = true
  description = "SSH public key injected into tenant VMs for IaaS-team break-glass access. Empty string disables."
  default     = ""
}

variable "operator_password" {
  type        = string
  sensitive   = true
  description = "Console password injected into tenant VMs for IaaS-team break-glass access. Empty string disables."
  default     = ""
}

# ── GHCR image pull credentials ───────────────────────────────────────────────

variable "ghcr_username" {
  type        = string
  description = "GitHub username for pulling the DC-API image from ghcr.io."
  default     = "hiranadikari"
}

variable "ghcr_pat" {
  type        = string
  sensitive   = true
  description = "GitHub Personal Access Token with read:packages scope for pulling the DC-API image."
}

# ── GitHub Actions Runner Controller ─────────────────────────────────────────

variable "github_repo_url" {
  type        = string
  description = "GitHub repository URL the ARC runner registers against."
  default     = "https://github.com/HiranAdikari/sovereign-cloud"
}

variable "github_runner_pat" {
  type        = string
  sensitive   = true
  description = "Classic GitHub PAT with repo scope, used by ARC's runner scale-set listener to register/deregister ephemeral runners."
}

variable "arc_chart_version" {
  type        = string
  description = "Version of the gha-runner-scale-set / controller Helm charts to install."
  default     = "0.14.1"
}

# ── Helm CLI bypass kubeconfig ────────────────────────────────────────────────
# The TF helm provider repeatedly hits "http: request body too large" when
# posting the ARC release Secret through Rancher's proxy chain (cause not
# pinned down — Rancher 2.14 introduced something subtle). Helm CLI directly
# against the same Rancher proxy URL works fine. So the two ARC helm_release
# resources are replaced with null_resource + local-exec calling helm CLI.
# This variable is the kubeconfig the local-exec uses; the consumer layer
# constructs it from the same host + token the helm provider would use.
variable "helm_kubeconfig" {
  type        = string
  sensitive   = true
  description = "Full kubeconfig YAML pointing at the Rancher proxy URL with admin token. Used by null_resource local-exec to invoke helm CLI directly, bypassing the TF helm provider."
}
