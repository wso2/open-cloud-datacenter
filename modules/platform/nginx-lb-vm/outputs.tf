output "vm_id" {
  value       = harvester_virtualmachine.nginx_lb.id
  description = "Harvester resource ID (namespace/name) of the nginx LB VM"
}

output "static_ip" {
  value       = var.static_ip
  description = "Static IP of the nginx LB VM"
}
