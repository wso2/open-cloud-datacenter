output "project_id" {
  description = "Rancher project ID of the provisioned tenant space."
  value       = module.tenant_space.project_id
}

output "project_name" {
  description = "Rancher project name of the provisioned tenant space."
  value       = module.tenant_space.project_name
}

output "namespace_ids" {
  description = "Map of workload namespace names to Rancher namespace IDs."
  value       = module.tenant_space.namespace_ids
}

output "network_namespace" {
  description = "Name of the namespace containing the tenant VM network."
  value       = module.tenant_space.network_namespace
}

output "network_namespace_id" {
  description = "Rancher namespace ID of the tenant network namespace."
  value       = module.tenant_space.network_namespace_id
}
