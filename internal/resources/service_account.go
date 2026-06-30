// Terraform resource definition for dcapi_service_account.
// Service accounts are project-scoped and fully immutable after creation (no update endpoint).
// The token field is a shown-once secret — stored in state at create time and preserved on
// subsequent reads because the GET response never includes it.
package resources

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
	"terraform-provider-dcapi/internal/client"
)

// ResourceServiceAccount returns the schema.Resource for "dcapi_service_account".
func ResourceServiceAccount() *schema.Resource {
	return &schema.Resource{
		// No UpdateContext — every field is ForceNew; changes require destroy + recreate.
		CreateContext: resourceServiceAccountCreate,
		ReadContext:   resourceServiceAccountRead,
		DeleteContext: resourceServiceAccountDelete,

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
				Description: "Service account name. Pattern: [a-z0-9][a-z0-9-]{0,61}[a-z0-9]. Immutable.",
			},
			"role": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.StringInSlice([]string{"owner", "member", "viewer"}, false),
				Description:  "Role: owner, member, or viewer. Immutable.",
			},

			// ── Optional + immutable ──

			"description": {
				Type:        schema.TypeString,
				Optional:    true,
				ForceNew:    true,
				Description: "Optional description. Max 256 chars. Immutable after creation.",
			},

			// ── Computed (set by the API) ──

			"sa_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "API-generated UUID4 for the service account.",
			},
			"created_at": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "RFC3339 creation timestamp. Set by the API.",
			},
			"last_used": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "RFC3339 timestamp of last authentication. Empty if never used.",
			},

			// ── Computed + Sensitive (shown-once secret) ──

			"token": {
				Type:      schema.TypeString,
				Computed:  true,
				Sensitive: true,
				Description: "Bearer token for this service account (format: dcapi_sa_<id>_<secret>). " +
					"SHOWN ONCE at creation — stored in state. If lost, delete and recreate the service account.",
			},
		},
	}
}

// resourceServiceAccountCreate calls POST /v1/tenants/{tenant_id}/projects/{project_id}/service-accounts,
// stores the one-time token in state, then sets the composite state ID.
func resourceServiceAccountCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	tenantID := d.Get("tenant_id").(string)
	projectID := d.Get("project_id").(string)

	req := client.ServiceAccountCreateRequest{
		Name:        d.Get("name").(string),
		Role:        d.Get("role").(string),
		Description: d.Get("description").(string),
	}

	sa, err := c.CreateServiceAccount(ctx, tenantID, projectID, req)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error creating service account: %w", err))
	}

	// State ID encodes all three slugs needed to rebuild the API URL on Read/Delete.
	d.SetId(fmt.Sprintf("%s/%s/%s", tenantID, projectID, sa.ID))

	var diags diag.Diagnostics
	if err := d.Set("sa_id", sa.ID); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("created_at", sa.CreatedAt); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	// Store the token immediately — the API will never return it again.
	if err := d.Set("token", sa.Token); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	return diags
}

// resourceServiceAccountRead fetches current state and refreshes Terraform state.
// It does NOT overwrite the token field — the GET response never includes it.
func resourceServiceAccountRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 3)
	if len(parts) != 3 {
		return diag.FromErr(fmt.Errorf("invalid service account state ID %q: expected 'tenant_id/project_id/sa_id'", d.Id()))
	}
	tenantID, projectID, saID := parts[0], parts[1], parts[2]

	sa, err := c.GetServiceAccount(ctx, tenantID, projectID, saID)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error reading service account %q: %w", saID, err))
	}
	// nil, nil from GetServiceAccount means HTTP 404 — deleted outside Terraform.
	if sa == nil {
		d.SetId("")
		return nil
	}

	var diags diag.Diagnostics
	if err := d.Set("sa_id", sa.ID); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("tenant_id", sa.TenantID); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("name", sa.Name); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("role", sa.Role); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("description", sa.Description); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("created_at", sa.CreatedAt); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}

	lastUsed := ""
	if sa.LastUsed != nil {
		lastUsed = *sa.LastUsed
	}
	if err := d.Set("last_used", lastUsed); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}

	// Preserve the token from state — the GET response never includes it.
	// Reading the existing state value and writing it back keeps it unchanged.
	if err := d.Set("token", d.Get("token").(string)); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}

	return diags
}

// resourceServiceAccountDelete calls DELETE /v1/tenants/{tenant_id}/projects/{project_id}/service-accounts/{sa_id}.
func resourceServiceAccountDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 3)
	if len(parts) != 3 {
		return diag.FromErr(fmt.Errorf("invalid service account state ID %q: expected 'tenant_id/project_id/sa_id'", d.Id()))
	}
	tenantID, projectID, saID := parts[0], parts[1], parts[2]

	if err := c.DeleteServiceAccount(ctx, tenantID, projectID, saID); err != nil {
		return diag.FromErr(fmt.Errorf("error deleting service account %q: %w", saID, err))
	}
	d.SetId("")
	return nil
}
