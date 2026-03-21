output "prometheus_rule_storage_name" {
  description = "Name of the storage PrometheusRule CRD."
  value       = kubernetes_manifest.prometheus_rule_storage.manifest.metadata.name
}

output "prometheus_rule_vm_name" {
  description = "Name of the VM PrometheusRule CRD."
  value       = kubernetes_manifest.prometheus_rule_vm.manifest.metadata.name
}

output "prometheus_rule_node_name" {
  description = "Name of the node PrometheusRule CRD."
  value       = kubernetes_manifest.prometheus_rule_node.manifest.metadata.name
}

output "alertmanager_config_name" {
  description = "Name of the Alertmanager base config Secret."
  value       = "alertmanager-rancher-monitoring-alertmanager"
}

output "grafana_dashboard_storage_name" {
  description = "Name of the Grafana storage health dashboard ConfigMap."
  value       = kubernetes_manifest.grafana_dashboard_storage.manifest.metadata.name
}

output "grafana_dashboard_vm_name" {
  description = "Name of the Grafana VM health dashboard ConfigMap."
  value       = kubernetes_manifest.grafana_dashboard_vm.manifest.metadata.name
}

output "grafana_dashboard_node_name" {
  description = "Name of the Grafana node health dashboard ConfigMap."
  value       = kubernetes_manifest.grafana_dashboard_node.manifest.metadata.name
}

output "monitoring_namespace" {
  description = "Namespace where all monitoring resources are deployed."
  value       = var.monitoring_namespace
}
