output "vlan_id" {
  value       = var.vlan_id
  description = "VLAN ID for this tenant."
}

output "subnet" {
  value       = local.subnet
  description = "Tenant subnet in CIDR notation, e.g. '10.0.0.0/23'."
}

output "subnet_cidr" {
  value       = local.subnet
  description = "Alias for subnet. Tenant subnet in CIDR notation, e.g. '10.0.0.0/23'."
}

output "gateway_ip" {
  value       = local.gateway_ip
  description = "Tenant gateway IP (VyOS vif address), e.g. '10.0.0.1'."
}

output "dhcp_range" {
  value       = "${local.dhcp_start}-${local.dhcp_stop}"
  description = "DHCP pool range for tenant VMs."
}

output "subnet_cidr" {
  value       = local.subnet
  description = "Alias for subnet. Tenant /23 subnet in CIDR notation."
}
