// Project-related API calls for the DC-API client.
// All functions are methods on *DCAPIClient so they share the HTTP plumbing in client.go.
// Projects are the second level in the hierarchy (tenant → project). VNets, Subnets, and
// VMs all require both tenantID and projectID in their URL paths and request bodies.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ProjectCreateRequest maps to POST /v1/tenants/{tenant_id}/projects body.
// Fields tagged omitempty are excluded from JSON when zero-valued; the API then applies its defaults.

type ProjectCreateRequest struct {
	// ID is the user-chosen project slug, e.g. "infra". REQUIRED. Immutable.
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	
	// Quota fields default to 20/64/500 server-side when omitted.
	CPUCores  int `json:"cpu_cores,omitempty"`
	MemoryGB  int `json:"memory_gb,omitempty"`
	StorageGB int `json:"storage_gb,omitempty"`
	
	// Limit fields default to 10/2/50/3 server-side when omitted. Immutable after creation.
	MaxVNets     int `json:"max_vnets,omitempty"`
	MaxClusters  int `json:"max_clusters,omitempty"`
	MaxVolumes   int `json:"max_volumes,omitempty"`
	MaxPublicIPs int `json:"max_public_ips,omitempty"`
}

// ProjectUpdateRequest maps to PATCH /v1/tenants/{tenant_id}/projects/{project_id} body.
// *int pointers with omitempty: nil means "omit this field / leave unchanged"; &v means "update to v".
type ProjectUpdateRequest struct {
	CPUCores  *int `json:"cpu_cores,omitempty"`
	MemoryGB  *int `json:"memory_gb,omitempty"`
	StorageGB *int `json:"storage_gb,omitempty"`
}

// ProjectResponse is the JSON shape returned by Create (201), Read (200), and Update (200).
type ProjectResponse struct {
	ID          string `json:"id"`
	TenantID    string `json:"tenant_id"`
	ProjectUUID string `json:"project_uuid"`
	TenantUUID  string `json:"tenant_uuid"`
	Name        string `json:"name"`
	Description string `json:"description"`
	CPUCores    int    `json:"cpu_cores"`
	MemoryGB    int    `json:"memory_gb"`
	StorageGB   int    `json:"storage_gb"`
	MaxVNets    int    `json:"max_vnets"`
	MaxClusters int    `json:"max_clusters"`
	MaxVolumes  int    `json:"max_volumes"`
	MaxPublicIPs int   `json:"max_public_ips"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	CreatedBy   string `json:"created_by"`
}

// CreateProject sends POST /v1/tenants/{tenantID}/projects and returns the created project.
func (c *DCAPIClient) CreateProject(ctx context.Context, tenantID string, req ProjectCreateRequest) (*ProjectResponse, error) {
	
	path := fmt.Sprintf("/v1/tenants/%s/projects", tenantID)

	respBytes, err := c.doRequest(ctx, "POST", path, req)
	
	if err != nil {
		return nil, fmt.Errorf("CreateProject: %w", err)
	}

	var project ProjectResponse
	
	if err := json.Unmarshal(respBytes, &project); err != nil {
		return nil, fmt.Errorf("CreateProject: failed to parse response: %w", err)
	}

	return &project, nil
}

// GetProjectByID sends GET /v1/tenants/{tenantID}/projects/{projectID}.
// Returns (nil, nil) when the API responds with HTTP 404 — signals drift to the caller.
func (c *DCAPIClient) GetProjectByID(ctx context.Context, tenantID, projectID string) (*ProjectResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s", tenantID, projectID)
	respBytes, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return nil, nil
		}
		return nil, fmt.Errorf("GetProjectByID: %w", err)
	}
	var project ProjectResponse
	if err := json.Unmarshal(respBytes, &project); err != nil {
		return nil, fmt.Errorf("GetProjectByID: failed to parse response: %w", err)
	}
	return &project, nil
}

// UpdateProject sends PATCH /v1/tenants/{tenantID}/projects/{projectID} and returns the updated project.
func (c *DCAPIClient) UpdateProject(ctx context.Context, tenantID, projectID string, req ProjectUpdateRequest) (*ProjectResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s", tenantID, projectID)
	respBytes, err := c.doRequest(ctx, "PATCH", path, req)
	if err != nil {
		return nil, fmt.Errorf("UpdateProject (tenant %q, project %q): %w", tenantID, projectID, err)
	}
	var project ProjectResponse
	if err := json.Unmarshal(respBytes, &project); err != nil {
		return nil, fmt.Errorf("UpdateProject: failed to parse response: %w", err)
	}
	return &project, nil
}

// DeleteProject sends DELETE /v1/tenants/{tenantID}/projects/{projectID}.
// The API returns 204 No Content on success; 409 Conflict if child resources still exist.
func (c *DCAPIClient) DeleteProject(ctx context.Context, tenantID, projectID string) error {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s", tenantID, projectID)
	_, err := c.doRequest(ctx, "DELETE", path, nil)
	if err != nil {
		return fmt.Errorf("DeleteProject (tenant %q, project %q): %w", tenantID, projectID, err)
	}
	return nil
}
