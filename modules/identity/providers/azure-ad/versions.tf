terraform {
  required_providers {
    # hashicorp/azuread ~> 2.0 will be required once app registration
    # resources are implemented. No provider needed for the current
    # "bring your own app" mode.
  }
}
