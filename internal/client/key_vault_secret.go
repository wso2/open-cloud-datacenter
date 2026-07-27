// KeyVaultSecret API calls for the DC-API client.
// Secrets are a pass-through proxy to OpenBao, nested under an ACTIVE KeyVault. WRITE (PUT) is
// an upsert — the same call creates the secret on first use and updates it (bumping version) on
// every call after that. DELETE soft-deletes: the key can be restored server-side via a
// dedicated /restore endpoint that this client does not expose, since the resource layer treats
// a delete as final. These routes return HTTP 501 if the KVI provisioner is not enabled on the
// DC-API instance — doRequest surfaces that as a plain error.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// KeyVaultSecretWriteRequest maps to PUT .../keyvaults/{id}/secrets/{key}.
type KeyVaultSecretWriteRequest struct {
	Value    string            `json:"value"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// KeyVaultSecretResponse is the JSON shape returned by PUT (200) and GET (200).
type KeyVaultSecretResponse struct {
	Key       string            `json:"key"`
	Value     string            `json:"value"`
	Version   int               `json:"version"`
	Metadata  map[string]string `json:"metadata"`
	CreatedAt string            `json:"created_at"`
	DeletedAt *string           `json:"deleted_at"`
}

// WriteKeyVaultSecret sends PUT .../keyvaults/{keyVaultID}/secrets/{key} — creates the secret
// if it doesn't exist, or updates it (bumping version) if it does.
func (c *DCAPIClient) WriteKeyVaultSecret(ctx context.Context, tenantID, projectID, keyVaultID, key string, req KeyVaultSecretWriteRequest) (*KeyVaultSecretResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/keyvaults/%s/secrets/%s", tenantID, projectID, keyVaultID, key)

	respBytes, err := c.doRequest(ctx, "PUT", path, req)
	if err != nil {
		return nil, fmt.Errorf("WriteKeyVaultSecret: %w", err)
	}

	var secret KeyVaultSecretResponse
	if err := json.Unmarshal(respBytes, &secret); err != nil {
		return nil, fmt.Errorf("WriteKeyVaultSecret: failed to parse response: %w", err)
	}
	return &secret, nil
}

// GetKeyVaultSecret sends GET .../keyvaults/{keyVaultID}/secrets/{key}.
// Returns (nil, nil) on HTTP 404 (never existed) or HTTP 410 (soft-deleted) — both signal
// drift to the caller, since neither leaves a readable value in place.
func (c *DCAPIClient) GetKeyVaultSecret(ctx context.Context, tenantID, projectID, keyVaultID, key string) (*KeyVaultSecretResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/keyvaults/%s/secrets/%s", tenantID, projectID, keyVaultID, key)

	respBytes, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") || strings.Contains(err.Error(), "HTTP 410") {
			return nil, nil
		}
		return nil, fmt.Errorf("GetKeyVaultSecret: %w", err)
	}

	var secret KeyVaultSecretResponse
	if err := json.Unmarshal(respBytes, &secret); err != nil {
		return nil, fmt.Errorf("GetKeyVaultSecret: failed to parse response: %w", err)
	}
	return &secret, nil
}

// DeleteKeyVaultSecret sends DELETE .../keyvaults/{keyVaultID}/secrets/{key}. The DC-API
// soft-deletes the key (recoverable server-side via /restore); Terraform treats this as final.
func (c *DCAPIClient) DeleteKeyVaultSecret(ctx context.Context, tenantID, projectID, keyVaultID, key string) error {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/keyvaults/%s/secrets/%s", tenantID, projectID, keyVaultID, key)

	_, err := c.doRequest(ctx, "DELETE", path, nil)
	if err != nil {
		return fmt.Errorf("DeleteKeyVaultSecret (keyvault %q, key %q): %w", keyVaultID, key, err)
	}
	return nil
}
