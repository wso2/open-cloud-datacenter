# ── VyOS connection ───────────────────────────────────────────────────────────

variable "vyos_endpoint" {
  type        = string
  description = "VyOS HTTPS API endpoint, e.g. 'https://172.22.100.50'."
}

variable "vyos_api_key" {
  type        = string
  description = "VyOS HTTPS API key (set during post-install manual config)."
  sensitive   = true
}

# ── Tenant identity ───────────────────────────────────────────────────────────

variable "tenant_name" {
  type        = string
  description = "Short tenant name used for DHCP shared-network label, e.g. 'tenant-a'."
}

variable "vlan_id" {
  type        = number
  description = <<-EOT
    VLAN ID for this tenant (1000–2999). Determines:
      - VyOS sub-interface: eth1 vif <vlan_id>
      - Subnet: cidrsubnet("10.0.0.0/8", 15, vlan_id - 1000)
      - Kea DHCP subnet-id: vlan_id
      - NAT source rule number: vlan_id
    Must match the harvester_network vlan_id for the tenant exactly.
  EOT

  validation {
    condition     = var.vlan_id >= 1000 && var.vlan_id <= 2999
    error_message = "vlan_id must be between 1000 and 2999."
  }
}

# ── Network config ────────────────────────────────────────────────────────────

variable "dhcp_range_start_offset" {
  type        = number
  description = "Offset from the subnet base for the DHCP pool start address. Default 10 → 10.0.x.10."
  default     = 10

  validation {
    condition     = var.dhcp_range_start_offset >= 2 && var.dhcp_range_start_offset <= 510
    error_message = "dhcp_range_start_offset must be between 2 and 510 (offset 1 is reserved for the gateway)."
  }
}

variable "dhcp_range_end_offset" {
  type        = number
  description = "Offset from the subnet base for the DHCP pool end address. Default 456 → 10.0.x.200 in a /23 (0+456=456 → .1.200 in the second /24)."
  default     = 456

  validation {
    condition     = var.dhcp_range_end_offset >= 2 && var.dhcp_range_end_offset <= 510
    error_message = "dhcp_range_end_offset must be between 2 and 510."
  }
}

variable "dns_servers" {
  type        = list(string)
  description = "DNS servers to hand out via DHCP."
  default     = ["8.8.8.8", "8.8.4.4"]
}
