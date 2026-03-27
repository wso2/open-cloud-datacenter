terraform {
  required_version = ">= 1.5"
  required_providers {
    harvester = {
      source  = "harvester/harvester"
      version = "~> 1.7"
    }
    tls = {
      source  = "hashicorp/tls"
      version = "~> 4.0"
    }
  }
}

# ── SSH key pair (greenfield only) ────────────────────────────────────────────
# Set create_ssh_key = false to attach existing ssh_key_ids instead.
resource "tls_private_key" "bootstrap_key" {
  count     = var.create_ssh_key ? 1 : 0
  algorithm = "RSA"
  rsa_bits  = 4096
}

resource "harvester_ssh_key" "bootstrap_key" {
  count      = var.create_ssh_key ? 1 : 0
  name       = "${var.vm_name}-ssh-key"
  namespace  = var.harvester_namespace
  public_key = tls_private_key.bootstrap_key[0].public_key_openssh
}

# ── Cloud-init secret (greenfield only) ──────────────────────────────────────
# Set create_cloudinit_secret = false and provide existing_cloudinit_secret_name instead.
resource "harvester_cloudinit_secret" "cloudinit" {
  count     = var.create_cloudinit_secret ? var.node_count : 0
  name      = var.node_count > 1 ? "${var.vm_name}-cloudinit-${count.index}" : "${var.vm_name}-cloudinit"
  namespace = var.harvester_namespace

  user_data = templatefile("${path.module}/templates/cloud-init.yaml.tpl", {
    password         = var.vm_password
    cluster_dns      = var.rancher_hostname
    rancher_password = var.bootstrap_password
    ssh_public_key   = tls_private_key.bootstrap_key[0].public_key_openssh
    node_index       = count.index
    node_count       = var.node_count
    lb_ip            = var.ippool_start
  })
}

locals {
  # Resolve the SSH key IDs: either freshly generated or caller-supplied
  ssh_key_ids = var.create_ssh_key ? [harvester_ssh_key.bootstrap_key[0].id] : var.ssh_key_ids
}

# ── Input validation ──────────────────────────────────────────────────────────

# The cloud-init template embeds the generated SSH public key. If create_ssh_key
# is false the tls_private_key resource is empty, causing an invalid-index error.
check "ssh_key_required_for_cloudinit" {
  assert {
    condition     = !var.create_cloudinit_secret || var.create_ssh_key
    error_message = "create_ssh_key must be true when create_cloudinit_secret is true (the cloud-init template embeds the generated SSH public key)."
  }
}

# When reusing an existing cloud-init secret, the name must be provided.
check "existing_cloudinit_secret_name_required" {
  assert {
    condition     = var.create_cloudinit_secret || var.existing_cloudinit_secret_name != ""
    error_message = "existing_cloudinit_secret_name is required when create_cloudinit_secret = false."
  }
}

# ── Rancher server VM ─────────────────────────────────────────────────────────
resource "harvester_virtualmachine" "rancher_server" {
  count                = var.node_count
  name                 = var.node_count > 1 ? "${var.vm_name}-${count.index}" : var.vm_name
  namespace            = var.harvester_namespace
  restart_after_update = true

  cpu    = var.vm_cpu
  memory = var.vm_memory

  run_strategy = "RerunOnFailure"
  machine_type = "q35"

  ssh_keys = local.ssh_key_ids

  # Masquerade (NAT): default for greenfield; no external network required
  dynamic "network_interface" {
    for_each = var.network_type == "masquerade" ? [1] : []
    content {
      name = var.network_interface_name
      type = "masquerade"
    }
  }

  # Bridge: for VMs that need direct VLAN access (e.g. existing production VMs)
  dynamic "network_interface" {
    for_each = var.network_type == "bridge" ? [1] : []
    content {
      name         = var.network_interface_name
      type         = "bridge"
      network_name = var.network_name
      mac_address  = var.network_mac_address != "" ? var.network_mac_address : null
    }
  }

  disk {
    name       = var.vm_disk_name
    type       = "disk"
    size       = var.vm_disk_size
    bus        = "virtio"
    boot_order = 1

    image       = var.ubuntu_image_id
    auto_delete = var.vm_disk_auto_delete
  }

  # USB tablet input device — some VMs require this for correct cursor behaviour
  # in the Harvester console; set enable_usb_tablet = true to include it.
  dynamic "input" {
    for_each = var.enable_usb_tablet ? [1] : []
    content {
      name = "tablet"
      type = "tablet"
      bus  = "usb"
    }
  }

  cloudinit {
    user_data_secret_name    = var.create_cloudinit_secret ? harvester_cloudinit_secret.cloudinit[count.index].name : var.existing_cloudinit_secret_name
    network_data_secret_name = var.create_cloudinit_secret ? null : var.existing_cloudinit_secret_name
  }

  provisioner "local-exec" {
    command = var.create_cloudinit_secret ? "echo 'VM created — cloud-init will install RKE2/Rancher internally.'" : "echo 'VM imported — cloud-init ran at initial provision time.'"
  }
}

# ── Load Balancer + IP Pool (greenfield only) ─────────────────────────────────
# Set create_lb = false when the Rancher VM is reachable directly via its
# bridge IP (no dedicated LB/IP-pool needed).
resource "harvester_loadbalancer" "rancher_lb" {
  count     = var.create_lb ? 1 : 0
  name      = "${var.vm_name}-lb"
  namespace = var.harvester_namespace

  depends_on = [
    harvester_virtualmachine.rancher_server,
    harvester_ippool.rancher_ips,
  ]

  workload_type = "vm"
  ipam          = "pool"
  ippool        = harvester_ippool.rancher_ips[0].name

  listener {
    name         = "https"
    port         = 443
    protocol     = "TCP"
    backend_port = 443
  }

  listener {
    name         = "http"
    port         = 80
    protocol     = "TCP"
    backend_port = 80
  }

  backend_selector {
    key    = "harvesterhci.io/vmName"
    values = harvester_virtualmachine.rancher_server[*].name
  }

  healthcheck {
    port              = 443
    success_threshold = 1
    failure_threshold = 3
    period_seconds    = 10
    timeout_seconds   = 5
  }
}

resource "harvester_ippool" "rancher_ips" {
  count = var.create_lb ? 1 : 0
  name  = "${var.vm_name}-ips"

  range {
    start   = var.ippool_start
    end     = var.ippool_end
    subnet  = var.ippool_subnet
    gateway = var.ippool_gateway
  }
}
