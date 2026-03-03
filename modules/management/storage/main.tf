terraform {
  required_providers {
    harvester = {
      source  = "harvester/harvester"
      version = "~> 0.6.0"
    }
  }
}

resource "harvester_image" "base_images" {
  for_each = var.managed_images

  name      = each.key
  namespace = var.harvester_namespace

  display_name = each.value.display_name
  source_type  = "download"
  url          = each.value.url
}
