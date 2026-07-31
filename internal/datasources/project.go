// Terraform data source definition for dcapi_project.
// Looks up a project that is not managed by this Terraform config — e.g. one provisioned by
// platform admins or a separate root module.
//
// project_id is itself the human-chosen slug (not an opaque UUID) and doubles as the API
// path parameter, so this needs no List+filter-by-name — it reuses GetProjectByID directly,
// the same call the dcapi_project resource's Read uses internally.
package datasources

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// DataSourceProject returns the schema.Resource for "dcapi_project" data source lookups.
func DataSourceProject() *schema.Resource {
	return &schema.Resource{
		ReadContext: dataSourceProjectRead,

		Schema: map[string]*schema.Schema{

			"tenant_id": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "Slug of the parent tenant.",
			},
			"project_id": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "Slug of the project to look up.",
			},

			"name": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Human-readable label.",
			},
			"description": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Free-text description.",
			},
			"cpu_cores": {
				Type:        schema.TypeInt,
				Computed:    true,
				Description: "CPU core quota.",
			},
			"memory_gb": {
				Type:        schema.TypeInt,
				Computed:    true,
				Description: "Memory quota in GB.",
			},
			"storage_gb": {
				Type:        schema.TypeInt,
				Computed:    true,
				Description: "Storage quota in GB.",
			},
			"max_vnets": {
				Type:        schema.TypeInt,
				Computed:    true,
				Description: "Maximum VNets allowed.",
			},
			"max_clusters": {
				Type:        schema.TypeInt,
				Computed:    true,
				Description: "Maximum Kubernetes clusters allowed.",
			},
			"max_volumes": {
				Type:        schema.TypeInt,
				Computed:    true,
				Description: "Maximum persistent volumes allowed.",
			},
			"max_public_ips": {
				Type:        schema.TypeInt,
				Computed:    true,
				Description: "Maximum public IPs allowed.",
			},
			"project_uuid": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "API-generated UUID4 for the project.",
			},
			"tenant_uuid": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "UUID of the parent tenant.",
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
			"created_by": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "OIDC sub of the caller who created the project.",
			},
		},
	}
}

func dataSourceProjectRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	
	c := meta.(*client.DCAPIClient)

	tenantID := d.Get("tenant_id").(string)
	id := d.Get("project_id").(string)

	project, err := c.GetProjectByID(ctx, tenantID, id)
	
	if err != nil {
		return diag.FromErr(fmt.Errorf("error looking up project %q in tenant %q: %w", id, tenantID, err))
	}
	if project == nil {
		return diag.Errorf("no project found with id %q in tenant %q", id, tenantID)
	}

	d.SetId(fmt.Sprintf("%s/%s", tenantID, project.ID))

	var diags diag.Diagnostics

	diags = appendSet(diags, d, "name", project.Name)
	diags = appendSet(diags, d, "description", project.Description)
	diags = appendSet(diags, d, "cpu_cores", project.CPUCores)
	diags = appendSet(diags, d, "memory_gb", project.MemoryGB)
	diags = appendSet(diags, d, "storage_gb", project.StorageGB)
	diags = appendSet(diags, d, "max_vnets", project.MaxVNets)
	diags = appendSet(diags, d, "max_clusters", project.MaxClusters)
	diags = appendSet(diags, d, "max_volumes", project.MaxVolumes)
	diags = appendSet(diags, d, "max_public_ips", project.MaxPublicIPs)
	diags = appendSet(diags, d, "project_uuid", project.ProjectUUID)
	diags = appendSet(diags, d, "tenant_uuid", project.TenantUUID)
	diags = appendSet(diags, d, "created_at", project.CreatedAt)
	diags = appendSet(diags, d, "updated_at", project.UpdatedAt)
	diags = appendSet(diags, d, "created_by", project.CreatedBy)
	
	return diags
}
