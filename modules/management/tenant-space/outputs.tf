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
  value       = local.create_net_ns ? rancher2_namespace.network[0].name : null
  description = "Name of the network namespace (<project_name>-net). Non-null when create_network_namespace = true or vlan_id is set."
}

output "network_namespace_id" {
  value       = local.create_net_ns ? rancher2_namespace.network[0].id : null
  description = "Rancher namespace ID of the network namespace. Non-null when create_network_namespace = true or vlan_id is set."
}

output "network_name" {
  value       = var.vlan_id != null ? "${harvester_network.tenant[0].namespace}/${harvester_network.tenant[0].name}" : null
  description = "Full harvester_network reference (<namespace>/<name>) for attaching tenant VMs. Null when vlan_id is not set."
}

output "subnet_cidr" {
  value       = var.vlan_id != null ? local.tenant_subnet : null
  description = "Tenant /23 subnet CIDR (e.g. 10.0.0.0/23). Null when vlan_id is not set."
}

output "gateway_ip" {
  value       = var.vlan_id != null ? local.tenant_gateway : null
  description = "VyOS gateway IP for this tenant. Null when vlan_id is not set."
}
