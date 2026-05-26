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
  description = "Harvester VM image reference ('namespace/image-id') to clone the VM root disks from. Required when create_local_storage_class is false (operator provides a pre-existing image). When create_local_storage_class is true the module creates its OWN image in the project namespace (with the local-fast SC baked in) and this variable is ignored — pass an empty string."
  default     = ""
}

variable "node_image_url" {
  type        = string
  description = "Download URL for the cloud image the module bakes into its own VirtualMachineImage when create_local_storage_class is true. Defaults to the upstream Ubuntu 22.04 jammy cloud image. Ignored when create_local_storage_class is false."
  default     = "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img"
}

variable "node_image_display_name" {
  type        = string
  description = "Friendly display name for the VirtualMachineImage when the module creates one. Ignored when create_local_storage_class is false."
  default     = "DC-API Control-Plane Ubuntu 22.04"
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

# ── VM root-disk storage ─────────────────────────────────────────────────────
# By default, this module creates a single-replica Longhorn StorageClass with
# dataLocality=best-effort on the Harvester host cluster and points the
# control-plane VM root disks at it. best-effort keeps a replica co-located
# with the VM's node whenever possible (low fsync latency for etcd) but
# permits Longhorn to migrate the volume to another node on host failure so
# the VM can restart elsewhere. The fsync-latency drop versus the default
# 2-replica SC is 5-10× — necessary for etcd quorum to bootstrap and stay
# healthy on slow or overloaded host storage. The trade is reduced
# per-disk redundancy (1 replica vs 2); etcd quorum at the application layer
# is what compensates.
#
# For environments with fast underlying storage (good NVMe + spare IOPS),
# set create_local_storage_class = false to fall back to the host cluster's
# default StorageClass and recover full per-disk replication.
#
# In-cluster PVCs (e.g. dc-postgres) are unaffected — they continue to use
# the downstream cluster's default StorageClass, which the Harvester CSI
# driver translates to the host's default (replicated) SC.
variable "create_local_storage_class" {
  type        = bool
  description = "When true, create a single-replica Longhorn StorageClass with dataLocality=best-effort on the Harvester host cluster for the control-plane VM root disks. Recommended for dev / slow-disk environments where etcd fsync latency is the bottleneck. Set false in production when host storage has adequate IOPS for the default 2-replica SC."
  default     = true
}

variable "local_storage_class_name" {
  type        = string
  description = "Name of the single-replica, best-effort-local StorageClass created on the Harvester host cluster when create_local_storage_class = true."
  default     = "dcapi-controlplane-local"
}

variable "vm_storage_class_override" {
  type        = string
  description = "Explicit StorageClass name for the control-plane VM root disks. Takes precedence over create_local_storage_class — use this to point at a pre-existing custom SC on the Harvester cluster. Empty string defers to the create_local_storage_class logic."
  default     = ""
}

# Cross-variable consistency check: node_count and node_ips must agree, since
# per-node cloud-init + machine pools are built from node_ips and a mismatch
# would silently provision the wrong number of nodes. Uses a `check` block
# (Terraform 1.5+) because the `validation` block didn't gain cross-variable
# refs until Terraform 1.9 and required_version here is >= 1.7.
check "node_count_matches_node_ips" {
  assert {
    condition     = length(var.node_ips) == var.node_count
    error_message = "length(node_ips) (${length(var.node_ips)}) must equal node_count (${var.node_count}). The module derives both cloud-init and machine pools from node_ips; a mismatch would silently change the provisioned cluster size."
  }
}
