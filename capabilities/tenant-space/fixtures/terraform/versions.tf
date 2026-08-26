terraform {
  required_version = "= 1.15.8"

  backend "local" {
    path = "../state/terraform.tfstate"
  }

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
      source  = "hashicorp/kubernetes"
      version = "~> 2.35"
    }
  }
}
