// Tenant-related API calls. Part of package client — shares doRequest() from client.go.
// Tenants are the root of the DC-API resource hierarchy: every project, VNet, subnet, and
// VM is scoped under a tenant. The tenant slug appears in every downstream API URL path
// as /v1/tenants/{tenant_id}/... — changing it would require replacing all child resources.
package client

import (
	"context"
	"encoding/json"
	"fmt"
)

// TenantCreateRequest maps to POST /v1/admin/tenants body.
// omitempty omits zero-valued fields so the API applies its own defaults.
type TenantCreateRequest struct {
	ID           string `json:"id"`
	Name         string `json:"name,omitempty"`
	Description  string `json:"description,omitempty"`
	CPUCoresCap  int    `json:"cpu_cores_cap,omitempty"`
	MemoryGBCap  int    `json:"memory_gb_cap,omitempty"`
	StorageGBCap int    `json:"storage_gb_cap,omitempty"`
}

// TenantUpdateRequest maps to PATCH /v1/admin/tenants/{tenant_id} body.
// *int pointers with omitempty: nil = omit field (leave unchanged); &v = send value v.
type TenantUpdateRequest struct {
	CPUCoresCap  *int `json:"cpu_cores_cap,omitempty"`
	MemoryGBCap  *int `json:"memory_gb_cap,omitempty"`
	StorageGBCap *int `json:"storage_gb_cap,omitempty"`
}

// TenantResponse is the JSON shape returned by Create (201), List (200), and Update (200).
type TenantResponse struct {
	ID            string `json:"id"`
	TenantUUID    string `json:"tenant_uuid"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	AsgardeoGroup string `json:"asgardeo_group"`
	CPUCoresCap   int    `json:"cpu_cores_cap"`
	MemoryGBCap   int    `json:"memory_gb_cap"`
	StorageGBCap  int    `json:"storage_gb_cap"`
	CreatedAt     string `json:"created_at"`
	CreatedBy     string `json:"created_by"`
}

// CreateTenant sends POST /v1/admin/tenants and returns the created tenant.
func (c *DCAPIClient) CreateTenant(ctx context.Context, req TenantCreateRequest) (*TenantResponse, error) {
	respBytes, err := c.doRequest(ctx, "POST", "/v1/admin/tenants", req)
	if err != nil {
		return nil, fmt.Errorf("CreateTenant: %w", err)
	}
	var tenant TenantResponse
	if err := json.Unmarshal(respBytes, &tenant); err != nil {
		return nil, fmt.Errorf("CreateTenant: failed to parse response: %w", err)
	}
	return &tenant, nil
}

// GetTenantByID lists all tenants and returns the one matching id, or (nil, nil) if not found.
// The DC-API has no GET-by-id endpoint for tenants, so a list scan is the only option.
func (c *DCAPIClient) GetTenantByID(ctx context.Context, id string) (*TenantResponse, error) {
	respBytes, err := c.doRequest(ctx, "GET", "/v1/tenants", nil)
	if err != nil {
		return nil, fmt.Errorf("GetTenantByID: list request failed: %w", err)
	}
	var tenants []TenantResponse
	if err := json.Unmarshal(respBytes, &tenants); err != nil {
		return nil, fmt.Errorf("GetTenantByID: failed to parse list response: %w", err)
	}
	for i := range tenants {
		if tenants[i].ID == id {
			return &tenants[i], nil
		}
	}
	return nil, nil
}

// UpdateTenant sends PATCH /v1/admin/tenants/{tenantID} and returns the updated tenant.
func (c *DCAPIClient) UpdateTenant(ctx context.Context, tenantID string, req TenantUpdateRequest) (*TenantResponse, error) {
	path := fmt.Sprintf("/v1/admin/tenants/%s", tenantID)
	respBytes, err := c.doRequest(ctx, "PATCH", path, req)
	if err != nil {
		return nil, fmt.Errorf("UpdateTenant (tenant %q): %w", tenantID, err)
	}
	var tenant TenantResponse
	if err := json.Unmarshal(respBytes, &tenant); err != nil {
		return nil, fmt.Errorf("UpdateTenant: failed to parse response: %w", err)
	}
	return &tenant, nil
}
