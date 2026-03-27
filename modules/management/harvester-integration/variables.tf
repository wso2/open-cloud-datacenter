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
