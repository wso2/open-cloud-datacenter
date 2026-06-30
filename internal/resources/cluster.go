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

// ResourceCluster returns the schema.Resource for "dcapi_cluster".
func ResourceCluster() *schema.Resource {
	return &schema.Resource{
		CreateContext: resourceClusterCreate,
		ReadContext:   resourceClusterRead,
		DeleteContext: resourceClusterDelete,

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(30 * time.Minute),
			Delete: schema.DefaultTimeout(20 * time.Minute),
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
			"name": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Cluster name. DNS-label pattern, max 32 chars. Immutable.",
			},
			"k8s_version": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Kubernetes version (e.g. \"v1.33.10+rke2r3\"). Immutable.",
			},
			"image_name": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "VM image for cluster nodes (format: \"namespace/resource-name\"). Immutable.",
			},

			// ── system_pool block (required) ──

			"system_pool": {
				Type:        schema.TypeList,
				Required:    true,
				ForceNew:    true,
				MaxItems:    1,
				Description: "System (control-plane + etcd) node pool. Immutable.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"size": {
							Type:        schema.TypeString,
							Required:    true,
							ForceNew:    true,
							Description: "Node size: \"small\"|\"medium\"|\"large\"|\"xlarge\".",
						},
						"count": {
							Type:        schema.TypeInt,
							Required:    true,
							ForceNew:    true,
							Description: "Number of system nodes. Must be 1, 3, or 5 (etcd quorum sizes).",
						},
						"disk_gb": {
							Type:        schema.TypeInt,
							Optional:    true,
							ForceNew:    true,
							Description: "Root disk size in GB. Minimum 40. Defaults to size default if omitted.",
						},
					},
				},
			},

			// ── worker_pools block (optional) ──

			"worker_pools": {
				Type:        schema.TypeList,
				Optional:    true,
				ForceNew:    true,
				Description: "Initial worker node pools. Immutable after creation; manage post-create pools via dcapi_node_pool.",
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"name": {
							Type:        schema.TypeString,
							Required:    true,
							ForceNew:    true,
							Description: "Pool name. DNS-label pattern; must not be \"system\".",
						},
						"size": {
							Type:        schema.TypeString,
							Required:    true,
							ForceNew:    true,
							Description: "Node size: \"small\"|\"medium\"|\"large\"|\"xlarge\".",
						},
						"count": {
							Type:        schema.TypeInt,
							Required:    true,
							ForceNew:    true,
							Description: "Number of worker nodes (1–50).",
						},
						"disk_gb": {
							Type:        schema.TypeInt,
							Optional:    true,
							ForceNew:    true,
							Description: "Root disk size in GB. Minimum 40.",
						},
						"image_name": {
							Type:        schema.TypeString,
							Optional:    true,
							ForceNew:    true,
							Description: "Override image for this pool. Inherits cluster image_name if omitted.",
						},
						"taints": {
							Type:        schema.TypeList,
							Optional:    true,
							ForceNew:    true,
							Description: "Kubernetes taints to apply to nodes in this pool (max 10).",
							Elem: &schema.Resource{
								Schema: map[string]*schema.Schema{
									"key": {
										Type:     schema.TypeString,
										Required: true,
										ForceNew: true,
									},
									"value": {
										Type:     schema.TypeString,
										Optional: true,
										ForceNew: true,
									},
									"effect": {
										Type:        schema.TypeString,
										Required:    true,
										ForceNew:    true,
										Description: "\"NoSchedule\"|\"PreferNoSchedule\"|\"NoExecute\".",
									},
								},
							},
						},
						"labels": {
							Type:        schema.TypeMap,
							Optional:    true,
							ForceNew:    true,
							Description: "Kubernetes node labels (max 50 entries).",
							Elem:        &schema.Schema{Type: schema.TypeString},
						},
					},
				},
			},

			// ── Networking — mutually exclusive ──

			"network_name": {
				Type:        schema.TypeString,
				Optional:    true,
				ForceNew:    true,
				Description: "Legacy bridge network name (e.g. \"iaas/vm-network-001\"). Mutually exclusive with vnet_id/subnet_id.",
			},
			"vnet_id": {
				Type:        schema.TypeString,
				Optional:    true,
				ForceNew:    true,
				Description: "UUID of the VNet (VPC mode). Requires subnet_id. Mutually exclusive with network_name.",
			},
			"subnet_id": {
				Type:        schema.TypeString,
				Optional:    true,
				ForceNew:    true,
				Description: "UUID of the Subnet (VPC mode). Requires vnet_id. Mutually exclusive with network_name.",
			},

			// ── Computed ──

			"cluster_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "API-generated UUID4 for the cluster.",
			},
			"status": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Lifecycle status: PENDING | ACTIVE | FAILED. Set by the API.",
			},
			"provider_type": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Underlying infrastructure provider (e.g. \"rancher\"). Set by the API.",
			},
			"worker_pool_count": {
				Type:        schema.TypeInt,
				Computed:    true,
				Description: "Number of worker pools. Set by the API.",
			},
			"total_node_count": {
				Type:        schema.TypeInt,
				Computed:    true,
				Description: "Total node count across all pools. Set by the API.",
			},
			"message": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Human-readable status message. Useful for debugging FAILED state.",
			},
			"created_at": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "RFC3339 creation timestamp. Set by the API.",
			},
			"kubeconfig": {
				Type:        schema.TypeString,
				Computed:    true,
				Sensitive:   true,
				Description: "Kubeconfig YAML for connecting to the cluster. Populated once ACTIVE.",
			},
		},
	}
}

func resourceClusterCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	tenantID := d.Get("tenant_id").(string)
	projectID := d.Get("project_id").(string)
	networkName := d.Get("network_name").(string)
	vnetID := d.Get("vnet_id").(string)
	subnetID := d.Get("subnet_id").(string)

	// Validate networking mutual exclusion.
	if networkName != "" && (vnetID != "" || subnetID != "") {
		return diag.FromErr(fmt.Errorf("network_name is mutually exclusive with vnet_id/subnet_id"))
	}
	if vnetID != "" && subnetID == "" {
		return diag.FromErr(fmt.Errorf("subnet_id is required when vnet_id is set"))
	}
	if subnetID != "" && vnetID == "" {
		return diag.FromErr(fmt.Errorf("vnet_id is required when subnet_id is set"))
	}

	systemPool, err := expandSystemPool(d)
	if err != nil {
		return diag.FromErr(err)
	}

	req := client.ClusterCreateRequest{
		Name:        d.Get("name").(string),
		K8sVersion:  d.Get("k8s_version").(string),
		ImageName:   d.Get("image_name").(string),
		SystemPool:  systemPool,
		WorkerPools: expandWorkerPools(d),
		NetworkName: networkName,
		VNetID:      vnetID,
		SubnetID:    subnetID,
	}

	resp, err := c.CreateCluster(ctx, tenantID, projectID, req)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error creating cluster: %w", err))
	}
	if resp.Resource == nil {
		return diag.FromErr(fmt.Errorf("CreateCluster: API response missing 'resource' object"))
	}

	d.SetId(fmt.Sprintf("%s/%s/%s", tenantID, projectID, resp.Resource.ID))

	var diags diag.Diagnostics
	diags = appendSet(diags, d, "cluster_id", resp.Resource.ID)
	diags = appendSet(diags, d, "status", resp.Resource.Status)
	diags = appendSet(diags, d, "provider_type", resp.Resource.ProviderType)
	diags = appendSet(diags, d, "worker_pool_count", resp.Resource.WorkerPoolCount)
	diags = appendSet(diags, d, "total_node_count", resp.Resource.TotalNodeCount)
	diags = appendSet(diags, d, "message", resp.Resource.Message)
	diags = appendSet(diags, d, "created_at", resp.Resource.CreatedAt)
	if diags.HasError() {
		return diags
	}

	if err := waitForClusterActive(ctx, c, tenantID, projectID, resp.Resource.ID, d.Timeout(schema.TimeoutCreate)); err != nil {
		return diag.FromErr(err)
	}

	// Fetch kubeconfig once the cluster is ACTIVE.
	kubeconfig, err := c.GetClusterKubeconfig(ctx, tenantID, projectID, resp.Resource.ID)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error fetching cluster kubeconfig: %w", err))
	}
	if err := d.Set("kubeconfig", kubeconfig); err != nil {
		return diag.FromErr(err)
	}

	return resourceClusterRead(ctx, d, meta)
}

func resourceClusterRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 3)
	if len(parts) != 3 {
		return diag.FromErr(fmt.Errorf("invalid cluster state ID %q: expected 'tenant_id/project_id/cluster_id'", d.Id()))
	}
	tenantID, projectID, clusterID := parts[0], parts[1], parts[2]

	cluster, err := c.GetCluster(ctx, tenantID, projectID, clusterID)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error reading cluster %q: %w", clusterID, err))
	}
	if cluster == nil {
		d.SetId("")
		return nil
	}

	var diags diag.Diagnostics
	diags = appendSet(diags, d, "cluster_id", cluster.ID)
	diags = appendSet(diags, d, "tenant_id", cluster.TenantID)
	diags = appendSet(diags, d, "name", cluster.Name)
	diags = appendSet(diags, d, "status", cluster.Status)
	diags = appendSet(diags, d, "provider_type", cluster.ProviderType)
	diags = appendSet(diags, d, "worker_pool_count", cluster.WorkerPoolCount)
	diags = appendSet(diags, d, "total_node_count", cluster.TotalNodeCount)
	diags = appendSet(diags, d, "message", cluster.Message)
	diags = appendSet(diags, d, "created_at", cluster.CreatedAt)

	if cluster.SystemPool != nil {
		diags = appendSet(diags, d, "system_pool", []interface{}{
			map[string]interface{}{
				"size":    cluster.SystemPool.Size,
				"count":   cluster.SystemPool.Count,
				"disk_gb": cluster.SystemPool.DiskGB,
			},
		})
	}

	if diags.HasError() {
		return diags
	}

	// Re-fetch kubeconfig when cluster is ACTIVE (keeps it current across refreshes).
	if cluster.Status == "ACTIVE" {
		kubeconfig, err := c.GetClusterKubeconfig(ctx, tenantID, projectID, clusterID)
		if err != nil {
			// Non-fatal: preserve existing value if the kubeconfig endpoint is temporarily unavailable.
			diags = append(diags, diag.Diagnostic{
				Severity: diag.Warning,
				Summary:  "Could not refresh kubeconfig",
				Detail:   err.Error(),
			})
		} else if kubeconfig != "" {
			diags = appendSet(diags, d, "kubeconfig", kubeconfig)
		}
	}

	return diags
}

func resourceClusterDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)

	parts := strings.SplitN(d.Id(), "/", 3)
	if len(parts) != 3 {
		return diag.FromErr(fmt.Errorf("invalid cluster state ID %q: expected 'tenant_id/project_id/cluster_id'", d.Id()))
	}
	tenantID, projectID, clusterID := parts[0], parts[1], parts[2]

	if err := c.DeleteCluster(ctx, tenantID, projectID, clusterID); err != nil {
		return diag.FromErr(fmt.Errorf("error deleting cluster %q: %w", clusterID, err))
	}

	if err := waitForClusterDeleted(ctx, c, tenantID, projectID, clusterID, d.Timeout(schema.TimeoutDelete)); err != nil {
		return diag.FromErr(err)
	}

	return nil
}

func waitForClusterActive(ctx context.Context, c *client.DCAPIClient, tenantID, projectID, clusterID string, timeout time.Duration) error {
	conf := &resource.StateChangeConf{
		Pending:    []string{"PENDING"},
		Target:     []string{"ACTIVE"},
		Timeout:    timeout,
		MinTimeout: 15 * time.Second,
		Refresh: func() (interface{}, string, error) {
			cluster, err := c.GetCluster(ctx, tenantID, projectID, clusterID)
			if err != nil {
				return nil, "", err
			}
			if cluster == nil {
				return nil, "", fmt.Errorf("cluster %q disappeared while waiting for ACTIVE status", clusterID)
			}
			if cluster.Status == "FAILED" {
				return nil, "FAILED", fmt.Errorf("cluster %q provisioning failed: %s", clusterID, cluster.Message)
			}
			return cluster, cluster.Status, nil
		},
	}
	_, err := conf.WaitForStateContext(ctx)
	return err
}

func waitForClusterDeleted(ctx context.Context, c *client.DCAPIClient, tenantID, projectID, clusterID string, timeout time.Duration) error {
	conf := &resource.StateChangeConf{
		Pending:    []string{"ACTIVE", "DELETING"},
		Target:     []string{"DELETED"},
		Timeout:    timeout,
		MinTimeout: 15 * time.Second,
		Refresh: func() (interface{}, string, error) {
			cluster, err := c.GetCluster(ctx, tenantID, projectID, clusterID)
			if err != nil {
				return nil, "", err
			}
			if cluster == nil {
				return "deleted", "DELETED", nil
			}
			return cluster, cluster.Status, nil
		},
	}
	_, err := conf.WaitForStateContext(ctx)
	return err
}

// expandSystemPool extracts the system_pool block from state into a ClusterSystemPool.
func expandSystemPool(d *schema.ResourceData) (client.ClusterSystemPool, error) {
	list := d.Get("system_pool").([]interface{})
	if len(list) == 0 {
		return client.ClusterSystemPool{}, fmt.Errorf("system_pool is required")
	}
	m := list[0].(map[string]interface{})
	sp := client.ClusterSystemPool{
		Size:  m["size"].(string),
		Count: m["count"].(int),
	}
	if v, ok := m["disk_gb"].(int); ok && v > 0 {
		sp.DiskGB = v
	}
	return sp, nil
}

// expandWorkerPools extracts the worker_pools list from state into []ClusterWorkerPool.
func expandWorkerPools(d *schema.ResourceData) []client.ClusterWorkerPool {
	list := d.Get("worker_pools").([]interface{})
	if len(list) == 0 {
		return nil
	}
	pools := make([]client.ClusterWorkerPool, len(list))
	for i, raw := range list {
		m := raw.(map[string]interface{})
		pool := client.ClusterWorkerPool{
			Name:  m["name"].(string),
			Size:  m["size"].(string),
			Count: m["count"].(int),
		}
		if v, ok := m["disk_gb"].(int); ok && v > 0 {
			pool.DiskGB = v
		}
		if v, ok := m["image_name"].(string); ok && v != "" {
			pool.ImageName = v
		}
		if taintsList, ok := m["taints"].([]interface{}); ok {
			for _, t := range taintsList {
				tm := t.(map[string]interface{})
				taint := client.ClusterTaint{
					Key:    tm["key"].(string),
					Effect: tm["effect"].(string),
				}
				if v, ok := tm["value"].(string); ok {
					taint.Value = v
				}
				pool.Taints = append(pool.Taints, taint)
			}
		}
		if labelsMap, ok := m["labels"].(map[string]interface{}); ok && len(labelsMap) > 0 {
			pool.Labels = make(map[string]string, len(labelsMap))
			for k, v := range labelsMap {
				pool.Labels[k] = v.(string)
			}
		}
		pools[i] = pool
	}
	return pools
}

// appendSet calls d.Set and appends any error into the diagnostics slice.
func appendSet(diags diag.Diagnostics, d *schema.ResourceData, key string, val interface{}) diag.Diagnostics {
	if err := d.Set(key, val); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	return diags
}
