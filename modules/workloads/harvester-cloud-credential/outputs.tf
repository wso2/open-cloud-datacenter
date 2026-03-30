output "secret_name" {
  value       = kubernetes_secret.harvesterconfig.metadata[0].name
  description = "Name of the harvesterconfig secret in fleet-default. Pass this to the k8s-cluster module's cloud_provider_config_secret variable."
  depends_on = [
    kubernetes_cluster_role_binding.csi,
    kubernetes_role_binding.cloud_provider,
  ]
}

output "service_account_name" {
  value       = kubernetes_service_account.csi.metadata[0].name
  description = "Name of the ServiceAccount created in the VM namespace on Harvester."
}
