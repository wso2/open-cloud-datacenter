package resources

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// ResourceNodePool returns the schema.Resource for "dcapi_node_pool".
func ResourceNodePool() *schema.Resource {
	return &schema.Resource{
		CreateContext: resourceNodePoolCreate,
		ReadContext:   resourceNodePoolRead,
		UpdateContext: resourceNodePoolUpdate,
		DeleteContext: resourceNodePoolDelete,

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(15 * time.Minute),
			Update: schema.DefaultTimeout(15 * time.Minute),
			Delete: schema.DefaultTimeout(10 * time.Minute),
		},

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
			"cluster_id": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "UUID of the parent cluster. Immutable.",
			},
			"name": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Pool name. DNS-label pattern; must not be \"system\". Used as the API path key. Immutable.",
			},
			"size": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Node size: \"small\"|\"medium\"|\"large\"|\"xlarge\". Immutable.",
			},

			// ── Required + updatable ──

			"node_count": {
				Type:        schema.TypeInt,
				Required:    true,
				Description: "Number of worker nodes (1–50). Can be scaled after creation.",
			},

			// ── Optional + immutable ──

			"disk_gb": {
				Type:        schema.TypeInt,
				Optional:    true,
				ForceNew:    true,
				Description: "Root disk size in GB. Minimum 40. Defaults to size default if omitted. Immutable.",
			},
			"image_name": {
				Type:        schema.TypeString,
				Optional:    true,
				ForceNew:    true,
				Description: "Override VM image for this pool (format: \"namespace/resource-name\"). Inherits cluster image_name if omitted. Immutable.",
			},

			// ── Optional + updatable ──

			"taints": {
				Type:        schema.TypeList,
				Optional:    true,
				Description: "Kubernetes taints to apply to nodes in this pool (max 10). Full-replace on update.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"key": {
							Type:     schema.TypeString,
							Required: true,
						},
						"value": {
							Type:     schema.TypeString,
							Optional: true,
						},
						"effect": {
							Type:        schema.TypeString,
							Required:    true,
							Description: "\"NoSchedule\"|\"PreferNoSchedule\"|\"NoExecute\".",
						},
					},
				},
			},
			"labels": {
				Type:        schema.TypeMap,
				Optional:    true,
				Description: "Kubernetes node labels (max 50 entries). Full-replace on update.",
				Elem:        &schema.Schema{Type: schema.TypeString},
			},

			// ── Computed ──

			"node_pool_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "API-generated UUID4 for the node pool.",
			},
			"role": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Pool role: always \"worker\" for node pools created via this resource.",
			},
			"status": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Lifecycle status: provisioning | ready | scaling | deleting | failed.",
			},
			"message": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Human-readable status message. Useful for debugging failed state.",
			},
			"created_at": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "RFC3339 creation timestamp. Set by the API.",
			},
		},
	}
}

func resourceNodePoolCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	tenantID := d.Get("tenant_id").(string)
	projectID := d.Get("project_id").(string)
	clusterID := d.Get("cluster_id").(string)
	poolName := d.Get("name").(string)

	req := client.NodePoolCreateRequest{
		Name:   poolName,
		Size:   d.Get("size").(string),
		Count:  d.Get("node_count").(int),
		Taints: expandNodePoolTaints(d.Get("taints").([]interface{})),
		Labels: expandNodePoolLabels(d.Get("labels").(map[string]interface{})),
	}
	if v, ok := d.Get("disk_gb").(int); ok && v > 0 {
		req.DiskGB = v
	}
	if v, ok := d.Get("image_name").(string); ok && v != "" {
		req.ImageName = v
	}

	pool, err := c.CreateNodePool(ctx, tenantID, projectID, clusterID, req)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error creating node pool: %w", err))
	}

	// State ID encodes all four path components needed to rebuild the API URL.
	d.SetId(fmt.Sprintf("%s/%s/%s/%s", tenantID, projectID, clusterID, poolName))

	var diags diag.Diagnostics
	diags = appendSet(diags, d, "node_pool_id", pool.ID)
	diags = appendSet(diags, d, "role", pool.Role)
	diags = appendSet(diags, d, "status", pool.Status)
	diags = appendSet(diags, d, "message", pool.Message)
	diags = appendSet(diags, d, "created_at", pool.CreatedAt)
	if diags.HasError() {
		return diags
	}

	if err := waitForNodePoolReady(ctx, c, tenantID, projectID, clusterID, poolName, d.Timeout(schema.TimeoutCreate)); err != nil {
		return diag.FromErr(err)
	}

	return resourceNodePoolRead(ctx, d, meta)
}

func resourceNodePoolRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 4)
	if len(parts) != 4 {
		return diag.FromErr(fmt.Errorf("invalid node pool state ID %q: expected 'tenant_id/project_id/cluster_id/pool_name'", d.Id()))
	}
	tenantID, projectID, clusterID, poolName := parts[0], parts[1], parts[2], parts[3]

	pool, err := c.GetNodePool(ctx, tenantID, projectID, clusterID, poolName)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error reading node pool %q: %w", poolName, err))
	}
	if pool == nil {
		d.SetId("")
		return nil
	}

	var diags diag.Diagnostics
	diags = appendSet(diags, d, "node_pool_id", pool.ID)
	diags = appendSet(diags, d, "name", pool.Name)
	diags = appendSet(diags, d, "role", pool.Role)
	diags = appendSet(diags, d, "size", pool.Size)
	diags = appendSet(diags, d, "node_count", pool.Count)
	diags = appendSet(diags, d, "disk_gb", pool.DiskGB)
	diags = appendSet(diags, d, "image_name", pool.ImageName)
	diags = appendSet(diags, d, "status", pool.Status)
	diags = appendSet(diags, d, "message", pool.Message)
	diags = appendSet(diags, d, "created_at", pool.CreatedAt)
	diags = appendSet(diags, d, "taints", flattenNodePoolTaints(pool.Taints))
	diags = appendSet(diags, d, "labels", flattenNodePoolLabels(pool.Labels))

	return diags
}

func resourceNodePoolUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 4)
	if len(parts) != 4 {
		return diag.FromErr(fmt.Errorf("invalid node pool state ID %q: expected 'tenant_id/project_id/cluster_id/pool_name'", d.Id()))
	}
	tenantID, projectID, clusterID, poolName := parts[0], parts[1], parts[2], parts[3]

	// Send all updatable fields; taints/labels use full-replace semantics on the API side.
	taints := expandNodePoolTaints(d.Get("taints").([]interface{}))
	if taints == nil {
		taints = []client.NodePoolTaint{}
	}
	labels := expandNodePoolLabels(d.Get("labels").(map[string]interface{}))
	if labels == nil {
		labels = map[string]string{}
	}

	req := client.NodePoolUpdateRequest{
		Count:  d.Get("node_count").(int),
		Taints: taints,
		Labels: labels,
	}

	if _, err := c.UpdateNodePool(ctx, tenantID, projectID, clusterID, poolName, req); err != nil {
		return diag.FromErr(fmt.Errorf("error updating node pool %q: %w", poolName, err))
	}

	if err := waitForNodePoolReady(ctx, c, tenantID, projectID, clusterID, poolName, d.Timeout(schema.TimeoutUpdate)); err != nil {
		return diag.FromErr(err)
	}

	return resourceNodePoolRead(ctx, d, meta)
}

func resourceNodePoolDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 4)
	if len(parts) != 4 {
		return diag.FromErr(fmt.Errorf("invalid node pool state ID %q: expected 'tenant_id/project_id/cluster_id/pool_name'", d.Id()))
	}
	tenantID, projectID, clusterID, poolName := parts[0], parts[1], parts[2], parts[3]

	if err := c.DeleteNodePool(ctx, tenantID, projectID, clusterID, poolName); err != nil {
		return diag.FromErr(fmt.Errorf("error deleting node pool %q: %w", poolName, err))
	}

	if err := waitForNodePoolDeleted(ctx, c, tenantID, projectID, clusterID, poolName, d.Timeout(schema.TimeoutDelete)); err != nil {
		return diag.FromErr(err)
	}

	return nil
}

func waitForNodePoolReady(ctx context.Context, c *client.DCAPIClient, tenantID, projectID, clusterID, poolName string, timeout time.Duration) error {
	conf := &resource.StateChangeConf{
		Pending:    []string{"provisioning", "scaling"},
		Target:     []string{"ready"},
		Timeout:    timeout,
		MinTimeout: 15 * time.Second,
		Refresh: func() (interface{}, string, error) {
			pool, err := c.GetNodePool(ctx, tenantID, projectID, clusterID, poolName)
			if err != nil {
				return nil, "", err
			}
			if pool == nil {
				return nil, "", fmt.Errorf("node pool %q disappeared while waiting for ready status", poolName)
			}
			if pool.Status == "failed" {
				return nil, "failed", fmt.Errorf("node pool %q operation failed: %s", poolName, pool.Message)
			}
			return pool, pool.Status, nil
		},
	}
	_, err := conf.WaitForStateContext(ctx)
	return err
}

func waitForNodePoolDeleted(ctx context.Context, c *client.DCAPIClient, tenantID, projectID, clusterID, poolName string, timeout time.Duration) error {
	conf := &resource.StateChangeConf{
		Pending:    []string{"ready", "provisioning", "scaling", "deleting"},
		Target:     []string{"DELETED"},
		Timeout:    timeout,
		MinTimeout: 15 * time.Second,
		Refresh: func() (interface{}, string, error) {
			pool, err := c.GetNodePool(ctx, tenantID, projectID, clusterID, poolName)
			if err != nil {
				return nil, "", err
			}
			if pool == nil {
				return "deleted", "DELETED", nil
			}
			return pool, pool.Status, nil
		},
	}
	_, err := conf.WaitForStateContext(ctx)
	return err
}

// expandNodePoolTaints converts the Terraform taint list to []client.NodePoolTaint.
func expandNodePoolTaints(raw []interface{}) []client.NodePoolTaint {
	if len(raw) == 0 {
		return nil
	}
	taints := make([]client.NodePoolTaint, len(raw))
	for i, v := range raw {
		m := v.(map[string]interface{})
		taints[i] = client.NodePoolTaint{
			Key:    m["key"].(string),
			Effect: m["effect"].(string),
		}
		if val, ok := m["value"].(string); ok {
			taints[i].Value = val
		}
	}
	return taints
}

// expandNodePoolLabels converts a Terraform map to map[string]string.
func expandNodePoolLabels(raw map[string]interface{}) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	labels := make(map[string]string, len(raw))
	for k, v := range raw {
		labels[k] = v.(string)
	}
	return labels
}

// flattenNodePoolTaints converts []client.NodePoolTaint to a Terraform-compatible list.
func flattenNodePoolTaints(taints []client.NodePoolTaint) []interface{} {
	result := make([]interface{}, len(taints))
	for i, t := range taints {
		result[i] = map[string]interface{}{
			"key":    t.Key,
			"value":  t.Value,
			"effect": t.Effect,
		}
	}
	return result
}

// flattenNodePoolLabels converts map[string]string to a Terraform-compatible map.
func flattenNodePoolLabels(labels map[string]string) map[string]interface{} {
	result := make(map[string]interface{}, len(labels))
	for k, v := range labels {
		result[k] = v
	}
	return result
}
