output "namespace" {
  description = "Kubernetes namespace where the operator's Deployment + RBAC live."
  value       = kubernetes_namespace.operator.metadata[0].name
}

output "deployment_name" {
  description = "Name of the operator manager Deployment. Useful for kubectl rollout commands."
  value       = kubernetes_deployment.manager.metadata[0].name
}

output "service_account_name" {
  description = "ServiceAccount the manager pod runs as. Useful for RoleBindings in tenant namespaces."
  value       = kubernetes_service_account.manager.metadata[0].name
}

output "metrics_service" {
  description = "ClusterIP Service exposing the manager's /metrics endpoint on :8443. Wire a Prometheus scrape job at <namespace>/<this-service-name>:8443/metrics."
  value = {
    namespace = kubernetes_namespace.operator.metadata[0].name
    name      = kubernetes_service.metrics.metadata[0].name
    port      = 8443
  }
}

output "crd_groups" {
  description = "CRD api group + plurals the operator owns. Consumers can use this to wait on the CRDs before creating CRs."
  value = {
    group   = "keyvault.opencloud.wso2.com"
    plurals = ["keyvaultbackends", "keyvaultinstances"]
  }
}
