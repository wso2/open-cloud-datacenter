// Terraform data source definition for dcapi_image.
// Images are registered once by platform operators under a tenant and referenced by many
// VMs — this looks one up by its human-readable display_name to obtain the composite id
// needed for dcapi_virtual_machine's image_name field.
package datasources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// DataSourceImage returns the schema.Resource for "dcapi_image" data source lookups.
func DataSourceImage() *schema.Resource {
	return &schema.Resource{
		ReadContext: dataSourceImageRead,

		Schema: map[string]*schema.Schema{

			"tenant_id": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "Slug of the parent tenant.",
			},
			"display_name": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "Human-readable name of the image to look up (e.g. \"Ubuntu 22.04\").",
			},

			"image_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Composite \"namespace/resource-name\" identifier. Pass this as image_name on dcapi_virtual_machine.",
			},
			"namespace": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Namespace the image is registered under.",
			},
		},
	}
}

func dataSourceImageRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)
	tenantID := d.Get("tenant_id").(string)
	displayName := d.Get("display_name").(string)

	images, err := c.ListImages(ctx, tenantID)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error listing images in tenant %q: %w", tenantID, err))
	}

	for _, image := range images {
		if image.DisplayName != displayName {
			continue
		}

		d.SetId(fmt.Sprintf("%s/%s", tenantID, image.ID))

		var diags diag.Diagnostics
		diags = appendSet(diags, d, "image_id", image.ID)
		diags = appendSet(diags, d, "namespace", image.Namespace)
		return diags
	}

	return diag.Errorf("no image named %q found in tenant %q", displayName, tenantID)
}
