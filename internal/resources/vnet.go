// Terraform resource definition for dcapi_vnet.
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

// ResourceVNet returns the *schema.Resource that describes the "dcapi_vnet" resource type.
func ResourceVNet() *schema.Resource {
	return &schema.Resource{
		// No UpdateContext — every field is ForceNew; changes require destroy + recreate.
		CreateContext: resourceVNetCreate,
		ReadContext:   resourceVNetRead,
		DeleteContext: resourceVNetDelete,

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(5 * time.Minute),
			Delete: schema.DefaultTimeout(5 * time.Minute),
		},

		Schema: map[string]*schema.Schema{

			// ── Required + immutable ──

			"name": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Label for this VNet. Immutable — changing it destroys and recreates the VNet.",
			},

			// TypeList preserves insertion order, which matters for network routing.
			"address_space": {
				Type:     schema.TypeList,
				Required: true,
				ForceNew: true,
				Elem: &schema.Schema{
					Type: schema.TypeString,
				},
				Description: "List of RFC1918 CIDRs this VNet owns (e.g. [\"10.1.0.0/16\"]). Immutable.",
			},

			"region": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "DC-API region slug (e.g. \"lk-dev\"). Must match an existing region. Immutable.",
			},

			// ── Optional + immutable ──

			"description": {
				Type:        schema.TypeString,
				Optional:    true,
				ForceNew:    true,
				Description: "Free-text note for this VNet. Immutable — changing it recreates the VNet.",
			},

			"tenant_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Slug of the parent tenant. Used in the API URL path (not the request body). Immutable.",
			},

			"project_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Slug of the parent project. Used in the API URL path (not the request body). Immutable.",
			},

			// ── Computed ──

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

			// vnet_uuid exposes the bare API-assigned UUID for use as a path parameter in
			// child resources (e.g. dcapi_subnet.vnet_id). The resource id is a composite
			// "tenant/project/uuid" key; child resources need only the UUID portion.
			"vnet_uuid": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "API-assigned UUID of this VNet. Use this (not id) when setting vnet_id on child resources such as dcapi_subnet.",
			},
		},
	}
}

func resourceVNetCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	tenantID := d.Get("tenant_id").(string)
	projectID := d.Get("project_id").(string)

	rawAddressSpace := d.Get("address_space").([]interface{})
	addressSpace := make([]string, len(rawAddressSpace))
	for i, v := range rawAddressSpace {
		addressSpace[i] = v.(string)
	}

	req := client.VNetCreateRequest{
		Name:         d.Get("name").(string),
		AddressSpace: addressSpace,
		Region:       d.Get("region").(string),
		Description:  d.Get("description").(string),
	}

	vnet, err := c.CreateVNet(ctx, tenantID, projectID, req)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error creating VNet: %w", err))
	}

	d.SetId(fmt.Sprintf("%s/%s/%s", tenantID, projectID, vnet.ID))
	if err := d.Set("vnet_uuid", vnet.ID); err != nil {
		return diag.FromErr(err)
	}

	var diags diag.Diagnostics
	if err := d.Set("status", vnet.Status); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("provider_type", vnet.ProviderType); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("message", vnet.Message); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("created_at", vnet.CreatedAt); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("updated_at", vnet.UpdatedAt); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if diags.HasError() {
		return diags
	}

	if err := waitForVNetActive(ctx, c, tenantID, projectID, vnet.ID, d.Timeout(schema.TimeoutCreate)); err != nil {
		return diag.FromErr(err)
	}

	return resourceVNetRead(ctx, d, meta)
}

func resourceVNetRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 3)
	if len(parts) != 3 {
		return diag.FromErr(fmt.Errorf("invalid VNet state ID %q: expected 'tenant_id/project_id/vnet_id'", d.Id()))
	}
	tenantID, projectID, vnetID := parts[0], parts[1], parts[2]

	vnet, err := c.GetVNet(ctx, tenantID, projectID, vnetID)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error reading VNet %q: %w", vnetID, err))
	}

	// nil, nil from GetVNet means HTTP 404 — deleted outside Terraform.
	if vnet == nil {
		d.SetId("")
		return nil
	}

	var diags diag.Diagnostics
	if err := d.Set("name", vnet.Name); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("address_space", vnet.AddressSpace); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("region", vnet.Region); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("description", vnet.Description); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	// Re-set path params from our state ID — the API does not echo ProjectID in the response body.
	if err := d.Set("tenant_id", tenantID); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("project_id", projectID); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("vnet_uuid", vnetID); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("status", vnet.Status); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("provider_type", vnet.ProviderType); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("message", vnet.Message); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("created_at", vnet.CreatedAt); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("updated_at", vnet.UpdatedAt); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	return diags
}

func resourceVNetDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 3)
	if len(parts) != 3 {
		return diag.FromErr(fmt.Errorf("invalid VNet state ID %q: expected 'tenant_id/project_id/vnet_id'", d.Id()))
	}
	tenantID, projectID, vnetID := parts[0], parts[1], parts[2]

	err := c.DeleteVNet(ctx, tenantID, projectID, vnetID)
	if err != nil {
		// HTTP 409 means the VNet still has subnets; all dcapi_subnet resources must be destroyed first.
		if strings.Contains(err.Error(), "HTTP 409") {
			return diag.FromErr(fmt.Errorf(
				"cannot delete VNet %q: it still contains Subnets. "+
					"All dcapi_subnet resources inside this VNet must be destroyed first. "+
					"If you are using 'terraform destroy', Terraform should handle this ordering "+
					"automatically when resource references like vnet_id = dcapi_vnet.example.id are used. "+
					"Original API error: %w",
				vnetID, err,
			))
		}
		return diag.FromErr(fmt.Errorf("error deleting VNet %q: %w", vnetID, err))
	}

	// DELETE returns 202 — poll until HTTP 404 before returning so Terraform doesn't
	// attempt to delete the parent project while the VNet row still exists.
	if err := waitForVNetDeleted(ctx, c, tenantID, projectID, vnetID, d.Timeout(schema.TimeoutDelete)); err != nil {
		return diag.FromErr(err)
	}

	return nil
}

// waitForVNetActive polls GET /vnets/{vnetID} until status reaches "ACTIVE" or the timeout expires.
func waitForVNetActive(ctx context.Context, c *client.DCAPIClient, tenantID, projectID, vnetID string, timeout time.Duration) error {
	conf := &resource.StateChangeConf{
		Pending:    []string{"PENDING"},
		Target:     []string{"ACTIVE"},
		Timeout:    timeout,
		MinTimeout: 15 * time.Second,
		Refresh: func() (interface{}, string, error) {
			vnet, err := c.GetVNet(ctx, tenantID, projectID, vnetID)
			if err != nil {
				return nil, "", err
			}
			if vnet == nil {
				return nil, "", fmt.Errorf("VNet %q disappeared while waiting for ACTIVE status", vnetID)
			}
			if vnet.Status == "FAILED" {
				return nil, "FAILED", fmt.Errorf("VNet %q provisioning failed: %s", vnetID, vnet.Message)
			}
			return vnet, vnet.Status, nil
		},
	}

	_, err := conf.WaitForStateContext(ctx)
	return err
}

// waitForVNetDeleted polls GET /vnets/{vnetID} until the API returns HTTP 404,
// confirming that the async deletion is complete.
func waitForVNetDeleted(ctx context.Context, c *client.DCAPIClient, tenantID, projectID, vnetID string, timeout time.Duration) error {
	conf := &resource.StateChangeConf{
		// "ACTIVE" is included because the GET may briefly return ACTIVE before DC-API moves the VNet to DELETING.
		Pending:    []string{"ACTIVE", "DELETING"},
		Target:     []string{"DELETED"},
		Timeout:    timeout,
		MinTimeout: 15 * time.Second,
		Refresh: func() (interface{}, string, error) {
			vnet, err := c.GetVNet(ctx, tenantID, projectID, vnetID)
			if err != nil {
				return nil, "", err
			}
			if vnet == nil {
				return "deleted", "DELETED", nil
			}
			return vnet, vnet.Status, nil
		},
	}

	_, err := conf.WaitForStateContext(ctx)
	return err
}
