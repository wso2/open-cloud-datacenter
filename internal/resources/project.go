// Terraform resource definition for dcapi_project.
// Calls internal/client/project.go for API calls; knows nothing about HTTP internals.
package resources

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// ResourceProject returns the schema.Resource for "dcapi_project".
func ResourceProject() *schema.Resource {
	
	return &schema.Resource{

		CreateContext: resourceProjectCreate,
		ReadContext:   resourceProjectRead,
		UpdateContext: resourceProjectUpdate,
		DeleteContext: resourceProjectDelete,

		Schema: map[string]*schema.Schema{

			// ── Required + immutable ──

			"project_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Unique slug for the project within its tenant. Immutable.",
			},

			"tenant_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Slug of the parent tenant. Immutable.",
			},

			// ── Optional + immutable (no PATCH endpoint for these fields) ──

			"name": {
				Type:        schema.TypeString,
				Optional:    true,
				ForceNew:    true,
				Description: "Human-readable label. Immutable after creation.",
			},
			"description": {
				Type:        schema.TypeString,
				Optional:    true,
				ForceNew:    true,
				Description: "Free-text description. Immutable after creation.",
			},

			// ── Optional + immutable + Computed (API fills in defaults when omitted) ──

			"max_vnets": {
				Type:        schema.TypeInt,
				Optional:    true,
				Computed:    true,
				ForceNew:    true,
				Description: "Maximum VNets allowed. API default: 10. Immutable after creation.",
			},
			"max_clusters": {
				Type:        schema.TypeInt,
				Optional:    true,
				Computed:    true,
				ForceNew:    true,
				Description: "Maximum Kubernetes clusters allowed. API default: 2. Immutable after creation.",
			},
			"max_volumes": {
				Type:        schema.TypeInt,
				Optional:    true,
				Computed:    true,
				ForceNew:    true,
				Description: "Maximum persistent volumes allowed. API default: 50. Immutable after creation.",
			},
			"max_public_ips": {
				Type:        schema.TypeInt,
				Optional:    true,
				Computed:    true,
				ForceNew:    true,
				Description: "Maximum public IPs allowed. API default: 3. Immutable after creation.",
			},

			// ── Optional + Computed (updatable via PATCH; API fills defaults when omitted) ──

			"cpu_cores": {
				Type:        schema.TypeInt,
				Optional:    true,
				Computed:    true,
				Description: "CPU core quota. API default: 20. Updatable after creation.",
			},
			"memory_gb": {
				Type:        schema.TypeInt,
				Optional:    true,
				Computed:    true,
				Description: "Memory quota in GB. API default: 64. Updatable after creation.",
			},
			"storage_gb": {
				Type:        schema.TypeInt,
				Optional:    true,
				Computed:    true,
				Description: "Storage quota in GB. API default: 500. Updatable after creation.",
			},

			// ── Computed-only (set entirely by the API) ──

			"project_uuid": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "API-generated UUID4 for the project. Immutable.",
			},
			"tenant_uuid": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "UUID of the parent tenant. Set by the API.",
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
			"created_by": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "OIDC sub of the caller who created the project. Set by the API.",
			},
		},
	}
}

// resourceProjectCreate calls POST /v1/tenants/{tenant_id}/projects and commits state.
// d.SetId encodes both slugs as "tenant_id/project_id" so Read/Update/Delete can reconstruct the URL.
func resourceProjectCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	tenantID := d.Get("tenant_id").(string)
	projectID := d.Get("project_id").(string)

	req := client.ProjectCreateRequest{
		ID:           projectID,
		Name:         d.Get("name").(string),
		Description:  d.Get("description").(string),
		CPUCores:     d.Get("cpu_cores").(int),
		MemoryGB:     d.Get("memory_gb").(int),
		StorageGB:    d.Get("storage_gb").(int),
		MaxVNets:     d.Get("max_vnets").(int),
		MaxClusters:  d.Get("max_clusters").(int),
		MaxVolumes:   d.Get("max_volumes").(int),
		MaxPublicIPs: d.Get("max_public_ips").(int),
	}

	project, err := c.CreateProject(ctx, tenantID, req)
	
	if err != nil {
		return diag.FromErr(fmt.Errorf("error creating project: %w", err))
	}

	// Composite ID encodes both slugs needed for subsequent API paths.
	d.SetId(fmt.Sprintf("%s/%s", tenantID, project.ID))

	var diags diag.Diagnostics
	
	if err := d.Set("project_uuid", project.ProjectUUID); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("tenant_uuid", project.TenantUUID); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("cpu_cores", project.CPUCores); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("memory_gb", project.MemoryGB); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("storage_gb", project.StorageGB); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("max_vnets", project.MaxVNets); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("max_clusters", project.MaxClusters); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("max_volumes", project.MaxVolumes); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("max_public_ips", project.MaxPublicIPs); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("created_at", project.CreatedAt); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("updated_at", project.UpdatedAt); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("created_by", project.CreatedBy); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	return diags
}

// resourceProjectRead fetches current state from the API and refreshes Terraform state.
// Calls d.SetId("") and returns nil when the project no longer exists (external drift).
func resourceProjectRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 2)
	if len(parts) != 2 {
		return diag.FromErr(fmt.Errorf("invalid project state ID %q: expected 'tenant_id/project_id'", d.Id()))
	}
	tenantID, projectID := parts[0], parts[1]

	project, err := c.GetProjectByID(ctx, tenantID, projectID)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error reading project %q in tenant %q: %w", projectID, tenantID, err))
	}
	// nil, nil from GetProjectByID means HTTP 404 — project deleted outside Terraform.
	if project == nil {
		d.SetId("")
		return nil
	}

	var diags diag.Diagnostics

	if err := d.Set("project_id", project.ID); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("tenant_id", project.TenantID); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("name", project.Name); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("description", project.Description); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("cpu_cores", project.CPUCores); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("memory_gb", project.MemoryGB); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("storage_gb", project.StorageGB); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("max_vnets", project.MaxVNets); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("max_clusters", project.MaxClusters); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("max_volumes", project.MaxVolumes); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("max_public_ips", project.MaxPublicIPs); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("project_uuid", project.ProjectUUID); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("tenant_uuid", project.TenantUUID); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("created_at", project.CreatedAt); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("updated_at", project.UpdatedAt); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("created_by", project.CreatedBy); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	return diags
}

// resourceProjectUpdate calls PATCH with only the fields that changed (cpu_cores, memory_gb, storage_gb).
// *int nil pointers are omitted from JSON — the API leaves unset fields unchanged.
func resourceProjectUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 2)
	if len(parts) != 2 {
		return diag.FromErr(fmt.Errorf("invalid project state ID %q: expected 'tenant_id/project_id'", d.Id()))
	}
	tenantID, projectID := parts[0], parts[1]

	req := client.ProjectUpdateRequest{}
	if d.HasChange("cpu_cores") {
		v := d.Get("cpu_cores").(int)
		req.CPUCores = &v
	}
	if d.HasChange("memory_gb") {
		v := d.Get("memory_gb").(int)
		req.MemoryGB = &v
	}
	if d.HasChange("storage_gb") {
		v := d.Get("storage_gb").(int)
		req.StorageGB = &v
	}

	project, err := c.UpdateProject(ctx, tenantID, projectID, req)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error updating project %q in tenant %q: %w", projectID, tenantID, err))
	}

	var diags diag.Diagnostics
	if err := d.Set("cpu_cores", project.CPUCores); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("memory_gb", project.MemoryGB); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("storage_gb", project.StorageGB); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	if err := d.Set("updated_at", project.UpdatedAt); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	return diags
}

// resourceProjectDelete calls DELETE /v1/tenants/{tenant_id}/projects/{project_id}.
// Returns HTTP 409 if child resources (VMs, clusters, VNets) still exist inside the project.
func resourceProjectDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 2)
	if len(parts) != 2 {
		return diag.FromErr(fmt.Errorf("invalid project state ID %q: expected 'tenant_id/project_id'", d.Id()))
	}
	tenantID, projectID := parts[0], parts[1]

	if err := c.DeleteProject(ctx, tenantID, projectID); err != nil {
		return diag.FromErr(fmt.Errorf("error deleting project %q in tenant %q: %w", projectID, tenantID, err))
	}
	d.SetId("")
	return nil
}
