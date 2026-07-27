// Terraform resource definition for dcapi_private_dns_zone.
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

// ResourcePrivateDnsZone returns the schema.Resource for "dcapi_private_dns_zone".
func ResourcePrivateDnsZone() *schema.Resource {
	return &schema.Resource{
		// No UpdateContext — every field is ForceNew (immutable); the DC-API provides no
		// PATCH endpoint for DNS zones.
		CreateContext: resourcePrivateDnsZoneCreate,
		ReadContext:   resourcePrivateDnsZoneRead,
		DeleteContext: resourcePrivateDnsZoneDelete,

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
				Description: "UUID of the parent VNet. Used in the API URL path. Immutable.",
			},
			"name": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "DNS zone name (e.g. \"internal.wso2.com\"). Immutable.",
			},

			// ── Optional + immutable ──

			"description": {
				Type:        schema.TypeString,
				Optional:    true,
				ForceNew:    true,
				Description: "Free-text note for this DNS zone. Immutable.",
			},

			// ── Computed ──

			"zone_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "API-generated UUID4 of this DNS zone. Use this (not id) when setting zone_id on dcapi_dns_record.",
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

// resourcePrivateDnsZoneCreate calls POST .../vnets/{vnet_id}/dns-zones, then polls until the
// zone reaches ACTIVE before returning control to Terraform.
//
// State ID encodes all four path components: "tenantID/projectID/vnetID/zoneID".
func resourcePrivateDnsZoneCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	tenantID := d.Get("tenant_id").(string)
	projectID := d.Get("project_id").(string)
	vnetID := d.Get("vnet_id").(string)

	req := client.PrivateDnsZoneCreateRequest{
		Name:        d.Get("name").(string),
		Description: d.Get("description").(string),
	}

	zone, err := c.CreatePrivateDnsZone(ctx, tenantID, projectID, vnetID, req)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error creating PrivateDnsZone: %w", err))
	}

	d.SetId(fmt.Sprintf("%s/%s/%s/%s", tenantID, projectID, vnetID, zone.ID))

	var diags diag.Diagnostics
	diags = appendSet(diags, d, "zone_id", zone.ID)
	diags = appendSet(diags, d, "status", zone.Status)
	diags = appendSet(diags, d, "provider_type", zone.ProviderType)
	diags = appendSet(diags, d, "message", zone.Message)
	diags = appendSet(diags, d, "created_at", zone.CreatedAt)
	diags = appendSet(diags, d, "updated_at", zone.UpdatedAt)
	if diags.HasError() {
		return diags
	}

	if err := waitForPrivateDnsZoneActive(ctx, c, tenantID, projectID, vnetID, zone.ID, d.Timeout(schema.TimeoutCreate)); err != nil {
		return diag.FromErr(err)
	}

	return resourcePrivateDnsZoneRead(ctx, d, meta)
}

// resourcePrivateDnsZoneRead fetches current state from the API and refreshes Terraform state.
func resourcePrivateDnsZoneRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 4)
	if len(parts) != 4 {
		return diag.FromErr(fmt.Errorf("invalid PrivateDnsZone state ID %q: expected 'tenant_id/project_id/vnet_id/zone_id'", d.Id()))
	}
	tenantID, projectID, vnetID, zoneID := parts[0], parts[1], parts[2], parts[3]

	zone, err := c.GetPrivateDnsZone(ctx, tenantID, projectID, vnetID, zoneID)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error reading PrivateDnsZone %q: %w", zoneID, err))
	}
	if zone == nil {
		d.SetId("")
		return nil
	}

	var diags diag.Diagnostics
	diags = appendSet(diags, d, "tenant_id", tenantID)
	diags = appendSet(diags, d, "project_id", projectID)
	diags = appendSet(diags, d, "vnet_id", vnetID)
	diags = appendSet(diags, d, "zone_id", zone.ID)
	diags = appendSet(diags, d, "name", zone.Name)
	diags = appendSet(diags, d, "description", zone.Description)
	diags = appendSet(diags, d, "status", zone.Status)
	diags = appendSet(diags, d, "provider_type", zone.ProviderType)
	diags = appendSet(diags, d, "message", zone.Message)
	diags = appendSet(diags, d, "created_at", zone.CreatedAt)
	diags = appendSet(diags, d, "updated_at", zone.UpdatedAt)
	return diags
}

// resourcePrivateDnsZoneDelete initiates deletion and polls until the API confirms it is gone.
func resourcePrivateDnsZoneDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 4)
	if len(parts) != 4 {
		return diag.FromErr(fmt.Errorf("invalid PrivateDnsZone state ID %q: expected 'tenant_id/project_id/vnet_id/zone_id'", d.Id()))
	}
	tenantID, projectID, vnetID, zoneID := parts[0], parts[1], parts[2], parts[3]

	if err := c.DeletePrivateDnsZone(ctx, tenantID, projectID, vnetID, zoneID); err != nil {
		return diag.FromErr(fmt.Errorf("error deleting PrivateDnsZone %q: %w", zoneID, err))
	}

	if err := waitForPrivateDnsZoneDeleted(ctx, c, tenantID, projectID, vnetID, zoneID, d.Timeout(schema.TimeoutDelete)); err != nil {
		return diag.FromErr(err)
	}

	return nil
}

// waitForPrivateDnsZoneActive polls until the zone reaches status "ACTIVE" or timeout.
func waitForPrivateDnsZoneActive(ctx context.Context, c *client.DCAPIClient, tenantID, projectID, vnetID, zoneID string, timeout time.Duration) error {
	conf := &resource.StateChangeConf{
		Pending:    []string{"PENDING"},
		Target:     []string{"ACTIVE"},
		Timeout:    timeout,
		MinTimeout: 15 * time.Second,
		Refresh: func() (interface{}, string, error) {
			zone, err := c.GetPrivateDnsZone(ctx, tenantID, projectID, vnetID, zoneID)
			if err != nil {
				return nil, "", err
			}
			if zone == nil {
				return nil, "", fmt.Errorf("PrivateDnsZone %q disappeared while waiting for ACTIVE status", zoneID)
			}
			if zone.Status == "FAILED" {
				return nil, "FAILED", fmt.Errorf("PrivateDnsZone %q provisioning failed: %s", zoneID, zone.Message)
			}
			return zone, zone.Status, nil
		},
	}

	_, err := conf.WaitForStateContext(ctx)
	return err
}

// waitForPrivateDnsZoneDeleted polls until the zone is gone (HTTP 404) after a DELETE call.
func waitForPrivateDnsZoneDeleted(ctx context.Context, c *client.DCAPIClient, tenantID, projectID, vnetID, zoneID string, timeout time.Duration) error {
	conf := &resource.StateChangeConf{
		Pending:    []string{"ACTIVE", "DELETING"},
		Target:     []string{"DELETED"},
		Timeout:    timeout,
		MinTimeout: 15 * time.Second,
		Refresh: func() (interface{}, string, error) {
			zone, err := c.GetPrivateDnsZone(ctx, tenantID, projectID, vnetID, zoneID)
			if err != nil {
				return nil, "", err
			}
			if zone == nil {
				return "deleted", "DELETED", nil
			}
			return zone, zone.Status, nil
		},
	}

	_, err := conf.WaitForStateContext(ctx)
	return err
}
