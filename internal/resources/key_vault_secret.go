// Terraform resource definition for dcapi_key_vault_secret.
package resources

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
	"terraform-provider-dcapi/internal/client"
)

// secretKeyPattern matches the DC-API's constraint on KeyVault secret keys.
var secretKeyPattern = regexp.MustCompile(`^[a-z0-9._-]{1,256}$`)

// ResourceKeyVaultSecret returns the *schema.Resource for "dcapi_key_vault_secret".
func ResourceKeyVaultSecret() *schema.Resource {
	return &schema.Resource{
		CreateContext: resourceKeyVaultSecretCreate,
		ReadContext:   resourceKeyVaultSecretRead,
		UpdateContext: resourceKeyVaultSecretUpdate,
		DeleteContext: resourceKeyVaultSecretDelete,

		Schema: map[string]*schema.Schema{

			// ── Path parameters, Required, ForceNew ───────────────────────────────────────

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
			"key_vault_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "UUID of the parent KeyVault. The vault must be ACTIVE. Immutable.",
			},

			// ── Identity, Required, ForceNew (part of the API path) ──────────────────────────

			"key": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.StringMatch(secretKeyPattern, "must match ^[a-z0-9._-]{1,256}$"),
				Description:  "Secret key name. Pattern: ^[a-z0-9._-]{1,256}$. Immutable — changing it creates a different secret.",
			},

			// ── User-supplied, updatable via full-replace PUT ────────────────────────────────

			"value": {
				Type:        schema.TypeString,
				Required:    true,
				Sensitive:   true,
				Description: "Secret value (UTF-8, max 1 MiB). Updatable — writes bump the version.",
			},
			"metadata": {
				Type:        schema.TypeMap,
				Optional:    true,
				Elem:        &schema.Schema{Type: schema.TypeString},
				Description: "Arbitrary string metadata attached to the secret (max 64 entries). Full-replace on update.",
			},

			// ── Computed-only fields (set entirely by the API) ─────────────────────────────

			"version": {
				Type:        schema.TypeInt,
				Computed:    true,
				Description: "Version number, incremented by every write. Set by the API.",
			},
			"created_at": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "RFC3339 creation timestamp of the current version. Set by the API.",
			},
		},
	}
}

// resourceKeyVaultSecretCreate calls PUT .../secrets/{key}, which upserts the key.
func resourceKeyVaultSecretCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	tenantID := d.Get("tenant_id").(string)
	projectID := d.Get("project_id").(string)
	keyVaultID := d.Get("key_vault_id").(string)
	key := d.Get("key").(string)

	req := client.KeyVaultSecretWriteRequest{
		Value:    d.Get("value").(string),
		Metadata: expandKeyVaultSecretMetadata(d.Get("metadata").(map[string]interface{})),
	}

	secret, err := c.WriteKeyVaultSecret(ctx, tenantID, projectID, keyVaultID, key, req)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error writing KeyVault secret %q: %w", key, err))
	}

	// Composite ID encodes all four path components needed to rebuild API URLs later.
	d.SetId(fmt.Sprintf("%s/%s/%s/%s", tenantID, projectID, keyVaultID, key))

	var diags diag.Diagnostics
	diags = appendSet(diags, d, "version", secret.Version)
	diags = appendSet(diags, d, "created_at", secret.CreatedAt)
	return diags
}

// resourceKeyVaultSecretRead fetches the current value/version and refreshes Terraform state.
func resourceKeyVaultSecretRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 4)
	if len(parts) != 4 {
		return diag.FromErr(fmt.Errorf("invalid KeyVault secret state ID %q: expected 'tenant_id/project_id/key_vault_id/key'", d.Id()))
	}
	tenantID, projectID, keyVaultID, key := parts[0], parts[1], parts[2], parts[3]

	secret, err := c.GetKeyVaultSecret(ctx, tenantID, projectID, keyVaultID, key)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error reading KeyVault secret %q: %w", key, err))
	}
	if secret == nil {
		d.SetId("")
		return nil
	}

	var diags diag.Diagnostics
	diags = appendSet(diags, d, "tenant_id", tenantID)
	diags = appendSet(diags, d, "project_id", projectID)
	diags = appendSet(diags, d, "key_vault_id", keyVaultID)
	diags = appendSet(diags, d, "key", secret.Key)
	diags = appendSet(diags, d, "value", secret.Value)
	diags = appendSet(diags, d, "metadata", flattenKeyVaultSecretMetadata(secret.Metadata))
	diags = appendSet(diags, d, "version", secret.Version)
	diags = appendSet(diags, d, "created_at", secret.CreatedAt)
	return diags
}

// resourceKeyVaultSecretUpdate re-sends the full desired value/metadata via the same upsert
// PUT that Create uses — there is no partial-update endpoint.
func resourceKeyVaultSecretUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 4)
	if len(parts) != 4 {
		return diag.FromErr(fmt.Errorf("invalid KeyVault secret state ID %q: expected 'tenant_id/project_id/key_vault_id/key'", d.Id()))
	}
	tenantID, projectID, keyVaultID, key := parts[0], parts[1], parts[2], parts[3]

	req := client.KeyVaultSecretWriteRequest{
		Value:    d.Get("value").(string),
		Metadata: expandKeyVaultSecretMetadata(d.Get("metadata").(map[string]interface{})),
	}

	secret, err := c.WriteKeyVaultSecret(ctx, tenantID, projectID, keyVaultID, key, req)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error updating KeyVault secret %q: %w", key, err))
	}

	var diags diag.Diagnostics
	diags = appendSet(diags, d, "version", secret.Version)
	diags = appendSet(diags, d, "created_at", secret.CreatedAt)
	return diags
}

// resourceKeyVaultSecretDelete calls DELETE .../secrets/{key}. The API soft-deletes the key;
// this resource does not expose a restore path, so the delete is treated as final.
func resourceKeyVaultSecretDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 4)
	if len(parts) != 4 {
		return diag.FromErr(fmt.Errorf("invalid KeyVault secret state ID %q: expected 'tenant_id/project_id/key_vault_id/key'", d.Id()))
	}
	tenantID, projectID, keyVaultID, key := parts[0], parts[1], parts[2], parts[3]

	if err := c.DeleteKeyVaultSecret(ctx, tenantID, projectID, keyVaultID, key); err != nil {
		return diag.FromErr(fmt.Errorf("error deleting KeyVault secret %q: %w", key, err))
	}
	d.SetId("")
	return nil
}

// expandKeyVaultSecretMetadata converts a Terraform map to map[string]string.
func expandKeyVaultSecretMetadata(raw map[string]interface{}) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	metadata := make(map[string]string, len(raw))
	for k, v := range raw {
		metadata[k] = v.(string)
	}
	return metadata
}

// flattenKeyVaultSecretMetadata converts map[string]string to a Terraform-compatible map.
func flattenKeyVaultSecretMetadata(metadata map[string]string) map[string]interface{} {
	result := make(map[string]interface{}, len(metadata))
	for k, v := range metadata {
		result[k] = v
	}
	return result
}
