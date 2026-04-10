terraform {
  required_version = ">= 1.5"

  required_providers {
    harvester = {
      source  = "harvester/harvester"
      version = "~> 1.7"
    }
    vyos = {
      source  = "thomasfinstad/vyos-rolling"
      version = "~> 19.0"
    }
  }
}
