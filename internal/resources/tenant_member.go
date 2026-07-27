// Terraform resource definition for dcapi_tenant_member.
// Grants a user a role (owner|member|viewer) on a tenant. Every field is ForceNew: the DC-API
// has no Update endpoint for a membership — to change a role, delete the assignment and
// re-invite the user (a destroy+create in Terraform terms).
//
// There is no GET-by-id endpoint either, only List. Read resolves the specific membership by
// scanning ListTenantMembers for a matching principal_id, the same way GetTenantByID scans
// ListTenants in tenant.go.
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

// ResourceTenantMember returns the schema.Resource for "dcapi_tenant_member".
func ResourceTenantMember() *schema.Resource {
	return &schema.Resource{
		// No UpdateContext — every field is ForceNew; role changes require destroy + recreate.
		CreateContext: resourceTenantMemberCreate,
		ReadContext:   resourceTenantMemberRead,
		DeleteContext: resourceTenantMemberDelete,

		Schema: map[string]*schema.Schema{

			// ── Required + immutable ──

			"tenant_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Slug of the tenant this membership is scoped to. Immutable.",
			},
			"user_sub": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "OIDC subject claim of the user to invite (e.g. \"auth0|abc123\"). Becomes principal_id. Immutable.",
			},
			"role": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.StringInSlice([]string{"owner", "member", "viewer"}, false),
				Description:  "Role: owner, member, or viewer. Immutable — delete and re-create to change it.",
			},

			// ── Optional + immutable ──

			"display_alias": {
				Type:        schema.TypeString,
				Optional:    true,
				ForceNew:    true,
				Description: "Human-readable label for the member. Max 256 chars. Immutable.",
			},

			// ── Computed (set by the API) ──

			"member_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "API-generated UUID4 of the role_assignment row.",
			},
			"principal_type": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Principal type, e.g. \"user\".",
			},
			"scope_type": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Scope type of the role assignment, e.g. \"tenant\".",
			},
			"scope_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Scope identifier — the tenant slug.",
			},
			"granted_at": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "RFC3339 timestamp the role was granted. Set by the API.",
			},
			"granted_by": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "OIDC sub of the caller who granted the role.",
			},
		},
	}
}

// resourceTenantMemberCreate calls POST /v1/tenants/{tenant_id}/members and sets the
// composite state ID to "tenant_id/principal_id" — principal_id is what the DELETE path
// needs, and List (used by Read) is scanned by that same field.
func resourceTenantMemberCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	tenantID := d.Get("tenant_id").(string)

	req := client.TenantMemberCreateRequest{
		UserSub:      d.Get("user_sub").(string),
		Role:         d.Get("role").(string),
		DisplayAlias: d.Get("display_alias").(string),
	}

	member, err := c.CreateTenantMember(ctx, tenantID, req)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error creating tenant member: %w", err))
	}

	d.SetId(fmt.Sprintf("%s/%s", tenantID, member.PrincipalID))

	var diags diag.Diagnostics
	diags = appendSet(diags, d, "member_id", member.ID)
	diags = appendSet(diags, d, "principal_type", member.PrincipalType)
	diags = appendSet(diags, d, "scope_type", member.ScopeType)
	diags = appendSet(diags, d, "scope_id", member.ScopeID)
	diags = appendSet(diags, d, "granted_at", member.GrantedAt)
	diags = appendSet(diags, d, "granted_by", member.GrantedBy)
	return diags
}

// resourceTenantMemberRead lists all members of the tenant and finds the one matching this
// resource's principal_id. Sets the state ID to "" (drift) if no match is found.
func resourceTenantMemberRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 2)
	if len(parts) != 2 {
		return diag.FromErr(fmt.Errorf("invalid tenant member state ID %q: expected 'tenant_id/principal_id'", d.Id()))
	}
	tenantID, principalID := parts[0], parts[1]

	members, err := c.ListTenantMembers(ctx, tenantID)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error listing tenant members for tenant %q: %w", tenantID, err))
	}

	var found *client.TenantMemberResponse
	for i := range members {
		if members[i].PrincipalID == principalID {
			found = &members[i]
			break
		}
	}
	// Not found in the list means the membership was revoked outside Terraform.
	if found == nil {
		d.SetId("")
		return nil
	}

	var diags diag.Diagnostics
	diags = appendSet(diags, d, "tenant_id", tenantID)
	diags = appendSet(diags, d, "user_sub", found.PrincipalID)
	diags = appendSet(diags, d, "role", found.Role)
	diags = appendSet(diags, d, "display_alias", found.DisplayAlias)
	diags = appendSet(diags, d, "member_id", found.ID)
	diags = appendSet(diags, d, "principal_type", found.PrincipalType)
	diags = appendSet(diags, d, "scope_type", found.ScopeType)
	diags = appendSet(diags, d, "scope_id", found.ScopeID)
	diags = appendSet(diags, d, "granted_at", found.GrantedAt)
	diags = appendSet(diags, d, "granted_by", found.GrantedBy)
	return diags
}

// resourceTenantMemberDelete calls DELETE /v1/tenants/{tenant_id}/members/{principal_id}.
func resourceTenantMemberDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 2)
	if len(parts) != 2 {
		return diag.FromErr(fmt.Errorf("invalid tenant member state ID %q: expected 'tenant_id/principal_id'", d.Id()))
	}
	tenantID, principalID := parts[0], parts[1]

	if err := c.DeleteTenantMember(ctx, tenantID, principalID); err != nil {
		return diag.FromErr(fmt.Errorf("error deleting tenant member %q: %w", principalID, err))
	}
	d.SetId("")
	return nil
}
