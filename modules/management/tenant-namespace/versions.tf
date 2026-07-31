terraform {
  required_version = ">= 1.5"
  required_providers {
    rancher2 = {
      source  = "rancher/rancher2"
      version = "~> 13.1"
    }
    harvester = {
      source  = "harvester/harvester"
      version = "~> 1.7"
    }
    kubernetes = {
      source                = "hashicorp/kubernetes"
      version               = "~> 2.35"
      configuration_aliases = [kubernetes.harvester]
    }
  }
}
