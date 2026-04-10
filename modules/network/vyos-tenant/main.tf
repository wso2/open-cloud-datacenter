# ── vyos-tenant module ────────────────────────────────────────────────────────
#
# Provisions one tenant VLAN on a running VyOS gateway via the HTTPS REST API.
# One call to this module = one tenant VLAN. Add more tenants by calling it
# again with a different vlan_id.
#
# IPAM:
#   subnet    = cidrsubnet("10.0.0.0/8", 15, vlan_id - 1000)
#   gateway   = cidrhost(subnet, 1)   e.g. 10.0.0.1 for VLAN 1000
#   subnet-id = vlan_id               stable, traceable 1:1 key
#
# Prerequisites:
#   1. VyOS installed from ISO and rebooted (bootstrap module, phase 2)
#   2. VyOS HTTPS API enabled manually post-install:
#        configure
#        set service https api keys id terraform key '<api_key>'
#        set service https api allow-client address <management_cidr>
#        set service https certificates system-generated-certificate
#        commit; save
#   3. VyOS uplink interface (eth0) and default route configured
# ─────────────────────────────────────────────────────────────────────────────

locals {
  # IPAM — cidrsubnet("10.0.0.0/8", 15, vlan_id - 1000)
  # /8 + 15 bits = /23 per tenant; index = vlan_id - 1000
  subnet       = cidrsubnet("10.0.0.0/8", 15, var.vlan_id - 1000)
  gateway_ip   = cidrhost(local.subnet, 1)
  gateway_cidr = "${local.gateway_ip}/${split("/", local.subnet)[1]}"

  # DHCP range — offsets from subnet base
  subnet_base = split("/", local.subnet)[0]

  dhcp_start = cidrhost(local.subnet, var.dhcp_range_start_offset)
  dhcp_stop  = cidrhost(local.subnet, var.dhcp_range_end_offset)

  vlan_label = "VLAN${var.vlan_id}"
  vif_path   = "interfaces ethernet eth1 vif ${var.vlan_id}"
}

# ── VyOS configuration via HTTPS REST API ─────────────────────────────────────

# eth1.vlan_id sub-interface — tenant gateway
resource "vyos_config_block_tree" "vif" {
  path = local.vif_path

  configs = {
    "address"     = local.gateway_cidr
    "description" = "${var.tenant_name}-${local.vlan_label}"
  }
}

# DHCP shared network for this tenant
resource "vyos_config_block_tree" "dhcp" {
  path = "service dhcp-server shared-network-name ${local.vlan_label}"

  configs = {
    # subnet-id equals vlan_id — traceable 1:1 mapping
    "subnet ${local.subnet} subnet-id"                 = tostring(var.vlan_id)
    "subnet ${local.subnet} option default-router"     = local.gateway_ip
    "subnet ${local.subnet} range 0 start"             = local.dhcp_start
    "subnet ${local.subnet} range 0 stop"              = local.dhcp_stop
  }

  depends_on = [vyos_config_block_tree.vif]
}

# DHCP DNS options — all name-server entries in a single resource.
# Each server IP is embedded in the config key so the map keys are unique
# and the provider sees one authoritative config block for this path.
resource "vyos_config_block_tree" "dhcp_dns" {
  path = "service dhcp-server shared-network-name ${local.vlan_label} subnet ${local.subnet} option"

  configs = {
    for srv in var.dns_servers : "name-server ${srv}" => ""
  }

  depends_on = [vyos_config_block_tree.dhcp]
}

# NAT source rule — masquerade tenant traffic out the uplink interface (internet egress)
# Rule number = vlan_id for traceability
resource "vyos_config_block_tree" "nat_egress" {
  path = "nat source rule ${var.vlan_id}"

  configs = {
    "outbound-interface name" = "eth0"
    "source address"          = local.subnet
    "translation address"     = "masquerade"
  }
}

# ── Harvester network resource ────────────────────────────────────────────────
# vlan_id MUST match the VyOS vif tag above — this is what wires L2 frames
# from tenant VMs to the correct VyOS sub-interface.

resource "harvester_network" "tenant" {
  name                 = "${var.tenant_name}-${lower(local.vlan_label)}"
  namespace            = var.network_namespace
  vlan_id              = var.vlan_id
  cluster_network_name = var.cluster_network_name
  route_mode           = "manual"
  route_cidr           = local.subnet
  route_gateway        = local.gateway_ip

  depends_on = [
    vyos_config_block_tree.vif,
    vyos_config_block_tree.dhcp,
  ]
}
