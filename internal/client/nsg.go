package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// NSGRule is a single security rule within a NetworkSecurityGroup.
type NSGRule struct {
	Name                     string `json:"name"`
	Direction                string `json:"direction"`
	Priority                 int    `json:"priority"`
	Protocol                 string `json:"protocol"`
	SourceAddressPrefix      string `json:"source_address_prefix"`
	SourcePortRange          string `json:"source_port_range"`
	DestinationAddressPrefix string `json:"destination_address_prefix"`
	DestinationPortRange     string `json:"destination_port_range"`
	Action                   string `json:"action"`
}

// NSGAttachmentEntry is an attachment embedded in the NSG read response.
type NSGAttachmentEntry struct {
	ID         string `json:"id"`
	SGID       string `json:"sg_id"`
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	CreatedAt  string `json:"created_at"`
}

// NSGCreateRequest maps to POST .../security-groups.
type NSGCreateRequest struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Rules       []NSGRule `json:"rules,omitempty"`
}

// NSGUpdateRulesRequest maps to PUT .../security-groups/{sg_id}/rules.
// Rules is a full-replace — send the complete desired rules list.
type NSGUpdateRulesRequest struct {
	Rules []NSGRule `json:"rules"`
}

// NSGResponse is returned by Create (201) and Read (200).
type NSGResponse struct {
	ID           string               `json:"id"`
	TenantID     string               `json:"tenant_id"`
	Name         string               `json:"name"`
	Description  string               `json:"description"`
	Rules        []NSGRule            `json:"rules"`
	Attachments  []NSGAttachmentEntry `json:"attachments"`
	Status       string               `json:"status"`
	ProviderType string               `json:"provider_type"`
	CreatedAt    string               `json:"created_at"`
	UpdatedAt    string               `json:"updated_at"`
}

// NSGAttachmentCreateRequest maps to POST .../security-groups/{sg_id}/attachments.
type NSGAttachmentCreateRequest struct {
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
}

// NSGAttachmentResponse is returned by POST .../attachments (201).
type NSGAttachmentResponse struct {
	ID         string `json:"id"`
	SGID       string `json:"sg_id"`
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	CreatedAt  string `json:"created_at"`
}

// CreateNSG sends POST .../security-groups. Returns 201 Created (sync).
func (c *DCAPIClient) CreateNSG(ctx context.Context, tenantID, projectID string, req NSGCreateRequest) (*NSGResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/security-groups", tenantID, projectID)
	respBytes, err := c.doRequest(ctx, "POST", path, req)
	if err != nil {
		return nil, fmt.Errorf("CreateNSG: %w", err)
	}
	var nsg NSGResponse
	if err := json.Unmarshal(respBytes, &nsg); err != nil {
		return nil, fmt.Errorf("CreateNSG: failed to parse response: %w", err)
	}
	return &nsg, nil
}

// GetNSG sends GET .../security-groups/{sgID}.
// Returns (nil, nil) on HTTP 404 — signals drift to the caller.
func (c *DCAPIClient) GetNSG(ctx context.Context, tenantID, projectID, sgID string) (*NSGResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/security-groups/%s", tenantID, projectID, sgID)
	respBytes, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return nil, nil
		}
		return nil, fmt.Errorf("GetNSG: %w", err)
	}
	var nsg NSGResponse
	if err := json.Unmarshal(respBytes, &nsg); err != nil {
		return nil, fmt.Errorf("GetNSG: failed to parse response: %w", err)
	}
	return &nsg, nil
}

// UpdateNSGRules sends PUT .../security-groups/{sgID}/rules.
// The entire rules array is replaced with what is sent. Returns 200 OK (sync).
func (c *DCAPIClient) UpdateNSGRules(ctx context.Context, tenantID, projectID, sgID string, req NSGUpdateRulesRequest) (*NSGResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/security-groups/%s/rules", tenantID, projectID, sgID)
	respBytes, err := c.doRequest(ctx, "PUT", path, req)
	if err != nil {
		return nil, fmt.Errorf("UpdateNSGRules: %w", err)
	}
	var nsg NSGResponse
	if err := json.Unmarshal(respBytes, &nsg); err != nil {
		return nil, fmt.Errorf("UpdateNSGRules: failed to parse response: %w", err)
	}
	return &nsg, nil
}

// DeleteNSG sends DELETE .../security-groups/{sgID}.
// Returns 204 No Content (sync). Blocked (409) if the NSG has active attachments.
func (c *DCAPIClient) DeleteNSG(ctx context.Context, tenantID, projectID, sgID string) error {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/security-groups/%s", tenantID, projectID, sgID)
	_, err := c.doRequest(ctx, "DELETE", path, nil)
	if err != nil {
		return fmt.Errorf("DeleteNSG (sg %q): %w", sgID, err)
	}
	return nil
}

// CreateNSGAttachment sends POST .../security-groups/{sgID}/attachments.
// Returns 201 Created (sync).
func (c *DCAPIClient) CreateNSGAttachment(ctx context.Context, tenantID, projectID, sgID string, req NSGAttachmentCreateRequest) (*NSGAttachmentResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/security-groups/%s/attachments", tenantID, projectID, sgID)
	respBytes, err := c.doRequest(ctx, "POST", path, req)
	if err != nil {
		return nil, fmt.Errorf("CreateNSGAttachment: %w", err)
	}
	var attachment NSGAttachmentResponse
	if err := json.Unmarshal(respBytes, &attachment); err != nil {
		return nil, fmt.Errorf("CreateNSGAttachment: failed to parse response: %w", err)
	}
	return &attachment, nil
}

// DeleteNSGAttachment sends DELETE .../security-groups/{sgID}/attachments/{attachmentID}.
// Returns 204 No Content (sync).
func (c *DCAPIClient) DeleteNSGAttachment(ctx context.Context, tenantID, projectID, sgID, attachmentID string) error {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/security-groups/%s/attachments/%s", tenantID, projectID, sgID, attachmentID)
	_, err := c.doRequest(ctx, "DELETE", path, nil)
	if err != nil {
		return fmt.Errorf("DeleteNSGAttachment (sg %q, attachment %q): %w", sgID, attachmentID, err)
	}
	return nil
}
