terraform {
  required_version = ">= 1.5"
  required_providers {
    rancher2 = {
      source  = "rancher/rancher2"
      version = "~> 13.1"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.0"
    }
  }
}
