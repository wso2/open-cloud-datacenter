# ── Identity / auth ──────────────────────────────────────────────────────────

variable "oidc_issuer" {
  type        = string
  description = "OIDC issuer URL for Asgardeo (e.g. https://api.asgardeo.io/t/wso2). Passed to DC-API as DCAPI_OIDC_ISSUER."
}

variable "oidc_audience" {
  type        = list(string)
  description = "Every client ID dc-api should accept as a valid OIDC `aud` claim. dcctl, cloud-ui SPA, cloud-ui-bff confidential client, Rancher SSO client, and any other Asgardeo app whose tokens hit dc-api. Joined with commas at projection time and passed as DCAPI_OIDC_AUDIENCE."

  validation {
    condition     = length(var.oidc_audience) > 0
    error_message = "oidc_audience must contain at least one client ID."
  }
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
  description = "Public hostname for the DC-API ingress. Must resolve to the LoadBalancer IP (via /etc/hosts on dev machines or via internal/public DNS in production). Consumers should pick a per-environment hostname (e.g. dcapi.<env>.example.com) to keep environments routable independently."
}

variable "ingress_additional_dns_names" {
  type        = list(string)
  description = "Extra DNS names to add to the self-signed cert's SAN list (e.g. environment wildcards like *.dev.example.com). Empty by default — the cert only covers dcapi_hostname. Setting a wildcard here is what lets a single cert cover both dc-api and cloud-ui Ingresses without two browser warnings."
  default     = []
}

variable "cloudui_hostname" {
  type        = string
  description = "Public hostname for the cloud-ui ingress. When non-empty, this module creates an Ingress that routes the hostname to a Service named 'cloud-ui' in the dc-system namespace, AND — if cloudui_image is also set — the cloud-ui Deployment + Service themselves. The Ingress re-uses the dc-api self-signed cert via SAN; either set ingress_additional_dns_names to a wildcard that covers cloudui_hostname, or enable auto_include_cloudui_in_tls_sans to have the module add it explicitly. Set to empty string to opt out."
  default     = ""
}

variable "auto_include_cloudui_in_tls_sans" {
  type        = bool
  description = "When true AND cloudui_hostname is non-empty, the dc-api self-signed cert's SAN list automatically includes cloudui_hostname. Defaults to false so existing deployments that already cover cloud-ui via ingress_additional_dns_names (e.g. a wildcard) don't regenerate their cert on apply. New consumers who don't want to manage SANs manually should set this true."
  default     = false
}

variable "cloudui_service_name" {
  type        = string
  description = "Backend Service name the cloud-ui Ingress points at. Defaults to 'cloud-ui' — matching the cloud-ui workflow's Service manifest. Override only if the consumer renames the Service."
  default     = "cloud-ui"
}

variable "cloudui_service_port" {
  type        = number
  description = "Backend Service port the cloud-ui Ingress points at."
  default     = 80
}

variable "cloudui_image" {
  type        = string
  description = "Container image for the cloud-ui Deployment (e.g. registry/cloud-ui:sha). When empty, the Deployment + Service are skipped entirely — useful for environments that don't ship a cloud-ui. CI is expected to roll the image forward via `kubectl set image`; the Deployment's lifecycle ignores image changes so TF and CI don't fight."
  default     = ""
}

variable "cloudui_replicas" {
  type        = number
  description = "Replica count for the cloud-ui Deployment."
  default     = 1
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

# ── F15: VPC external (SNAT) network ──────────────────────────────────────────
# These get projected into the dc-api ConfigMap as DCAPI_VPC_EXTERNAL_* env
# vars. dc-api reads them at startup to bootstrap the KubeOVN ProviderNetwork
# / Vlan / Subnet / NetworkAttachmentDefinition that tenant VPCs SNAT through.

variable "vpc_external_bridge" {
  type        = string
  description = "Host bridge name backing the external network (e.g. 'mgmt-br'). Matches Harvester's host NIC/bridge."
}

variable "vpc_external_cidr" {
  type        = string
  description = "CIDR of the external (management) network. dc-api places NAT gateway pod NICs and EIPs inside this CIDR."
}

variable "vpc_external_gateway" {
  type        = string
  description = "Upstream gateway IP for the external network."
}

variable "vpc_external_reserved_ips" {
  type        = string
  description = "Comma-separated IPs already in use on the external network. Listed so kube-ovn IPAM avoids them."
}

variable "vpc_external_vlan_id" {
  type        = number
  description = "VLAN tag for the external network. 0 = untagged."
  default     = 0
}

# ── F7: BFF confidential OIDC client ──────────────────────────────────────────
# Non-empty bff_client_id activates /v1/auth/{login,callback,logout,me} in
# dc-api. Sensitive values land in the dc-api-secrets Secret; redirect / cookie
# config lands in the dc-api-config ConfigMap.

variable "bff_client_id" {
  type        = string
  description = "Asgardeo client ID for the BFF confidential OIDC client. Empty string disables BFF (dc-api falls back to Bearer-only auth). Projected as DCAPI_BFF_CLIENT_ID."
  default     = ""
}

variable "bff_client_secret" {
  type        = string
  sensitive   = true
  description = "Asgardeo client secret for the BFF client. Projected as DCAPI_BFF_CLIENT_SECRET."
  default     = ""
}

variable "bff_session_secret" {
  type        = string
  sensitive   = true
  description = "Base64-encoded 32-byte AES-256 key used by dc-api to seal BFF session cookies. Projected as DCAPI_BFF_SESSION_SECRET. Rotating this invalidates every active session."
  default     = ""
}

variable "bff_redirect_url" {
  type        = string
  description = "Asgardeo redirect URI for the BFF authorization-code flow (must be whitelisted in the Asgardeo app). Projected as DCAPI_BFF_REDIRECT_URL."
  default     = ""
}

variable "bff_post_login_redirect" {
  type        = string
  description = "URL dc-api 302s the browser to after a successful BFF login. Projected as DCAPI_BFF_POST_LOGIN_REDIRECT."
  default     = ""
}

variable "bff_post_logout_redirect" {
  type        = string
  description = "URL dc-api 302s the browser to after BFF logout. Projected as DCAPI_BFF_POST_LOGOUT_REDIRECT."
  default     = ""
}

variable "bff_cookie_domain" {
  type        = string
  description = "Cookie domain scope for BFF session cookies (e.g. '.lk-dev.internal.wso2.com'). Projected as DCAPI_BFF_COOKIE_DOMAIN."
  default     = ""
}

variable "bff_cookie_secure" {
  type        = bool
  description = "Whether to set the Secure flag on the BFF session cookie. Should be true everywhere except localhost http://. Projected as DCAPI_BFF_COOKIE_SECURE."
  default     = true
}
