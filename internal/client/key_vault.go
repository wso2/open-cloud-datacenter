// KeyVault API calls for the DC-API client.
// KeyVault wraps a per-tenant OpenBao (Vault) mount. Unlike VNet/VM, Create returns HTTP 201
// (not 202) but the resource can still be "PENDING" while the KVI operator finishes provisioning
// — callers must poll GetKeyVault until status is "ACTIVE" before treating it as usable.
//
// Credentials (role_id/secret_id) are fetched via a SEPARATE endpoint, not returned by Create.
// GET /credentials succeeds exactly once; every call after that returns HTTP 410 Gone.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// KeyVaultCreateRequest maps to POST /v1/tenants/{tenant_id}/projects/{project_id}/keyvaults.
type KeyVaultCreateRequest struct {
	Name string `json:"name"`
	// SoftDeleteDays is omitempty so a zero value lets the API apply its own default (30).
	SoftDeleteDays int `json:"soft_delete_days,omitempty"`
}

// KeyVaultResponse is the flat JSON body returned by Create (201) and Read (200) — unlike
// VNet/VM, there is no outer "resource" wrapper for KeyVault.
type KeyVaultResponse struct {
	ID              string `json:"id"`
	TenantID        string `json:"tenant_id"`
	Name            string `json:"name"`
	SoftDeleteDays  int    `json:"soft_delete_days"`
	Status          string `json:"status"`
	Message         string `json:"message"`
	MountPath       string `json:"mount_path"`
	EndpointAddress string `json:"endpoint_address"`
	EndpointPort    int `json:"endpoint_port"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

// KeyVaultCredentialsResponse is returned by GET .../credentials (first call only) and by
// POST .../credentials/rotate. SecretID is shown exactly once per call — the caller must
// persist it immediately.
type KeyVaultCredentialsResponse struct {
	RoleID         string `json:"role_id"`
	SecretID       string `json:"secret_id"`
	MountPath      string `json:"mount_path"`
	BackendAddress string `json:"backend_address"`
	BackendPort    string `json:"backend_port"`
}

// CreateKeyVault sends POST /v1/tenants/{tenantID}/projects/{projectID}/keyvaults.
func (c *DCAPIClient) CreateKeyVault(ctx context.Context, tenantID, projectID string, req KeyVaultCreateRequest) (*KeyVaultResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/keyvaults", tenantID, projectID)

	respBytes, err := c.doRequest(ctx, "POST", path, req)
	if err != nil {
		return nil, fmt.Errorf("CreateKeyVault: %w", err)
	}

	var kv KeyVaultResponse
	if err := json.Unmarshal(respBytes, &kv); err != nil {
		return nil, fmt.Errorf("CreateKeyVault: failed to parse response: %w", err)
	}
	return &kv, nil
}

// GetKeyVault sends GET /v1/tenants/{tenantID}/projects/{projectID}/keyvaults/{keyVaultID}.
// Returns (nil, nil) on HTTP 404 — the drift sentinel used consistently across this package.
func (c *DCAPIClient) GetKeyVault(ctx context.Context, tenantID, projectID, keyVaultID string) (*KeyVaultResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/keyvaults/%s", tenantID, projectID, keyVaultID)

	respBytes, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return nil, nil
		}
		return nil, fmt.Errorf("GetKeyVault: %w", err)
	}

	var kv KeyVaultResponse
	if err := json.Unmarshal(respBytes, &kv); err != nil {
		return nil, fmt.Errorf("GetKeyVault: failed to parse response: %w", err)
	}
	return &kv, nil
}

// ListKeyVaults sends GET /v1/tenants/{tenantID}/projects/{projectID}/keyvaults.
// There is no "get by name" endpoint — the dcapi_key_vault data source calls this and
// filters client-side to resolve a human-readable name to a KeyVault's UUID.
func (c *DCAPIClient) ListKeyVaults(ctx context.Context, tenantID, projectID string) ([]KeyVaultResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/keyvaults", tenantID, projectID)

	respBytes, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("ListKeyVaults: %w", err)
	}

	var kvs []KeyVaultResponse
	if err := json.Unmarshal(respBytes, &kvs); err != nil {
		return nil, fmt.Errorf("ListKeyVaults: failed to parse response: %w", err)
	}
	return kvs, nil
}

// DeleteKeyVault sends DELETE /v1/tenants/{tenantID}/projects/{projectID}/keyvaults/{keyVaultID}.
// The API returns 204 No Content synchronously — no polling required after this call.
func (c *DCAPIClient) DeleteKeyVault(ctx context.Context, tenantID, projectID, keyVaultID string) error {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/keyvaults/%s", tenantID, projectID, keyVaultID)

	_, err := c.doRequest(ctx, "DELETE", path, nil)
	if err != nil {
		return fmt.Errorf("DeleteKeyVault (tenant %q, project %q, keyvault %q): %w", tenantID, projectID, keyVaultID, err)
	}
	return nil
}

// GetKeyVaultCredentials sends GET .../keyvaults/{keyVaultID}/credentials.
// The DC-API only returns secret_id successfully on the FIRST call; every subsequent call
// returns HTTP 410 Gone. The resource layer is responsible for calling this exactly once
// (immediately after the KeyVault reaches ACTIVE) — this method does not special-case 410,
// since a 410 here means the caller violated that contract.
func (c *DCAPIClient) GetKeyVaultCredentials(ctx context.Context, tenantID, projectID, keyVaultID string) (*KeyVaultCredentialsResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/keyvaults/%s/credentials", tenantID, projectID, keyVaultID)

	respBytes, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("GetKeyVaultCredentials: %w", err)
	}

	var creds KeyVaultCredentialsResponse
	if err := json.Unmarshal(respBytes, &creds); err != nil {
		return nil, fmt.Errorf("GetKeyVaultCredentials: failed to parse response: %w", err)
	}
	return &creds, nil
}

// RotateKeyVaultCredentials sends POST .../keyvaults/{keyVaultID}/credentials/rotate.
// Mints a new secret_id (shown once) and invalidates the old one; role_id stays stable.
func (c *DCAPIClient) RotateKeyVaultCredentials(ctx context.Context, tenantID, projectID, keyVaultID string) (*KeyVaultCredentialsResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/keyvaults/%s/credentials/rotate", tenantID, projectID, keyVaultID)

	respBytes, err := c.doRequest(ctx, "POST", path, nil)
	if err != nil {
		return nil, fmt.Errorf("RotateKeyVaultCredentials: %w", err)
	}

	var creds KeyVaultCredentialsResponse
	if err := json.Unmarshal(respBytes, &creds); err != nil {
		return nil, fmt.Errorf("RotateKeyVaultCredentials: failed to parse response: %w", err)
	}
	return &creds, nil
}
