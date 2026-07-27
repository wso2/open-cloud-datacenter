// Terraform data source definition for dcapi_vnet.
// Looks up a VNet that is not managed by this Terraform config, by name, since its API
// UUID is opaque and unknown to the caller ahead of time.
package datasources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// DataSourceVNet returns the schema.Resource for "dcapi_vnet" data source lookups.
func DataSourceVNet() *schema.Resource {
	return &schema.Resource{
		ReadContext: dataSourceVNetRead,

		Schema: map[string]*schema.Schema{

			"tenant_id": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "Slug of the parent tenant.",
			},
			"project_id": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "Slug of the parent project.",
			},
			"name": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "Name of the VNet to look up.",
			},

			"vnet_uuid": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "API-assigned UUID of this VNet. Use this (not id) when setting vnet_id on child resources such as dcapi_subnet.",
			},
			"address_space": {
				Type:        schema.TypeList,
				Computed:    true,
				Elem:        &schema.Schema{Type: schema.TypeString},
				Description: "List of RFC1918 CIDRs this VNet owns.",
			},
			"region": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "DC-API region slug this VNet was provisioned in.",
			},
			"description": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Free-text note for this VNet.",
			},
			"status": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Lifecycle status: PENDING | ACTIVE | FAILED.",
			},
			"provider_type": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Underlying network fabric (e.g. \"kubeovn\").",
			},
			"message": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Human-readable status message.",
			},
			"created_at": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "RFC3339 creation timestamp.",
			},
			"updated_at": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "RFC3339 last-updated timestamp.",
			},
		},
	}
}

func dataSourceVNetRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)
	tenantID := d.Get("tenant_id").(string)
	projectID := d.Get("project_id").(string)
	name := d.Get("name").(string)

	vnets, err := c.ListVNets(ctx, tenantID, projectID)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error listing VNets in project %q: %w", projectID, err))
	}

	for _, vnet := range vnets {
		if vnet.Name != name {
			continue
		}

		d.SetId(fmt.Sprintf("%s/%s/%s", tenantID, projectID, vnet.ID))

		var diags diag.Diagnostics
		diags = appendSet(diags, d, "vnet_uuid", vnet.ID)
		diags = appendSet(diags, d, "address_space", vnet.AddressSpace)
		diags = appendSet(diags, d, "region", vnet.Region)
		diags = appendSet(diags, d, "description", vnet.Description)
		diags = appendSet(diags, d, "status", vnet.Status)
		diags = appendSet(diags, d, "provider_type", vnet.ProviderType)
		diags = appendSet(diags, d, "message", vnet.Message)
		diags = appendSet(diags, d, "created_at", vnet.CreatedAt)
		diags = appendSet(diags, d, "updated_at", vnet.UpdatedAt)
		return diags
	}

	return diag.Errorf("no VNet named %q found in tenant %q, project %q", name, tenantID, projectID)
}
