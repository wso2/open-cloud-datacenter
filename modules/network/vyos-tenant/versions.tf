terraform {
  required_version = ">= 1.5"

  required_providers {
    harvester = {
      source  = "harvester/harvester"
      version = "~> 1.7"
    }
    vyos = {
      source  = "hiranadikari/vyos"
      version = "~> 0.1"
    }
  }
}
