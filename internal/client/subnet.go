// Subnet-related API calls for the DC-API client.
// Subnets are always nested inside a VNet; every endpoint path requires both vnetID and subnetID.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// SubnetCreateRequest maps to POST /v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/subnets.
type SubnetCreateRequest struct {
	Name        string `json:"name"`
	CIDR        string `json:"cidr"`              // must fall within parent VNet's address_space
	Gateway     string `json:"gateway,omitempty"` // API assigns first usable IP in CIDR when omitted
	Description string `json:"description,omitempty"`
}

// SubnetResponse is returned by the create (202) and read (200) endpoints.
type SubnetResponse struct {
	ID           string `json:"id"`
	VNetID       string `json:"vnet_id"`    // filled from URL path, not request body
	TenantID     string `json:"tenant_id"`
	Name         string `json:"name"`
	CIDR         string `json:"cidr"`
	Gateway      string `json:"gateway"`
	Description  string `json:"description"`
	Status       string `json:"status"`
	ProviderType string `json:"provider_type"`
	Message      string `json:"message"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

// SubnetCreateResponse is the outer 202 wrapper; the subnet is in Resource.
type SubnetCreateResponse struct {
	Resource *SubnetResponse `json:"resource"`
	Note     string          `json:"note"`
}

// CreateSubnet sends POST .../vnets/{vnetID}/subnets and returns the inner SubnetResponse.
func (c *DCAPIClient) CreateSubnet(ctx context.Context, tenantID, projectID, vnetID string, req SubnetCreateRequest) (*SubnetResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/vnets/%s/subnets", tenantID, projectID, vnetID)

	respBytes, err := c.doRequest(ctx, "POST", path, req)
	if err != nil {
		return nil, fmt.Errorf("CreateSubnet: %w", err)
	}

	var wrapper SubnetCreateResponse
	if err := json.Unmarshal(respBytes, &wrapper); err != nil {
		return nil, fmt.Errorf("CreateSubnet: failed to parse response: %w", err)
	}

	return wrapper.Resource, nil
}

// GetSubnet sends GET .../vnets/{vnetID}/subnets/{subnetID}.
// Returns (nil, nil) on HTTP 404 — signals drift to the caller.
func (c *DCAPIClient) GetSubnet(ctx context.Context, tenantID, projectID, vnetID, subnetID string) (*SubnetResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/vnets/%s/subnets/%s", tenantID, projectID, vnetID, subnetID)

	respBytes, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return nil, nil
		}
		return nil, fmt.Errorf("GetSubnet: %w", err)
	}

	var subnet SubnetResponse
	if err := json.Unmarshal(respBytes, &subnet); err != nil {
		return nil, fmt.Errorf("GetSubnet: failed to parse response: %w", err)
	}
	return &subnet, nil
}

// DeleteSubnet sends DELETE .../vnets/{vnetID}/subnets/{subnetID}.
// Deletion is async (202). Returns HTTP 409 if NSG attachments exist on this subnet.
// Deleting the last subnet in a VNet triggers extra cleanup (NAT gateway, CoreDNS teardown),
// which can add significant latency.
func (c *DCAPIClient) DeleteSubnet(ctx context.Context, tenantID, projectID, vnetID, subnetID string) error {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/vnets/%s/subnets/%s", tenantID, projectID, vnetID, subnetID)

	_, err := c.doRequest(ctx, "DELETE", path, nil)
	if err != nil {
		return fmt.Errorf("DeleteSubnet (tenant %q, project %q, vnet %q, subnet %q): %w",
			tenantID, projectID, vnetID, subnetID, err)
	}
	return nil
}
