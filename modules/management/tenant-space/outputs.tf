output "project_id" {
  value       = rancher2_project.this.id
  description = "Rancher project ID for this tenant space."
}

output "project_name" {
  value       = rancher2_project.this.name
  description = "Rancher project name."
}

output "namespace_id" {
  value       = rancher2_namespace.this.id
  description = "ID of the primary namespace created within the project."
}

output "namespace_name" {
  value       = rancher2_namespace.this.name
  description = "Name of the primary namespace."
}
