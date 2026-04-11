output "project_id" {
  value       = rancher2_project.this.id
  description = "Rancher project ID for this tenant space."
}

output "project_name" {
  value       = rancher2_project.this.name
  description = "Rancher project name."
}

output "namespace_ids" {
  value       = { for ns, r in rancher2_namespace.this : ns => r.id }
  description = "Map of namespace name → Rancher namespace ID for each namespace in the project."
}

output "network_namespace" {
  value       = var.vlan_id != null ? rancher2_namespace.network[0].name : null
  description = "Name of the network namespace created for this tenant. Null when vlan_id is not set."
}

output "network_name" {
  value       = var.vlan_id != null ? module.vyos_tenant[0].network_name : null
  description = "Full harvester_network reference (<namespace>/<name>) for use in VM definitions. Null when vlan_id is not set."
}

output "subnet_cidr" {
  value       = var.vlan_id != null ? module.vyos_tenant[0].subnet_cidr : null
  description = "Tenant /23 subnet CIDR. Null when vlan_id is not set."
}

output "gateway_ip" {
  value       = var.vlan_id != null ? module.vyos_tenant[0].gateway_ip : null
  description = "VyOS gateway IP for this tenant. Null when vlan_id is not set."
}
