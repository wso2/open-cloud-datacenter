// VirtualMachine-related API calls for the DC-API client.
// VMs sit directly under projects (/v1/tenants/{t}/projects/{p}/virtual-machines/{id}).
//
// VMs support two mutually exclusive networking modes:
//
//	Option A — Legacy bridge mode: provide network_name (e.g. "iaas/vm-network-001")
//	Option B — VPC mode:           provide BOTH vnet_id AND subnet_id
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// VMCreateRequest maps to POST /v1/tenants/{tenant_id}/projects/{project_id}/virtual-machines.
// network_name and (vnet_id + subnet_id) are mutually exclusive networking modes.
type VMCreateRequest struct {
	Name        string `json:"name"`
	Size        string `json:"size"`
	DiskGB      int    `json:"disk_gb,omitempty"`
	ImageName   string `json:"image_name"`
	NetworkName string `json:"network_name,omitempty"` // Option A: legacy bridge mode
	VNetID      string `json:"vnet_id,omitempty"`      // Option B: VPC mode (requires SubnetID)
	SubnetID    string `json:"subnet_id,omitempty"`    // Option B: VPC mode (requires VNetID)
}

// VMResource is the inner "resource" object inside the create response (202).
type VMResource struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Size         string `json:"size"`
	Status       string `json:"status"`
	TenantID     string `json:"tenant_id"`
	ProviderType string `json:"provider_type"`
	IPAddress    string `json:"ip_address"` // empty until ACTIVE
	Message      string `json:"message"`
	CreatedAt    string `json:"created_at"`
}

// VMCreateResponse is the 202 wrapper returned by create.
// It includes one-time secrets (private_key, console_password) not present in the GET response.
// The create and read responses have different JSON shapes, so separate structs are required.
type VMCreateResponse struct {
	Resource        *VMResource `json:"resource"`
	PrivateKey      string      `json:"private_key"`      // shown once — store in state immediately
	ConsolePassword string      `json:"console_password"` // shown once — store in state immediately
	Note            string      `json:"note"`
}

// VMReadResponse is the flat shape returned by GET /virtual-machines/{id}.
// Unlike the create response, it has no outer wrapper and no secrets.
type VMReadResponse struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Size         string `json:"size"`
	Status       string `json:"status"`
	TenantID     string `json:"tenant_id"`
	ProviderType string `json:"provider_type"`
	IPAddress    string `json:"ip_address"`
	Message      string `json:"message"`
	CreatedAt    string `json:"created_at"`
}

// CreateVM sends POST .../virtual-machines.
// The response includes private_key and console_password — shown once; the caller must
// store them in Terraform state immediately as they are never returned again.
func (c *DCAPIClient) CreateVM(ctx context.Context, tenantID, projectID string, req VMCreateRequest) (*VMCreateResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/virtual-machines", tenantID, projectID)

	respBytes, err := c.doRequest(ctx, "POST", path, req)
	if err != nil {
		return nil, fmt.Errorf("CreateVM: %w", err)
	}

	var resp VMCreateResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, fmt.Errorf("CreateVM: failed to parse response: %w", err)
	}

	return &resp, nil
}

// GetVM sends GET .../virtual-machines/{vmID}.
// Returns (nil, nil) on HTTP 404 — signals drift to the caller.
// The GET response does not include private_key or console_password.
func (c *DCAPIClient) GetVM(ctx context.Context, tenantID, projectID, vmID string) (*VMReadResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/virtual-machines/%s", tenantID, projectID, vmID)

	respBytes, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return nil, nil
		}
		return nil, fmt.Errorf("GetVM: %w", err)
	}

	var vm VMReadResponse
	if err := json.Unmarshal(respBytes, &vm); err != nil {
		return nil, fmt.Errorf("GetVM: failed to parse response: %w", err)
	}
	return &vm, nil
}

// DeleteVM sends DELETE .../virtual-machines/{vmID}.
// Deletion is async (202) — poll GetVM until (nil, nil) to confirm removal.
func (c *DCAPIClient) DeleteVM(ctx context.Context, tenantID, projectID, vmID string) error {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/virtual-machines/%s", tenantID, projectID, vmID)

	_, err := c.doRequest(ctx, "DELETE", path, nil)
	if err != nil {
		return fmt.Errorf("DeleteVM (tenant %q, project %q, vm %q): %w", tenantID, projectID, vmID, err)
	}
	return nil
}
