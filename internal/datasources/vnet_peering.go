// Terraform data source definition for dcapi_vnet_peering.
// Looks up a VNet peering that is not managed by this Terraform config, by name, since its
// API UUID is opaque and unknown to the caller ahead of time. Recall that peerings are
// directional — this only inspects the peering originating FROM vnet_id.
package datasources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// DataSourceVNetPeering returns the schema.Resource for "dcapi_vnet_peering" data source lookups.
func DataSourceVNetPeering() *schema.Resource {
	return &schema.Resource{
		ReadContext: dataSourceVNetPeeringRead,

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
				Description: "UUID of the VNet this peering originates from.",
			},
			"name": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "Name of the peering to look up.",
			},

			"peering_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "API-generated UUID4 for this peering.",
			},
			"peer_vnet_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "UUID of the peer VNet.",
			},
			"allow_forwarded_traffic": {
				Type:        schema.TypeBool,
				Computed:    true,
				Description: "Whether traffic forwarded from the peer VNet is allowed.",
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
			"warning": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Optional advisory message from the API.",
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

func dataSourceVNetPeeringRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)
	tenantID := d.Get("tenant_id").(string)
	projectID := d.Get("project_id").(string)
	vnetID := d.Get("vnet_id").(string)
	name := d.Get("name").(string)

	peerings, err := c.ListVNetPeerings(ctx, tenantID, projectID, vnetID)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error listing peerings on VNet %q: %w", vnetID, err))
	}

	for _, peering := range peerings {
		if peering.Name != name {
			continue
		}

		d.SetId(fmt.Sprintf("%s/%s/%s/%s", tenantID, projectID, vnetID, peering.ID))

		var diags diag.Diagnostics
		diags = appendSet(diags, d, "peering_id", peering.ID)
		diags = appendSet(diags, d, "peer_vnet_id", peering.PeerVNetID)
		diags = appendSet(diags, d, "allow_forwarded_traffic", peering.AllowForwardedTraffic)
		diags = appendSet(diags, d, "status", peering.Status)
		diags = appendSet(diags, d, "provider_type", peering.ProviderType)
		diags = appendSet(diags, d, "message", peering.Message)
		diags = appendSet(diags, d, "warning", peering.Warning)
		diags = appendSet(diags, d, "created_at", peering.CreatedAt)
		diags = appendSet(diags, d, "updated_at", peering.UpdatedAt)
		return diags
	}

	return diag.Errorf("no peering named %q found on VNet %q", name, vnetID)
}
