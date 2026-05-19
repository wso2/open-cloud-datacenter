variable "cluster_name" {
  type        = string
  description = "Name of the RKE2 cluster for the control plane (e.g. 'dcapi-controlplane-rke2')."
}

variable "project_name" {
  type        = string
  description = "Rancher project name. Also used as the VM namespace and NAD namespace."
  default     = "dc-api"
}

variable "harvester_cluster_id" {
  type        = string
  description = "Rancher cluster ID of the Harvester HCI cluster."
}

variable "cloud_credential_id" {
  type        = string
  description = "Rancher cloud credential ID for provisioning VMs on Harvester."
}

variable "kubernetes_version" {
  type        = string
  description = "RKE2 version string (e.g. 'v1.33.10+rke2r3')."
}

variable "mgmt_cluster_network" {
  type        = string
  description = "Harvester cluster network for the management NAD."
  default     = "mgmt"
}

# ── Node count + per-node IPs ────────────────────────────────────────────────
variable "node_count" {
  type        = number
  description = "Number of control-plane nodes. Must be 1, 3 or 5 to form an etcd quorum. 3 is the recommended HA default."
  default     = 3

  validation {
    condition     = contains([1, 3, 5], var.node_count)
    error_message = "node_count must be 1, 3 or 5 (etcd quorum requirement)."
  }
}

variable "node_ips" {
  type        = list(string)
  description = "Per-node static management IPs. List length must equal node_count. Order is significant — index N maps to pool 'node{N+1}'."

  validation {
    condition     = length(var.node_ips) == length(distinct(var.node_ips))
    error_message = "node_ips entries must be unique."
  }
}

variable "node_mgmt_cidr_suffix" {
  type        = number
  description = "Prefix length for each node's static IP (e.g. 24 for /24)."
  default     = 24
}

variable "node_default_gateway" {
  type        = string
  description = "Default gateway for the management network."
}

variable "node_dns_servers" {
  type        = list(string)
  description = "DNS servers written into each node's netplan."
  default     = ["8.8.8.8", "1.1.1.1"]
}

variable "node_interface_name" {
  type        = string
  description = "Network interface name inside the VM that the static IP is pinned to. enp1s0 is the Harvester default for the first NIC."
  default     = "enp1s0"
}

# ── Node sizing ──────────────────────────────────────────────────────────────
variable "node_cpu_count" {
  type        = string
  description = "vCPU count per node."
  default     = "4"
}

variable "node_memory_size" {
  type        = string
  description = "Memory in GiB per node (as a string, e.g. '8')."
  default     = "8"
}

variable "node_disk_size" {
  type        = number
  description = "Root disk size in GiB per node."
  default     = 40
}

variable "node_image_name" {
  type        = string
  description = "Harvester VM image reference ('namespace/image-id')."
}

# ── Cloud-init knobs ─────────────────────────────────────────────────────────
variable "node_password" {
  type        = string
  sensitive   = true
  description = "Password for the ssh_user account on every node."
}

variable "ssh_user" {
  type        = string
  description = "Linux user that cloud-init creates and that the node_password / ssh_authorized_keys apply to."
  default     = "ubuntu"
}

variable "ssh_authorized_keys" {
  type        = list(string)
  description = "SSH public keys injected into ssh_user on every node."
  default     = []
}

variable "ntp_server" {
  type        = string
  description = "NTP server written into /etc/systemd/timesyncd.conf on every node."
  default     = "time.cloudflare.com"
}

# ── VIPs ─────────────────────────────────────────────────────────────────────
variable "api_vip" {
  type        = string
  description = "Virtual IP for the Kubernetes API server. Served by kube-vip running as a static pod on each control-plane node (ARP mode, leader-elected). DigiOps must reserve this IP on the management VLAN out-of-band — it is NOT allocated from the Harvester IPPool."
}

variable "ingress_vip" {
  type        = string
  description = "Single VIP for Service type=LoadBalancer. Used by the dc-api ingress. Allocated from the Harvester IPPool created by this module."
}

# ── LoadBalancer IPPool ──────────────────────────────────────────────────────
variable "lb_subnet" {
  type        = string
  description = "Subnet CIDR containing the ingress VIP."
}

variable "lb_gateway" {
  type        = string
  description = "Default gateway for the LB subnet."
}

# ── kube-vip ─────────────────────────────────────────────────────────────────
variable "kube_vip_image" {
  type        = string
  description = "kube-vip container image used for the apiserver VIP static pod."
  default     = "ghcr.io/kube-vip/kube-vip:v0.8.7"
}

# ── RKE2 machine config ──────────────────────────────────────────────────────
variable "manage_rke_config" {
  type        = bool
  description = "When true, Terraform manages the full RKE2 machine configuration."
  default     = true
}

variable "machine_global_config" {
  type        = string
  description = "Full machine_global_config YAML for the cluster. When null (the default), the module generates a config that: (a) sets cni=cilium, (b) adds the API VIP and ingress hostnames to tls-san so the apiserver cert is valid for client connections via the VIP, and (c) extends kube-apiserver etcd healthcheck + kube-controller-manager/kube-scheduler leader-election timeouts to tolerate Longhorn-backed disk fsync latency."
  default     = null
}

variable "tls_san_extra" {
  type        = list(string)
  description = "Extra hostnames/IPs to add to the apiserver tls-san list (e.g. an external DNS name fronting the API VIP). The API VIP itself is added automatically."
  default     = []
}

variable "harvester_kubeconfig_path" {
  type        = string
  description = "Path to the Harvester kubeconfig file. Used to run the kubectl patch that sets the IPPool selector.scope after cluster creation."
}
