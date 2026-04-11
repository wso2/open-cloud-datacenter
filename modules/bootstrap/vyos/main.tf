# ── VyOS bootstrap module ─────────────────────────────────────────────────────
#
# Deploys a VyOS gateway VM on Harvester with two NICs:
#   eth0 — uplink to external network (static IP set manually post-install)
#   eth1 — trunk port for tenant VLANs (VyOS manages 802.1Q vif sub-interfaces)
#
# Two-apply workflow required (VyOS only ships as ISO; no cloud-init qcow2):
#
#   Apply 1 (iso_installed = false):
#     - Creates image, trunk network, and VM with ISO CDROM (boot_order=1)
#     - Operator opens Harvester console → logs in → runs 'install image'
#     - Operator reboots the VM
#
#   Apply 2 (iso_installed = true):
#     - Removes the CDROM disk; rootdisk becomes the sole boot device
#     - VM restarts from installed disk
#
#   After Apply 2: use the vyos-tenant module to configure VyOS via REST API.
#
# Note: VyOS does not ship qemu-guest-agent — VM IP will not appear in the
# Harvester UI. Use the static IP set manually on eth0 post-install.
# ─────────────────────────────────────────────────────────────────────────────

locals {
  trunk_network_namespace = coalesce(var.trunk_network_namespace, var.vm_namespace)
  trunk_network_name      = "${var.vm_name}-eth1-trunk"
}

# ── VyOS ISO image ────────────────────────────────────────────────────────────

resource "harvester_image" "vyos" {
  name         = var.image_name
  display_name = "VyOS Rolling"
  namespace    = var.image_namespace
  source_type  = "download"
  url          = var.image_url

  lifecycle {
    # URL changes between nightly builds; don't force image re-download on update
    ignore_changes = [url]
  }
}

# ── eth1 trunk network ────────────────────────────────────────────────────────
# eth1 is a raw bridge port — no VLAN filter. Harvester passes all tagged frames
# to VyOS, which handles 802.1Q sub-interfaces (vif) per tenant VLAN.
#
# route_mode = "manual" is required by the Harvester provider when route_cidr
# is specified. The CIDR (10.0.0.0/8) is informational only; routing is
# handled by VyOS sub-interfaces, not by this NAD.

resource "harvester_network" "eth1_trunk" {
  name                 = local.trunk_network_name
  namespace            = local.trunk_network_namespace
  vlan_id              = 0
  cluster_network_name = var.cluster_network_name
  route_mode           = "manual"
  route_cidr           = "10.0.0.0/8"
  route_gateway        = "0.0.0.0"
}

# ── VyOS gateway VM ───────────────────────────────────────────────────────────

resource "harvester_virtualmachine" "vyos" {
  name                 = var.vm_name
  namespace            = var.vm_namespace
  cpu                  = var.cpu
  memory               = var.memory
  restart_after_update = true
  run_strategy         = "RerunOnFailure"
  machine_type         = "q35"

  # eth0 — uplink NIC. Static IP set manually post-install.
  network_interface {
    name         = "eth0"
    type         = "bridge"
    network_name = var.uplink_network_name
  }

  # eth1 — tenant trunk NIC. VyOS creates 802.1Q vif sub-interfaces per tenant.
  network_interface {
    name         = "eth1"
    type         = "bridge"
    network_name = "${local.trunk_network_namespace}/${harvester_network.eth1_trunk.name}"
  }

  # eth2 — optional management NIC. Attaches VyOS to the Harvester management
  # cluster network so in-cluster processes (e.g. DHCP reconciler pods with
  # hostNetwork) can reach the VyOS HTTPS API at a stable management IP.
  dynamic "network_interface" {
    for_each = var.management_network_name != null ? [1] : []
    content {
      name         = "eth2"
      type         = "bridge"
      network_name = var.management_network_name
    }
  }

  # Root disk — VyOS is installed here via 'install image' from the ISO.
  disk {
    name        = "rootdisk"
    type        = "disk"
    size        = var.disk_size
    bus         = "virtio"
    boot_order  = var.iso_installed ? 1 : 2
    auto_delete = true
  }

  # CDROM — present only before iso_installed = true.
  # After the second apply this disk block disappears, detaching the CDROM.
  dynamic "disk" {
    for_each = var.iso_installed ? [] : [1]
    content {
      name        = "cdrom"
      type        = "cd-rom"
      size        = "1Gi"
      bus         = "sata"
      boot_order  = 1
      image       = harvester_image.vyos.id
      auto_delete = true
    }
  }

  # Harvester may flip auto_delete on the rootdisk after the CDROM is
  # removed, causing a perpetual diff. Ignore only that attribute so all
  # other disk config changes (size, bus, etc.) remain Terraform-managed.
  lifecycle {
    ignore_changes = [disk[0].auto_delete]
  }

  depends_on = [harvester_network.eth1_trunk]
}
