// Package client handles all HTTP communication with the DC-API.
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
	
	// reciever c means it reffered to the current object of the DCAPIClient struct, so it can access its fields like baseURL and token.
	url := c.baseURL + path

	var requestBody io.Reader

	if body != nil {

		// create a json file which is represented as byte slice
		// []byte{'{', '"', 'n', 'a', 'm', 'e', '"', ':', '"', 'A', 'l', 'i', 'c', 'e', '"', ',', '"', 'a', 'g', 'e', '"', ':', '2', '5', '}'}
		// [123 34 110 97 109 101 34 58 34 65 108 105 99 101 34 44 34 97 103 101 34 58 50 53 125]
		jsonBytes, err := json.Marshal(body)
		
		if err != nil {
			return nil, fmt.Errorf("doRequest: failed to encode request body: %w", err)
		}
		
		// // A Reader implements the [io.Reader], [io.ReaderAt], [io.WriterTo], [io.Seeker],
		// // [io.ByteScanner], and [io.RuneScanner] interfaces by reading from
		// // a byte slice.
		// // Unlike a [Buffer], a Reader is read-only and supports seeking.
		// // The zero value for Reader operates like a Reader of an empty slice.
		// type Reader struct {
		// 	s        []byte
		// 	i        int64 // current reading index
		// 	prevRune int   // index of previous rune; or < 0
		// }
		
		requestBody = bytes.NewReader(jsonBytes)
	}


	req, err := http.NewRequestWithContext(ctx, method, url, requestBody)
	
	if err != nil {
		return nil, fmt.Errorf("doRequest: failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json") // Tells the server what response format you want
	
	// body is nil for GET/DELETE, so don't set Content-Type in that case. Otherwise, set it to application/json.
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("doRequest %s %s: network error: %w", method, url, err)
	}
	

	// From httpClient.Do() docs:
	//
	// If the returned error is nil, the [Response] will contain a non-nil
	// Body which the user is expected to close. If the Body is not both
	// read to EOF and closed, the [Client]'s underlying [RoundTripper]
	// (typically [Transport]) may not be able to re-use a persistent TCP
	// connection to the server for a subsequent "keep-alive" request.
	//
	// defer schedules Close() to run when doRequest returns, preventing TCP connection leaks.
	defer resp.Body.Close()

	// if the server returns a JSON response, then respBytes will contain the UTF-8 bytes of that text.
	// respBytes is byte slice
	// Read the entire stream from resp.Body until EOF and return it as a single []byte
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

		// If the response body is not valid JSON or doesn't contain an "error" field, 
		// return the raw response body as a string.
		return nil, fmt.Errorf("DC-API returned HTTP %d: %s", resp.StatusCode, string(respBytes))
	}

	return respBytes, nil
}