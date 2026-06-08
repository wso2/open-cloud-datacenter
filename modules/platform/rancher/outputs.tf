locals {
  # When use_metallb = true the Harvester LB resource is not created even though
  # create_lb = true; use ippool_start (the MetalLB VIP) as the canonical IP instead.
  rancher_lb_ip = (
    var.create_lb && !var.use_metallb
    ? harvester_loadbalancer.rancher_lb[0].ip_address
    : var.use_metallb
    ? var.ippool_start
    : var.static_rancher_ip
  )
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

output "vm_image_id" {
  value       = var.image_url != "" ? "${harvester_image.vm_image[0].namespace}/${harvester_image.vm_image[0].name}" : var.ubuntu_image_id
  description = "Harvester image reference (namespace/name) for the OS image downloaded by this module. Re-use in downstream layers to avoid downloading the same image twice."
}
