variable "harvester_cluster_name" {
  type        = string
  description = "The name of the local Harvester cluster registered in Rancher"
  default     = "local"
}

variable "projects" {
  type = map(object({
    cpu_limit     = string
    memory_limit  = string
    storage_limit = string
  }))
  description = "A map of team names to their resource quotas"
  default     = {}
}
