package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// RouteEntry is a single route within a RouteTable.
type RouteEntry struct {
	Name            string `json:"name"`
	DestinationCIDR string `json:"destination_cidr"`
	NextHopType     string `json:"next_hop_type"`
	NextHopIP       string `json:"next_hop_ip,omitempty"`
}

// RouteTableCreateRequest maps to POST .../vnets/{vnet_id}/route-tables.
type RouteTableCreateRequest struct {
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	Routes      []RouteEntry `json:"routes,omitempty"`
}

// RouteTableUpdateRequest maps to PUT .../vnets/{vnet_id}/route-tables/{rt_id}.
// Routes is a full-replace — send the complete desired routes list.
type RouteTableUpdateRequest struct {
	Routes []RouteEntry `json:"routes"`
}

// RouteTableAssociationEntry is an association embedded in the RouteTable read response.
type RouteTableAssociationEntry struct {
	ID           string `json:"id"`
	RouteTableID string `json:"route_table_id"`
	SubnetID     string `json:"subnet_id"`
	CreatedAt    string `json:"created_at"`
}

// RouteTableResponse is returned by Create (201) and Read (200).
type RouteTableResponse struct {
	ID           string                       `json:"id"`
	VNetID       string                       `json:"vnet_id"`
	TenantID     string                       `json:"tenant_id"`
	Name         string                       `json:"name"`
	Description  string                       `json:"description"`
	Routes       []RouteEntry                 `json:"routes"`
	Associations []RouteTableAssociationEntry `json:"associations"`
	Status       string                       `json:"status"`
	ProviderType string                       `json:"provider_type"`
	CreatedAt    string                       `json:"created_at"`
	UpdatedAt    string                       `json:"updated_at"`
}

// RouteTableAssociationCreateRequest maps to POST .../route-tables/{rt_id}/associations.
type RouteTableAssociationCreateRequest struct {
	SubnetID string `json:"subnet_id"`
}

// RouteTableAssociationResponse is returned by POST .../associations (201).
type RouteTableAssociationResponse struct {
	ID           string `json:"id"`
	RouteTableID string `json:"route_table_id"`
	SubnetID     string `json:"subnet_id"`
	CreatedAt    string `json:"created_at"`
	Warning      string `json:"warning"`
}

// CreateRouteTable sends POST .../vnets/{vnetID}/route-tables.
// Returns 201 Created (sync — no polling required).
func (c *DCAPIClient) CreateRouteTable(ctx context.Context, tenantID, projectID, vnetID string, req RouteTableCreateRequest) (*RouteTableResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/vnets/%s/route-tables", tenantID, projectID, vnetID)
	respBytes, err := c.doRequest(ctx, "POST", path, req)
	if err != nil {
		return nil, fmt.Errorf("CreateRouteTable: %w", err)
	}
	var rt RouteTableResponse
	if err := json.Unmarshal(respBytes, &rt); err != nil {
		return nil, fmt.Errorf("CreateRouteTable: failed to parse response: %w", err)
	}
	return &rt, nil
}

// GetRouteTable sends GET .../vnets/{vnetID}/route-tables/{rtID}.
// Returns (nil, nil) on HTTP 404 — signals drift to the caller.
// The response includes an Associations field when the route table has subnet associations.

func (c *DCAPIClient) GetRouteTable(ctx context.Context, tenantID, projectID, vnetID, rtID string) (*RouteTableResponse, error) {
	
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/vnets/%s/route-tables/%s", tenantID, projectID, vnetID, rtID)
	respBytes, err := c.doRequest(ctx, "GET", path, nil)
	
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return nil, nil
		}
		return nil, fmt.Errorf("GetRouteTable: %w", err)
	}

	var rt RouteTableResponse
	if err := json.Unmarshal(respBytes, &rt); err != nil {
		return nil, fmt.Errorf("GetRouteTable: failed to parse response: %w", err)
	}
	
	return &rt, nil
}

// UpdateRouteTable sends PUT .../vnets/{vnetID}/route-tables/{rtID}.
// Routes is a full-replace — the entire routes array is replaced with what is sent.
// Returns 200 OK (sync — no polling required).
func (c *DCAPIClient) UpdateRouteTable(ctx context.Context, tenantID, projectID, vnetID, rtID string, req RouteTableUpdateRequest) (*RouteTableResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/vnets/%s/route-tables/%s", tenantID, projectID, vnetID, rtID)
	respBytes, err := c.doRequest(ctx, "PUT", path, req)
	if err != nil {
		return nil, fmt.Errorf("UpdateRouteTable: %w", err)
	}
	var rt RouteTableResponse
	if err := json.Unmarshal(respBytes, &rt); err != nil {
		return nil, fmt.Errorf("UpdateRouteTable: failed to parse response: %w", err)
	}
	return &rt, nil
}

// DeleteRouteTable sends DELETE .../vnets/{vnetID}/route-tables/{rtID}.
// Returns 204 No Content (sync — no polling required).
func (c *DCAPIClient) DeleteRouteTable(ctx context.Context, tenantID, projectID, vnetID, rtID string) error {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/vnets/%s/route-tables/%s", tenantID, projectID, vnetID, rtID)
	_, err := c.doRequest(ctx, "DELETE", path, nil)
	if err != nil {
		return fmt.Errorf("DeleteRouteTable (vnet %q, rt %q): %w", vnetID, rtID, err)
	}
	return nil
}

// CreateRouteTableAssociation sends POST .../route-tables/{rtID}/associations.
// Returns 201 Created (sync — no polling required).
func (c *DCAPIClient) CreateRouteTableAssociation(ctx context.Context, tenantID, projectID, vnetID, rtID string, req RouteTableAssociationCreateRequest) (*RouteTableAssociationResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/vnets/%s/route-tables/%s/associations", tenantID, projectID, vnetID, rtID)
	respBytes, err := c.doRequest(ctx, "POST", path, req)
	if err != nil {
		return nil, fmt.Errorf("CreateRouteTableAssociation: %w", err)
	}
	var assoc RouteTableAssociationResponse
	if err := json.Unmarshal(respBytes, &assoc); err != nil {
		return nil, fmt.Errorf("CreateRouteTableAssociation: failed to parse response: %w", err)
	}
	return &assoc, nil
}

// DeleteRouteTableAssociation sends DELETE .../route-tables/{rtID}/associations/{assocID}.
// Returns 204 No Content (sync — no polling required).
func (c *DCAPIClient) DeleteRouteTableAssociation(ctx context.Context, tenantID, projectID, vnetID, rtID, assocID string) error {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/vnets/%s/route-tables/%s/associations/%s", tenantID, projectID, vnetID, rtID, assocID)
	_, err := c.doRequest(ctx, "DELETE", path, nil)
	if err != nil {
		return fmt.Errorf("DeleteRouteTableAssociation (rt %q, assoc %q): %w", rtID, assocID, err)
	}
	return nil
}
