terraform {
  required_version = ">= 1.3"
  required_providers {
    harvester = {
      source  = "harvester/harvester"
      version = "~> 1.7"
    }
  }
}
