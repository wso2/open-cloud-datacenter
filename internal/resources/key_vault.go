// Terraform resource definition for dcapi_key_vault.
package resources

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/retry"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// ResourceKeyVault returns the *schema.Resource for "dcapi_key_vault".
func ResourceKeyVault() *schema.Resource {
	return &schema.Resource{
		CreateContext: resourceKeyVaultCreate,
		ReadContext:   resourceKeyVaultRead,
		UpdateContext: resourceKeyVaultUpdate,
		DeleteContext: resourceKeyVaultDelete,

		Timeouts: &schema.ResourceTimeout{
			// KeyVault provisioning is lightweight (OpenBao mount setup); 5 minutes is ample.
			Create: schema.DefaultTimeout(5 * time.Minute),
		},

		Schema: map[string]*schema.Schema{

			// ── Path parameters, Required, ForceNew ───────────────────────────────────────

			"tenant_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Slug of the parent tenant. Used in the API URL path. Immutable.",
			},
			"project_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Slug of the parent project. Used in the API URL path. Immutable.",
			},

			// ── User-supplied, Required/Optional, ForceNew (no update endpoint) ──────────────

			"name": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "The name of the Key Vault. 3-63 chars, DNS label, starts with a letter. Immutable.",
			},

			"soft_delete_days": {
				Type:     schema.TypeInt,
				Optional: true,
				ForceNew: true,
				Default:  30,
				Description: "The number of days that the Key Vault will retain deleted keys, secrets, and " +
					"certificates. Valid values are between 7 and 90 days. Immutable — no policy-update endpoint.",
			},

			// ── Update-only trigger: bump this value to rotate credentials in place ─────────

			"credentials_rotation": {
				Type:     schema.TypeString,
				Optional: true,
				Description: "Arbitrary string the user changes to trigger credential rotation (like " +
					"null_resource triggers), e.g. a date or reason. Changing this value calls " +
					"POST /credentials/rotate and mints a new secret_id; role_id is left unchanged.",
			},

			// ── Computed-only fields (set entirely by the API) ─────────────────────────────

			"status": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Lifecycle status: PENDING | ACTIVE | FAILED. Set by the API.",
			},
			"message": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Human-readable status message. Most useful when status is FAILED.",
			},
			"mount_path": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "OpenBao mount path for this vault, e.g. \"tenant-wso2/prod-secrets\". Empty until ACTIVE.",
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
				Description: "RFC3339 creation timestamp. Set by the API.",
			},
			"updated_at": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "RFC3339 last-update timestamp. Set by the API.",
			},

			// ── Computed, one-time AppRole credentials ─────────────────────────────────────

			"role_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Stable AppRole role_id for this vault. Fetched once at create time; unchanged by credential rotation.",
			},
			"secret_id": {
				Type:      schema.TypeString,
				Computed:  true,
				Sensitive: true,
				Description: "AppRole secret_id. SHOWN ONCE — fetched immediately after the KeyVault becomes " +
					"ACTIVE and stored in state. If lost, bump credentials_rotation to mint a new one; the old " +
					"secret_id is invalidated.",
			},
		},
	}
}

// resourceKeyVaultCreate calls POST .../keyvaults, waits for the vault to become ACTIVE, then
// fetches the one-time AppRole credentials exactly once before syncing final state via Read.
func resourceKeyVaultCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	tenantID := d.Get("tenant_id").(string)
	projectID := d.Get("project_id").(string)

	req := client.KeyVaultCreateRequest{
		Name:           d.Get("name").(string),
		SoftDeleteDays: d.Get("soft_delete_days").(int),
	}

	kv, err := c.CreateKeyVault(ctx, tenantID, projectID, req)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error creating KeyVault: %w", err))
	}

	// Composite ID encodes all three path components needed to rebuild API URLs later.
	d.SetId(fmt.Sprintf("%s/%s/%s", tenantID, projectID, kv.ID))

	var diags diag.Diagnostics
	diags = appendSet(diags, d, "status", kv.Status)
	diags = appendSet(diags, d, "message", kv.Message)
	diags = appendSet(diags, d, "mount_path", kv.MountPath)
	diags = appendSet(diags, d, "endpoint_address", kv.EndpointAddress)
	diags = appendSet(diags, d, "endpoint_port", kv.EndpointPort)
	diags = appendSet(diags, d, "created_at", kv.CreatedAt)
	diags = appendSet(diags, d, "updated_at", kv.UpdatedAt)
	if diags.HasError() {
		return diags
	}

	// Create returned 201, but the KVI operator may still be provisioning the OpenBao mount.
	// Credentials are only meaningful once the vault is ACTIVE — poll before fetching them.
	if err := waitForKeyVaultActive(ctx, c, tenantID, projectID, kv.ID, d.Timeout(schema.TimeoutCreate)); err != nil {
		return diag.FromErr(err)
	}

	// Fetch the one-time AppRole credentials EXACTLY ONCE, immediately, and store secret_id
	// right away — the API will return HTTP 410 Gone on every subsequent call to this endpoint,
	// and there is no way to retrieve secret_id again except by rotating it.
	creds, err := c.GetKeyVaultCredentials(ctx, tenantID, projectID, kv.ID)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error fetching KeyVault credentials: %w", err))
	}
	if err := d.Set("role_id", creds.RoleID); err != nil {
		return diag.FromErr(err)
	}
	if err := d.Set("secret_id", creds.SecretID); err != nil {
		return diag.FromErr(err)
	}

	// Sync final state — status/mount_path/endpoint_* reflect post-provisioning values.
	return resourceKeyVaultRead(ctx, d, meta)
}

// resourceKeyVaultRead fetches current state from the API and refreshes Terraform state.
func resourceKeyVaultRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 3)
	if len(parts) != 3 {
		return diag.FromErr(fmt.Errorf("invalid KeyVault state ID %q: expected 'tenant_id/project_id/keyvault_id'", d.Id()))
	}
	tenantID, projectID, keyVaultID := parts[0], parts[1], parts[2]

	kv, err := c.GetKeyVault(ctx, tenantID, projectID, keyVaultID)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error reading KeyVault %q: %w", keyVaultID, err))
	}
	// (nil, nil) from GetKeyVault means HTTP 404 — deleted outside Terraform.
	if kv == nil {
		d.SetId("")
		return nil
	}

	var diags diag.Diagnostics
	diags = appendSet(diags, d, "tenant_id", tenantID)
	diags = appendSet(diags, d, "project_id", projectID)
	diags = appendSet(diags, d, "name", kv.Name)
	diags = appendSet(diags, d, "soft_delete_days", kv.SoftDeleteDays)
	diags = appendSet(diags, d, "status", kv.Status)
	diags = appendSet(diags, d, "message", kv.Message)
	diags = appendSet(diags, d, "mount_path", kv.MountPath)
	diags = appendSet(diags, d, "endpoint_address", kv.EndpointAddress)
	diags = appendSet(diags, d, "endpoint_port", kv.EndpointPort)
	diags = appendSet(diags, d, "created_at", kv.CreatedAt)
	diags = appendSet(diags, d, "updated_at", kv.UpdatedAt)

	// Preserve the one-time credentials — the GET response never includes them, and
	// re-fetching would hit HTTP 410 Gone. Read back what Create (or a prior rotation) stored.
	diags = appendSet(diags, d, "role_id", d.Get("role_id").(string))
	diags = appendSet(diags, d, "secret_id", d.Get("secret_id").(string))

	return diags
}

// resourceKeyVaultUpdate only reacts to credentials_rotation changes — every other field is
// ForceNew, so this is the sole in-place update path.
func resourceKeyVaultUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 3)
	if len(parts) != 3 {
		return diag.FromErr(fmt.Errorf("invalid KeyVault state ID %q: expected 'tenant_id/project_id/keyvault_id'", d.Id()))
	}
	tenantID, projectID, keyVaultID := parts[0], parts[1], parts[2]

	if !d.HasChange("credentials_rotation") {
		return nil
	}

	creds, err := c.RotateKeyVaultCredentials(ctx, tenantID, projectID, keyVaultID)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error rotating KeyVault credentials for %q: %w", keyVaultID, err))
	}

	var diags diag.Diagnostics

	// role_id is documented as stable across rotation, but we re-set it defensively from the
	// rotate response in case the API ever changes that guarantee.
	diags = appendSet(diags, d, "role_id", creds.RoleID)
	diags = appendSet(diags, d, "secret_id", creds.SecretID)

	return diags
}

// resourceKeyVaultDelete calls DELETE .../keyvaults/{id}. This is synchronous (204) —
// unlike VNet/VM, there is no "DELETING" intermediate status to poll for.
func resourceKeyVaultDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 3)
	if len(parts) != 3 {
		return diag.FromErr(fmt.Errorf("invalid KeyVault state ID %q: expected 'tenant_id/project_id/keyvault_id'", d.Id()))
	}
	tenantID, projectID, keyVaultID := parts[0], parts[1], parts[2]

	if err := c.DeleteKeyVault(ctx, tenantID, projectID, keyVaultID); err != nil {
		return diag.FromErr(fmt.Errorf("error deleting KeyVault %q: %w", keyVaultID, err))
	}
	
	d.SetId("")
	return nil
}

// waitForKeyVaultActive polls GET .../keyvaults/{id} until status reaches "ACTIVE" or the
// timeout expires. Needed even though Create returns 201 (sync), because the KVI operator
// provisions the underlying OpenBao mount asynchronously in the background.
func waitForKeyVaultActive(ctx context.Context, c *client.DCAPIClient, tenantID, projectID, keyVaultID string, timeout time.Duration) error {
	conf := &retry.StateChangeConf{
		Pending:    []string{"PENDING"},
		Target:     []string{"ACTIVE"},
		Timeout:    timeout,
		MinTimeout: 15 * time.Second,
		Refresh: func() (interface{}, string, error) {
			kv, err := c.GetKeyVault(ctx, tenantID, projectID, keyVaultID)
			if err != nil {
				return nil, "", err
			}
			if kv == nil {
				return nil, "", fmt.Errorf("KeyVault %q disappeared while waiting for ACTIVE status", keyVaultID)
			}
			if kv.Status == "FAILED" {
				return nil, "FAILED", fmt.Errorf("KeyVault %q provisioning failed: %s", keyVaultID, kv.Message)
			}
			return kv, kv.Status, nil
		},
	}

	_, err := conf.WaitForStateContext(ctx)
	return err
}
