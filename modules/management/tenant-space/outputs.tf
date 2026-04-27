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

output "network_names" {
  value       = { for id, r in harvester_network.tenant : id => "${r.namespace}/${r.name}" }
  description = "Map of VLAN ID (string) → full harvester_network reference (<namespace>/<name>) for attaching tenant VMs. Empty map when vlan_id is null or empty."
}

output "subnet_cidr" {
  value       = local.tenant_subnet
  description = "Tenant /23 subnet CIDR (e.g. 10.0.0.0/23). Non-null only when vlan_id and vyos_endpoint are both set."
}

output "gateway_ip" {
  value       = local.tenant_gateway
  description = "VyOS gateway IP for this tenant (first host in subnet_cidr). Non-null only when vlan_id and vyos_endpoint are both set."
}

output "vm_access_kubeconfig" {
  value       = local.vm_access_kubeconfig
  sensitive   = true
  description = "Namespace-scoped Harvester kubeconfig for the tenant team. Non-null when expose_vm_kubeconfig = true and the namespace-credential-provisioner has already created the secret. Hand to the tenant team once at onboarding. See examples/consumer-workloads in wso2-datacenter-project for usage."
}
