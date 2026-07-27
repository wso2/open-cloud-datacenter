// Terraform data source definition for dcapi_network_security_group.
// Looks up an NSG that is not managed by this Terraform config, by name, since its API
// UUID is opaque and unknown to the caller ahead of time.
package datasources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// DataSourceNSG returns the schema.Resource for "dcapi_network_security_group" data source lookups.
func DataSourceNSG() *schema.Resource {
	return &schema.Resource{
		ReadContext: dataSourceNSGRead,

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
				Description: "Name of the NSG to look up.",
			},

			"sg_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "API-generated UUID4 for the NSG.",
			},
			"description": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Human-readable description.",
			},
			"rules": {
				Type:        schema.TypeList,
				Computed:    true,
				Description: "Security rules currently defined on this NSG.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"name":                       {Type: schema.TypeString, Computed: true},
						"direction":                  {Type: schema.TypeString, Computed: true},
						"priority":                   {Type: schema.TypeInt, Computed: true},
						"protocol":                   {Type: schema.TypeString, Computed: true},
						"source_address_prefix":      {Type: schema.TypeString, Computed: true},
						"source_port_range":          {Type: schema.TypeString, Computed: true},
						"destination_address_prefix": {Type: schema.TypeString, Computed: true},
						"destination_port_range":     {Type: schema.TypeString, Computed: true},
						"action":                     {Type: schema.TypeString, Computed: true},
					},
				},
			},
			"attachments": {
				Type:        schema.TypeList,
				Computed:    true,
				Description: "Current attachments of this NSG to subnets or NICs.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"id":          {Type: schema.TypeString, Computed: true},
						"sg_id":       {Type: schema.TypeString, Computed: true},
						"target_type": {Type: schema.TypeString, Computed: true},
						"target_id":   {Type: schema.TypeString, Computed: true},
						"created_at":  {Type: schema.TypeString, Computed: true},
					},
				},
			},
			"status": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Lifecycle status (e.g. \"ACTIVE\").",
			},
			"provider_type": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Underlying infrastructure provider (e.g. \"kubeovn\").",
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

func dataSourceNSGRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)
	tenantID := d.Get("tenant_id").(string)
	projectID := d.Get("project_id").(string)
	name := d.Get("name").(string)

	nsgs, err := c.ListNSGs(ctx, tenantID, projectID)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error listing NSGs in project %q: %w", projectID, err))
	}

	for _, nsg := range nsgs {
		if nsg.Name != name {
			continue
		}

		d.SetId(fmt.Sprintf("%s/%s/%s", tenantID, projectID, nsg.ID))

		var diags diag.Diagnostics
		diags = appendSet(diags, d, "sg_id", nsg.ID)
		diags = appendSet(diags, d, "description", nsg.Description)
		diags = appendSet(diags, d, "rules", flattenNSGRules(nsg.Rules))
		diags = appendSet(diags, d, "attachments", flattenNSGAttachments(nsg.Attachments))
		diags = appendSet(diags, d, "status", nsg.Status)
		diags = appendSet(diags, d, "provider_type", nsg.ProviderType)
		diags = appendSet(diags, d, "created_at", nsg.CreatedAt)
		diags = appendSet(diags, d, "updated_at", nsg.UpdatedAt)
		return diags
	}

	return diag.Errorf("no NSG named %q found in project %q", name, projectID)
}

// flattenNSGRules converts []client.NSGRule to a Terraform-compatible list.
func flattenNSGRules(rules []client.NSGRule) []interface{} {
	result := make([]interface{}, len(rules))
	for i, r := range rules {
		result[i] = map[string]interface{}{
			"name":                       r.Name,
			"direction":                  r.Direction,
			"priority":                   r.Priority,
			"protocol":                   r.Protocol,
			"source_address_prefix":      r.SourceAddressPrefix,
			"source_port_range":          r.SourcePortRange,
			"destination_address_prefix": r.DestinationAddressPrefix,
			"destination_port_range":     r.DestinationPortRange,
			"action":                     r.Action,
		}
	}
	return result
}

// flattenNSGAttachments converts []client.NSGAttachmentEntry to a Terraform-compatible list.
func flattenNSGAttachments(attachments []client.NSGAttachmentEntry) []interface{} {
	result := make([]interface{}, len(attachments))
	for i, a := range attachments {
		result[i] = map[string]interface{}{
			"id":          a.ID,
			"sg_id":       a.SGID,
			"target_type": a.TargetType,
			"target_id":   a.TargetID,
			"created_at":  a.CreatedAt,
		}
	}
	return result
}
