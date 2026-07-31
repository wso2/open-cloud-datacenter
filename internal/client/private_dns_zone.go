// PrivateDnsZone-related API calls for the DC-API client.
// A PrivateDnsZone is nested under a VNet; DnsRecord (dns_record.go) nests under a zone.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// PrivateDnsZoneCreateRequest maps to POST .../vnets/{vnet_id}/dns-zones.
type PrivateDnsZoneCreateRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// PrivateDnsZoneResponse is the inner "resource" object returned by Create and Read.
type PrivateDnsZoneResponse struct {
	ID           string `json:"id"`
	VNetID       string `json:"vnet_id"`
	TenantID     string `json:"tenant_id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Status       string `json:"status"`
	ProviderType string `json:"provider_type"`
	Message      string `json:"message"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

// PrivateDnsZoneCreateResponse is the outer wrapper returned by the create endpoint (HTTP 202).
type PrivateDnsZoneCreateResponse struct {
	Resource *PrivateDnsZoneResponse `json:"resource"`
	Note     string                  `json:"note"`
}

// CreatePrivateDnsZone sends POST /v1/tenants/{tenantID}/projects/{projectID}/vnets/{vnetID}/dns-zones.
// Returns 202 Accepted — the zone starts in status "PENDING".
func (c *DCAPIClient) CreatePrivateDnsZone(ctx context.Context, tenantID, projectID, vnetID string, req PrivateDnsZoneCreateRequest) (*PrivateDnsZoneResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/vnets/%s/dns-zones", tenantID, projectID, vnetID)
	respBytes, err := c.doRequest(ctx, "POST", path, req)
	if err != nil {
		return nil, fmt.Errorf("CreatePrivateDnsZone: %w", err)
	}
	var wrapper PrivateDnsZoneCreateResponse
	if err := json.Unmarshal(respBytes, &wrapper); err != nil {
		return nil, fmt.Errorf("CreatePrivateDnsZone: failed to parse response: %w", err)
	}
	if wrapper.Resource == nil {
		return nil, fmt.Errorf("CreatePrivateDnsZone: response missing resource")
	}
	return wrapper.Resource, nil
}

// GetPrivateDnsZone sends GET .../vnets/{vnetID}/dns-zones/{zoneID}.
// Returns (nil, nil) on HTTP 404 — the drift sentinel used across all client files.
func (c *DCAPIClient) GetPrivateDnsZone(ctx context.Context, tenantID, projectID, vnetID, zoneID string) (*PrivateDnsZoneResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/vnets/%s/dns-zones/%s", tenantID, projectID, vnetID, zoneID)
	respBytes, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return nil, nil
		}
		return nil, fmt.Errorf("GetPrivateDnsZone: %w", err)
	}
	var zone PrivateDnsZoneResponse
	if err := json.Unmarshal(respBytes, &zone); err != nil {
		return nil, fmt.Errorf("GetPrivateDnsZone: failed to parse response: %w", err)
	}
	return &zone, nil
}

// ListPrivateDnsZones sends GET .../vnets/{vnetID}/dns-zones.
// There is no "get by name" endpoint — the dcapi_private_dns_zone data source calls this
// and filters client-side to resolve a human-readable name to a zone's UUID.
func (c *DCAPIClient) ListPrivateDnsZones(ctx context.Context, tenantID, projectID, vnetID string) ([]PrivateDnsZoneResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/vnets/%s/dns-zones", tenantID, projectID, vnetID)
	respBytes, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("ListPrivateDnsZones: %w", err)
	}
	var zones []PrivateDnsZoneResponse
	if err := json.Unmarshal(respBytes, &zones); err != nil {
		return nil, fmt.Errorf("ListPrivateDnsZones: failed to parse response: %w", err)
	}
	return zones, nil
}

// DeletePrivateDnsZone sends DELETE .../vnets/{vnetID}/dns-zones/{zoneID}.
// Returns 202 Accepted — deletion is ASYNCHRONOUS; the caller must poll GetPrivateDnsZone
// until it returns (nil, nil) to confirm the zone is actually gone.
func (c *DCAPIClient) DeletePrivateDnsZone(ctx context.Context, tenantID, projectID, vnetID, zoneID string) error {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/vnets/%s/dns-zones/%s", tenantID, projectID, vnetID, zoneID)
	_, err := c.doRequest(ctx, "DELETE", path, nil)
	if err != nil {
		return fmt.Errorf("DeletePrivateDnsZone (vnet %q, zone %q): %w", vnetID, zoneID, err)
	}
	return nil
}
