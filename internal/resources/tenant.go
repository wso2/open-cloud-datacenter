// Terraform resource definition for dcapi_tenant.
// Calls internal/client/tenant.go for API calls; knows nothing about HTTP internals.
package resources

import (
	"context"
	"fmt"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// ResourceTenant returns the schema.Resource for "dcapi_tenant".
func ResourceTenant() *schema.Resource {
	return &schema.Resource{
		CreateContext: resourceTenantCreate,
		ReadContext:   resourceTenantRead,
		UpdateContext: resourceTenantUpdate,
		DeleteContext: resourceTenantDelete,

		Schema: map[string]*schema.Schema{

			// ── Required + immutable ──

			// tenant_id is the user-defined slug used as the Terraform state ID and in API paths.
			"tenant_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Unique slug identifier for the tenant. Immutable after creation.",
			},

			// ── Optional + immutable (no update endpoint for these fields) ──

			"name": {
				Type:        schema.TypeString,
				Optional:    true,
				ForceNew:    true,
				Description: "Human-readable display name. Immutable after creation.",
			},
			"description": {
				Type:        schema.TypeString,
				Optional:    true,
				ForceNew:    true,
				Description: "Free-text description. Immutable after creation.",
			},

			// ── Optional + updatable (PATCH /v1/admin/tenants/{id} changes these) ──
			// Default: 0 tells the API to apply the platform default (80/256/2000).

			"cpu_cores_cap": {
				Type:        schema.TypeInt,
				Optional:    true,
				Default:     0,
				Description: "CPU core quota ceiling. 0 = platform default (80). Updatable.",
			},
			"memory_gb_cap": {
				Type:        schema.TypeInt,
				Optional:    true,
				Default:     0,
				Description: "Memory quota ceiling in GB. 0 = platform default (256). Updatable.",
			},
			"storage_gb_cap": {
				Type:        schema.TypeInt,
				Optional:    true,
				Default:     0,
				Description: "Storage quota ceiling in GB. 0 = platform default (2000). Updatable.",
			},

			// ── Computed-only (set entirely by the API) ──

			"tenant_uuid": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "API-generated UUID4 for the tenant. Immutable.",
			},
			"asgardeo_group": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Asgardeo group derived by the API as 'dc-tenant-<id>'. Read-only.",
			},
			"created_at": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "RFC3339 creation timestamp. Set by the API.",
			},
			"created_by": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "OIDC sub of the caller who created the tenant. Set by the API.",
			},
		},
	}
}

// resourceTenantCreate calls POST /v1/admin/tenants, sets the state ID to the tenant slug,
// and stores all API-returned fields (including resolved cap defaults) in state.
func resourceTenantCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	req := client.TenantCreateRequest{
		ID:           d.Get("tenant_id").(string),
		Name:         d.Get("name").(string),
		Description:  d.Get("description").(string),
		CPUCoresCap:  d.Get("cpu_cores_cap").(int),
		MemoryGBCap:  d.Get("memory_gb_cap").(int),
		StorageGBCap: d.Get("storage_gb_cap").(int),
	}

	tenant, err := c.CreateTenant(ctx, req)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error creating tenant: %w", err))
	}

	// Use the slug as state ID — the DC-API uses it in all subsequent API paths.
	d.SetId(tenant.ID)

	var diags diag.Diagnostics
	if err := d.Set("tenant_uuid", tenant.TenantUUID); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("asgardeo_group", tenant.AsgardeoGroup); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("created_at", tenant.CreatedAt); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("created_by", tenant.CreatedBy); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	// Store resolved cap values — the API substitutes platform defaults when 0 was sent.
	if err := d.Set("cpu_cores_cap", tenant.CPUCoresCap); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("memory_gb_cap", tenant.MemoryGBCap); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("storage_gb_cap", tenant.StorageGBCap); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	return diags
}

// resourceTenantRead refreshes state from the API before every plan.
// Calls d.SetId("") and returns nil when the tenant is not found (external drift).
func resourceTenantRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)
	tenantID := d.Id()

	tenant, err := c.GetTenantByID(ctx, tenantID)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error reading tenant %q: %w", tenantID, err))
	}
	// nil, nil from GetTenantByID means not found — signal drift to Terraform.
	if tenant == nil {
		d.SetId("")
		return nil
	}

	var diags diag.Diagnostics
	if err := d.Set("name", tenant.Name); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("description", tenant.Description); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("cpu_cores_cap", tenant.CPUCoresCap); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("memory_gb_cap", tenant.MemoryGBCap); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("storage_gb_cap", tenant.StorageGBCap); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("tenant_uuid", tenant.TenantUUID); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("asgardeo_group", tenant.AsgardeoGroup); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("created_at", tenant.CreatedAt); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("created_by", tenant.CreatedBy); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	return diags
}

// resourceTenantUpdate sends PATCH with only the cap fields that changed.
// Terraform never calls this for ForceNew fields (tenant_id, name, description).
func resourceTenantUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)
	tenantID := d.Id()

	req := client.TenantUpdateRequest{}
	if d.HasChange("cpu_cores_cap") {
		v := d.Get("cpu_cores_cap").(int)
		req.CPUCoresCap = &v
	}
	if d.HasChange("memory_gb_cap") {
		v := d.Get("memory_gb_cap").(int)
		req.MemoryGBCap = &v
	}
	if d.HasChange("storage_gb_cap") {
		v := d.Get("storage_gb_cap").(int)
		req.StorageGBCap = &v
	}

	tenant, err := c.UpdateTenant(ctx, tenantID, req)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error updating tenant %q: %w", tenantID, err))
	}

	var diags diag.Diagnostics
	if err := d.Set("cpu_cores_cap", tenant.CPUCoresCap); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("memory_gb_cap", tenant.MemoryGBCap); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("storage_gb_cap", tenant.StorageGBCap); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	return diags
}

// resourceTenantDelete removes the resource from Terraform state only.
// The DC-API has no delete endpoint for tenants — the real tenant continues to exist.
func resourceTenantDelete(_ context.Context, d *schema.ResourceData, _ interface{}) diag.Diagnostics {
	d.SetId("")
	return nil
}
