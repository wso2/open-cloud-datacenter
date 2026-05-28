output "kv_namespace" {
  value       = kubernetes_namespace.keyvault_system.metadata[0].name
  description = "Namespace the keyvault-operator controller is deployed into."
}

output "kv_deployment_name" {
  value       = kubernetes_deployment.keyvault_controller_manager.metadata[0].name
  description = "Name of the keyvault-operator controller-manager Deployment."
}

output "kv_image" {
  value       = "${var.kv_image}:${var.kv_image_tag}"
  description = "Fully-qualified image reference (registry/image:tag) used by the keyvault-operator Deployment."
}
