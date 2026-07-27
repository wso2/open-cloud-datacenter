// Terraform data source definition for dcapi_tenant.
// Looks up a tenant that is not managed by this Terraform config — e.g. one provisioned by
// platform admins or a separate root module. Read-only: no Create/Update/Delete.
//
// The tenant's "id" is itself the human-chosen slug (not an opaque UUID), so unlike
// vnet/subnet/etc. this data source needs no List+filter-by-name — it reuses the same
// GetTenantByID the dcapi_tenant resource's Read uses internally.
package datasources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// DataSourceTenant returns the schema.Resource for "dcapi_tenant" data source lookups.
func DataSourceTenant() *schema.Resource {
	return &schema.Resource{
		ReadContext: dataSourceTenantRead,

		Schema: map[string]*schema.Schema{

			"id": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "Slug of the tenant to look up.",
			},

			"name": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Human-readable display name.",
			},
			"description": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Free-text description.",
			},
			"cpu_cores_cap": {
				Type:        schema.TypeInt,
				Computed:    true,
				Description: "CPU core quota ceiling.",
			},
			"memory_gb_cap": {
				Type:        schema.TypeInt,
				Computed:    true,
				Description: "Memory quota ceiling in GB.",
			},
			"storage_gb_cap": {
				Type:        schema.TypeInt,
				Computed:    true,
				Description: "Storage quota ceiling in GB.",
			},
			"tenant_uuid": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "API-generated UUID4 for the tenant.",
			},
			"asgardeo_group": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Asgardeo group derived by the API as 'dc-tenant-<id>'.",
			},
			"created_at": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "RFC3339 creation timestamp.",
			},
			"created_by": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "OIDC sub of the caller who created the tenant.",
			},
			"roles": {
				Type:        schema.TypeList,
				Computed:    true,
				Elem:        &schema.Schema{Type: schema.TypeString},
				Description: "Roles the calling principal holds on this tenant (e.g. \"owner\", \"member\", \"viewer\").",
			},
		},
	}
}

func dataSourceTenantRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)
	id := d.Get("id").(string)

	tenant, err := c.GetTenantByID(ctx, id)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error looking up tenant %q: %w", id, err))
	}
	if tenant == nil {
		return diag.Errorf("no tenant found with id %q", id)
	}

	d.SetId(tenant.ID)

	var diags diag.Diagnostics
	diags = appendSet(diags, d, "name", tenant.Name)
	diags = appendSet(diags, d, "description", tenant.Description)
	diags = appendSet(diags, d, "cpu_cores_cap", tenant.CPUCoresCap)
	diags = appendSet(diags, d, "memory_gb_cap", tenant.MemoryGBCap)
	diags = appendSet(diags, d, "storage_gb_cap", tenant.StorageGBCap)
	diags = appendSet(diags, d, "tenant_uuid", tenant.TenantUUID)
	diags = appendSet(diags, d, "asgardeo_group", tenant.AsgardeoGroup)
	diags = appendSet(diags, d, "created_at", tenant.CreatedAt)
	diags = appendSet(diags, d, "created_by", tenant.CreatedBy)
	diags = appendSet(diags, d, "roles", tenant.Roles)
	return diags
}
