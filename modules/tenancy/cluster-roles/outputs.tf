output "project_member_restricted_role_id" {
  value       = rancher2_role_template.project_member_restricted.id
  description = "Role template ID for the project-member-restricted role. Replaces the built-in project-member for Harvester VM tenants. Grants full VM lifecycle management (create/start/stop/console/delete) with VM image access hard-restricted to read-only. Use in tenant-space group_role_bindings instead of the built-in 'project-member' or 'project-owner'."
}

output "project_contributor_role_id" {
  value       = rancher2_role_template.project_contributor.id
  description = "Role template ID for the project-contributor role. Grants namespace and member management within a project without the ability to change resource quotas or delete the project. Pass to tenant-space module's group_role_bindings for infrastructure operators and team leads."
}

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

output "vm_creator_role_id" {
  value       = rancher2_role_template.vm_creator.id
  description = "Role template ID for the vm-creator cluster role. Bind at cluster level alongside a project role (vm-manager or project-owner) to allow VM creation."
}

output "vm_operator_role_id" {
  value       = rancher2_role_template.vm_operator.id
  description = "Role template ID for the vm-operator role. Pass to tenant-space module's group_role_bindings for teams that operate but do not provision VMs."
}

output "cluster_operator_role_id" {
  value       = rancher2_role_template.cluster_operator.id
  description = "Role template ID for the cluster-operator cluster role. Pass to rancher2_cluster_role_template_binding for SREs who scale RKE2 node pools."
}

output "cluster_reader_role_id" {
  value       = rancher2_role_template.cluster_reader.id
  description = "Role template ID for the cluster-reader cluster role. Grants read-only visibility into a downstream cluster (nodes, events, metrics, machines) without any write access. Use in rancher2_cluster_role_template_binding for groups that observe but must not modify clusters."
}

output "cluster_contributor_role_id" {
  value       = rancher2_role_template.cluster_contributor.id
  description = "Role template ID for the cluster-contributor cluster role. Grants full workload/config management via kubectl (exec, logs, deploy, scale) without Rancher management-plane permissions. Use for SRE groups that operate downstream clusters but must not delete or reconfigure them."
}
