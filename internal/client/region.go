// Region-related API calls for the DC-API client.
// Regions are platform-wide — the endpoint takes no tenant_id/project_id path parameters.
// Read-only from the provider's perspective: no Create/Update/Delete, only List, since
// regions are provisioned by platform operators, never by Terraform.
package client

import (
	"context"
	"encoding/json"
	"fmt"
)

// AgentStatus reports the last heartbeat from the agent running in a Zone. Nil (absent)
// when no agent has ever reported in for that zone.
type AgentStatus struct {
	Version  string `json:"version"`
	LastSeen string `json:"last_seen"`
}

// Zone is a failure-domain subdivision within a Region.
type Zone struct {
	Name   string       `json:"name"`
	Status string       `json:"status"`
	Agent  *AgentStatus `json:"agent"`
}

// Region is a physical data-centre region the DC-API can provision resources into.
type Region struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	// Status is derived by the API from agent heartbeat age: "up"|"degraded"|"down"|"unknown".
	Status string `json:"status"`
	Zones  []Zone `json:"zones"`
}

// regionListResponse is the outer envelope returned by GET /v1/regions: {"items": [...]}.
// Unlike every other List endpoint in this package, this one is not a bare JSON array.
type regionListResponse struct {
	Items []Region `json:"items"`
}

// ListRegions sends GET /v1/regions. There is no per-region GET endpoint — the
// dcapi_region data source calls this and filters client-side by name.
func (c *DCAPIClient) ListRegions(ctx context.Context) ([]Region, error) {
	respBytes, err := c.doRequest(ctx, "GET", "/v1/regions", nil)
	if err != nil {
		return nil, fmt.Errorf("ListRegions: %w", err)
	}

	var wrapper regionListResponse
	if err := json.Unmarshal(respBytes, &wrapper); err != nil {
		return nil, fmt.Errorf("ListRegions: failed to parse response: %w", err)
	}
	return wrapper.Items, nil
}
