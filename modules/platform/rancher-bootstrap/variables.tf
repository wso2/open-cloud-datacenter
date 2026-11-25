# ── Placement ─────────────────────────────────────────────────────────────────
variable "harvester_namespace" {
  type        = string
  description = "Existing Harvester namespace the VMs are created in — platform/harvester-config's namespace output. Not created here."
  default     = "default"
}

variable "vm_name_prefix" {
  type        = string
  description = "Name prefix for the node VMs, cloud-init secrets and SSH key. Each VM is named '<prefix>-<pool>-<index>'."
  default     = "rancher"
}

variable "image_id" {
  type        = string
  description = "Harvester image reference ('<namespace>/<name>') the nodes boot from — platform/harvester-config's vm_image_id output."
}

variable "network_name" {
  type        = string
  description = "Full '<namespace>/<name>' reference of the VM network the nodes attach to — platform/harvester-config's vm_network_name output."
}

variable "network_interface_name" {
  type        = string
  description = "Name of the network interface inside the VM spec."
  default     = "nic-1"
}

# ── Node topology ─────────────────────────────────────────────────────────────
variable "node_pools" {
  type = list(object({
    name          = string
    ips           = list(string)
    control_plane = optional(bool, true)
    etcd          = optional(bool, true)
    worker        = optional(bool, true)
  }))
  description = "Node pools, one VM per entry in ips. A node with control_plane or etcd runs rke2-server; the rest join as agents. The first server bootstraps etcd."

  validation {
    condition     = length(var.node_pools) > 0
    error_message = "At least one node pool is required."
  }

  validation {
    condition     = alltrue([for p in var.node_pools : length(p.ips) > 0])
    error_message = "Every node pool must list at least one IP."
  }

  validation {
    condition     = anytrue([for p in var.node_pools : coalesce(p.control_plane, true) || coalesce(p.etcd, true)])
    error_message = "At least one node must carry control_plane or etcd; otherwise no node runs rke2-server and the cluster has no API."
  }

  validation {
    condition     = length(flatten([for p in var.node_pools : p.ips])) == length(distinct(flatten([for p in var.node_pools : p.ips])))
    error_message = "node_pools contains a duplicate node IP."
  }
}

variable "node_gateway" {
  type        = string
  description = "Default gateway for the node static IPs."
}

variable "node_subnet_prefix" {
  type        = number
  description = "CIDR prefix length of the node network (e.g. 25 for a /25)."
  default     = 24
}

variable "dns_servers" {
  type        = list(string)
  description = "DNS servers written into netplan and systemd-resolved. Defaults to 8.8.8.8 inside the template when empty."
  default     = []
}

# ── Cluster endpoint ──────────────────────────────────────────────────────────
variable "kube_api_host" {
  type        = string
  description = "Address clients and joining nodes reach the cluster on — the static IP of the TCP load balancer fronting 6443/9345 (platform/nginx-lb-vm's static_ip). Also a certificate SAN, and the server address of the kubeconfig published on each server node."

  validation {
    condition     = var.kube_api_host != ""
    error_message = "kube_api_host is required: it is the join address and the apiserver certificate SAN."
  }
}

variable "extra_tls_sans" {
  type        = list(string)
  description = "Additional apiserver certificate SANs (e.g. a DNS name for the cluster endpoint). The LB address and every node IP are included automatically."
  default     = []
}

# ── VM hardware ───────────────────────────────────────────────────────────────
variable "vm_cpu" {
  type        = number
  description = "vCPU count per node."
  default     = 8
}

variable "vm_memory" {
  type        = string
  description = "Memory per node (e.g. '16Gi')."
  default     = "16Gi"
}

variable "vm_disk_name" {
  type        = string
  description = "Name of the root disk in the VM spec."
  default     = "disk-0"
}

variable "vm_disk_size" {
  type        = string
  description = "Root disk size per node (e.g. '40Gi')."
  default     = "40Gi"
}

variable "vm_disk_auto_delete" {
  type        = bool
  description = "Delete the root disk when the VM is destroyed. Set false for production nodes."
  default     = true
}

variable "enable_usb_tablet" {
  type        = bool
  description = "Attach a USB tablet input device for correct console cursor behaviour."
  default     = true
}

# ── Credentials ───────────────────────────────────────────────────────────────
variable "vm_password" {
  type        = string
  description = "Password for the 'ubuntu' user on each node."
  sensitive   = true
}

variable "create_ssh_key" {
  type        = bool
  description = "Generate an RSA key pair and register it as a Harvester SSH key. Needed to copy the admin kubeconfig off the init node. Set false to attach ssh_key_ids instead."
  default     = true
}

variable "ssh_key_ids" {
  type        = list(string)
  description = "Existing Harvester SSH key IDs to attach when create_ssh_key = false."
  default     = []

  validation {
    condition     = var.create_ssh_key || length(var.ssh_key_ids) > 0
    error_message = "ssh_key_ids is required when create_ssh_key = false, otherwise the nodes are unreachable."
  }
}

# ── RKE2 ──────────────────────────────────────────────────────────────────────
variable "rke2_version" {
  type        = string
  description = "RKE2 version to install, as INSTALL_RKE2_VERSION expects it (e.g. 'v1.30.5+rke2r1'). Pin this — an unpinned install is not reproducible."

  validation {
    condition     = can(regex("^v[0-9]+\\.[0-9]+\\.[0-9]+\\+rke2r[0-9]+$", var.rke2_version))
    error_message = "rke2_version must be a pinned RKE2 release, e.g. 'v1.30.5+rke2r1'."
  }
}

variable "ingress_controller" {
  type        = list(string)
  description = <<-EOT
    RKE2 ingress controllers to deploy, written to the servers' config.yaml as
    `ingress-controller`. The FIRST entry becomes the cluster's default ingress
    class. Valid entries: "traefik", "ingress-nginx", "none".

    Defaults to ["traefik"]. Ingress NGINX reached end-of-life in March 2026;
    RKE2 v1.36 makes Traefik the default for new clusters and v1.37 drops
    standalone ingress-nginx support. ["ingress-nginx", "traefik"] is the
    documented dual-ingress setup used when migrating an existing cluster.

    This is applied through cloud-init, which only runs on first boot — changing
    it does not reconfigure running nodes. Switching an existing cluster means
    either rebuilding the VMs or following RKE2's phased ingress migration.
  EOT
  default     = ["traefik"]

  validation {
    condition     = length(var.ingress_controller) > 0
    error_message = "ingress_controller must list at least one controller; use [\"none\"] to deploy none."
  }

  validation {
    condition = length(setsubtract(
      toset(var.ingress_controller), toset(["traefik", "ingress-nginx", "none"])
    )) == 0
    error_message = "ingress_controller entries must each be one of: traefik, ingress-nginx, none."
  }

  validation {
    condition     = length(distinct(var.ingress_controller)) == length(var.ingress_controller)
    error_message = "ingress_controller must not repeat a controller."
  }

  validation {
    condition     = !contains(var.ingress_controller, "none") || length(var.ingress_controller) == 1
    error_message = "ingress_controller = [\"none\"] cannot be combined with another controller."
  }
}

variable "extra_server_config" {
  type        = any
  description = "Additional keys merged into /etc/rancher/rke2/config.yaml on server nodes (e.g. { disable = [\"rke2-ingress-nginx\"] })."
  default     = {}
}

variable "extra_agent_config" {
  type        = any
  description = "Additional keys merged into /etc/rancher/rke2/config.yaml on agent nodes."
  default     = {}
}
