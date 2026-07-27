// Terraform resource definition for dcapi_vnet_peering.
//
// Peerings are DIRECTIONAL: creating one dcapi_vnet_peering only adds routes from vnet_id
// towards peer_vnet_id. For full bidirectional connectivity, declare two of these resources —
// one on each VNet pointing at the other. See dc-api-reference.md section 8.
//
// This file follows the same structure as vnet.go (async 202 create/delete, StateChangeConf
// polling, drift detection via the (nil, nil) sentinel). Refer to vnet.go for detailed
// explanations of patterns shared across all resource files.
package resources

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// ResourceVNetPeering returns the schema.Resource for "dcapi_vnet_peering".
func ResourceVNetPeering() *schema.Resource {
	return &schema.Resource{
		// No UpdateContext — every field is ForceNew (immutable). The DC-API provides no
		// PATCH endpoint for peerings.
		CreateContext: resourceVNetPeeringCreate,
		ReadContext:   resourceVNetPeeringRead,
		DeleteContext: resourceVNetPeeringDelete,

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(5 * time.Minute),
			Delete: schema.DefaultTimeout(5 * time.Minute),
		},

		Schema: map[string]*schema.Schema{

			// ── Required + immutable ──

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
			"vnet_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "UUID of the VNet this peering originates from. Used in the API URL path. Immutable.",
			},
			"name": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Peering name, unique within the requesting VNet. Immutable.",
			},
			"peer_vnet_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "UUID of the peer VNet. Must be in the same tenant and region as vnet_id, with non-overlapping address spaces. Immutable.",
			},

			// ── Optional + immutable ──

			"allow_forwarded_traffic": {
				Type:        schema.TypeBool,
				Optional:    true,
				ForceNew:    true,
				Default:     false,
				Description: "Whether traffic forwarded from the peer VNet (not originating there) is allowed. Immutable.",
			},

			// ── Computed ──

			"peering_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "API-generated UUID4 for this peering.",
			},
			"status": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Lifecycle status: PENDING | ACTIVE | FAILED. Set by the API.",
			},
			"provider_type": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Underlying network fabric (e.g. \"kubeovn\"). Set by the API.",
			},
			"message": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Human-readable status message. Most useful when status is FAILED.",
			},
			"warning": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Optional advisory message from the API.",
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

// resourceVNetPeeringCreate calls POST .../vnets/{vnet_id}/peerings, then polls until the
// peering reaches ACTIVE before returning control to Terraform.
//
// State ID encodes all four path components needed to rebuild the API URL:
// "tenantID/projectID/vnetID/peeringID".
func resourceVNetPeeringCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	tenantID := d.Get("tenant_id").(string)
	projectID := d.Get("project_id").(string)
	vnetID := d.Get("vnet_id").(string)

	req := client.VNetPeeringCreateRequest{
		Name:                  d.Get("name").(string),
		PeerVNetID:            d.Get("peer_vnet_id").(string),
		AllowForwardedTraffic: d.Get("allow_forwarded_traffic").(bool),
	}

	peering, err := c.CreateVNetPeering(ctx, tenantID, projectID, vnetID, req)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error creating VNet peering: %w", err))
	}

	d.SetId(fmt.Sprintf("%s/%s/%s/%s", tenantID, projectID, vnetID, peering.ID))

	var diags diag.Diagnostics
	diags = appendSet(diags, d, "peering_id", peering.ID)
	diags = appendSet(diags, d, "status", peering.Status)
	diags = appendSet(diags, d, "provider_type", peering.ProviderType)
	diags = appendSet(diags, d, "message", peering.Message)
	diags = appendSet(diags, d, "warning", peering.Warning)
	diags = appendSet(diags, d, "created_at", peering.CreatedAt)
	diags = appendSet(diags, d, "updated_at", peering.UpdatedAt)
	if diags.HasError() {
		return diags
	}

	// Create returned 202 — poll until the peering reaches ACTIVE before handing control back.
	if err := waitForVNetPeeringActive(ctx, c, tenantID, projectID, vnetID, peering.ID, d.Timeout(schema.TimeoutCreate)); err != nil {
		return diag.FromErr(err)
	}

	return resourceVNetPeeringRead(ctx, d, meta)
}

// resourceVNetPeeringRead fetches current state from the API and refreshes Terraform state.
func resourceVNetPeeringRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 4)
	if len(parts) != 4 {
		return diag.FromErr(fmt.Errorf("invalid VNet peering state ID %q: expected 'tenant_id/project_id/vnet_id/peering_id'", d.Id()))
	}
	tenantID, projectID, vnetID, peeringID := parts[0], parts[1], parts[2], parts[3]

	peering, err := c.GetVNetPeering(ctx, tenantID, projectID, vnetID, peeringID)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error reading VNet peering %q: %w", peeringID, err))
	}
	// (nil, nil) = HTTP 404 = drift: the peering was deleted outside of Terraform.
	if peering == nil {
		d.SetId("")
		return nil
	}

	var diags diag.Diagnostics
	diags = appendSet(diags, d, "tenant_id", tenantID)
	diags = appendSet(diags, d, "project_id", projectID)
	diags = appendSet(diags, d, "vnet_id", vnetID)
	diags = appendSet(diags, d, "peering_id", peering.ID)
	diags = appendSet(diags, d, "name", peering.Name)
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

// resourceVNetPeeringDelete initiates deletion and polls until the API confirms it is gone.
func resourceVNetPeeringDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 4)
	if len(parts) != 4 {
		return diag.FromErr(fmt.Errorf("invalid VNet peering state ID %q: expected 'tenant_id/project_id/vnet_id/peering_id'", d.Id()))
	}
	tenantID, projectID, vnetID, peeringID := parts[0], parts[1], parts[2], parts[3]

	if err := c.DeleteVNetPeering(ctx, tenantID, projectID, vnetID, peeringID); err != nil {
		return diag.FromErr(fmt.Errorf("error deleting VNet peering %q: %w", peeringID, err))
	}

	if err := waitForVNetPeeringDeleted(ctx, c, tenantID, projectID, vnetID, peeringID, d.Timeout(schema.TimeoutDelete)); err != nil {
		return diag.FromErr(err)
	}

	return nil
}

// waitForVNetPeeringActive polls until the peering reaches status "ACTIVE" or timeout.
func waitForVNetPeeringActive(ctx context.Context, c *client.DCAPIClient, tenantID, projectID, vnetID, peeringID string, timeout time.Duration) error {
	conf := &resource.StateChangeConf{
		Pending:    []string{"PENDING"},
		Target:     []string{"ACTIVE"},
		Timeout:    timeout,
		MinTimeout: 15 * time.Second,
		Refresh: func() (interface{}, string, error) {
			peering, err := c.GetVNetPeering(ctx, tenantID, projectID, vnetID, peeringID)
			if err != nil {
				return nil, "", err
			}
			if peering == nil {
				return nil, "", fmt.Errorf("VNet peering %q disappeared while waiting for ACTIVE status", peeringID)
			}
			if peering.Status == "FAILED" {
				return nil, "FAILED", fmt.Errorf("VNet peering %q provisioning failed: %s", peeringID, peering.Message)
			}
			return peering, peering.Status, nil
		},
	}

	_, err := conf.WaitForStateContext(ctx)
	return err
}

// waitForVNetPeeringDeleted polls until the peering is gone (HTTP 404) after a DELETE call.
func waitForVNetPeeringDeleted(ctx context.Context, c *client.DCAPIClient, tenantID, projectID, vnetID, peeringID string, timeout time.Duration) error {
	conf := &resource.StateChangeConf{
		Pending:    []string{"ACTIVE", "DELETING"},
		Target:     []string{"DELETED"},
		Timeout:    timeout,
		MinTimeout: 15 * time.Second,
		Refresh: func() (interface{}, string, error) {
			peering, err := c.GetVNetPeering(ctx, tenantID, projectID, vnetID, peeringID)
			if err != nil {
				return nil, "", err
			}
			if peering == nil {
				return "deleted", "DELETED", nil
			}
			return peering, peering.Status, nil
		},
	}

	_, err := conf.WaitForStateContext(ctx)
	return err
}
