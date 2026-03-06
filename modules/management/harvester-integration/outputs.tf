output "harvester_cluster_id" {
  value       = rancher2_cluster.harvester_hci.id
  description = "Rancher cluster ID for the imported Harvester HCI cluster. Use as cluster_id in tenant-space and rbac modules."
}

output "harvester_cluster_name" {
  value       = rancher2_cluster.harvester_hci.name
  description = "Name of the Harvester cluster as registered in Rancher."
}
