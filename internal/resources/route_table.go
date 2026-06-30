package resources

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
	"terraform-provider-dcapi/internal/client"
)

// ResourceRouteTable returns the schema.Resource for "dcapi_route_table".
func ResourceRouteTable() *schema.Resource {
	return &schema.Resource{
		CreateContext: resourceRouteTableCreate,
		ReadContext:   resourceRouteTableRead,
		UpdateContext: resourceRouteTableUpdate,
		DeleteContext: resourceRouteTableDelete,

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
			"name": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Route table name. Must be unique within the VNet. Immutable.",
			},

			// ── Optional + immutable ──

			"description": {
				Type:        schema.TypeString,
				Optional:    true,
				ForceNew:    true,
				Description: "Human-readable description. Max 256 chars. Immutable.",
			},

			// ── Optional + updatable ──

			"routes": {
				Type:        schema.TypeList,
				Optional:    true,
				Description: "Route entries. Full-replace on update — send the complete desired list.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"name": {
							Type:        schema.TypeString,
							Required:    true,
							Description: "Route name. Must be unique within the route table.",
						},
						"destination_cidr": {
							Type:         schema.TypeString,
							Required:     true,
							Description:  "Destination CIDR (e.g. \"0.0.0.0/0\").",
							ValidateFunc: validation.IsCIDR,
						},
						"next_hop_type": {
							Type:        schema.TypeString,
							Required:    true,
							Description: "\"vnet_local\"|\"internet\"|\"virtual_appliance\"|\"none\".",
							ValidateFunc: validation.StringInSlice([]string{
								"vnet_local", "internet", "virtual_appliance", "none",
							}, false),
						},
						"next_hop_ip": {
							Type:         schema.TypeString,
							Optional:     true,
							Description:  "Next-hop IP. Required when next_hop_type is \"virtual_appliance\".",
							ValidateFunc: validation.IsIPAddress,
						},
					},
				},
			},

			// ── Computed ──

			"route_table_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "API-generated UUID4 for the route table.",
			},
			"status": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Lifecycle status (e.g. \"ACTIVE\"). Set by the API.",
			},
			"provider_type": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Underlying infrastructure provider (e.g. \"kubeovn\"). Set by the API.",
			},
			"created_at": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "RFC3339 creation timestamp. Set by the API.",
			},
			"updated_at": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "RFC3339 last-update timestamp. Set by the API.",
			},
		},
	}
}

func resourceRouteTableCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	tenantID := d.Get("tenant_id").(string)
	projectID := d.Get("project_id").(string)
	vnetID := d.Get("vnet_id").(string)

	req := client.RouteTableCreateRequest{
		Name:   d.Get("name").(string),
		Routes: expandRouteEntries(d.Get("routes").([]interface{})),
	}
	if v, ok := d.Get("description").(string); ok {
		req.Description = v
	}

	rt, err := c.CreateRouteTable(ctx, tenantID, projectID, vnetID, req)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error creating route table: %w", err))
	}

	d.SetId(fmt.Sprintf("%s/%s/%s/%s", tenantID, projectID, vnetID, rt.ID))

	var diags diag.Diagnostics
	diags = appendSet(diags, d, "route_table_id", rt.ID)
	diags = appendSet(diags, d, "status", rt.Status)
	diags = appendSet(diags, d, "provider_type", rt.ProviderType)
	diags = appendSet(diags, d, "created_at", rt.CreatedAt)
	diags = appendSet(diags, d, "updated_at", rt.UpdatedAt)
	diags = appendSet(diags, d, "routes", flattenRouteEntries(rt.Routes))
	return diags
}

func resourceRouteTableRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 4)
	if len(parts) != 4 {
		return diag.FromErr(fmt.Errorf("invalid route table state ID %q: expected 'tenant_id/project_id/vnet_id/rt_id'", d.Id()))
	}
	tenantID, projectID, vnetID, rtID := parts[0], parts[1], parts[2], parts[3]

	rt, err := c.GetRouteTable(ctx, tenantID, projectID, vnetID, rtID)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error reading route table %q: %w", rtID, err))
	}
	if rt == nil {
		d.SetId("")
		return nil
	}

	var diags diag.Diagnostics
	diags = appendSet(diags, d, "route_table_id", rt.ID)
	diags = appendSet(diags, d, "name", rt.Name)
	diags = appendSet(diags, d, "description", rt.Description)
	diags = appendSet(diags, d, "status", rt.Status)
	diags = appendSet(diags, d, "provider_type", rt.ProviderType)
	diags = appendSet(diags, d, "created_at", rt.CreatedAt)
	diags = appendSet(diags, d, "updated_at", rt.UpdatedAt)
	diags = appendSet(diags, d, "routes", flattenRouteEntries(rt.Routes))
	return diags
}

func resourceRouteTableUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 4)
	if len(parts) != 4 {
		return diag.FromErr(fmt.Errorf("invalid route table state ID %q: expected 'tenant_id/project_id/vnet_id/rt_id'", d.Id()))
	}
	tenantID, projectID, vnetID, rtID := parts[0], parts[1], parts[2], parts[3]

	// Full-replace: always send the complete desired routes list; nil becomes [].
	routes := expandRouteEntries(d.Get("routes").([]interface{}))
	if routes == nil {
		routes = []client.RouteEntry{}
	}

	req := client.RouteTableUpdateRequest{Routes: routes}
	rt, err := c.UpdateRouteTable(ctx, tenantID, projectID, vnetID, rtID, req)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error updating route table %q: %w", rtID, err))
	}

	var diags diag.Diagnostics
	diags = appendSet(diags, d, "routes", flattenRouteEntries(rt.Routes))
	diags = appendSet(diags, d, "updated_at", rt.UpdatedAt)
	return diags
}

func resourceRouteTableDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 4)
	if len(parts) != 4 {
		return diag.FromErr(fmt.Errorf("invalid route table state ID %q: expected 'tenant_id/project_id/vnet_id/rt_id'", d.Id()))
	}
	tenantID, projectID, vnetID, rtID := parts[0], parts[1], parts[2], parts[3]

	if err := c.DeleteRouteTable(ctx, tenantID, projectID, vnetID, rtID); err != nil {
		return diag.FromErr(fmt.Errorf("error deleting route table %q: %w", rtID, err))
	}
	return nil
}

// expandRouteEntries converts the Terraform routes list to []client.RouteEntry.
func expandRouteEntries(raw []interface{}) []client.RouteEntry {
	if len(raw) == 0 {
		return nil
	}
	entries := make([]client.RouteEntry, len(raw))
	for i, v := range raw {
		m := v.(map[string]interface{})
		entries[i] = client.RouteEntry{
			Name:            m["name"].(string),
			DestinationCIDR: m["destination_cidr"].(string),
			NextHopType:     m["next_hop_type"].(string),
		}
		if ip, ok := m["next_hop_ip"].(string); ok {
			entries[i].NextHopIP = ip
		}
	}
	return entries
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
