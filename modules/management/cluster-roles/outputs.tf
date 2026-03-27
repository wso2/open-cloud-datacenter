output "vm_manager_role_id" {
  value       = rancher2_role_template.vm_manager.id
  description = "Role template ID for the vm-manager role. Pass to tenant-space module's group_role_bindings."
}

output "vm_metrics_observer_role_id" {
  value       = rancher2_role_template.vm_metrics_observer.id
  description = "Role template ID for the vm-metrics-observer role. Pass to tenant-space module's group_role_bindings."
}

output "network_manager_role_id" {
  value       = rancher2_role_template.network_manager.id
  description = "Role template ID for the network-manager cluster role. Pass to rancher2_cluster_role_template_binding for the DC ops OIDC group."
}
