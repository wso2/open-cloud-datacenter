// VNet-related API calls for the DC-API client.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// VNetCreateRequest maps to POST /v1/tenants/{tenant_id}/projects/{project_id}/vnets.
type VNetCreateRequest struct {
	Name         string   `json:"name"`          // must match [a-z][a-z0-9-]{0,61}[a-z0-9]
	AddressSpace []string `json:"address_space"` // 1–5 RFC1918 CIDR blocks (e.g. ["10.1.0.0/16"])
	Region       string   `json:"region"`
	Description  string   `json:"description,omitempty"`
}

// VNetResponse is the inner "resource" object returned by create (202) and read (200).
type VNetResponse struct {
	ID           string   `json:"id"`
	TenantID     string   `json:"tenant_id"`
	Name         string   `json:"name"`
	Region       string   `json:"region"`
	AddressSpace []string `json:"address_space"`
	Description  string   `json:"description"`
	Status       string   `json:"status"`
	ProviderType string   `json:"provider_type"`
	Message      string   `json:"message"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
}

// VNetCreateResponse is the outer 202 wrapper; the VNet is in Resource.
type VNetCreateResponse struct {
	Resource *VNetResponse `json:"resource"`
	Note     string        `json:"note"`
}

// CreateVNet sends POST .../vnets and returns the inner VNetResponse.
func (c *DCAPIClient) CreateVNet(ctx context.Context, tenantID, projectID string, req VNetCreateRequest) (*VNetResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/vnets", tenantID, projectID)

	respBytes, err := c.doRequest(ctx, "POST", path, req)
	if err != nil {
		return nil, fmt.Errorf("CreateVNet: %w", err)
	}

	var wrapper VNetCreateResponse
	if err := json.Unmarshal(respBytes, &wrapper); err != nil {
		return nil, fmt.Errorf("CreateVNet: failed to parse response: %w", err)
	}
	if wrapper.Resource == nil {
		return nil, fmt.Errorf("CreateVNet: response missing resource")
	}

	return wrapper.Resource, nil
}

// GetVNet sends GET .../vnets/{vnetID}.
// Returns (nil, nil) on HTTP 404 — signals drift to the caller.
func (c *DCAPIClient) GetVNet(ctx context.Context, tenantID, projectID, vnetID string) (*VNetResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/vnets/%s", tenantID, projectID, vnetID)

	respBytes, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return nil, nil
		}
		return nil, fmt.Errorf("GetVNet: %w", err)
	}

	var vnet VNetResponse
	if err := json.Unmarshal(respBytes, &vnet); err != nil {
		return nil, fmt.Errorf("GetVNet: failed to parse response: %w", err)
	}
	return &vnet, nil
}

// ListVNets sends GET /v1/tenants/{tenantID}/projects/{projectID}/vnets.
// There is no "get by name" endpoint — the dcapi_vnet data source calls this and filters
// client-side to resolve a human-readable name to a VNet's UUID.
func (c *DCAPIClient) ListVNets(ctx context.Context, tenantID, projectID string) ([]VNetResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/vnets", tenantID, projectID)

	respBytes, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("ListVNets: %w", err)
	}

	var vnets []VNetResponse
	if err := json.Unmarshal(respBytes, &vnets); err != nil {
		return nil, fmt.Errorf("ListVNets: failed to parse response: %w", err)
	}
	return vnets, nil
}

// DeleteVNet sends DELETE /v1/tenants/{tenantID}/projects/{projectID}/vnets/{vnetID}.
//
// The API returns HTTP 202 Accepted — deletion is ASYNCHRONOUS. This function only
// INITIATES the delete; the caller must poll with GetVNet until it returns (nil, nil)
// to confirm the VNet is actually gone.
//
// Returns:
//   nil   — deletion was accepted by the API (HTTP 202).
//   error — may contain "HTTP 409" if subnets still exist inside this VNet (the DC-API
//            blocks VNet deletion until all child subnets are removed). The error message
//            will contain "HTTP 409" which the resource layer checks for to produce a
//            more helpful message to the user.
func (c *DCAPIClient) DeleteVNet(ctx context.Context, tenantID, projectID, vnetID string) error {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/vnets/%s", tenantID, projectID, vnetID)

	_, err := c.doRequest(ctx, "DELETE", path, nil)
	if err != nil {
		return fmt.Errorf("DeleteVNet (tenant %q, project %q, vnet %q): %w", tenantID, projectID, vnetID, err)
	}
	return nil
}
