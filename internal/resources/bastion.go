// Terraform resource definition for dcapi_bastion.
// Bastions are async (202 create, poll until ACTIVE) and fully immutable after creation.
// Like VMs, they return private_key and console_password exactly once in the create response —
// Read preserves these from state rather than overwriting with empty strings.
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

// ResourceBastion returns the schema.Resource for "dcapi_bastion".
func ResourceBastion() *schema.Resource {
	return &schema.Resource{
		// No UpdateContext — every field is ForceNew; changes require destroy + recreate.
		CreateContext: resourceBastionCreate,
		ReadContext:   resourceBastionRead,
		DeleteContext: resourceBastionDelete,

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(15 * time.Minute),
			Delete: schema.DefaultTimeout(10 * time.Minute),
		},

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
			"name": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Bastion name. Max 63 characters. Immutable.",
			},
			"vnet_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "UUID of the VNet this bastion is deployed in. Immutable.",
			},
			"subnet_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "UUID of the Subnet this bastion is attached to. Immutable.",
			},

			// ── Optional + immutable ──

			"description": {
				Type:        schema.TypeString,
				Optional:    true,
				ForceNew:    true,
				Description: "Optional description. Max 256 chars. Immutable after creation.",
			},

			// ── Computed (set by the API) ──

			"bastion_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "API-generated UUID4 for the bastion.",
			},
			"status": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Lifecycle status: PENDING | ACTIVE | FAILED. Set by the API.",
			},
			"provider_type": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Underlying infrastructure provider (e.g. \"harvester\"). Set by the API.",
			},
			"mgmt_ip": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Management-plane IP address. Empty until ACTIVE; populated by the API.",
			},
			"internal_ip": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "VPC-side IP address. Empty until ACTIVE; populated by the API.",
			},
			"message": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Human-readable status message. Useful for debugging FAILED state.",
			},
			"created_at": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "RFC3339 creation timestamp. Set by the API.",
			},

			// ── Computed + Sensitive (shown-once secrets) ──

			"private_key": {
				Type:      schema.TypeString,
				Computed:  true,
				Sensitive: true,
				Description: "SSH private key (PEM). SHOWN ONCE at creation — stored in state. " +
					"If state is lost, the bastion must be deleted and recreated.",
			},
			"console_password": {
				Type:      schema.TypeString,
				Computed:  true,
				Sensitive: true,
				Description: "Web-console password. SHOWN ONCE at creation — stored in state. " +
					"If state is lost, the bastion must be deleted and recreated.",
			},
		},
	}
}

// resourceBastionCreate calls POST, stores the one-time secrets, polls until ACTIVE,
// then syncs final state (mgmt_ip and internal_ip are only populated once ACTIVE).
func resourceBastionCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	tenantID := d.Get("tenant_id").(string)
	projectID := d.Get("project_id").(string)

	req := client.BastionCreateRequest{
		Name:        d.Get("name").(string),
		VNetID:      d.Get("vnet_id").(string),
		SubnetID:    d.Get("subnet_id").(string),
		Description: d.Get("description").(string),
	}

	resp, err := c.CreateBastion(ctx, tenantID, projectID, req)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error creating bastion: %w", err))
	}
	if resp.Resource == nil {
		return diag.FromErr(fmt.Errorf("CreateBastion: API response missing 'resource' object"))
	}

	// State ID: tenant_id/project_id/bastion_uuid
	d.SetId(fmt.Sprintf("%s/%s/%s", tenantID, projectID, resp.Resource.ID))

	var diags diag.Diagnostics
	if err := d.Set("bastion_id", resp.Resource.ID); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("status", resp.Resource.Status); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("provider_type", resp.Resource.ProviderType); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("mgmt_ip", resp.Resource.MgmtIP); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("internal_ip", resp.Resource.InternalIP); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("message", resp.Resource.Message); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("created_at", resp.Resource.CreatedAt); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	// Store the shown-once secrets before polling — if polling times out the secrets are not lost.
	if err := d.Set("private_key", resp.PrivateKey); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("console_password", resp.ConsolePassword); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if diags.HasError() {
		return diags
	}

	if err := waitForBastionActive(ctx, c, tenantID, projectID, resp.Resource.ID, d.Timeout(schema.TimeoutCreate)); err != nil {
		return diag.FromErr(err)
	}

	// Sync final state — mgmt_ip and internal_ip are populated once ACTIVE.
	return resourceBastionRead(ctx, d, meta)
}

// resourceBastionRead fetches current state and refreshes Terraform state.
// It does NOT overwrite private_key or console_password — the GET response never includes them.
func resourceBastionRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 3)
	if len(parts) != 3 {
		return diag.FromErr(fmt.Errorf("invalid bastion state ID %q: expected 'tenant_id/project_id/bastion_id'", d.Id()))
	}
	tenantID, projectID, bastionID := parts[0], parts[1], parts[2]

	bastion, err := c.GetBastion(ctx, tenantID, projectID, bastionID)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error reading bastion %q: %w", bastionID, err))
	}
	if bastion == nil {
		d.SetId("")
		return nil
	}

	var diags diag.Diagnostics
	if err := d.Set("bastion_id", bastion.ID); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("tenant_id", bastion.TenantID); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("name", bastion.Name); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("vnet_id", bastion.VNetID); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("subnet_id", bastion.SubnetID); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("description", bastion.Description); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("status", bastion.Status); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("provider_type", bastion.ProviderType); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("mgmt_ip", bastion.MgmtIP); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("internal_ip", bastion.InternalIP); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("message", bastion.Message); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("created_at", bastion.CreatedAt); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}

	// Preserve the shown-once secrets — the GET response never includes them.
	existingPrivateKey := d.Get("private_key").(string)
	existingConsolePassword := d.Get("console_password").(string)
	if err := d.Set("private_key", existingPrivateKey); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("console_password", existingConsolePassword); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}

	return diags
}

// resourceBastionDelete initiates async deletion and polls until the API confirms removal.
func resourceBastionDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 3)
	if len(parts) != 3 {
		return diag.FromErr(fmt.Errorf("invalid bastion state ID %q: expected 'tenant_id/project_id/bastion_id'", d.Id()))
	}
	tenantID, projectID, bastionID := parts[0], parts[1], parts[2]

	if err := c.DeleteBastion(ctx, tenantID, projectID, bastionID); err != nil {
		return diag.FromErr(fmt.Errorf("error deleting bastion %q: %w", bastionID, err))
	}

	if err := waitForBastionDeleted(ctx, c, tenantID, projectID, bastionID, d.Timeout(schema.TimeoutDelete)); err != nil {
		return diag.FromErr(err)
	}

	return nil
}

// waitForBastionActive polls until the bastion reaches status "ACTIVE" or the timeout expires.
func waitForBastionActive(ctx context.Context, c *client.DCAPIClient, tenantID, projectID, bastionID string, timeout time.Duration) error {
	conf := &resource.StateChangeConf{
		Pending:    []string{"PENDING"},
		Target:     []string{"ACTIVE"},
		Timeout:    timeout,
		MinTimeout: 15 * time.Second,
		Refresh: func() (interface{}, string, error) {
			bastion, err := c.GetBastion(ctx, tenantID, projectID, bastionID)
			if err != nil {
				return nil, "", err
			}
			if bastion == nil {
				return nil, "", fmt.Errorf("bastion %q disappeared while waiting for ACTIVE status", bastionID)
			}
			if bastion.Status == "FAILED" {
				return nil, "FAILED", fmt.Errorf("bastion %q provisioning failed: %s", bastionID, bastion.Message)
			}
			return bastion, bastion.Status, nil
		},
	}
	_, err := conf.WaitForStateContext(ctx)
	return err
}

// waitForBastionDeleted polls until the bastion is gone (HTTP 404) after an async DELETE.
func waitForBastionDeleted(ctx context.Context, c *client.DCAPIClient, tenantID, projectID, bastionID string, timeout time.Duration) error {
	conf := &resource.StateChangeConf{
		Pending:    []string{"ACTIVE", "DELETING"},
		Target:     []string{"DELETED"},
		Timeout:    timeout,
		MinTimeout: 15 * time.Second,
		Refresh: func() (interface{}, string, error) {
			bastion, err := c.GetBastion(ctx, tenantID, projectID, bastionID)
			if err != nil {
				return nil, "", err
			}
			if bastion == nil {
				return "deleted", "DELETED", nil
			}
			return bastion, bastion.Status, nil
		},
	}
	_, err := conf.WaitForStateContext(ctx)
	return err
}
