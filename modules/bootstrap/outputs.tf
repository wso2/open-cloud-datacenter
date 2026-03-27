locals {
  rancher_lb_ip = var.create_lb ? harvester_loadbalancer.rancher_lb[0].ip_address : var.static_rancher_ip
}

output "rancher_hostname" {
  value       = var.rancher_hostname
  description = "FQDN of the Rancher server"
}

output "rancher_lb_ip" {
  value       = local.rancher_lb_ip
  description = "IP used to reach Rancher: LoadBalancer IP (greenfield) or bridge VM IP (brownfield)"
}

output "vm_id" {
  value       = harvester_virtualmachine.rancher_server[0].id
  description = "Harvester resource ID of the Rancher server VM (namespace/name)"
}
