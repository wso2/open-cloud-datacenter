// Terraform data source definition for dcapi_route_table.
// Looks up a RouteTable that is not managed by this Terraform config, by name, since its
// API UUID is opaque and unknown to the caller ahead of time.
package datasources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// DataSourceRouteTable returns the schema.Resource for "dcapi_route_table" data source lookups.
func DataSourceRouteTable() *schema.Resource {
	return &schema.Resource{
		ReadContext: dataSourceRouteTableRead,

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
				Description: "Name of the route table to look up.",
			},

			"route_table_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "API-generated UUID4 for the route table.",
			},
			"description": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Human-readable description.",
			},
			"routes": {
				Type:        schema.TypeList,
				Computed:    true,
				Description: "Route entries currently defined on this route table.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"name":             {Type: schema.TypeString, Computed: true},
						"destination_cidr": {Type: schema.TypeString, Computed: true},
						"next_hop_type":    {Type: schema.TypeString, Computed: true},
						"next_hop_ip":      {Type: schema.TypeString, Computed: true},
					},
				},
			},
			"status": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Lifecycle status (e.g. \"ACTIVE\").",
			},
			"provider_type": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Underlying infrastructure provider (e.g. \"kubeovn\").",
			},
			"created_at": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "RFC3339 creation timestamp.",
			},
			"updated_at": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "RFC3339 last-update timestamp.",
			},
		},
	}
}

func dataSourceRouteTableRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)
	tenantID := d.Get("tenant_id").(string)
	projectID := d.Get("project_id").(string)
	vnetID := d.Get("vnet_id").(string)
	name := d.Get("name").(string)

	routeTables, err := c.ListRouteTables(ctx, tenantID, projectID, vnetID)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error listing route tables in VNet %q: %w", vnetID, err))
	}

	for _, rt := range routeTables {
		if rt.Name != name {
			continue
		}

		d.SetId(fmt.Sprintf("%s/%s/%s/%s", tenantID, projectID, vnetID, rt.ID))

		var diags diag.Diagnostics
		diags = appendSet(diags, d, "route_table_id", rt.ID)
		diags = appendSet(diags, d, "description", rt.Description)
		diags = appendSet(diags, d, "routes", flattenRouteEntries(rt.Routes))
		diags = appendSet(diags, d, "status", rt.Status)
		diags = appendSet(diags, d, "provider_type", rt.ProviderType)
		diags = appendSet(diags, d, "created_at", rt.CreatedAt)
		diags = appendSet(diags, d, "updated_at", rt.UpdatedAt)
		return diags
	}

	return diag.Errorf("no route table named %q found in VNet %q", name, vnetID)
}

// flattenRouteEntries converts []client.RouteEntry to a Terraform-compatible list.
func flattenRouteEntries(routes []client.RouteEntry) []interface{} {
	result := make([]interface{}, len(routes))
	for i, r := range routes {
		result[i] = map[string]interface{}{
			"name":             r.Name,
			"destination_cidr": r.DestinationCIDR,
			"next_hop_type":    r.NextHopType,
			"next_hop_ip":      r.NextHopIP,
		}
	}
	return result
}
