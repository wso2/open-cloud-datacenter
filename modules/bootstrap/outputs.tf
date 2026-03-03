output "rancher_hostname" {
  value       = var.rancher_hostname
  description = "The FQDN of the bootstrapped Rancher server"
}

output "rancher_lb_ip" {
  value       = harvester_loadbalancer.rancher_lb.ip_address
  description = "The IP address of the LoadBalancer exposing Rancher"
}
