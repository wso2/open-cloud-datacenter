// Package client handles all HTTP communication with the DC-API.
// It knows nothing about Terraform — no schema, no ResourceData, no diag.

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// DCAPIClient holds the credentials and HTTP client needed to talk to one DC-API server.
// Created once by configureProvider (provider.go) and passed to every resource CRUD function
// in internal/resources/ as the meta interface{} parameter (retrieved via type assertion).
// All five files — tenant.go, project.go, vnet.go, subnet.go, vm.go — call methods on it.

type DCAPIClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// NewClient constructs a DCAPIClient. Returns a pointer so the single instance is shared.

func NewClient(baseURL, token string) (*DCAPIClient, error) {
	return &DCAPIClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		httpClient: http.DefaultClient,
	}, nil
}

// apiErrorResponse models the standard DC-API error body: {"error": "..."}.
type apiErrorResponse struct {
	Error string `json:"error"`
}

// quotaCapDetail holds one set of resource numbers inside a quota-exceeded error.
type quotaCapDetail struct {
	CPUCores  int `json:"cpu_cores"`
	MemoryGB  int `json:"memory_gb"`
	StorageGB int `json:"storage_gb"`
}

// quotaErrorResponse models the HTTP 400 quota-exceeded body with cap/allocated/available/requested detail.
type quotaErrorResponse struct {
	Error     string         `json:"error"`
	Message   string         `json:"message"`
	TenantCap quotaCapDetail `json:"tenant_cap"`
	Allocated quotaCapDetail `json:"allocated"`
	Available quotaCapDetail `json:"available"`
	Requested quotaCapDetail `json:"requested"`
}

// doRequest is the single HTTP helper used by every resource function.
// It JSON-encodes body (pass nil for GET/DELETE), sets auth headers, sends the request,
// and converts non-2xx responses into errors with human-readable messages.
// All HTTP traffic in this package flows through here — no other file makes HTTP calls.

func (c *DCAPIClient) doRequest(ctx context.Context, method, path string, body interface{}) ([]byte, error) {
	
	url := c.baseURL + path

	var requestBody io.Reader

	if body != nil {		
		jsonBytes, err := json.Marshal(body)		
		if err != nil {
			return nil, fmt.Errorf("doRequest: failed to encode request body: %w", err)
		}		
		requestBody = bytes.NewReader(jsonBytes)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, requestBody)
	
	if err != nil {
		return nil, fmt.Errorf("doRequest: failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)	
	req.Header.Set("Accept", "application/json")
	
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("doRequest %s %s: network error: %w", method, url, err)
	}
	
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	
	if err != nil {
		return nil, fmt.Errorf("doRequest: failed to read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {

		// Surface detailed quota numbers on quota_exceeded (HTTP 400).
		if resp.StatusCode == 400 {
			var q quotaErrorResponse
			if jsonErr := json.Unmarshal(respBytes, &q); jsonErr == nil && q.Error == "quota_exceeded" {
				return nil, fmt.Errorf(
					"quota exceeded: %s — cap: cpu=%d mem=%dGB storage=%dGB | allocated: cpu=%d mem=%dGB storage=%dGB | available: cpu=%d mem=%dGB storage=%dGB | requested: cpu=%d mem=%dGB storage=%dGB",
					q.Message,
					q.TenantCap.CPUCores, q.TenantCap.MemoryGB, q.TenantCap.StorageGB,
					q.Allocated.CPUCores, q.Allocated.MemoryGB, q.Allocated.StorageGB,
					q.Available.CPUCores, q.Available.MemoryGB, q.Available.StorageGB,
					q.Requested.CPUCores, q.Requested.MemoryGB, q.Requested.StorageGB,
				)
			}
		}

		var apiErr apiErrorResponse

		if jsonErr := json.Unmarshal(respBytes, &apiErr); jsonErr == nil && apiErr.Error != "" {
			return nil, fmt.Errorf("DC-API returned HTTP %d: %s", resp.StatusCode, apiErr.Error)
		}

		return nil, fmt.Errorf("DC-API returned HTTP %d: %s", resp.StatusCode, string(respBytes))
	}

	return respBytes, nil
}
