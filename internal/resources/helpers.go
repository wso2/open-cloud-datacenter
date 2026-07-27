// Shared helpers used across multiple resource files in this package.
package resources

import (
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// appendSet calls d.Set and appends any error into the diagnostics slice.
func appendSet(diags diag.Diagnostics, d *schema.ResourceData, key string, val interface{}) diag.Diagnostics {
	if err := d.Set(key, val); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	return diags
}
