output "namespaces" {
  description = "Map of created rancher2_namespace resources keyed by namespace name."
  value       = rancher2_namespace.this
}

output "namespace_ids" {
  description = "Map of created namespace IDs."
  value       = { for k, v in rancher2_namespace.this : k => v.id }
}
