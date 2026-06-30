package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// NodePoolTaint is a Kubernetes taint applied to a node pool.
type NodePoolTaint struct {
	Key    string `json:"key"`
	Value  string `json:"value,omitempty"`
	Effect string `json:"effect"`
}

// NodePoolCreateRequest maps to POST .../clusters/{clusterID}/node-pools.
type NodePoolCreateRequest struct {
	Name      string            `json:"name"`
	Size      string            `json:"size"`
	Count     int               `json:"count"`
	DiskGB    int               `json:"disk_gb,omitempty"`
	ImageName string            `json:"image_name,omitempty"`
	Taints    []NodePoolTaint   `json:"taints,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// NodePoolUpdateRequest maps to PATCH .../clusters/{clusterID}/node-pools/{poolName}.
// Taints and Labels use full-replace semantics — send the complete desired state.
// An empty slice/map clears the existing values; omitting (nil) leaves them unchanged.
type NodePoolUpdateRequest struct {
	Count  int               `json:"count"`
	Taints []NodePoolTaint   `json:"taints"`
	Labels map[string]string `json:"labels"`
}

// NodePoolResponse is the shape returned by Create (202) and Read (200).
// Unlike Cluster, Create returns this directly without a "resource" wrapper.
type NodePoolResponse struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Role      string            `json:"role"`
	Size      string            `json:"size"`
	Count     int               `json:"count"`
	DiskGB    int               `json:"disk_gb"`
	ImageName string            `json:"image_name"`
	Taints    []NodePoolTaint   `json:"taints"`
	Labels    map[string]string `json:"labels"`
	Status    string            `json:"status"`
	Message   string            `json:"message"`
	CreatedAt string            `json:"created_at"`
}

// CreateNodePool sends POST .../clusters/{clusterID}/node-pools.
// Returns 202 Accepted; poll GetNodePool until status "ready".
func (c *DCAPIClient) CreateNodePool(ctx context.Context, tenantID, projectID, clusterID string, req NodePoolCreateRequest) (*NodePoolResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/clusters/%s/node-pools", tenantID, projectID, clusterID)
	respBytes, err := c.doRequest(ctx, "POST", path, req)
	if err != nil {
		return nil, fmt.Errorf("CreateNodePool: %w", err)
	}
	var pool NodePoolResponse
	if err := json.Unmarshal(respBytes, &pool); err != nil {
		return nil, fmt.Errorf("CreateNodePool: failed to parse response: %w", err)
	}
	return &pool, nil
}

// GetNodePool sends GET .../clusters/{clusterID}/node-pools/{poolName}.
// {poolName} is the string name, not a UUID.
// Returns (nil, nil) on HTTP 404 — signals drift to the caller.
func (c *DCAPIClient) GetNodePool(ctx context.Context, tenantID, projectID, clusterID, poolName string) (*NodePoolResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/clusters/%s/node-pools/%s", tenantID, projectID, clusterID, poolName)
	respBytes, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return nil, nil
		}
		return nil, fmt.Errorf("GetNodePool: %w", err)
	}
	var pool NodePoolResponse
	if err := json.Unmarshal(respBytes, &pool); err != nil {
		return nil, fmt.Errorf("GetNodePool: failed to parse response: %w", err)
	}
	return &pool, nil
}

// UpdateNodePool sends PATCH .../clusters/{clusterID}/node-pools/{poolName}.
// count, taints, and labels are the only updatable fields; taints/labels use full-replace semantics.
func (c *DCAPIClient) UpdateNodePool(ctx context.Context, tenantID, projectID, clusterID, poolName string, req NodePoolUpdateRequest) (*NodePoolResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/clusters/%s/node-pools/%s", tenantID, projectID, clusterID, poolName)
	respBytes, err := c.doRequest(ctx, "PATCH", path, req)
	if err != nil {
		return nil, fmt.Errorf("UpdateNodePool: %w", err)
	}
	var pool NodePoolResponse
	if err := json.Unmarshal(respBytes, &pool); err != nil {
		return nil, fmt.Errorf("UpdateNodePool: failed to parse response: %w", err)
	}
	return &pool, nil
}

// DeleteNodePool sends DELETE .../clusters/{clusterID}/node-pools/{poolName}.
// Deletion is async — poll GetNodePool until (nil, nil) to confirm removal.
func (c *DCAPIClient) DeleteNodePool(ctx context.Context, tenantID, projectID, clusterID, poolName string) error {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/clusters/%s/node-pools/%s", tenantID, projectID, clusterID, poolName)
	_, err := c.doRequest(ctx, "DELETE", path, nil)
	if err != nil {
		return fmt.Errorf("DeleteNodePool (cluster %q, pool %q): %w", clusterID, poolName, err)
	}
	return nil
}
