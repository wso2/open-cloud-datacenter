// Bastion-related API calls for the DC-API client.
// Bastions are project-scoped SSH jump hosts that require a VNet and Subnet (VPC-only).
// Like VMs, the create response is async (202) and wraps the resource in a "resource" key
// alongside two shown-once secrets (private_key, console_password).
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// BastionCreateRequest maps to POST /v1/tenants/{tenant_id}/projects/{project_id}/bastions.
type BastionCreateRequest struct {
	Name        string `json:"name"`
	VNetID      string `json:"vnet_id"`
	SubnetID    string `json:"subnet_id"`
	Description string `json:"description,omitempty"`
}

// BastionResource is the inner "resource" object inside the 202 create response.
type BastionResource struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	TenantID     string `json:"tenant_id"`
	VNetID       string `json:"vnet_id"`
	SubnetID     string `json:"subnet_id"`
	ProviderType string `json:"provider_type"`
	MgmtIP       string `json:"mgmt_ip"`     // management-plane IP; empty until ACTIVE
	InternalIP   string `json:"internal_ip"` // VPC-side IP; empty until ACTIVE
	Description  string `json:"description"`
	Message      string `json:"message"`
	CreatedAt    string `json:"created_at"`
}

// BastionCreateResponse is the outer 202 wrapper that includes the shown-once secrets.
type BastionCreateResponse struct {
	Resource        *BastionResource `json:"resource"`
	PrivateKey      string           `json:"private_key"`      // shown once — store in state immediately
	ConsolePassword string           `json:"console_password"` // shown once — store in state immediately
	Note            string           `json:"note"`
}

// BastionReadResponse is the flat shape returned by GET /bastions/{id} (no wrapper, no secrets).
type BastionReadResponse struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	TenantID     string `json:"tenant_id"`
	VNetID       string `json:"vnet_id"`
	SubnetID     string `json:"subnet_id"`
	ProviderType string `json:"provider_type"`
	MgmtIP       string `json:"mgmt_ip"`
	InternalIP   string `json:"internal_ip"`
	Description  string `json:"description"`
	Message      string `json:"message"`
	CreatedAt    string `json:"created_at"`
}

// CreateBastion sends POST /v1/tenants/{tenantID}/projects/{projectID}/bastions.
// The response contains private_key and console_password — the caller must store them immediately.
func (c *DCAPIClient) CreateBastion(ctx context.Context, tenantID, projectID string, req BastionCreateRequest) (*BastionCreateResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/bastions", tenantID, projectID)
	respBytes, err := c.doRequest(ctx, "POST", path, req)
	if err != nil {
		return nil, fmt.Errorf("CreateBastion: %w", err)
	}
	var resp BastionCreateResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, fmt.Errorf("CreateBastion: failed to parse response: %w", err)
	}
	return &resp, nil
}

// GetBastion sends GET /v1/tenants/{tenantID}/projects/{projectID}/bastions/{bastionID}.
// Returns (nil, nil) on HTTP 404 — signals drift to the caller.
func (c *DCAPIClient) GetBastion(ctx context.Context, tenantID, projectID, bastionID string) (*BastionReadResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/bastions/%s", tenantID, projectID, bastionID)
	respBytes, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return nil, nil
		}
		return nil, fmt.Errorf("GetBastion: %w", err)
	}
	var bastion BastionReadResponse
	if err := json.Unmarshal(respBytes, &bastion); err != nil {
		return nil, fmt.Errorf("GetBastion: failed to parse response: %w", err)
	}
	return &bastion, nil
}

// DeleteBastion sends DELETE /v1/tenants/{tenantID}/projects/{projectID}/bastions/{bastionID}.
// Deletion is async — the caller must poll GetBastion until (nil, nil) to confirm removal.
func (c *DCAPIClient) DeleteBastion(ctx context.Context, tenantID, projectID, bastionID string) error {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/bastions/%s", tenantID, projectID, bastionID)
	_, err := c.doRequest(ctx, "DELETE", path, nil)
	if err != nil {
		return fmt.Errorf("DeleteBastion (tenant %q, project %q, bastion %q): %w", tenantID, projectID, bastionID, err)
	}
	return nil
}
