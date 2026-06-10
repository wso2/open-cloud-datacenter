# ── VM identity ───────────────────────────────────────────────────────────────
variable "vm_name" {
  type    = string
  default = "rancher-bootstrap"
}

variable "harvester_namespace" {
  type    = string
  default = "default"
}

variable "node_count" {
  type        = number
  description = "Number of VM instances (legacy mode when node_pools is empty)"
  default     = 1
}

# ── Static node pools ─────────────────────────────────────────────────────────
variable "node_pools" {
  type = list(object({
    name          = string
    ips           = list(string)
    control_plane = bool
    etcd          = bool
    worker        = bool
  }))
  description = "Static IP node pools. When non-empty, overrides node_count; each node gets a static IP."
  default     = []
}

variable "node_gateway" {
  type        = string
  description = "Default gateway for static IP nodes. Required when node_pools is set."
  default     = ""
}

variable "node_subnet_prefix" {
  type        = number
  description = "CIDR prefix length for static IP nodes (e.g. 25 for /25)"
  default     = 24
}

# ── VM hardware ───────────────────────────────────────────────────────────────
variable "vm_cpu" {
  type    = number
  default = 4
}

variable "vm_memory" {
  type    = string
  default = "8Gi"
}

variable "vm_disk_name" {
  type    = string
  default = "disk-0"
}

variable "vm_disk_size" {
  type    = string
  default = "40Gi"
}

variable "vm_disk_auto_delete" {
  type    = bool
  default = true
}

variable "image_url" {
  type    = string
  default = ""
}

variable "image_name" {
  type    = string
  default = "ubuntu-22-04"
}

variable "image_display_name" {
  type    = string
  default = "ubuntu-22.04"
}

variable "ubuntu_image_id" {
  type    = string
  default = ""
}

variable "enable_usb_tablet" {
  type    = bool
  default = false
}

# ── Network ───────────────────────────────────────────────────────────────────
variable "network_type" {
  type    = string
  default = "masquerade"

  validation {
    condition     = contains(["masquerade", "bridge"], var.network_type)
    error_message = "network_type must be 'masquerade' or 'bridge'."
  }
}

variable "network_interface_name" {
  type    = string
  default = "nic-1"
}

variable "network_name" {
  type    = string
  default = ""
  validation {
    condition     = var.network_type != "bridge" || var.network_name != ""
    error_message = "network_name is required when network_type = 'bridge'."
  }
}

variable "network_mac_address" {
  type    = string
  default = ""
}

variable "create_bridge_network" {
  type    = bool
  default = false
}

variable "cluster_network_name" {
  type    = string
  default = "mgmt"
}

variable "cluster_vlan_id" {
  type    = number
  default = 100
}

variable "cluster_vlan_gateway" {
  type    = string
  default = ""
}

variable "cluster_vlan_cidr" {
  type    = string
  default = ""
}

# ── SSH key ───────────────────────────────────────────────────────────────────
variable "create_ssh_key" {
  type    = bool
  default = true
}

variable "ssh_key_ids" {
  type    = list(string)
  default = []
}

# ── Cloud-init secret ─────────────────────────────────────────────────────────
variable "create_cloudinit_secret" {
  type    = bool
  default = true
}

variable "existing_cloudinit_secret_name" {
  type    = string
  default = ""
}

variable "vm_password" {
  type      = string
  sensitive = true
  default   = ""
}

variable "rancher_hostname" {
  type = string
}

variable "bootstrap_password" {
  type      = string
  sensitive = true
  default   = ""
}

# ── Load Balancer / IP Pool ───────────────────────────────────────────────────
variable "create_lb" {
  type    = bool
  default = true
}

variable "static_rancher_ip" {
  type    = string
  default = ""
}

variable "ippool_subnet" {
  type    = string
  default = ""
  validation {
    condition     = !var.create_lb || var.ippool_subnet != ""
    error_message = "ippool_subnet is required when create_lb = true."
  }
}

variable "ippool_gateway" {
  type    = string
  default = ""
  validation {
    condition     = !var.create_lb || var.ippool_gateway != ""
    error_message = "ippool_gateway is required when create_lb = true."
  }
}

variable "ippool_start" {
  type    = string
  default = ""
  validation {
    condition     = !var.create_lb || var.ippool_start != ""
    error_message = "ippool_start is required when create_lb = true."
  }
}

variable "ippool_end" {
  type    = string
  default = ""
  validation {
    condition     = !var.create_lb || var.ippool_end != ""
    error_message = "ippool_end is required when create_lb = true."
  }
}

variable "ippool_network_name" {
  type    = string
  default = ""
}

# ── nginx LB ──────────────────────────────────────────────────────────────────
variable "use_nginx_lb" {
  type        = bool
  description = "When true, an external nginx VM is the load balancer. Skips Harvester LB and MetalLB. Requires nginx_lb_ip to be set."
  default     = false
}

variable "nginx_lb_ip" {
  type        = string
  description = "Static IP of the nginx LB VM. Required when use_nginx_lb = true."
  default     = ""

  validation {
    condition     = !var.use_nginx_lb || var.nginx_lb_ip != ""
    error_message = "nginx_lb_ip is required when use_nginx_lb = true."
  }
}

# ── Storage class ─────────────────────────────────────────────────────────────
variable "manage_storage_class" {
  type    = bool
  default = true
}

variable "harvester_kubeconfig_path" {
  type    = string
  default = ""
}

variable "storage_class_name" {
  type    = string
  default = "harvester-longhorn-2r"
}

variable "storage_class_replicas" {
  type    = number
  default = 2
}

# ── Storage network ───────────────────────────────────────────────────────────
variable "manage_storage_network" {
  type    = bool
  default = false
}

variable "storage_network_vlan" {
  type    = number
  default = 0
}

variable "storage_network_cluster_network" {
  type    = string
  default = ""
}

variable "storage_network_range" {
  type    = string
  default = ""
}

variable "storage_network_exclude_ranges" {
  type    = list(string)
  default = []
}

# ── TLS certificate ───────────────────────────────────────────────────────────
variable "tls_source" {
  type    = string
  default = "rancher"

  validation {
    condition     = contains(["rancher", "secret"], var.tls_source)
    error_message = "tls_source must be 'rancher' or 'secret'."
  }
}

variable "tls_cert" {
  type      = string
  sensitive = true
  default   = ""
}

variable "tls_key" {
  type      = string
  sensitive = true
  default   = ""
}

# ── RKE2 / Rancher versions ───────────────────────────────────────────────────
variable "rke2_version" {
  type    = string
  default = "v1.34.7+rke2r1"
}

variable "rancher_version" {
  type    = string
  default = ""
}

variable "dns_servers" {
  type        = list(string)
  description = "DNS servers for cluster nodes. Written as nameservers in netplan (static nodes) and as DNS= in systemd-resolved (DHCP nodes). 8.8.8.8 is always appended as FallbackDNS in systemd-resolved. Defaults to empty list (DHCP DNS only for DHCP nodes; 8.8.8.8 for static nodes)."
  default     = []
}

variable "use_rancher_prime" {
  type        = bool
  description = "When true, install from the rancher-prime Helm chart repository instead of rancher-latest."
  default     = false
}
