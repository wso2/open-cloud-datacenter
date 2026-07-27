// Shared helpers used across multiple data source files in this package.
// Deliberately duplicated from internal/resources/helpers.go rather than shared via a new
// internal/common package — it is six lines, and a shared package would only add an import
// indirection between two otherwise-independent packages.
package datasources

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
