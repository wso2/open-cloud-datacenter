// Package main is the entry point of the Terraform provider binary.
package main

import (
	"github.com/hashicorp/terraform-plugin-sdk/v2/plugin"
	"terraform-provider-dcapi/internal/provider"
)


func main() {

	plugin.Serve(&plugin.ServeOpts{

		ProviderFunc: provider.New,
		// Debug:        true,
	})
}
