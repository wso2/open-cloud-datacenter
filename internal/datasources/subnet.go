// Terraform data source definition for dcapi_subnet.
// Looks up a Subnet that is not managed by this Terraform config, by name, since its API
// UUID is opaque and unknown to the caller ahead of time.
package datasources

import (
	"context"
	"fmt"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// DataSourceSubnet returns the schema.Resource for "dcapi_subnet" data source lookups.
func DataSourceSubnet() *schema.Resource {
	return &schema.Resource{
		ReadContext: dataSourceSubnetRead,

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
			"vnet_id": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "UUID of the parent VNet.",
			},
			"name": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "Name of the Subnet to look up.",
			},

			"subnet_uuid": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "API-assigned UUID of this Subnet. Use this (not id) when setting subnet_id on child resources such as dcapi_virtual_machine.",
			},
			"cidr": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "CIDR range of this Subnet.",
			},
			"gateway": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Gateway IP within the CIDR.",
			},
			"description": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Free-text note for this Subnet.",
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

func dataSourceSubnetRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)
	tenantID := d.Get("tenant_id").(string)
	projectID := d.Get("project_id").(string)
	vnetID := d.Get("vnet_id").(string)
	name := d.Get("name").(string)

	subnets, err := c.ListSubnets(ctx, tenantID, projectID, vnetID)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error listing Subnets in VNet %q: %w", vnetID, err))
	}

	for _, subnet := range subnets {
		if subnet.Name != name {
			continue
		}

		d.SetId(fmt.Sprintf("%s/%s/%s/%s", tenantID, projectID, vnetID, subnet.ID))

		var diags diag.Diagnostics
		diags = appendSet(diags, d, "subnet_uuid", subnet.ID)
		diags = appendSet(diags, d, "cidr", subnet.CIDR)
		diags = appendSet(diags, d, "gateway", subnet.Gateway)
		diags = appendSet(diags, d, "description", subnet.Description)
		diags = appendSet(diags, d, "status", subnet.Status)
		diags = appendSet(diags, d, "provider_type", subnet.ProviderType)
		diags = appendSet(diags, d, "message", subnet.Message)
		diags = appendSet(diags, d, "created_at", subnet.CreatedAt)
		diags = appendSet(diags, d, "updated_at", subnet.UpdatedAt)
		return diags
	}

	return diag.Errorf("no Subnet named %q found in VNet %q", name, vnetID)
}
