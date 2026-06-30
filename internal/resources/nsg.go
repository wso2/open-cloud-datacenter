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

// ResourceNetworkSecurityGroup returns the schema.Resource for "dcapi_network_security_group".
func ResourceNetworkSecurityGroup() *schema.Resource {
	return &schema.Resource{
		CreateContext: resourceNSGCreate,
		ReadContext:   resourceNSGRead,
		UpdateContext: resourceNSGUpdate,
		DeleteContext: resourceNSGDelete,

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
				Description: "NSG name. Must be unique within the tenant. Immutable.",
			},

			// ── Optional + immutable ──

			"description": {
				Type:        schema.TypeString,
				Optional:    true,
				ForceNew:    true,
				Description: "Human-readable description. Max 256 chars. Immutable.",
			},

			// ── Optional + updatable ──

			"rules": {
				Type:        schema.TypeList,
				Optional:    true,
				Description: "Security rules. Full-replace on update — send the complete desired list.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"name": {
							Type:        schema.TypeString,
							Required:    true,
							Description: "Rule name. Must be unique within the NSG.",
						},
						"direction": {
							Type:        schema.TypeString,
							Required:    true,
							Description: "\"inbound\" or \"outbound\".",
							ValidateFunc: validation.StringInSlice([]string{
								"inbound", "outbound",
							}, false),
						},
						"priority": {
							Type:         schema.TypeInt,
							Required:     true,
							Description:  "Rule priority (100–4096). Must be unique per direction within the NSG.",
							ValidateFunc: validation.IntBetween(100, 4096),
						},
						"protocol": {
							Type:        schema.TypeString,
							Required:    true,
							Description: "\"tcp\"|\"udp\"|\"icmp\"|\"*\".",
							ValidateFunc: validation.StringInSlice([]string{
								"tcp", "udp", "icmp", "*",
							}, false),
						},
						"source_address_prefix": {
							Type:        schema.TypeString,
							Required:    true,
							Description: "Source address prefix (CIDR or \"*\").",
						},
						"source_port_range": {
							Type:        schema.TypeString,
							Required:    true,
							Description: "Source port, range (e.g. \"1024-65535\"), or \"*\".",
						},
						"destination_address_prefix": {
							Type:        schema.TypeString,
							Required:    true,
							Description: "Destination address prefix (CIDR or \"*\").",
						},
						"destination_port_range": {
							Type:        schema.TypeString,
							Required:    true,
							Description: "Destination port, range (e.g. \"80-443\"), or \"*\".",
						},
						"action": {
							Type:        schema.TypeString,
							Required:    true,
							Description: "\"allow\" or \"deny\".",
							ValidateFunc: validation.StringInSlice([]string{
								"allow", "deny",
							}, false),
						},
					},
				},
			},

			// ── Computed ──

			"sg_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "API-generated UUID4 for the NSG.",
			},
			"status": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Lifecycle status (e.g. \"ACTIVE\"). Set by the API.",
			},
			"provider_type": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Underlying infrastructure provider (e.g. \"kubeovn\"). Set by the API.",
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
		},
	}
}

func resourceNSGCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	tenantID := d.Get("tenant_id").(string)
	projectID := d.Get("project_id").(string)

	req := client.NSGCreateRequest{
		Name:  d.Get("name").(string),
		Rules: expandNSGRules(d.Get("rules").([]interface{})),
	}
	if v, ok := d.Get("description").(string); ok {
		req.Description = v
	}

	nsg, err := c.CreateNSG(ctx, tenantID, projectID, req)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error creating network security group: %w", err))
	}

	d.SetId(fmt.Sprintf("%s/%s/%s", tenantID, projectID, nsg.ID))

	var diags diag.Diagnostics
	diags = appendSet(diags, d, "sg_id", nsg.ID)
	diags = appendSet(diags, d, "status", nsg.Status)
	diags = appendSet(diags, d, "provider_type", nsg.ProviderType)
	diags = appendSet(diags, d, "created_at", nsg.CreatedAt)
	diags = appendSet(diags, d, "updated_at", nsg.UpdatedAt)
	diags = appendSet(diags, d, "rules", flattenNSGRules(nsg.Rules))
	return diags
}

func resourceNSGRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 3)
	if len(parts) != 3 {
		return diag.FromErr(fmt.Errorf("invalid NSG state ID %q: expected 'tenant_id/project_id/sg_id'", d.Id()))
	}
	tenantID, projectID, sgID := parts[0], parts[1], parts[2]

	nsg, err := c.GetNSG(ctx, tenantID, projectID, sgID)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error reading network security group %q: %w", sgID, err))
	}
	if nsg == nil {
		d.SetId("")
		return nil
	}

	var diags diag.Diagnostics
	diags = appendSet(diags, d, "sg_id", nsg.ID)
	diags = appendSet(diags, d, "name", nsg.Name)
	diags = appendSet(diags, d, "description", nsg.Description)
	diags = appendSet(diags, d, "status", nsg.Status)
	diags = appendSet(diags, d, "provider_type", nsg.ProviderType)
	diags = appendSet(diags, d, "created_at", nsg.CreatedAt)
	diags = appendSet(diags, d, "updated_at", nsg.UpdatedAt)
	diags = appendSet(diags, d, "rules", flattenNSGRules(nsg.Rules))
	return diags
}

func resourceNSGUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 3)
	if len(parts) != 3 {
		return diag.FromErr(fmt.Errorf("invalid NSG state ID %q: expected 'tenant_id/project_id/sg_id'", d.Id()))
	}
	tenantID, projectID, sgID := parts[0], parts[1], parts[2]

	// Full-replace: always send the complete desired rules list; nil becomes [].
	rules := expandNSGRules(d.Get("rules").([]interface{}))
	if rules == nil {
		rules = []client.NSGRule{}
	}

	req := client.NSGUpdateRulesRequest{Rules: rules}
	nsg, err := c.UpdateNSGRules(ctx, tenantID, projectID, sgID, req)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error updating network security group %q rules: %w", sgID, err))
	}

	var diags diag.Diagnostics
	diags = appendSet(diags, d, "rules", flattenNSGRules(nsg.Rules))
	diags = appendSet(diags, d, "updated_at", nsg.UpdatedAt)
	return diags
}

func resourceNSGDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 3)
	if len(parts) != 3 {
		return diag.FromErr(fmt.Errorf("invalid NSG state ID %q: expected 'tenant_id/project_id/sg_id'", d.Id()))
	}
	tenantID, projectID, sgID := parts[0], parts[1], parts[2]

	if err := c.DeleteNSG(ctx, tenantID, projectID, sgID); err != nil {
		return diag.FromErr(fmt.Errorf("error deleting network security group %q: %w", sgID, err))
	}
	return nil
}

// expandNSGRules converts the Terraform rules list to []client.NSGRule.
func expandNSGRules(raw []interface{}) []client.NSGRule {
	if len(raw) == 0 {
		return nil
	}
	rules := make([]client.NSGRule, len(raw))
	for i, v := range raw {
		m := v.(map[string]interface{})
		rules[i] = client.NSGRule{
			Name:                     m["name"].(string),
			Direction:                m["direction"].(string),
			Priority:                 m["priority"].(int),
			Protocol:                 m["protocol"].(string),
			SourceAddressPrefix:      m["source_address_prefix"].(string),
			SourcePortRange:          m["source_port_range"].(string),
			DestinationAddressPrefix: m["destination_address_prefix"].(string),
			DestinationPortRange:     m["destination_port_range"].(string),
			Action:                   m["action"].(string),
		}
	}
	return rules
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
