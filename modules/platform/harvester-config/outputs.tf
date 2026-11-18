output "namespace" {
  value       = var.harvester_namespace
  description = "Harvester namespace holding the VM network and image"
  # Lets callers order on the namespace's creation, not just its name.
  depends_on = [kubernetes_namespace.harvester_ns]
}

output "vm_network_name" {
  value       = var.create_vm_network ? "${harvester_network.vm_network[0].namespace}/${harvester_network.vm_network[0].name}" : ""
  description = "VM network reference ('<namespace>/<name>') for VM network_interface blocks; empty when create_vm_network = false"
}

output "vm_image_id" {
  value       = var.image_url != "" ? "${harvester_image.vm_image[0].namespace}/${harvester_image.vm_image[0].name}" : ""
  description = "Harvester image reference (namespace/name) — pass to platform/rancher as ubuntu_image_id"
}

output "storage_class_name" {
  value       = var.manage_storage_class ? harvester_storageclass.default[0].name : ""
  description = "Name of the cluster-default Longhorn StorageClass; empty when manage_storage_class = false"
}

output "rwx_storage_class_name" {
  value       = var.manage_storage_class ? harvester_storageclass.longhorn_rwx[0].name : ""
  description = "Name of the RWX Longhorn StorageClass; empty when manage_storage_class = false"
}
