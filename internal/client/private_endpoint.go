// PrivateEndpoint-related API calls for the DC-API client.
// A PrivateEndpoint nests under a KeyVault and exposes it inside a VNet/Subnet.
// These routes return HTTP 501 Not Implemented if the endpoint provisioner is not enabled
// on the target DC-API instance — doRequest surfaces that as a plain error, same as any
// other non-2xx response.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// PrivateEndpointCreateRequest maps to POST .../keyvaults/{kv_id}/private-endpoints.
type PrivateEndpointCreateRequest struct {
	Name     string `json:"name"`
	VNetID   string `json:"vnet_id"`
	SubnetID string `json:"subnet_id"`
}

// PrivateEndpointResponse is returned by Create (201) and Read (200).
type PrivateEndpointResponse struct {
	ID         string `json:"id"`
	TenantID   string `json:"tenant_id"`
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	VNetID     string `json:"vnet_id"`
	SubnetID   string `json:"subnet_id"`
	Name       string `json:"name"`
	IPAddress  string `json:"ip_address"`
	Hostname   string `json:"hostname"`
	Status     string `json:"status"`
	Message    string `json:"message"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// CreatePrivateEndpoint sends POST .../keyvaults/{kvID}/private-endpoints.
// Returns 201 Created (sync — no polling required).
func (c *DCAPIClient) CreatePrivateEndpoint(ctx context.Context, tenantID, projectID, kvID string, req PrivateEndpointCreateRequest) (*PrivateEndpointResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/keyvaults/%s/private-endpoints", tenantID, projectID, kvID)
	respBytes, err := c.doRequest(ctx, "POST", path, req)
	if err != nil {
		return nil, fmt.Errorf("CreatePrivateEndpoint: %w", err)
	}
	var ep PrivateEndpointResponse
	if err := json.Unmarshal(respBytes, &ep); err != nil {
		return nil, fmt.Errorf("CreatePrivateEndpoint: failed to parse response: %w", err)
	}
	return &ep, nil
}

// GetPrivateEndpoint sends GET .../keyvaults/{kvID}/private-endpoints/{epID}.
// Returns (nil, nil) on HTTP 404 — the drift sentinel used across all client files.
func (c *DCAPIClient) GetPrivateEndpoint(ctx context.Context, tenantID, projectID, kvID, epID string) (*PrivateEndpointResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/keyvaults/%s/private-endpoints/%s", tenantID, projectID, kvID, epID)
	respBytes, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return nil, nil
		}
		return nil, fmt.Errorf("GetPrivateEndpoint: %w", err)
	}
	var ep PrivateEndpointResponse
	if err := json.Unmarshal(respBytes, &ep); err != nil {
		return nil, fmt.Errorf("GetPrivateEndpoint: failed to parse response: %w", err)
	}
	return &ep, nil
}

// DeletePrivateEndpoint sends DELETE .../keyvaults/{kvID}/private-endpoints/{epID}.
// Returns 204 No Content (sync — no polling required).
func (c *DCAPIClient) DeletePrivateEndpoint(ctx context.Context, tenantID, projectID, kvID, epID string) error {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/keyvaults/%s/private-endpoints/%s", tenantID, projectID, kvID, epID)
	_, err := c.doRequest(ctx, "DELETE", path, nil)
	if err != nil {
		return fmt.Errorf("DeletePrivateEndpoint (kv %q, endpoint %q): %w", kvID, epID, err)
	}
	return nil
}
