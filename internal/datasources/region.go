// Terraform data source definition for dcapi_region.
// Regions are platform-wide — provisioned by platform operators, never by Terraform.
// This data source looks one up by name, e.g. to validate a region slug before passing it
// into dcapi_vnet's region field, or to inspect current zone health.
package datasources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// DataSourceRegion returns the schema.Resource for "dcapi_region" data source lookups.
func DataSourceRegion() *schema.Resource {
	return &schema.Resource{
		ReadContext: dataSourceRegionRead,

		Schema: map[string]*schema.Schema{

			"name": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "Slug of the region to look up (e.g. \"lk\").",
			},

			"display_name": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Human-readable region name.",
			},
			"description": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Free-text description of the region.",
			},
			"status": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Derived region status: up | degraded | down | unknown.",
			},
			"zones": {
				Type:        schema.TypeList,
				Computed:    true,
				Description: "Failure-domain zones within this region.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"name":            {Type: schema.TypeString, Computed: true},
						"status":          {Type: schema.TypeString, Computed: true},
						"agent_version":   {Type: schema.TypeString, Computed: true},
						"agent_last_seen": {Type: schema.TypeString, Computed: true},
					},
				},
			},
		},
	}
}

func dataSourceRegionRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)
	name := d.Get("name").(string)

	regions, err := c.ListRegions(ctx)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error listing regions: %w", err))
	}

	for _, region := range regions {
		if region.Name != name {
			continue
		}

		d.SetId(region.Name)

		var diags diag.Diagnostics
		diags = appendSet(diags, d, "display_name", region.DisplayName)
		diags = appendSet(diags, d, "description", region.Description)
		diags = appendSet(diags, d, "status", region.Status)
		diags = appendSet(diags, d, "zones", flattenZones(region.Zones))
		return diags
	}

	return diag.Errorf("no region named %q found", name)
}

// flattenZones converts []client.Zone to a Terraform-compatible list, flattening the
// nested (possibly nil) AgentStatus into two plain string fields.
func flattenZones(zones []client.Zone) []interface{} {
	result := make([]interface{}, len(zones))
	for i, z := range zones {
		var agentVersion, agentLastSeen string
		if z.Agent != nil {
			agentVersion = z.Agent.Version
			agentLastSeen = z.Agent.LastSeen
		}
		result[i] = map[string]interface{}{
			"name":            z.Name,
			"status":          z.Status,
			"agent_version":   agentVersion,
			"agent_last_seen": agentLastSeen,
		}
	}
	return result
}
