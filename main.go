// Package main is the entry point of the Terraform provider binary.
//
// Data flow: Terraform (gRPC) → plugin.Serve (main.go) → provider.New (provider.go)
// → configureProvider builds *DCAPIClient → injected as meta into resource CRUD functions
// (resources/*.go) → each CRUD calls client/*.go methods → DC-API REST endpoints.
// Resource hierarchy enforced by URL paths: tenant → project → vnet → subnet;
// VMs sit directly under project but reference vnet/subnet UUIDs in VPC networking mode.
package main

import (
	"github.com/hashicorp/terraform-plugin-sdk/v2/plugin"
	"terraform-provider-dcapi/internal/provider"
)

func main() {
	plugin.Serve(&plugin.ServeOpts{
		ProviderFunc: provider.New,
	})
}
