// Terraform resource definition for dcapi_subnet.
// Subnets nest inside VNets — every API call requires tenantID, projectID, vnetID, and subnetID.
// This file follows the same structure as vnet.go; refer to vnet.go for detailed
// explanations of patterns that are used identically here (type assertions, d.Get/Set/SetId,
// StateChangeConf, polling, drift detection). Comments here focus on subnet-specific nuances.
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

// ResourceSubnet returns the *schema.Resource that describes the "dcapi_subnet" resource type.
// Terraform calls this once at startup to register the resource and learn its schema.
func ResourceSubnet() *schema.Resource {
	return &schema.Resource{
		// No UpdateContext — every field is ForceNew; changes require destroy + recreate.
		CreateContext: resourceSubnetCreate,
		ReadContext:   resourceSubnetRead,
		DeleteContext: resourceSubnetDelete,

		Timeouts: &schema.ResourceTimeout{
			// Subnet creation waits for the Kube-OVN logical-switch to become ready.
			Create: schema.DefaultTimeout(5 * time.Minute),
			// Subnet deletion may take up to 15 minutes if this is the LAST subnet in the
			// VNet — DC-API automatically tears down the per-VPC NAT gateway and CoreDNS
			// pods before removing the subnet. We use 10 minutes as the standard timeout
			// (covers the common case) and note the extended scenario in the poll helper below.
			Delete: schema.DefaultTimeout(10 * time.Minute),
		},

		Schema: map[string]*schema.Schema{

			// ── User-supplied, Required, ForceNew ──

			"name": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Label for this Subnet, unique within the parent VNet. Immutable.",
			},

			"cidr": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "CIDR range (e.g. \"10.1.1.0/24\"). Must fall within the VNet's address_space and must not overlap siblings. Immutable.",
			},

			// ── User-supplied, Optional, Computed, ForceNew ──
			"gateway": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				ForceNew:    true,
				Description: "Gateway IP within the CIDR (e.g. \"10.1.1.1\"). API assigns the first usable IP if omitted. Immutable.",
			},

			"description": {
				Type:        schema.TypeString,
				Optional:    true,
				ForceNew:    true,
				Description: "Free-text note for this Subnet. Immutable.",
			},

			// tenant_id is a URL path parameter, not a JSON body field.
			// Stored in state so Read and Delete can reconstruct the full API URL.
			"tenant_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Slug of the parent tenant. Used in the API URL path. Immutable.",
			},

			// project_id is also a URL path parameter.
			"project_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Slug of the parent project. Used in the API URL path. Immutable.",
			},

			// vnet_id is the UUID of the parent VNet and a URL path parameter.
			// In .tf files, this is typically set to dcapi_vnet.example.id, which creates
			// an implicit dependency: Terraform will create the VNet before this Subnet.
			"vnet_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "UUID of the parent VNet. Used in the API URL path. Creates an implicit dependency — the VNet must exist first. Immutable.",
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

			"subnet_uuid": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "API-assigned UUID of this Subnet. Use this (not id) when setting subnet_id on child resources such as dcapi_virtual_machine.",
			},
		},
	}
}

// resourceSubnetCreate calls the DC-API to create a Subnet, records state, then polls
// until the Subnet reaches ACTIVE status before returning control to Terraform.
//
// The Terraform state ID for Subnets uses FOUR parts:
//   "tenantID/projectID/vnetID/subnetID"
// because all four are needed to reconstruct the nested API URL:
//   GET /v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/subnets/{subnet_id}
func resourceSubnetCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	tenantID := d.Get("tenant_id").(string)
	projectID := d.Get("project_id").(string)
	vnetID := d.Get("vnet_id").(string)

	req := client.SubnetCreateRequest{
		Name:        d.Get("name").(string),
		CIDR:        d.Get("cidr").(string),
		Gateway:     d.Get("gateway").(string),
		Description: d.Get("description").(string),
	}

	subnet, err := c.CreateSubnet(ctx, tenantID, projectID, vnetID, req)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error creating Subnet: %w", err))
	}

	// d.SetId encodes all four path components so Read and Delete can reconstruct the URL.
	d.SetId(fmt.Sprintf("%s/%s/%s/%s", tenantID, projectID, vnetID, subnet.ID))
	if err := d.Set("subnet_uuid", subnet.ID); err != nil {
		return diag.FromErr(err)
	}

	// Store computed fields from the create response before polling.
	var diags diag.Diagnostics
	if err := d.Set("status", subnet.Status); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("provider_type", subnet.ProviderType); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("message", subnet.Message); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	// gateway is Computed+Optional — store the API-assigned default if the user omitted it.
	if err := d.Set("gateway", subnet.Gateway); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("created_at", subnet.CreatedAt); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("updated_at", subnet.UpdatedAt); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if diags.HasError() {
		return diags
	}

	// Create returned 202 — poll until ACTIVE before returning.
	if err := waitForSubnetActive(ctx, c, tenantID, projectID, vnetID, subnet.ID, d.Timeout(schema.TimeoutCreate)); err != nil {
		return diag.FromErr(err)
	}

	return resourceSubnetRead(ctx, d, meta)
}

// resourceSubnetRead fetches the current Subnet state from the API and refreshes Terraform state.
// Called before every plan (drift detection) and after every apply.
func resourceSubnetRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 4)
	if len(parts) != 4 {
		return diag.FromErr(fmt.Errorf("invalid Subnet state ID %q: expected 'tenant_id/project_id/vnet_id/subnet_id'", d.Id()))
	}
	tenantID, projectID, vnetID, subnetID := parts[0], parts[1], parts[2], parts[3]

	subnet, err := c.GetSubnet(ctx, tenantID, projectID, vnetID, subnetID)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error reading Subnet %q: %w", subnetID, err))
	}

	// (nil, nil) = HTTP 404 — subnet was deleted outside Terraform.
	if subnet == nil {
		d.SetId("")
		return nil
	}

	var diags diag.Diagnostics
	if err := d.Set("name", subnet.Name); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("cidr", subnet.CIDR); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("gateway", subnet.Gateway); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("description", subnet.Description); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	// Re-set the path params from the parsed state ID.
	if err := d.Set("tenant_id", tenantID); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("project_id", projectID); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("vnet_id", vnetID); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("subnet_uuid", subnetID); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("status", subnet.Status); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("provider_type", subnet.ProviderType); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("message", subnet.Message); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("created_at", subnet.CreatedAt); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("updated_at", subnet.UpdatedAt); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	return diags
}

// resourceSubnetDelete initiates subnet deletion and polls until the API confirms it is gone.
func resourceSubnetDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 4)
	if len(parts) != 4 {
		return diag.FromErr(fmt.Errorf("invalid Subnet state ID %q: expected 'tenant_id/project_id/vnet_id/subnet_id'", d.Id()))
	}
	tenantID, projectID, vnetID, subnetID := parts[0], parts[1], parts[2], parts[3]

	if err := c.DeleteSubnet(ctx, tenantID, projectID, vnetID, subnetID); err != nil {
		// HTTP 409 means NSG attachments still exist; all must be destroyed first.
		if strings.Contains(err.Error(), "HTTP 409") {
			return diag.FromErr(fmt.Errorf(
				"cannot delete Subnet %q: it has active NSG attachments. "+
					"All dcapi_nsg_attachment resources targeting this subnet must be destroyed first. "+
					"Original API error: %w",
				subnetID, err,
			))
		}
		return diag.FromErr(fmt.Errorf("error deleting Subnet %q: %w", subnetID, err))
	}

	// DELETE returned 202. Poll until HTTP 404 confirms the subnet is gone.
	if err := waitForSubnetDeleted(ctx, c, tenantID, projectID, vnetID, subnetID, d.Timeout(schema.TimeoutDelete)); err != nil {
		return diag.FromErr(err)
	}

	return nil
}

// waitForSubnetActive polls until the Subnet reaches status "ACTIVE" or the timeout expires.
func waitForSubnetActive(ctx context.Context, c *client.DCAPIClient, tenantID, projectID, vnetID, subnetID string, timeout time.Duration) error {
	conf := &resource.StateChangeConf{
		Pending:    []string{"PENDING"},
		Target:     []string{"ACTIVE"},
		Timeout:    timeout,
		MinTimeout: 15 * time.Second,
		Refresh: func() (interface{}, string, error) {
			subnet, err := c.GetSubnet(ctx, tenantID, projectID, vnetID, subnetID)
			if err != nil {
				return nil, "", err
			}
			if subnet == nil {
				return nil, "", fmt.Errorf("Subnet %q disappeared while waiting for ACTIVE status", subnetID)
			}
			if subnet.Status == "FAILED" {
				return nil, "FAILED", fmt.Errorf("Subnet %q provisioning failed: %s", subnetID, subnet.Message)
			}
			return subnet, subnet.Status, nil
		},
	}

	_, err := conf.WaitForStateContext(ctx)
	return err
}

// waitForSubnetDeleted polls until the Subnet is gone (HTTP 404) after a DELETE call.
// Deleting the last subnet in a VNet triggers extra cleanup (NAT gateway, CoreDNS teardown),
// which can add 5-10 minutes of latency — hence the 10-minute timeout.
func waitForSubnetDeleted(ctx context.Context, c *client.DCAPIClient, tenantID, projectID, vnetID, subnetID string, timeout time.Duration) error {
	conf := &resource.StateChangeConf{
		Pending:    []string{"ACTIVE", "DELETING"},
		Target:     []string{"DELETED"},
		Timeout:    timeout,
		MinTimeout: 15 * time.Second,
		Refresh: func() (interface{}, string, error) {
			subnet, err := c.GetSubnet(ctx, tenantID, projectID, vnetID, subnetID)
			if err != nil {
				return nil, "", err
			}
			if subnet == nil {
				return "deleted", "DELETED", nil
			}
			return subnet, subnet.Status, nil
		},
	}

	_, err := conf.WaitForStateContext(ctx)
	return err
}
