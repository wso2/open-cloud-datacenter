package resources

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// ResourceNSGAttachment returns the schema.Resource for "dcapi_nsg_attachment".
//
// The DC-API has no dedicated GET endpoint for attachments; their state is embedded in the
// NSG GET response. Read therefore calls GetNSG and scans the attachments list for the
// stored attachment ID. If the NSG is gone, the attachment is implicitly gone.
func ResourceNSGAttachment() *schema.Resource {
	return &schema.Resource{
		CreateContext: resourceNSGAttachmentCreate,
		ReadContext:   resourceNSGAttachmentRead,
		DeleteContext: resourceNSGAttachmentDelete,

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
			"sg_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "UUID of the NetworkSecurityGroup to attach to. Immutable.",
			},
			"target_type": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Target resource type. Only \"subnet\" is supported. Immutable.",
				ValidateFunc: func(val interface{}, key string) ([]string, []error) {
					v := val.(string)
					if v != "subnet" {
						return nil, []error{fmt.Errorf("%q must be \"subnet\", got %q", key, v)}
					}
					return nil, nil
				},
			},
			"target_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "UUID of the subnet to attach the NSG to. Immutable.",
			},

			// ── Computed ──

			"attachment_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "API-generated UUID4 for the attachment. Used in the delete path.",
			},
			"created_at": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "RFC3339 creation timestamp. Set by the API.",
			},
		},
	}
}

func resourceNSGAttachmentCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	tenantID := d.Get("tenant_id").(string)
	projectID := d.Get("project_id").(string)
	sgID := d.Get("sg_id").(string)

	req := client.NSGAttachmentCreateRequest{
		TargetType: d.Get("target_type").(string),
		TargetID:   d.Get("target_id").(string),
	}

	attachment, err := c.CreateNSGAttachment(ctx, tenantID, projectID, sgID, req)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error creating NSG attachment: %w", err))
	}

	// State ID encodes all four path components needed to delete the attachment.
	d.SetId(fmt.Sprintf("%s/%s/%s/%s", tenantID, projectID, sgID, attachment.ID))

	var diags diag.Diagnostics
	diags = appendSet(diags, d, "attachment_id", attachment.ID)
	diags = appendSet(diags, d, "created_at", attachment.CreatedAt)
	return diags
}

func resourceNSGAttachmentRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 4)
	if len(parts) != 4 {
		return diag.FromErr(fmt.Errorf("invalid NSG attachment state ID %q: expected 'tenant_id/project_id/sg_id/attachment_id'", d.Id()))
	}
	tenantID, projectID, sgID, attachmentID := parts[0], parts[1], parts[2], parts[3]

	// The API has no direct GET for attachments; scan the parent NSG's attachment list.
	nsg, err := c.GetNSG(ctx, tenantID, projectID, sgID)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error reading NSG %q for attachment %q: %w", sgID, attachmentID, err))
	}
	if nsg == nil {
		// NSG was deleted externally — attachment is gone with it.
		d.SetId("")
		return nil
	}

	for _, a := range nsg.Attachments {
		if a.ID == attachmentID {
			var diags diag.Diagnostics
			diags = appendSet(diags, d, "attachment_id", a.ID)
			diags = appendSet(diags, d, "target_type", a.TargetType)
			diags = appendSet(diags, d, "target_id", a.TargetID)
			diags = appendSet(diags, d, "created_at", a.CreatedAt)
			return diags
		}
	}

	// Attachment not found in the NSG — deleted externally.
	d.SetId("")
	return nil
}

func resourceNSGAttachmentDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 4)
	if len(parts) != 4 {
		return diag.FromErr(fmt.Errorf("invalid NSG attachment state ID %q: expected 'tenant_id/project_id/sg_id/attachment_id'", d.Id()))
	}
	tenantID, projectID, sgID, attachmentID := parts[0], parts[1], parts[2], parts[3]

	if err := c.DeleteNSGAttachment(ctx, tenantID, projectID, sgID, attachmentID); err != nil {
		return diag.FromErr(fmt.Errorf("error deleting NSG attachment %q: %w", attachmentID, err))
	}
	return nil
}
