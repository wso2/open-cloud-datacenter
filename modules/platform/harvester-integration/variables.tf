variable "harvester_kubeconfig" {
  type        = string
  description = "Content of the Harvester kubeconfig file"
  sensitive   = true
}

variable "harvester_cluster_name" {
  type        = string
  description = "Name for the Harvester cluster in Rancher"
  default     = "harvester-hci"
}

variable "cloud_credential_name" {
  type        = string
  description = "Display name for the Harvester cloud credential in Rancher"
  default     = "harvester-local-creds"
}

variable "credential_namespace" {
  type        = string
  description = "Kubernetes namespace for the cloud credential ServiceAccount and token Secret on Harvester. Override to match an existing namespace when importing brownfield state."
  default     = "kube-system"
  validation {
    condition     = trimspace(var.credential_namespace) != ""
    error_message = "credential_namespace must be a non-empty Kubernetes namespace."
  }
}

variable "cluster_labels" {
  type        = map(string)
  description = "Additional labels to set on the Harvester cluster object in Rancher. Merged with the required provider.cattle.io=harvester label."
  default     = {}
}

# ── Brownfield skip flags ─────────────────────────────────────────────────────
variable "manage_feature_flags" {
  type        = bool
  description = "Create/manage rancher2_feature resources for harvester and harvester-baremetal-container-workload. Set false when the flags are already enabled (brownfield)."
  default     = true
}

variable "create_cloud_credential" {
  type        = bool
  description = "Create a Harvester cloud credential in Rancher. Set false when one already exists (brownfield import)."
  default     = true
}

variable "apply_registration" {
  type        = bool
  description = "Run the null_resource that applies the cattle-cluster-agent manifest. Set false when the cluster is already registered (brownfield)."
  default     = true
}

variable "manage_app" {
  type        = bool
  description = "Create/manage the rancher2_app_v2 Harvester UI extension. Set false when the app is already installed (brownfield import)."
  default     = true
}

variable "harvester_chart_version" {
  type        = string
  description = "Version of the Harvester UI extension Helm chart. Check https://github.com/harvester/harvester-ui-extension/releases for latest."
  default     = "1.8.2"
}

# ── Optional: CoreDNS patching for private Rancher hostname ──────────────────
variable "manage_coredns" {
  type        = bool
  description = <<-EOT
    Whether this module manages the Harvester cluster's CoreDNS Corefile at all.
    Set false when the cluster's stock Corefile differs from this module's
    hardcoded default (chart versions drift between Harvester releases) and no
    hosts patch is needed — e.g. Rancher is on a publicly resolvable domain.

    Set this at creation time. Flipping true -> false on a cluster where the
    resource already exists destroys a kubernetes_config_map_v1_data, which
    removes the Corefile key it manages and breaks cluster DNS.

    To stop managing an existing one, take both steps, in this order:

      1. terraform state rm \
           'module.<name>.kubernetes_config_map_v1_data.harvester_coredns_patch'
         Terraform forgets the object; the live Corefile is left untouched.
         No index: that form removes every instance of the resource, so it
         matches both the counted address and the unindexed one left by module
         versions predating manage_coredns. Confirm with terraform state list.
      2. Set manage_coredns = false, so the next plan does not recreate it.

    The order is the point. Step 2 on its own is the destroy described above,
    and step 1 on its own leaves manage_coredns = true, so the next apply
    recreates the resource and force-writes the Corefile again. Confirm the
    following plan shows neither a create nor a destroy for that address.
  EOT
  default     = true
}

variable "patch_coredns" {
  type        = bool
  description = "Patch Harvester CoreDNS to resolve a private Rancher hostname. Set true when Rancher has no public DNS record (IP-only setup). Requires rancher_lb_ip and rancher_hostname."
  default     = false
}

variable "rancher_lb_ip" {
  type        = string
  description = "IP address of the Rancher server. Added to the Harvester CoreDNS hosts block when patch_coredns = true."
  default     = ""
}

variable "rancher_hostname" {
  type        = string
  description = "FQDN of the Rancher server. Added to the Harvester CoreDNS hosts block when patch_coredns = true."
  default     = ""
}
