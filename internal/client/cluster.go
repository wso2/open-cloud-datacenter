package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ClusterTaint is a Kubernetes taint applied to a node pool.
type ClusterTaint struct {
	Key    string `json:"key"`
	Value  string `json:"value,omitempty"`
	Effect string `json:"effect"`
}

// ClusterSystemPool is the system (control-plane + etcd) pool spec for a create request.
type ClusterSystemPool struct {
	Size   string `json:"size"`
	Count  int    `json:"count"`
	DiskGB int    `json:"disk_gb,omitempty"`
}

// ClusterWorkerPool is a worker node pool spec for a create request.
type ClusterWorkerPool struct {
	Name      string            `json:"name"`
	Size      string            `json:"size"`
	Count     int               `json:"count"`
	DiskGB    int               `json:"disk_gb,omitempty"`
	ImageName string            `json:"image_name,omitempty"`
	Taints    []ClusterTaint    `json:"taints,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// ClusterCreateRequest maps to POST /v1/tenants/{tenant_id}/projects/{project_id}/clusters.
type ClusterCreateRequest struct {
	Name        string              `json:"name"`
	K8sVersion  string              `json:"k8s_version"`
	ImageName   string              `json:"image_name"`
	SystemPool  ClusterSystemPool   `json:"system_pool"`
	WorkerPools []ClusterWorkerPool `json:"worker_pools,omitempty"`
	NetworkName string              `json:"network_name,omitempty"`
	VNetID      string              `json:"vnet_id,omitempty"`
	SubnetID    string              `json:"subnet_id,omitempty"`
}

// ClusterSystemPoolResponse is the system pool details in a create or read response.
type ClusterSystemPoolResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	Size      string `json:"size"`
	Count     int    `json:"count"`
	DiskGB    int    `json:"disk_gb"`
	Status    string `json:"status"`
	Message   string `json:"message"`
	CreatedAt string `json:"created_at"`
}

// ClusterResource is the inner "resource" object in the 202 create response.
type ClusterResource struct {
	ID              string                     `json:"id"`
	Name            string                     `json:"name"`
	Status          string                     `json:"status"`
	TenantID        string                     `json:"tenant_id"`
	ProviderType    string                     `json:"provider_type"`
	SystemPool      *ClusterSystemPoolResponse `json:"system_pool"`
	WorkerPoolCount int                        `json:"worker_pool_count"`
	TotalNodeCount  int                        `json:"total_node_count"`
	Message         string                     `json:"message"`
	CreatedAt       string                     `json:"created_at"`
}

// ClusterCreateResponse is the outer 202 wrapper for cluster creation.
type ClusterCreateResponse struct {
	Resource *ClusterResource `json:"resource"`
	Note     string           `json:"note"`
}

// ClusterReadResponse is the shape returned by GET /clusters/{id}.
type ClusterReadResponse struct {
	ID              string                     `json:"id"`
	Name            string                     `json:"name"`
	Status          string                     `json:"status"`
	TenantID        string                     `json:"tenant_id"`
	ProviderType    string                     `json:"provider_type"`
	SystemPool      *ClusterSystemPoolResponse `json:"system_pool"`
	WorkerPoolCount int                        `json:"worker_pool_count"`
	TotalNodeCount  int                        `json:"total_node_count"`
	Message         string                     `json:"message"`
	CreatedAt       string                     `json:"created_at"`
}

// CreateCluster sends POST /v1/tenants/{tenantID}/projects/{projectID}/clusters.
func (c *DCAPIClient) CreateCluster(ctx context.Context, tenantID, projectID string, req ClusterCreateRequest) (*ClusterCreateResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/clusters", tenantID, projectID)
	respBytes, err := c.doRequest(ctx, "POST", path, req)
	if err != nil {
		return nil, fmt.Errorf("CreateCluster: %w", err)
	}
	var resp ClusterCreateResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, fmt.Errorf("CreateCluster: failed to parse response: %w", err)
	}
	return &resp, nil
}

// GetCluster sends GET /v1/tenants/{tenantID}/projects/{projectID}/clusters/{clusterID}.
// Returns (nil, nil) on HTTP 404 — signals drift to the caller.
func (c *DCAPIClient) GetCluster(ctx context.Context, tenantID, projectID, clusterID string) (*ClusterReadResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/clusters/%s", tenantID, projectID, clusterID)
	respBytes, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return nil, nil
		}
		return nil, fmt.Errorf("GetCluster: %w", err)
	}
	var cluster ClusterReadResponse
	if err := json.Unmarshal(respBytes, &cluster); err != nil {
		return nil, fmt.Errorf("GetCluster: failed to parse response: %w", err)
	}
	return &cluster, nil
}

// DeleteCluster sends DELETE /v1/tenants/{tenantID}/projects/{projectID}/clusters/{clusterID}.
// Deletion is async — poll GetCluster until (nil, nil) to confirm removal.
func (c *DCAPIClient) DeleteCluster(ctx context.Context, tenantID, projectID, clusterID string) error {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/clusters/%s", tenantID, projectID, clusterID)
	_, err := c.doRequest(ctx, "DELETE", path, nil)
	if err != nil {
		return fmt.Errorf("DeleteCluster (tenant %q, project %q, cluster %q): %w", tenantID, projectID, clusterID, err)
	}
	return nil
}

// GetClusterKubeconfig fetches the raw kubeconfig YAML for an ACTIVE cluster.
// Returns ("", nil) on HTTP 404. Returns an error on HTTP 409 (cluster not yet ACTIVE).
func (c *DCAPIClient) GetClusterKubeconfig(ctx context.Context, tenantID, projectID, clusterID string) (string, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/clusters/%s/kubeconfig", tenantID, projectID, clusterID)
	respBytes, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return "", nil
		}
		return "", fmt.Errorf("GetClusterKubeconfig: %w", err)
	}
	return string(respBytes), nil
}
