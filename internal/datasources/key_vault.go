// Terraform data source definition for dcapi_key_vault.
// Looks up a KeyVault that is not managed by this Terraform config, by name, since its API
// UUID is opaque and unknown to the caller ahead of time. Credentials (role_id/secret_id)
// are deliberately NOT exposed — they are shown-once secrets fetched via a separate
// endpoint at Create time, and a data source has no create step to fetch them from.
package datasources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// DataSourceKeyVault returns the schema.Resource for "dcapi_key_vault" data source lookups.
func DataSourceKeyVault() *schema.Resource {
	return &schema.Resource{
		ReadContext: dataSourceKeyVaultRead,

		Schema: map[string]*schema.Schema{

			"tenant_id": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "Slug of the parent tenant.",
			},
			"project_id": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "Slug of the parent project.",
			},
			"name": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "Name of the Key Vault to look up.",
			},

			// kv_uuid is not exposed on the dcapi_key_vault RESOURCE (unlike vnet_uuid /
			// subnet_uuid on their resources) — it is added here because private_endpoint's
			// kv_id path parameter needs the bare UUID, and a data source lookup is exactly
			// the situation where callers don't already have it.
			"kv_uuid": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "API-assigned UUID of this Key Vault. Use this when setting kv_id on dcapi_private_endpoint.",
			},
			"soft_delete_days": {
				Type:        schema.TypeInt,
				Computed:    true,
				Description: "Number of days deleted keys/secrets/certificates are retained.",
			},
			"status": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Lifecycle status: PENDING | ACTIVE | FAILED.",
			},
			"message": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Human-readable status message.",
			},
			"mount_path": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "OpenBao mount path for this vault. Empty until ACTIVE.",
			},
			"endpoint_address": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "In-cluster OpenBao address. Empty until ACTIVE.",
			},
			"endpoint_port": {
				Type:        schema.TypeInt,
				Computed:    true,
				Description: "In-cluster OpenBao port (typically 8200). Empty until ACTIVE.",
			},
			"created_at": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "RFC3339 creation timestamp.",
			},
			"updated_at": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "RFC3339 last-update timestamp.",
			},
		},
	}
}

func dataSourceKeyVaultRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)
	tenantID := d.Get("tenant_id").(string)
	projectID := d.Get("project_id").(string)
	name := d.Get("name").(string)

	kvs, err := c.ListKeyVaults(ctx, tenantID, projectID)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error listing Key Vaults in project %q: %w", projectID, err))
	}

	for _, kv := range kvs {
		if kv.Name != name {
			continue
		}

		d.SetId(fmt.Sprintf("%s/%s/%s", tenantID, projectID, kv.ID))

		var diags diag.Diagnostics
		diags = appendSet(diags, d, "kv_uuid", kv.ID)
		diags = appendSet(diags, d, "soft_delete_days", kv.SoftDeleteDays)
		diags = appendSet(diags, d, "status", kv.Status)
		diags = appendSet(diags, d, "message", kv.Message)
		diags = appendSet(diags, d, "mount_path", kv.MountPath)
		diags = appendSet(diags, d, "endpoint_address", kv.EndpointAddress)
		diags = appendSet(diags, d, "endpoint_port", kv.EndpointPort)
		diags = appendSet(diags, d, "created_at", kv.CreatedAt)
		diags = appendSet(diags, d, "updated_at", kv.UpdatedAt)
		return diags
	}

	return diag.Errorf("no Key Vault named %q found in project %q", name, projectID)
}
