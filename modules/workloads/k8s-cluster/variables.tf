variable "cluster_name" {
  type        = string
  description = "Name of the downstream RKE2 cluster in Rancher"
}

variable "kubernetes_version" {
  type        = string
  description = "RKE2 Kubernetes version (e.g. v1.32.13+rke2r1)"
}

variable "cloud_credential_id" {
  type        = string
  sensitive   = true
  description = "Harvester cloud credential secret name (cattle-global-data:cc-xxxx)"
}

variable "cni" {
  type        = string
  description = "CNI plugin for the cluster"
  default     = "cilium"
}

variable "machine_global_config" {
  type        = string
  description = "Full machine_global_config YAML for the cluster. When null the module generates a default from the cni variable. Override to add extra args such as kube-proxy-arg."
  default     = null
}

variable "registries" {
  type = object({
    configs = optional(list(object({
      hostname                = string
      auth_config_secret_name = optional(string)
      insecure                = optional(bool, false)
      tls_secret_name         = optional(string)
      ca_bundle               = optional(string)
    })), [])
    mirrors = optional(list(object({
      hostname  = string
      endpoints = list(string)
    })), [])
  })
  description = "Private registry configurations for the cluster. Set to null to configure no registries."
  default     = null
}

# ── Machine pools ─────────────────────────────────────────────────────────────
# Each entry produces one rancher2_machine_config_v2 + one pool in the cluster.
# Use a single combined pool for small clusters; separate control-plane / worker
# entries for larger ones.
variable "machine_pools" {
  type = list(object({
    name          = string
    vm_namespace  = string
    quantity      = number
    cpu_count     = string        # string expected by Harvester API e.g. "4"
    memory_size   = string        # GiB as string e.g. "12"
    disk_size     = number        # GiB as integer
    image_name    = string        # "namespace/image-id"
    networks      = list(string)  # ["ns/nad", "iaas/storage-network", ...]
    control_plane = bool
    etcd          = bool
    worker        = bool
    # machine_labels are applied to Kubernetes nodes (RKEMachinePool.spec.labels).
    # Use these for node selectors and scheduling decisions (e.g. nodepool=build).
    # Note: machine_pools.labels in rancher2 v13 targets MachineDeployment metadata,
    # not the nodes themselves — this variable maps to machine_labels on the resource.
    machine_labels = optional(map(string), {})
    taints = optional(list(object({
      key    = string
      value  = string
      effect = string  # NoSchedule | PreferNoSchedule | NoExecute
    })), [])
  }))
  # Defaults to empty for brownfield callers (manage_rke_config = false).
  # A precondition on the cluster resource enforces at least one pool when
  # manage_rke_config = true.
  default = []

  validation {
    condition = length(var.machine_pools) == length(distinct([for p in var.machine_pools : p.name])) && alltrue([
      for p in var.machine_pools :
      p.quantity > 0 &&
      floor(p.quantity) == p.quantity &&
      p.disk_size > 0 &&
      floor(p.disk_size) == p.disk_size &&
      (p.control_plane || p.etcd || p.worker)
    ])
    error_message = "Each machine pool must have a unique name, integer quantity/disk_size > 0, and at least one role (control_plane, etcd, or worker) enabled."
  }
}

# ── Node cloud-init ───────────────────────────────────────────────────────────
variable "user_data" {
  type        = string
  sensitive   = true
  description = "cloud-init user-data applied to every node VM (plain YAML or base64)"
  default     = ""
}

variable "ssh_user" {
  type        = string
  description = "SSH username for the VM OS"
  default     = "ubuntu"
}

# ── Harvester cloud provider ──────────────────────────────────────────────────
variable "cloud_provider_config_secret" {
  type        = string
  description = "Secret name for Harvester cloud-provider-config, without namespace prefix (just the secret name part after 'fleet-default:'). Leave empty to skip."
  default     = ""
}

# ── Brownfield skip flag ──────────────────────────────────────────────────────
variable "manage_rke_config" {
  type        = bool
  description = "Create/manage machine configs and rke_config block. Set false for brownfield clusters where machine configs cannot be imported."
  default     = true
}

variable "machine_config_overrides" {
  type = map(object({
    kind = string
    name = string
  }))
  description = "Existing machine config kind/name keyed by pool name. When a pool name is present here, no rancher2_machine_config_v2 is created for it and the provided kind/name are used directly. Use this for brownfield pools whose machine configs already exist in Rancher and cannot be imported."
  default     = {}
}

# ── etcd S3 backup (optional) ─────────────────────────────────────────────────
variable "etcd_s3" {
  type = object({
    bucket              = string
    folder              = string
    region              = string
    cloud_credential_id = string
    snapshot_retention  = optional(number, 3)
    snapshot_schedule   = optional(string, "5 23 * * *")
  })
  default     = null
  description = "S3 etcd backup config. Set to null to disable."

  validation {
    condition = var.etcd_s3 == null || (
      trimspace(var.etcd_s3.bucket) != "" &&
      trimspace(var.etcd_s3.region) != "" &&
      trimspace(var.etcd_s3.cloud_credential_id) != "" &&
      try(var.etcd_s3.snapshot_retention, 3) > 0 &&
      floor(try(var.etcd_s3.snapshot_retention, 3)) == try(var.etcd_s3.snapshot_retention, 3) &&
      trimspace(try(var.etcd_s3.snapshot_schedule, "")) != ""
    )
    error_message = "When etcd_s3 is set, bucket/region/cloud_credential_id must be non-empty, snapshot_retention must be a positive integer, and snapshot_schedule must be non-empty."
  }
}
