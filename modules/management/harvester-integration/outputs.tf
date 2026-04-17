output "harvester_cluster_id" {
  value       = rancher2_cluster.harvester_hci.id
  description = "Rancher cluster ID for the imported Harvester HCI cluster. Use as cluster_id in tenant-space and rbac modules."
}

output "harvester_cluster_name" {
  value       = rancher2_cluster.harvester_hci.name
  description = "Name of the Harvester cluster as registered in Rancher."
}

output "cloud_credential_id" {
  value       = length(rancher2_cloud_credential.harvester) > 0 ? rancher2_cloud_credential.harvester[0].id : null
  description = "Rancher cloud credential ID (cattle-global-data:cc-xxxx) for the Harvester driver. Null when create_cloud_credential = false (brownfield). Pass to k8s-cluster module's cloud_credential_id."
}
