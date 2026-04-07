# ── VM identity ───────────────────────────────────────────────────────────────
variable "vm_name" {
  type        = string
  description = "Name of the Rancher server VM"
  default     = "rancher-bootstrap"
}

variable "harvester_namespace" {
  type        = string
  description = "Harvester namespace to deploy into"
  default     = "default"
}

variable "node_count" {
  type        = number
  description = "Number of VM instances (1 for single-node, 3 for HA)"
  default     = 1
}

# ── VM hardware ───────────────────────────────────────────────────────────────
variable "vm_cpu" {
  type        = number
  description = "vCPU count for the Rancher VM"
  default     = 4
}

variable "vm_memory" {
  type        = string
  description = "Memory size for the Rancher VM (e.g. '8Gi', '16Gi')"
  default     = "8Gi"
}

variable "vm_disk_name" {
  type        = string
  description = "Name of the root disk in the VM spec"
  default     = "disk-0"
}

variable "vm_disk_size" {
  type        = string
  description = "Root disk size for the Rancher VM"
  default     = "40Gi"
}

variable "vm_disk_auto_delete" {
  type        = bool
  description = "Delete the root disk when the VM is destroyed. Set false for production VMs."
  default     = true
}

variable "ubuntu_image_id" {
  type        = string
  description = "Harvester resource ID of the Ubuntu cloud image (e.g. 'default/image-cwl4b')"
}

variable "enable_usb_tablet" {
  type        = bool
  description = "Attach a USB tablet input device (required by some VMs for correct console cursor behaviour)"
  default     = false
}

# ── Network ───────────────────────────────────────────────────────────────────
variable "network_type" {
  type        = string
  description = "'masquerade' (NAT, default for greenfield) or 'bridge' (direct VLAN, for brownfield/production)"
  default     = "masquerade"

  validation {
    condition     = contains(["masquerade", "bridge"], var.network_type)
    error_message = "network_type must be 'masquerade' or 'bridge'."
  }
}

variable "network_interface_name" {
  type        = string
  description = "Name of the network interface inside the VM spec (e.g. 'nic-1' or 'default')"
  default     = "nic-1"
}

variable "network_name" {
  type        = string
  description = "NetworkAttachmentDefinition name for bridge networks (e.g. 'iaas-net/vm-subnet-001'). Only used when network_type = 'bridge'."
  default     = ""
  validation {
    condition     = var.network_type != "bridge" || var.network_name != ""
    error_message = "network_name is required when network_type = 'bridge'."
  }
}

variable "network_mac_address" {
  type        = string
  description = "MAC address to assign to the bridge NIC. Leave empty to auto-assign. Only used when network_type = 'bridge'."
  default     = ""
}

# Kept for backwards-compatibility (used in VLAN creation — currently disabled in the module).
variable "cluster_network_name" {
  type        = string
  description = "Name of the base cluster network in Harvester (e.g. 'mgmt')"
  default     = "mgmt"
}

variable "cluster_vlan_id" {
  type        = number
  description = "VLAN tag ID for the bootstrap node network"
  default     = 100
}

variable "cluster_vlan_gateway" {
  type        = string
  description = "Gateway IP for the new VLAN (optional)"
  default     = ""
}

# ── SSH key ───────────────────────────────────────────────────────────────────
variable "create_ssh_key" {
  type        = bool
  description = "If true, generate a new RSA key-pair and register it as a Harvester SSH key. Set false to use existing ssh_key_ids."
  default     = true
}

variable "ssh_key_ids" {
  type        = list(string)
  description = "List of existing Harvester SSH key IDs to attach when create_ssh_key = false (e.g. ['default/madawa'])."
  default     = []
}

# ── Cloud-init secret ─────────────────────────────────────────────────────────
variable "create_cloudinit_secret" {
  type        = bool
  description = "If true, render and create a cloud-init secret from the built-in template. Set false to reference an existing secret."
  default     = true
}

variable "existing_cloudinit_secret_name" {
  type        = string
  description = "Name of an existing cloud-init secret to attach when create_cloudinit_secret = false."
  default     = ""
}

variable "vm_password" {
  type        = string
  description = "Default password for the ubuntu user (used in cloud-init template). Required when create_cloudinit_secret = true."
  sensitive   = true
  default     = ""
}

variable "rancher_hostname" {
  type        = string
  description = "FQDN for the Rancher UI (e.g. 'rancher-lk-prod.wso2.com')"
}

variable "bootstrap_password" {
  type        = string
  description = "Temporary Rancher admin password set by the Helm chart during cloud-init install. Required when create_cloudinit_secret = true."
  sensitive   = true
  default     = ""
}

# ── Load Balancer / IP Pool ───────────────────────────────────────────────────
variable "create_lb" {
  type        = bool
  description = "If true, create a Harvester LoadBalancer and IP pool to expose Rancher. Set false when the VM is directly reachable via a bridge IP."
  default     = true
}

variable "static_rancher_ip" {
  type        = string
  description = "IP of the Rancher VM on the internal bridge network when create_lb = false. Passed through as rancher_lb_ip output for CoreDNS."
  default     = ""
}

variable "ippool_subnet" {
  type        = string
  description = "Subnet CIDR for the IP pool (e.g. '192.168.10.0/24'). Required when create_lb = true."
  default     = ""
  validation {
    condition     = !var.create_lb || var.ippool_subnet != ""
    error_message = "ippool_subnet is required when create_lb = true."
  }
}

variable "ippool_gateway" {
  type        = string
  description = "Gateway for the IP pool. Required when create_lb = true."
  default     = ""
  validation {
    condition     = !var.create_lb || var.ippool_gateway != ""
    error_message = "ippool_gateway is required when create_lb = true."
  }
}

variable "ippool_start" {
  type        = string
  description = "Start of the IP range for the pool. Required when create_lb = true."
  default     = ""
  validation {
    condition     = !var.create_lb || var.ippool_start != ""
    error_message = "ippool_start is required when create_lb = true."
  }
}

variable "ippool_end" {
  type        = string
  description = "End of the IP range for the pool. Required when create_lb = true."
  default     = ""
  validation {
    condition     = !var.create_lb || var.ippool_end != ""
    error_message = "ippool_end is required when create_lb = true."
  }
}

variable "ippool_network_name" {
  type        = string
  description = "NetworkAttachmentDefinition name to associate with the IP pool (e.g. 'default/vm-net-100'). Required when the LB VIP is on a VLAN network so kube-vip announces it on the correct interface."
  default     = ""
}
