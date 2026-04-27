terraform {
  required_version = ">= 1.7"
  required_providers {
    kubernetes = {
      source                = "hashicorp/kubernetes"
      version               = "~> 2.35"
      configuration_aliases = [kubernetes.harvester]
    }
  }
}
