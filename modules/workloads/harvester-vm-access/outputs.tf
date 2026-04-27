output "kubeconfig" {
  description = "Kubeconfig YAML for the consumer team. Scoped to their namespace on Harvester — use this to configure the harvester and kubernetes providers in the consumer's Terraform. Handle as a secret: write to a file that is gitignored."
  value       = local.kubeconfig
  sensitive   = true
}

output "service_account_name" {
  description = "ServiceAccount name created in the consumer namespace."
  value       = kubernetes_service_account_v1.consumer.metadata[0].name
}
