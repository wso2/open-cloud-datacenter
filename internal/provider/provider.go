// Package provider is the entry point for the DC-API Terraform provider.
// It defines the provider schema (endpoint, token) and registers all resource types.

package provider

import (
	"context"
	"fmt"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
	"terraform-provider-dcapi/internal/resources"
)

// New returns the provider. main.go passes this function to plugin.Serve.

func New() *schema.Provider {
	
	p := &schema.Provider{

		Schema: map[string]*schema.Schema{

			// endpoint accepts the DC-API base URL; falls back to DCAPI_ENDPOINT env var.
			"endpoint": {
				Type:        schema.TypeString,
				Optional:    true,
				DefaultFunc: schema.EnvDefaultFunc("DCAPI_ENDPOINT", nil),
				Description: "DC-API base URL. Falls back to DCAPI_ENDPOINT env var.",
			},

			// token accepts the service account bearer token; falls back to DCAPI_TOKEN env var.
			"token": {
				Type:        schema.TypeString,
				Optional:    true,
				Sensitive:   true,
				DefaultFunc: schema.EnvDefaultFunc("DCAPI_TOKEN", nil),
				Description: "Service account token (dcapi_sa_<id>_<secret>). Falls back to DCAPI_TOKEN env var.",
			},
		},

		// ResourcesMap wires .tf resource type names to factory functions in internal/resources/.
		// State IDs encode the full API path: tenant="slug", project="t/p", vnet="t/p/uuid",
		// subnet="t/p/vnet_uuid/subnet_uuid", vm="t/p/uuid" — Read/Delete use them to rebuild URLs.

		ResourcesMap: map[string]*schema.Resource{
			"dcapi_tenant":           resources.ResourceTenant(),
			"dcapi_project":          resources.ResourceProject(),
			"dcapi_vnet":             resources.ResourceVNet(),
			"dcapi_subnet":           resources.ResourceSubnet(),
			"dcapi_virtual_machine":  resources.ResourceVirtualMachine(),
			"dcapi_service_account":  resources.ResourceServiceAccount(),
			"dcapi_bastion":          resources.ResourceBastion(),
			"dcapi_cluster":                resources.ResourceCluster(),
			"dcapi_node_pool":              resources.ResourceNodePool(),
			"dcapi_route_table":              resources.ResourceRouteTable(),
			"dcapi_route_table_association":  resources.ResourceRouteTableAssociation(),
			"dcapi_network_security_group":   resources.ResourceNetworkSecurityGroup(),
			"dcapi_nsg_attachment":           resources.ResourceNSGAttachment(),
		},

		ConfigureContextFunc: configureProvider,
	}


	return p
}

// configureProvider validates credentials and builds the *DCAPIClient stored as meta.
// Every resource lifecycle function receives this client via the meta interface{} parameter.

func configureProvider(ctx context.Context, d *schema.ResourceData) (interface{}, diag.Diagnostics) {
	
	var diags diag.Diagnostics

	endpoint, _ := d.Get("endpoint").(string)
	token, _ := d.Get("token").(string)

	if endpoint == "" {
		diags = append(diags, diag.Diagnostic{

			Severity: diag.Error,
			Summary:  "Missing required provider configuration: endpoint",
			Detail:   "Set 'endpoint' in the provider block or export DCAPI_ENDPOINT.",
			
		})
	}

	if token == "" {
		diags = append(diags, diag.Diagnostic{
			Severity: diag.Error,
			Summary:  "Missing required provider configuration: token",
			Detail:   "Set 'token' in the provider block or export DCAPI_TOKEN (format: dcapi_sa_<id>_<secret>).",
		})
	}

	if diags.HasError() {
		return nil, diags
	}

	c, err := client.NewClient(endpoint, token)
	
	if err != nil {
		diags = append(diags, diag.Diagnostic{
			Severity: diag.Error,
			Summary:  "Failed to initialise DC-API client",
			Detail:   fmt.Sprintf("Error: %s", err.Error()),
		})
		return nil, diags
	}

	return c, diags
}
