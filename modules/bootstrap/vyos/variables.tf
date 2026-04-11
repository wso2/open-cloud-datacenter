# ── VyOS image ────────────────────────────────────────────────────────────────

variable "image_name" {
  type        = string
  description = "Name for the VyOS image resource in Harvester."
  default     = "vyos-rolling"
}

variable "image_namespace" {
  type        = string
  description = "Namespace for the VyOS image. Use 'harvester-public' to share across all namespaces."
  default     = "harvester-public"
}

variable "image_url" {
  type        = string
  description = "Download URL for the VyOS rolling ISO. Get the latest from https://github.com/vyos/vyos-nightly-build/releases — use the *-generic-amd64.iso file."
}

# ── VyOS VM ───────────────────────────────────────────────────────────────────

variable "vm_name" {
  type        = string
  description = "Name for the VyOS gateway VM."
  default     = "vyos-gw"
}

variable "vm_namespace" {
  type        = string
  description = "Harvester namespace to deploy the VyOS VM into."
  default     = "default"
}

variable "cpu" {
  type        = number
  description = "Number of vCPUs for the VyOS VM."
  default     = 2
}

variable "memory" {
  type        = string
  description = "Memory for the VyOS VM (e.g. '2Gi')."
  default     = "2Gi"
}

variable "disk_size" {
  type        = string
  description = "Root disk size. VyOS install requires at least 10Gi."
  default     = "20Gi"
}

# ── Networking ────────────────────────────────────────────────────────────────

variable "uplink_network_name" {
  type        = string
  description = "Full Harvester network ref for eth0 (the external uplink NIC), e.g. 'default/uplink-network'."
}

variable "cluster_network_name" {
  type        = string
  description = "Harvester cluster network name for eth1 (the trunk NIC that carries tenant VLANs). Must be a trunk-capable cluster network."
}

variable "trunk_network_namespace" {
  type        = string
  description = "Namespace for the trunk harvester_network resource (eth1). Defaults to the VM namespace."
  default     = null
}

variable "management_network_name" {
  type        = string
  description = "Full Harvester network ref for the optional eth2 management NIC, e.g. 'default/vyos-mgmt'. When set, attaches a third NIC on the Harvester management cluster network so in-cluster processes can reach the VyOS HTTPS API without routing through the external uplink."
  default     = null
}

# ── Phase control ─────────────────────────────────────────────────────────────

variable "iso_installed" {
  type        = bool
  description = <<-EOT
    Set to false on first apply — deploys the VM with the ISO as CDROM (boot_order=1).
    After manually running 'install image' and rebooting via Harvester console, set to
    true and re-apply — removes the CDROM and sets the rootdisk as the only boot device.
  EOT
  default     = false
}
