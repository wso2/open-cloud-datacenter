output "rancher_url" {
  value       = "https://${var.rancher_hostname}"
  description = "URL of the installed Rancher server"
}

output "rancher_hostname" {
  value       = var.rancher_hostname
  description = "FQDN Rancher is served on"
}

output "chart_repository" {
  value       = local.chart_repository
  description = "Helm repository the release was installed from (Prime or the community channel)"
}

output "rancher_version" {
  value       = helm_release.rancher.version
  description = "Rancher chart version currently installed"
}

output "rancher_namespace" {
  value       = kubernetes_namespace_v1.cattle_system.metadata[0].name
  description = "Namespace Rancher is installed into"
}

output "release_status" {
  value       = helm_release.rancher.status
  description = "Status of the Rancher Helm release"
}
