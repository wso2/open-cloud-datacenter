variable "harvester_namespace" {
  type        = string
  description = "Harvester namespace to manage images within"
  default     = "default"
}

variable "managed_images" {
  type = map(object({
    display_name = string
    url          = string
  }))
  description = "A map of OS images to sync into Harvester"
  default     = {}
}
