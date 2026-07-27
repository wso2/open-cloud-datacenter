// Terraform data source definition for dcapi_private_dns_zone.
// Looks up a PrivateDnsZone that is not managed by this Terraform config, by name, since
// its API UUID is opaque and unknown to the caller ahead of time.
package datasources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// DataSourcePrivateDNSZone returns the schema.Resource for "dcapi_private_dns_zone" data source lookups.
func DataSourcePrivateDNSZone() *schema.Resource {
	return &schema.Resource{
		ReadContext: dataSourcePrivateDnsZoneRead,

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
				Description: "Name of the DNS zone to look up (e.g. \"internal.wso2.com\").",
			},

			"zone_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "API-generated UUID4 of this DNS zone. Use this (not id) when setting zone_id on dcapi_dns_record.",
			},
			"description": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Free-text note for this DNS zone.",
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

func dataSourcePrivateDnsZoneRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)
	tenantID := d.Get("tenant_id").(string)
	projectID := d.Get("project_id").(string)
	vnetID := d.Get("vnet_id").(string)
	name := d.Get("name").(string)

	zones, err := c.ListPrivateDnsZones(ctx, tenantID, projectID, vnetID)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error listing DNS zones in VNet %q: %w", vnetID, err))
	}

	for _, zone := range zones {
		if zone.Name != name {
			continue
		}

		d.SetId(fmt.Sprintf("%s/%s/%s/%s", tenantID, projectID, vnetID, zone.ID))

		var diags diag.Diagnostics
		diags = appendSet(diags, d, "zone_id", zone.ID)
		diags = appendSet(diags, d, "description", zone.Description)
		diags = appendSet(diags, d, "status", zone.Status)
		diags = appendSet(diags, d, "provider_type", zone.ProviderType)
		diags = appendSet(diags, d, "message", zone.Message)
		diags = appendSet(diags, d, "created_at", zone.CreatedAt)
		diags = appendSet(diags, d, "updated_at", zone.UpdatedAt)
		return diags
	}

	return diag.Errorf("no DNS zone named %q found in VNet %q", name, vnetID)
}
