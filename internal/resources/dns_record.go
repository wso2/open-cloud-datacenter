package resources

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// ResourceDnsRecord returns the schema.Resource for "dcapi_dns_record".
func ResourceDnsRecord() *schema.Resource {
	return &schema.Resource{
		CreateContext: resourceDnsRecordCreate,
		ReadContext:   resourceDnsRecordRead,
		UpdateContext: resourceDnsRecordUpdate,
		DeleteContext: resourceDnsRecordDelete,

		Schema: map[string]*schema.Schema{

			// ── Required + immutable path params ──

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
			"vnet_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "UUID of the parent VNet. Used in the API URL path. Immutable.",
			},
			"zone_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "UUID of the parent PrivateDnsZone. Used in the API URL path. Immutable.",
			},

			// ── Required + immutable (upsert identity) ──

			"name": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Relative record name within the zone (e.g. \"www\"). Part of the record's upsert identity — immutable; change forces a new record.",
			},
			"type": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Record type: \"A\"|\"AAAA\"|\"CNAME\"|\"SRV\"|\"TXT\"|\"MX\". Part of the record's upsert identity — immutable.",
			},

			// ── Updatable ──

			"values": {
				Type:     schema.TypeList,
				Required: true,
				Elem:     &schema.Schema{Type: schema.TypeString},
				Description: "Record values (min 1). Full-replace on update — the complete desired " +
					"list is sent on every change.",
			},
			"ttl": {
				Type:        schema.TypeInt,
				Optional:    true,
				Default:     300,
				Description: "TTL in seconds (30-86400). Default 300.",
			},

			// ── Computed ──

			"record_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "API-generated UUID4 for this record.",
			},
			"created_at": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "RFC3339 creation timestamp. Set by the API.",
			},
		},
	}
}

// resourceDnsRecordCreate calls POST .../dns-zones/{zone_id}/records (an upsert keyed on
// name+type). Returns 201 Created — no polling required.
func resourceDnsRecordCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	tenantID := d.Get("tenant_id").(string)
	projectID := d.Get("project_id").(string)
	vnetID := d.Get("vnet_id").(string)
	zoneID := d.Get("zone_id").(string)

	req := client.DnsRecordUpsertRequest{
		Name:   d.Get("name").(string),
		Type:   d.Get("type").(string),
		Values: expandStringList(d.Get("values").([]interface{})),
		TTL:    d.Get("ttl").(int),
	}

	record, err := c.UpsertDnsRecord(ctx, tenantID, projectID, vnetID, zoneID, req)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error creating DnsRecord: %w", err))
	}

	d.SetId(fmt.Sprintf("%s/%s/%s/%s/%s", tenantID, projectID, vnetID, zoneID, record.ID))

	var diags diag.Diagnostics
	diags = appendSet(diags, d, "record_id", record.ID)
	diags = appendSet(diags, d, "ttl", record.TTL)
	diags = appendSet(diags, d, "created_at", record.CreatedAt)
	return diags
}

// resourceDnsRecordRead fetches current state from the API and refreshes Terraform state.
func resourceDnsRecordRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 5)
	if len(parts) != 5 {
		return diag.FromErr(fmt.Errorf("invalid DnsRecord state ID %q: expected 'tenant_id/project_id/vnet_id/zone_id/record_id'", d.Id()))
	}
	tenantID, projectID, vnetID, zoneID, recordID := parts[0], parts[1], parts[2], parts[3], parts[4]

	record, err := c.GetDnsRecord(ctx, tenantID, projectID, vnetID, zoneID, recordID)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error reading DnsRecord %q: %w", recordID, err))
	}
	if record == nil {
		d.SetId("")
		return nil
	}

	var diags diag.Diagnostics

	diags = appendSet(diags, d, "tenant_id", tenantID)
	diags = appendSet(diags, d, "project_id", projectID)
	diags = appendSet(diags, d, "vnet_id", vnetID)
	diags = appendSet(diags, d, "zone_id", zoneID)
	diags = appendSet(diags, d, "record_id", record.ID)
	diags = appendSet(diags, d, "name", record.Name)
	diags = appendSet(diags, d, "type", record.Type)
	diags = appendSet(diags, d, "values", record.Values)
	diags = appendSet(diags, d, "ttl", record.TTL)
	diags = appendSet(diags, d, "created_at", record.CreatedAt)
	
	return diags
}

// resourceDnsRecordUpdate sends PUT .../records/{record_id} with the full desired values list.
// name and type are ForceNew, so this only ever runs for values/ttl changes.
func resourceDnsRecordUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 5)
	if len(parts) != 5 {
		return diag.FromErr(fmt.Errorf("invalid DnsRecord state ID %q: expected 'tenant_id/project_id/vnet_id/zone_id/record_id'", d.Id()))
	}
	tenantID, projectID, vnetID, zoneID, recordID := parts[0], parts[1], parts[2], parts[3], parts[4]

	req := client.DnsRecordUpdateRequest{
		Values: expandStringList(d.Get("values").([]interface{})),
		TTL:    d.Get("ttl").(int),
	}

	record, err := c.UpdateDnsRecord(ctx, tenantID, projectID, vnetID, zoneID, recordID, req)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error updating DnsRecord %q: %w", recordID, err))
	}

	var diags diag.Diagnostics
	diags = appendSet(diags, d, "values", record.Values)
	diags = appendSet(diags, d, "ttl", record.TTL)
	return diags
}

// resourceDnsRecordDelete sends DELETE .../records/{record_id}. Returns 204 (sync).
func resourceDnsRecordDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 5)
	if len(parts) != 5 {
		return diag.FromErr(fmt.Errorf("invalid DnsRecord state ID %q: expected 'tenant_id/project_id/vnet_id/zone_id/record_id'", d.Id()))
	}
	tenantID, projectID, vnetID, zoneID, recordID := parts[0], parts[1], parts[2], parts[3], parts[4]

	if err := c.DeleteDnsRecord(ctx, tenantID, projectID, vnetID, zoneID, recordID); err != nil {
		return diag.FromErr(fmt.Errorf("error deleting DnsRecord %q: %w", recordID, err))
	}
	return nil
}

// expandStringList converts a Terraform TypeList of strings ([]interface{}) to []string.
func expandStringList(raw []interface{}) []string {
	out := make([]string, len(raw))
	for i, v := range raw {
		out[i] = v.(string)
	}
	return out
}
