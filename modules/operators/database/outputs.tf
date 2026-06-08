output "db_namespace" {
  value       = kubernetes_namespace.dbaas_system.metadata[0].name
  description = "Namespace the dbaas-operator controller is deployed into."
}

output "db_deployment_name" {
  value       = kubernetes_deployment.dbaas_controller_manager.metadata[0].name
  description = "Name of the dbaas-operator controller-manager Deployment."
}

output "db_image" {
  value       = "${var.db_image}:${var.db_image_tag}"
  description = "Fully-qualified image reference (registry/image:tag) used by the dbaas-operator Deployment."
}
