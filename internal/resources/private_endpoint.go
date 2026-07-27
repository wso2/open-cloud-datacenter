// Terraform resource definition for dcapi_private_endpoint.
// Nests under a KeyVault; exposes it inside a VNet/Subnet. Create/Delete are fully
// synchronous (201/204) — no polling required, unlike VNet/Subnet/Peering/DnsZone.
//
// NOTE: these routes return HTTP 501 Not Implemented if the endpoint provisioner is not
// enabled on the target DC-API instance. That surfaces as a plain API error from doRequest;
// no special handling is needed here beyond the standard diag.FromErr wrapping.
package resources

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// ResourcePrivateEndpoint returns the schema.Resource for "dcapi_private_endpoint".
func ResourcePrivateEndpoint() *schema.Resource {
	return &schema.Resource{
		// No UpdateContext — every field is ForceNew (immutable); the DC-API provides no
		// PATCH endpoint for private endpoints.
		CreateContext: resourcePrivateEndpointCreate,
		ReadContext:   resourcePrivateEndpointRead,
		DeleteContext: resourcePrivateEndpointDelete,

		Schema: map[string]*schema.Schema{

			// ── Required + immutable path params ──

			"tenant_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Slug of the parent tenant. Used in the API URL path. Immutable.",
			},
			"project_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Slug of the parent project. Used in the API URL path. Immutable.",
			},
			"kv_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "UUID of the parent KeyVault. Used in the API URL path. Immutable.",
			},

			// ── Required + immutable ──

			"name": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Private endpoint name. 3-63 chars, DNS label, starts with a letter. Immutable.",
			},
			"vnet_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "UUID of the VNet where the endpoint is reachable. Immutable.",
			},
			"subnet_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "UUID of the Subnet (within vnet_id) to place the endpoint in. Immutable.",
			},

			// ── Computed ──

			"endpoint_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "API-generated UUID4 for this private endpoint.",
			},
			"target_type": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Type of resource this endpoint exposes, e.g. \"key_vault\". Set by the API.",
			},
			"target_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "UUID of the parent KeyVault, echoed back by the API.",
			},
			"ip_address": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "VIP assigned from the subnet CIDR. Set by the API.",
			},
			"hostname": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "DNS-resolvable hostname, reachable only within the VPC. Set by the API.",
			},
			"status": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Lifecycle status. Set by the API.",
			},
			"message": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Human-readable status message. Set by the API.",
			},
			"created_at": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "RFC3339 creation timestamp. Set by the API.",
			},
			"updated_at": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "RFC3339 last-updated timestamp. Set by the API.",
			},
		},
	}
}

// resourcePrivateEndpointCreate calls POST .../keyvaults/{kv_id}/private-endpoints.
// Returns 201 Created — no polling required.
//
// State ID encodes all four path components: "tenantID/projectID/kvID/endpointID".
func resourcePrivateEndpointCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	tenantID := d.Get("tenant_id").(string)
	projectID := d.Get("project_id").(string)
	kvID := d.Get("kv_id").(string)

	req := client.PrivateEndpointCreateRequest{
		Name:     d.Get("name").(string),
		VNetID:   d.Get("vnet_id").(string),
		SubnetID: d.Get("subnet_id").(string),
	}

	ep, err := c.CreatePrivateEndpoint(ctx, tenantID, projectID, kvID, req)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error creating PrivateEndpoint: %w", err))
	}

	d.SetId(fmt.Sprintf("%s/%s/%s/%s", tenantID, projectID, kvID, ep.ID))

	var diags diag.Diagnostics
	diags = appendSet(diags, d, "endpoint_id", ep.ID)
	diags = appendSet(diags, d, "target_type", ep.TargetType)
	diags = appendSet(diags, d, "target_id", ep.TargetID)
	diags = appendSet(diags, d, "ip_address", ep.IPAddress)
	diags = appendSet(diags, d, "hostname", ep.Hostname)
	diags = appendSet(diags, d, "status", ep.Status)
	diags = appendSet(diags, d, "message", ep.Message)
	diags = appendSet(diags, d, "created_at", ep.CreatedAt)
	diags = appendSet(diags, d, "updated_at", ep.UpdatedAt)
	return diags
}

// resourcePrivateEndpointRead fetches current state from the API and refreshes Terraform state.
func resourcePrivateEndpointRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 4)
	if len(parts) != 4 {
		return diag.FromErr(fmt.Errorf("invalid PrivateEndpoint state ID %q: expected 'tenant_id/project_id/kv_id/endpoint_id'", d.Id()))
	}
	tenantID, projectID, kvID, epID := parts[0], parts[1], parts[2], parts[3]

	ep, err := c.GetPrivateEndpoint(ctx, tenantID, projectID, kvID, epID)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error reading PrivateEndpoint %q: %w", epID, err))
	}
	if ep == nil {
		d.SetId("")
		return nil
	}

	var diags diag.Diagnostics
	diags = appendSet(diags, d, "tenant_id", tenantID)
	diags = appendSet(diags, d, "project_id", projectID)
	diags = appendSet(diags, d, "kv_id", kvID)
	diags = appendSet(diags, d, "endpoint_id", ep.ID)
	diags = appendSet(diags, d, "name", ep.Name)
	diags = appendSet(diags, d, "vnet_id", ep.VNetID)
	diags = appendSet(diags, d, "subnet_id", ep.SubnetID)
	diags = appendSet(diags, d, "target_type", ep.TargetType)
	diags = appendSet(diags, d, "target_id", ep.TargetID)
	diags = appendSet(diags, d, "ip_address", ep.IPAddress)
	diags = appendSet(diags, d, "hostname", ep.Hostname)
	diags = appendSet(diags, d, "status", ep.Status)
	diags = appendSet(diags, d, "message", ep.Message)
	diags = appendSet(diags, d, "created_at", ep.CreatedAt)
	diags = appendSet(diags, d, "updated_at", ep.UpdatedAt)
	return diags
}

// resourcePrivateEndpointDelete sends DELETE .../private-endpoints/{endpoint_id}. Returns 204 (sync).
func resourcePrivateEndpointDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 4)
	if len(parts) != 4 {
		return diag.FromErr(fmt.Errorf("invalid PrivateEndpoint state ID %q: expected 'tenant_id/project_id/kv_id/endpoint_id'", d.Id()))
	}
	tenantID, projectID, kvID, epID := parts[0], parts[1], parts[2], parts[3]

	if err := c.DeletePrivateEndpoint(ctx, tenantID, projectID, kvID, epID); err != nil {
		return diag.FromErr(fmt.Errorf("error deleting PrivateEndpoint %q: %w", epID, err))
	}
	return nil
}
