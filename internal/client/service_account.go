// Service account API calls for the DC-API client.
// Service accounts are project-scoped; they require both tenantID and projectID in every URL.
// The token is returned exactly once at creation and never stored server-side — callers must
// persist it immediately.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ServiceAccountCreateRequest maps to POST /v1/tenants/{tenant_id}/projects/{project_id}/service-accounts.
type ServiceAccountCreateRequest struct {
	Name        string `json:"name"`
	Role        string `json:"role"`
	Description string `json:"description,omitempty"`
}

// ServiceAccountCreateResponse is the 201 body; it includes the one-time token.
type ServiceAccountCreateResponse struct {
	ID          string `json:"id"`
	TenantID    string `json:"tenant_id"`
	Name        string `json:"name"`
	Role        string `json:"role"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
	Token       string `json:"token"` // shown once — store in Terraform state immediately
}

// ServiceAccountResponse is the GET 200 body; token is absent.
type ServiceAccountResponse struct {
	ID          string  `json:"id"`
	TenantID    string  `json:"tenant_id"`
	Name        string  `json:"name"`
	Role        string  `json:"role"`
	Description string  `json:"description"`
	CreatedAt   string  `json:"created_at"`
	LastUsed    *string `json:"last_used"` // nullable RFC3339; nil when SA has never authenticated
}

// CreateServiceAccount sends POST /v1/tenants/{tenantID}/projects/{projectID}/service-accounts.
func (c *DCAPIClient) CreateServiceAccount(ctx context.Context, tenantID, projectID string, req ServiceAccountCreateRequest) (*ServiceAccountCreateResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/service-accounts", tenantID, projectID)
	respBytes, err := c.doRequest(ctx, "POST", path, req)
	if err != nil {
		return nil, fmt.Errorf("CreateServiceAccount: %w", err)
	}
	var sa ServiceAccountCreateResponse
	if err := json.Unmarshal(respBytes, &sa); err != nil {
		return nil, fmt.Errorf("CreateServiceAccount: failed to parse response: %w", err)
	}
	return &sa, nil
}

// GetServiceAccount sends GET /v1/tenants/{tenantID}/projects/{projectID}/service-accounts/{saID}.
// Returns (nil, nil) on HTTP 404 — signals drift to the caller.
func (c *DCAPIClient) GetServiceAccount(ctx context.Context, tenantID, projectID, saID string) (*ServiceAccountResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/service-accounts/%s", tenantID, projectID, saID)
	respBytes, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return nil, nil
		}
		return nil, fmt.Errorf("GetServiceAccount: %w", err)
	}
	var sa ServiceAccountResponse
	if err := json.Unmarshal(respBytes, &sa); err != nil {
		return nil, fmt.Errorf("GetServiceAccount: failed to parse response: %w", err)
	}
	return &sa, nil
}

// DeleteServiceAccount sends DELETE /v1/tenants/{tenantID}/projects/{projectID}/service-accounts/{saID}.
// The API returns 204 No Content on success.
func (c *DCAPIClient) DeleteServiceAccount(ctx context.Context, tenantID, projectID, saID string) error {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/service-accounts/%s", tenantID, projectID, saID)
	_, err := c.doRequest(ctx, "DELETE", path, nil)
	if err != nil {
		return fmt.Errorf("DeleteServiceAccount (tenant %q, project %q, sa %q): %w", tenantID, projectID, saID, err)
	}
	return nil
}
