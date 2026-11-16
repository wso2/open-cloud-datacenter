variable "harvester_namespace" {
  type        = string
  description = "Harvester namespace for the VM network and image. Created when not 'default'."
  default     = "default"
}

variable "harvester_kubeconfig_path" {
  type        = string
  description = "Filesystem path to the Harvester kubeconfig. Required when manage_storage_network = true (enforced at plan time) — the readiness waits run kubectl."
  default     = ""
}

# ── Cluster networks + uplinks ────────────────────────────────────────────────
variable "cluster_networks" {
  type        = set(string)
  description = "Cluster networks to create (e.g. ['vm-network', 'strg-network']). Leave out 'mgmt' — it is built in."
  default     = []
}

variable "vlan_configs" {
  type = map(object({
    cluster_network = string
    nics            = list(string)
    bond_mode       = optional(string, "active-backup")
    mtu             = optional(number, 1500)
    node_selector   = optional(map(string), {})
  }))
  description = "Uplink (VlanConfig) per cluster network, keyed by VlanConfig name. The storage network's cluster network must span all nodes."
  default     = {}

  validation {
    condition     = alltrue([for v in values(var.vlan_configs) : v.cluster_network != "mgmt"])
    error_message = "mgmt is built in and does not accept VLAN configs."
  }
}

# ── VM network ────────────────────────────────────────────────────────────────
variable "create_vm_network" {
  type        = bool
  description = "When true, creates a VM network (harvester_network) named vm_network_name in harvester_namespace."
  default     = false
}

variable "vm_network_name" {
  type        = string
  description = "Name of the VM network to create (without namespace). Required when create_vm_network = true."
  default     = ""
  validation {
    condition     = !var.create_vm_network || var.vm_network_name != ""
    error_message = "vm_network_name is required when create_vm_network = true."
  }
}

variable "vm_network_cluster_network" {
  type        = string
  description = "Harvester cluster network the VM network VLAN is attached to (e.g. 'vm-network')."
  default     = "mgmt"
}

variable "vm_network_vlan_id" {
  type        = number
  description = "VLAN tag ID for the VM network."
  default     = 100
}

variable "vm_network_gateway" {
  type        = string
  description = "Default gateway IP for the VM network route. Leave empty for auto route mode."
  default     = ""
}

variable "vm_network_cidr" {
  type        = string
  description = "CIDR for the VM network route (e.g. '0.0.0.0/0'). Required when vm_network_gateway is set."
  default     = ""
}

variable "vm_network_dhcp_server_ip" {
  type        = string
  description = "DHCP server Harvester queries to discover the VM network route in auto mode. Leave empty to broadcast. Cannot be combined with vm_network_gateway / vm_network_cidr."
  default     = ""
}

# ── Storage network ───────────────────────────────────────────────────────────
variable "manage_storage_network" {
  type        = bool
  description = "If true, configure the Harvester storage-network Setting (L2VlanNetwork for Longhorn replication traffic). The storage cluster network and its uplink must exist (cluster_networks / vlan_configs or pre-created)."
  default     = false
}

variable "storage_network_vlan" {
  type        = number
  description = "VLAN ID for the storage network (e.g. 699)."
  default     = 0
}

variable "storage_network_cluster_network" {
  type        = string
  description = "Name of the Harvester cluster network to use for storage traffic (e.g. 'strg-network')."
  default     = ""
}

variable "storage_network_range" {
  type        = string
  description = "IP CIDR range assigned to storage NICs (e.g. '172.23.0.0/19')."
  default     = ""
}

variable "storage_network_exclude_ranges" {
  type        = list(string)
  description = "CIDR blocks to exclude from the storage range. With manage_rwx_network the RWX network shares this range, so guest-cluster VM IPs must be excluded here."
  default     = []
}

variable "storage_network_timeout_seconds" {
  type        = number
  description = "How long to wait for Harvester to finish applying the storage network before failing."
  default     = 1200
}

variable "network_wait_timeout_seconds" {
  type        = number
  description = "How long to wait for the storage cluster network's VLAN status to become ready on all nodes."
  default     = 600
}

# ── RWX network ───────────────────────────────────────────────────────────────
variable "manage_rwx_network" {
  type        = bool
  description = "If true, set the rwx-network Setting to share the storage network ({\"share-storage-network\":true}) once the storage network is applied. Requires manage_storage_network = true."
  default     = false
}

# ── Storage class ─────────────────────────────────────────────────────────────
variable "manage_storage_class" {
  type        = bool
  description = "Create a custom Longhorn StorageClass as the cluster default (harvester-longhorn loses its default annotation) plus longhorn-rwx. No StorageClass is deleted. Set false for brownfield clusters."
  default     = true
}

variable "storage_class_name" {
  type        = string
  description = "Name of the custom default StorageClass to create."
  default     = "harvester-longhorn-2r"
}

variable "storage_class_replicas" {
  type        = number
  description = "Longhorn replica count for the custom default StorageClass. Fixed after creation (prevent_destroy) — use a new storage_class_name to change it."
  default     = 2
}

variable "rwx_storage_class_replicas" {
  type        = number
  description = "Longhorn replica count for the longhorn-rwx StorageClass. Fixed after creation (prevent_destroy)."
  default     = 1
}

# ── Cloud image ───────────────────────────────────────────────────────────────
variable "image_url" {
  type        = string
  description = "Download URL for the cloud image (e.g. Ubuntu 22.04 qcow2), registered as a Harvester backing image. Leave empty to skip."
  default     = ""
}

variable "image_name" {
  type        = string
  description = "Harvester resource name for the downloaded image. Only used when image_url is set."
  default     = "ubuntu-22-04"
}

variable "image_display_name" {
  type        = string
  description = "Human-readable display name shown in the Harvester UI. Only used when image_url is set."
  default     = "ubuntu-22.04"
}
