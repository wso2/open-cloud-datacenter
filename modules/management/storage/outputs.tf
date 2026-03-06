# Image IDs keyed by the same names passed to managed_images.
# Consumers reference images via remote state:
#
#   data.terraform_remote_state.management.outputs.image_ids["ubuntu-22-04"]
#
# The value is the Harvester image resource path (namespace/name) accepted by
# harvester_virtualmachine.disk.image and the k8s-cluster module's harvester_image_name.
output "image_ids" {
  value       = { for k, img in harvester_image.base_images : k => img.id }
  description = "Map of image key → Harvester image ID (namespace/name). Use the same keys passed to managed_images."
}
