// Package client — hand-written cluster and node-pool helpers.
//
// The generated client (internal/client/generated/dcapi.gen.go) carries the
// pre-R7 CreateClusterRequest shape (node_count / node_size / node_disk_gb)
// and has no node-pool operations at all.  Until the generator regenerates
// from the updated spec these methods provide the correct wire surface.
//
// Wire types mirror the dc-api handler structs exactly:
//   - CreateClusterRequest  ← handlers.CreateClusterRequest
//   - SystemPoolSpec        ← handlers.SystemPoolSpec
//   - ClusterResponse       ← handlers.ClusterResponse
//   - NodePoolResponse      ← handlers.NodePoolResponse
//   - NodePoolTaint         ← models.NodePoolTaint
//   - AddNodePoolRequest    ← handlers.AddNodePoolRequest
//   - PatchNodePoolRequest  ← handlers.PatchNodePoolRequest
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/viper"
)

// ── Wire types ───────────────────────────────────────────────────────────────

// NodePoolTaint mirrors models.NodePoolTaint and the OpenAPI NodePoolTaint schema.
type NodePoolTaint struct {
	Key    string `json:"key"`
	Value  string `json:"value,omitempty"`
	Effect string `json:"effect"`
}

// SystemPoolSpec mirrors handlers.SystemPoolSpec.
type SystemPoolSpec struct {
	Size   string `json:"size"`
	Count  int    `json:"count"`
	DiskGB int    `json:"disk_gb,omitempty"`
}

// CreateClusterRequest mirrors handlers.CreateClusterRequest (R7 shape).
// NetworkName OR (VNetID + SubnetID) must be set — not both, not neither.
// WorkerPools is optional; up to 10 worker pools can be provisioned together
// with the cluster instead of adding them after via node-pool add.
type CreateClusterRequest struct {
	Name        string               `json:"name"`
	K8sVersion  string               `json:"k8s_version"`
	ImageName   string               `json:"image_name"`
	SystemPool  *SystemPoolSpec      `json:"system_pool"`
	WorkerPools []AddNodePoolRequest `json:"worker_pools,omitempty"`
	NetworkName string               `json:"network_name,omitempty"`
	VNetID      string               `json:"vnet_id,omitempty"`
	SubnetID    string               `json:"subnet_id,omitempty"`
}

// NodePoolResponse mirrors handlers.NodePoolResponse.
type NodePoolResponse struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Role      string            `json:"role"`
	Size      string            `json:"size"`
	Count     int               `json:"count"`
	DiskGB    *int              `json:"disk_gb,omitempty"`
	Taints    []NodePoolTaint   `json:"taints,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	Status    string            `json:"status"`
	Message   string            `json:"message,omitempty"`
	CreatedAt string            `json:"created_at"`
}

// ClusterResponse mirrors handlers.ClusterResponse (R7 shape).
type ClusterResponse struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Status          string            `json:"status"`
	TenantID        string            `json:"tenant_id"`
	ProviderType    string            `json:"provider_type"`
	Message         string            `json:"message,omitempty"`
	CreatedAt       string            `json:"created_at"`
	SystemPool      *NodePoolResponse `json:"system_pool,omitempty"`
	WorkerPoolCount int               `json:"worker_pool_count"`
	TotalNodeCount  int               `json:"total_node_count"`
}

// CreateClusterResponse wraps the 202 response from POST /clusters.
type CreateClusterResponse struct {
	Resource ClusterResponse `json:"resource"`
	Note     string          `json:"note"`
}

// AddNodePoolRequest mirrors handlers.AddNodePoolRequest.
type AddNodePoolRequest struct {
	Name      string            `json:"name"`
	Size      string            `json:"size"`
	Count     int               `json:"count"`
	DiskGB    int               `json:"disk_gb,omitempty"`
	ImageName string            `json:"image_name,omitempty"`
	Taints    []NodePoolTaint   `json:"taints,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// PatchNodePoolRequest mirrors handlers.PatchNodePoolRequest.
// All fields are optional on the wire; a nil Taints/Labels means "don't change".
type PatchNodePoolRequest struct {
	Count  int               `json:"count,omitempty"`
	Taints *[]NodePoolTaint  `json:"taints,omitempty"`
	Labels *map[string]string `json:"labels,omitempty"`
}

// ── HTTP helpers ──────────────────────────────────────────────────────────────

// clusterURL builds the base path for cluster operations.
func clusterURL(tenantID, projectID string) string {
	base := viper.GetString("dcapi_url")
	return fmt.Sprintf("%s/v1/tenants/%s/projects/%s/clusters", base, tenantID, projectID)
}

func (c *Client) doJSON(ctx context.Context, method, url string, body interface{}) ([]byte, int, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal request body: %w", err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, r)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// The Typed client already has the auth editor but it's not accessible here.
	// Re-use the underlying HTTP client by calling through Typed's server URL,
	// but we need to inject the auth header ourselves.  The cleanest way is to
	// delegate to the generated client's underlying transport which already has
	// the request editor wired.  We do this by calling Typed's raw Do-method.
	// However, since oapi-codegen doesn't expose that directly, we replicate the
	// header injection using the same credentials already passed to New().
	// The access token was already embedded in the Typed client via the request
	// editor; we reproduce it here by reading from the same field.
	// Because client.New injects the token via a request editor there is no easy
	// path to recover it from Typed. The approach used across the codebase for
	// non-generated calls is to re-call client.New — but here we are *inside*
	// the client struct.  Instead, store the token at construction time.
	// NOTE: this is a chicken-and-egg problem with the current Client struct.
	// We resolve it by piggy-backing on the Typed client's underlying Do via the
	// generated ClientInterface which has a public field for the server.
	// Simplest pragmatic fix: pass the token as a header directly using the
	// token stored on the struct (requires adding the field — see below).
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	// Auth header is injected via c.token (set in New — see updated New func).
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return data, resp.StatusCode, err
}

func decodeJSON[T any](data []byte, status int, successStatus int) (*T, error) {
	if status != successStatus {
		var apiErr struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &apiErr) == nil && apiErr.Error != "" {
			return nil, fmt.Errorf("DC-API error (%d): %s", status, apiErr.Error)
		}
		return nil, fmt.Errorf("DC-API returned HTTP %d: %s", status, string(data))
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}

// ── Cluster methods ───────────────────────────────────────────────────────────

// CreateClusterV2 posts the R7-shaped cluster create request.
// Returns the 202 response body.
func (c *Client) CreateClusterV2(ctx context.Context, tenantID, projectID string, req *CreateClusterRequest) (*CreateClusterResponse, error) {
	url := clusterURL(tenantID, projectID)
	data, status, err := c.doJSON(ctx, http.MethodPost, url, req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", url, err)
	}
	return decodeJSON[CreateClusterResponse](data, status, http.StatusAccepted)
}

// GetClusterV2 fetches a single cluster by UUID string.
func (c *Client) GetClusterV2(ctx context.Context, tenantID, projectID, id string) (*ClusterResponse, error) {
	url := clusterURL(tenantID, projectID) + "/" + id
	data, status, err := c.doJSON(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	return decodeJSON[ClusterResponse](data, status, http.StatusOK)
}

// ListClustersV2 lists all clusters in a project.
func (c *Client) ListClustersV2(ctx context.Context, tenantID, projectID string) ([]ClusterResponse, error) {
	url := clusterURL(tenantID, projectID)
	data, status, err := c.doJSON(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	if status != http.StatusOK {
		var apiErr struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &apiErr) == nil && apiErr.Error != "" {
			return nil, fmt.Errorf("DC-API error (%d): %s", status, apiErr.Error)
		}
		return nil, fmt.Errorf("DC-API returned HTTP %d: %s", status, string(data))
	}
	var out []ClusterResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}

// DeleteClusterV2 issues DELETE and expects 202 Accepted.
func (c *Client) DeleteClusterV2(ctx context.Context, tenantID, projectID, id string) error {
	url := clusterURL(tenantID, projectID) + "/" + id
	data, status, err := c.doJSON(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("DELETE %s: %w", url, err)
	}
	if status >= http.StatusMultipleChoices {
		var apiErr struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &apiErr) == nil && apiErr.Error != "" {
			return fmt.Errorf("DC-API error (%d): %s", status, apiErr.Error)
		}
		return fmt.Errorf("DC-API returned HTTP %d: %s", status, string(data))
	}
	return nil
}

// ResolveClusterID resolves a cluster name or UUID to a UUID string.
// If idOrName is already a valid UUID it is returned as-is.
func (c *Client) ResolveClusterID(ctx context.Context, tenantID, projectID, idOrName string) (string, error) {
	// Quick UUID check — if it looks like a UUID, use it directly.
	if isUUID(idOrName) {
		return idOrName, nil
	}
	clusters, err := c.ListClustersV2(ctx, tenantID, projectID)
	if err != nil {
		return "", fmt.Errorf("list clusters for name resolution: %w", err)
	}
	for _, cl := range clusters {
		if cl.Name == idOrName {
			return cl.ID, nil
		}
	}
	return "", fmt.Errorf("cluster %q not found", idOrName)
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
}

// ── Node-pool methods ─────────────────────────────────────────────────────────

func nodePoolURL(tenantID, projectID, clusterID string) string {
	return clusterURL(tenantID, projectID) + "/" + clusterID + "/node-pools"
}

// ListNodePools returns all pools for a cluster.
func (c *Client) ListNodePools(ctx context.Context, tenantID, projectID, clusterID string) ([]NodePoolResponse, error) {
	url := nodePoolURL(tenantID, projectID, clusterID)
	data, status, err := c.doJSON(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	if status != http.StatusOK {
		var apiErr struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &apiErr) == nil && apiErr.Error != "" {
			return nil, fmt.Errorf("DC-API error (%d): %s", status, apiErr.Error)
		}
		return nil, fmt.Errorf("DC-API returned HTTP %d: %s", status, string(data))
	}
	var out []NodePoolResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}

// GetNodePool fetches a single pool by name.
func (c *Client) GetNodePool(ctx context.Context, tenantID, projectID, clusterID, poolName string) (*NodePoolResponse, error) {
	url := nodePoolURL(tenantID, projectID, clusterID) + "/" + poolName
	data, status, err := c.doJSON(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	return decodeJSON[NodePoolResponse](data, status, http.StatusOK)
}

// AddNodePool creates a new worker pool in an ACTIVE cluster.
func (c *Client) AddNodePool(ctx context.Context, tenantID, projectID, clusterID string, req *AddNodePoolRequest) (*NodePoolResponse, error) {
	url := nodePoolURL(tenantID, projectID, clusterID)
	data, status, err := c.doJSON(ctx, http.MethodPost, url, req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", url, err)
	}
	return decodeJSON[NodePoolResponse](data, status, http.StatusAccepted)
}

// ScaleOrUpdateNodePool patches a pool (count / taints / labels).
func (c *Client) ScaleOrUpdateNodePool(ctx context.Context, tenantID, projectID, clusterID, poolName string, req *PatchNodePoolRequest) (*NodePoolResponse, error) {
	url := nodePoolURL(tenantID, projectID, clusterID) + "/" + poolName
	data, status, err := c.doJSON(ctx, http.MethodPatch, url, req)
	if err != nil {
		return nil, fmt.Errorf("PATCH %s: %w", url, err)
	}
	return decodeJSON[NodePoolResponse](data, status, http.StatusAccepted)
}

// DeleteNodePool removes a worker pool (async; 202 Accepted).
func (c *Client) DeleteNodePool(ctx context.Context, tenantID, projectID, clusterID, poolName string) error {
	url := nodePoolURL(tenantID, projectID, clusterID) + "/" + poolName
	data, status, err := c.doJSON(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("DELETE %s: %w", url, err)
	}
	if status >= http.StatusMultipleChoices {
		var apiErr struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &apiErr) == nil && apiErr.Error != "" {
			return fmt.Errorf("DC-API error (%d): %s", status, apiErr.Error)
		}
		return fmt.Errorf("DC-API returned HTTP %d: %s", status, string(data))
	}
	return nil
}
