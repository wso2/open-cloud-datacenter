// VNetPeering-related API calls for the DC-API client.
// Peerings are nested under a VNet and are directional — see resources/vnet_peering.go
// for the Terraform-facing explanation of that constraint.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// VNetPeeringCreateRequest maps to POST .../vnets/{vnet_id}/peerings.
type VNetPeeringCreateRequest struct {
	Name                  string `json:"name"`
	PeerVNetID            string `json:"peer_vnet_id"`
	AllowForwardedTraffic bool   `json:"allow_forwarded_traffic,omitempty"`
}

// VNetPeeringResponse is the inner "resource" object returned by Create and Read.
type VNetPeeringResponse struct {
	ID                    string `json:"id"`
	VNetID                string `json:"vnet_id"`
	PeerVNetID            string `json:"peer_vnet_id"`
	TenantID              string `json:"tenant_id"`
	Name                  string `json:"name"`
	AllowForwardedTraffic bool   `json:"allow_forwarded_traffic"`
	Status                string `json:"status"`
	ProviderType          string `json:"provider_type"`
	Message               string `json:"message"`
	Warning               string `json:"warning"`
	CreatedAt             string `json:"created_at"`
	UpdatedAt             string `json:"updated_at"`
}

// VNetPeeringCreateResponse is the outer wrapper returned by the create endpoint (HTTP 202).
type VNetPeeringCreateResponse struct {
	Resource *VNetPeeringResponse `json:"resource"`
	Note     string               `json:"note"`
}

// CreateVNetPeering sends POST /v1/tenants/{tenantID}/projects/{projectID}/vnets/{vnetID}/peerings.
// Returns 202 Accepted — the peering starts in status "PENDING".
func (c *DCAPIClient) CreateVNetPeering(ctx context.Context, tenantID, projectID, vnetID string, req VNetPeeringCreateRequest) (*VNetPeeringResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/vnets/%s/peerings", tenantID, projectID, vnetID)
	respBytes, err := c.doRequest(ctx, "POST", path, req)
	if err != nil {
		return nil, fmt.Errorf("CreateVNetPeering: %w", err)
	}
	var wrapper VNetPeeringCreateResponse
	if err := json.Unmarshal(respBytes, &wrapper); err != nil {
		return nil, fmt.Errorf("CreateVNetPeering: failed to parse response: %w", err)
	}
	if wrapper.Resource == nil {
		return nil, fmt.Errorf("CreateVNetPeering: response missing resource")
	}
	return wrapper.Resource, nil
}

// GetVNetPeering sends GET .../vnets/{vnetID}/peerings/{peeringID}.
// Returns (nil, nil) on HTTP 404 — the drift sentinel used across all client files.
func (c *DCAPIClient) GetVNetPeering(ctx context.Context, tenantID, projectID, vnetID, peeringID string) (*VNetPeeringResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/vnets/%s/peerings/%s", tenantID, projectID, vnetID, peeringID)
	respBytes, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return nil, nil
		}
		return nil, fmt.Errorf("GetVNetPeering: %w", err)
	}
	var peering VNetPeeringResponse
	if err := json.Unmarshal(respBytes, &peering); err != nil {
		return nil, fmt.Errorf("GetVNetPeering: failed to parse response: %w", err)
	}
	return &peering, nil
}

// ListVNetPeerings sends GET .../vnets/{vnetID}/peerings.
// There is no "get by name" endpoint — the dcapi_vnet_peering data source calls this and
// filters client-side to resolve a human-readable name to a peering's UUID.
func (c *DCAPIClient) ListVNetPeerings(ctx context.Context, tenantID, projectID, vnetID string) ([]VNetPeeringResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/vnets/%s/peerings", tenantID, projectID, vnetID)
	respBytes, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("ListVNetPeerings: %w", err)
	}
	var peerings []VNetPeeringResponse
	if err := json.Unmarshal(respBytes, &peerings); err != nil {
		return nil, fmt.Errorf("ListVNetPeerings: failed to parse response: %w", err)
	}
	return peerings, nil
}

// DeleteVNetPeering sends DELETE .../vnets/{vnetID}/peerings/{peeringID}.
// Returns 202 Accepted — deletion is ASYNCHRONOUS; the caller must poll GetVNetPeering
// until it returns (nil, nil) to confirm the peering is actually gone.
func (c *DCAPIClient) DeleteVNetPeering(ctx context.Context, tenantID, projectID, vnetID, peeringID string) error {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/vnets/%s/peerings/%s", tenantID, projectID, vnetID, peeringID)
	_, err := c.doRequest(ctx, "DELETE", path, nil)
	if err != nil {
		return fmt.Errorf("DeleteVNetPeering (vnet %q, peering %q): %w", vnetID, peeringID, err)
	}
	return nil
}
