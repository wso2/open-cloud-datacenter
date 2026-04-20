terraform {
  required_version = ">= 1.5"

  required_providers {
    vyos = {
      source  = "hiranadikari/vyos"
      version = "~> 0.1"
    }
  }
}
