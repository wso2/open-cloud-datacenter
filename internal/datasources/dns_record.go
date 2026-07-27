// Terraform data source definition for dcapi_dns_record.
// Looks up a DnsRecord that is not managed by this Terraform config. Matches on (name, type)
// — the record's natural upsert identity within a zone, not name alone, since a zone can
// legitimately hold records with the same name but different types (e.g. "api" A and CNAME).
package datasources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// DataSourceDNSRecord returns the schema.Resource for "dcapi_dns_record" data source lookups.
func DataSourceDNSRecord() *schema.Resource {
	return &schema.Resource{
		ReadContext: dataSourceDnsRecordRead,

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
			"vnet_id": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "UUID of the parent VNet.",
			},
			"zone_id": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "UUID of the parent PrivateDnsZone.",
			},
			"name": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "Relative record name within the zone (e.g. \"www\").",
			},
			"type": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "Record type: \"A\"|\"AAAA\"|\"CNAME\"|\"SRV\"|\"TXT\"|\"MX\". Combined with name, forms the record's unique identity within the zone.",
			},

			"record_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "API-generated UUID4 for this record.",
			},
			"values": {
				Type:        schema.TypeList,
				Computed:    true,
				Elem:        &schema.Schema{Type: schema.TypeString},
				Description: "Current record values.",
			},
			"ttl": {
				Type:        schema.TypeInt,
				Computed:    true,
				Description: "TTL in seconds.",
			},
			"created_at": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "RFC3339 creation timestamp.",
			},
		},
	}
}

func dataSourceDnsRecordRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)
	tenantID := d.Get("tenant_id").(string)
	projectID := d.Get("project_id").(string)
	vnetID := d.Get("vnet_id").(string)
	zoneID := d.Get("zone_id").(string)
	name := d.Get("name").(string)
	recordType := d.Get("type").(string)

	records, err := c.ListDnsRecords(ctx, tenantID, projectID, vnetID, zoneID)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error listing DNS records in zone %q: %w", zoneID, err))
	}

	for _, record := range records {
		if record.Name != name || record.Type != recordType {
			continue
		}

		d.SetId(fmt.Sprintf("%s/%s/%s/%s/%s", tenantID, projectID, vnetID, zoneID, record.ID))

		var diags diag.Diagnostics
		diags = appendSet(diags, d, "record_id", record.ID)
		diags = appendSet(diags, d, "values", record.Values)
		diags = appendSet(diags, d, "ttl", record.TTL)
		diags = appendSet(diags, d, "created_at", record.CreatedAt)
		return diags
	}

	return diag.Errorf("no DNS record named %q of type %q found in zone %q", name, recordType, zoneID)
}
