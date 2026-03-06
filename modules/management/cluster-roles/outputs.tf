output "vm_metrics_observer_role_id" {
  value       = rancher2_role_template.vm_metrics_observer.id
  description = "Role template ID for the vm-metrics-observer role. Pass to tenant-space module's member_role_ids."
}
