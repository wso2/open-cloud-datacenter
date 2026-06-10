locals {
  rancher_lb_ip = (
    var.create_lb
    ? harvester_loadbalancer.rancher_lb[0].ip_address
    : var.use_nginx_lb
    ? var.nginx_lb_ip
    : var.static_rancher_ip
  )

  # VM ID of the init/primary node for downstream references
  _init_vm_id = (
    local.use_static_nodes
    ? (local._first_cp_key != "" ? harvester_virtualmachine.rancher_server_static[local._first_cp_key].id : "")
    : harvester_virtualmachine.rancher_server[0].id
  )

  _ssh_key_id = var.create_ssh_key ? harvester_ssh_key.bootstrap_key[0].id : (
    length(var.ssh_key_ids) > 0 ? var.ssh_key_ids[0] : ""
  )
}

output "rancher_hostname" {
  value = var.rancher_hostname
}

output "rancher_url" {
  value = "https://${var.rancher_hostname}"
}

output "rancher_lb_ip" {
  value       = local.rancher_lb_ip
  description = "IP used to reach Rancher: Harvester LB, nginx LB, or bridge VM IP"
}

output "vm_id" {
  value       = local._init_vm_id
  description = "Harvester resource ID of the primary Rancher node"
}

output "vm_image_id" {
  value       = var.image_url != "" ? "${harvester_image.vm_image[0].namespace}/${harvester_image.vm_image[0].name}" : var.ubuntu_image_id
  description = "Harvester image reference (namespace/name) — re-use in downstream layers"
}

output "ssh_key_id" {
  value       = local._ssh_key_id
  description = "Harvester SSH key ID attached to the Rancher VMs"
}

output "ssh_public_key" {
  value       = var.create_ssh_key ? tls_private_key.bootstrap_key[0].public_key_openssh : ""
  description = "OpenSSH public key text — pass to nginx_lb_vm so the Harvester webhook can match it against ssh_key_ids"
}

output "node_ips" {
  value       = local.use_static_nodes ? [for n in values(local.static_node_map) : n.ip] : []
  description = "Static IPs of the RKE2 nodes (empty when using DHCP/legacy mode)"
}
