package resources

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// ResourceRouteTableAssociation returns the schema.Resource for "dcapi_route_table_association".
//
// The DC-API has no dedicated GET endpoint for associations; their state is embedded in the
// RouteTable GET response. Read therefore calls GetRouteTable and scans the associations list
// for the stored association ID. If the route table is gone, the association is implicitly gone.
func ResourceRouteTableAssociation() *schema.Resource {
	return &schema.Resource{
		CreateContext: resourceRouteTableAssociationCreate,
		ReadContext:   resourceRouteTableAssociationRead,
		DeleteContext: resourceRouteTableAssociationDelete,

		Schema: map[string]*schema.Schema{

			// ── Required + immutable ──

			"tenant_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Slug of the parent tenant. Immutable.",
			},
			"project_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Slug of the parent project. Immutable.",
			},
			"vnet_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "UUID of the parent VNet. Immutable.",
			},
			"route_table_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "UUID of the route table to associate with. Immutable.",
			},
			"subnet_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "UUID of the subnet to associate. Immutable.",
			},

			// ── Computed ──

			"association_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "API-generated UUID4 for the association. Used in the delete path.",
			},
			"created_at": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "RFC3339 creation timestamp. Set by the API.",
			},
			"warning": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Optional advisory message from the API (e.g. routing not yet enforced).",
			},
		},
	}
}

func resourceRouteTableAssociationCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	tenantID := d.Get("tenant_id").(string)
	projectID := d.Get("project_id").(string)
	vnetID := d.Get("vnet_id").(string)
	rtID := d.Get("route_table_id").(string)

	req := client.RouteTableAssociationCreateRequest{
		SubnetID: d.Get("subnet_id").(string),
	}

	assoc, err := c.CreateRouteTableAssociation(ctx, tenantID, projectID, vnetID, rtID, req)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error creating route table association: %w", err))
	}

	// State ID encodes all five path components needed to delete the association.
	d.SetId(fmt.Sprintf("%s/%s/%s/%s/%s", tenantID, projectID, vnetID, rtID, assoc.ID))

	var diags diag.Diagnostics
	diags = appendSet(diags, d, "association_id", assoc.ID)
	diags = appendSet(diags, d, "created_at", assoc.CreatedAt)
	diags = appendSet(diags, d, "warning", assoc.Warning)
	return diags
}

func resourceRouteTableAssociationRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 5)
	if len(parts) != 5 {
		return diag.FromErr(fmt.Errorf("invalid route table association state ID %q: expected 'tenant_id/project_id/vnet_id/rt_id/assoc_id'", d.Id()))
	}
	tenantID, projectID, vnetID, rtID, assocID := parts[0], parts[1], parts[2], parts[3], parts[4]

	// The API has no direct GET for associations; scan the parent route table's association list.
	rt, err := c.GetRouteTable(ctx, tenantID, projectID, vnetID, rtID)
	
	if err != nil {
		return diag.FromErr(fmt.Errorf("error reading route table %q for association %q: %w", rtID, assocID, err))
	}
	if rt == nil {
		// Route table was deleted externally — association is gone with it.
		d.SetId("")
		return nil
	}

	for _, a := range rt.Associations {
		if a.ID == assocID {
			// Association still exists — refresh computed fields.
			var diags diag.Diagnostics
			diags = appendSet(diags, d, "association_id", a.ID)
			diags = appendSet(diags, d, "subnet_id", a.SubnetID)
			diags = appendSet(diags, d, "created_at", a.CreatedAt)
			return diags
		}
	}

	// Association not found in the route table — deleted externally.
	d.SetId("")
	return nil
}

func resourceRouteTableAssociationDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 5)
	if len(parts) != 5 {
		return diag.FromErr(fmt.Errorf("invalid route table association state ID %q: expected 'tenant_id/project_id/vnet_id/rt_id/assoc_id'", d.Id()))
	}
	tenantID, projectID, vnetID, rtID, assocID := parts[0], parts[1], parts[2], parts[3], parts[4]

	if err := c.DeleteRouteTableAssociation(ctx, tenantID, projectID, vnetID, rtID, assocID); err != nil {
		return diag.FromErr(fmt.Errorf("error deleting route table association %q: %w", assocID, err))
	}
	return nil
}
