// DnsRecord-related API calls for the DC-API client.
// Records nest under a PrivateDnsZone. Create is an UPSERT (matches on name+type within the
// zone); Update targets an explicit record ID via PUT. Both operations are synchronous.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// DnsRecordUpsertRequest maps to POST .../dns-zones/{zone_id}/records (create-or-update by name+type).
type DnsRecordUpsertRequest struct {
	Name   string   `json:"name"`
	Type   string   `json:"type"`
	Values []string `json:"values"`
	TTL    int      `json:"ttl,omitempty"`
}

// DnsRecordUpdateRequest maps to PUT .../dns-zones/{zone_id}/records/{record_id}.
// Values is a full-replace; the record's name/type identity cannot change via PUT.
type DnsRecordUpdateRequest struct {
	Values []string `json:"values"`
	TTL    int      `json:"ttl,omitempty"`
}

// DnsRecordResponse is returned by Create/Upsert (201), Read (200), and Update (200).
type DnsRecordResponse struct {
	ID        string   `json:"id"`
	ZoneID    string   `json:"zone_id"`
	TenantID  string   `json:"tenant_id"`
	Type      string   `json:"type"`
	Name      string   `json:"name"`
	Values    []string `json:"values"`
	TTL       int      `json:"ttl"`
	CreatedAt string   `json:"created_at"`
}

// UpsertDnsRecord sends POST .../dns-zones/{zoneID}/records. Returns 201 Created (sync).
// Creates a new record, or updates the existing one matching the same name+type within the zone.
func (c *DCAPIClient) UpsertDnsRecord(ctx context.Context, tenantID, projectID, vnetID, zoneID string, req DnsRecordUpsertRequest) (*DnsRecordResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/vnets/%s/dns-zones/%s/records", tenantID, projectID, vnetID, zoneID)
	respBytes, err := c.doRequest(ctx, "POST", path, req)
	if err != nil {
		return nil, fmt.Errorf("UpsertDnsRecord: %w", err)
	}
	var record DnsRecordResponse
	if err := json.Unmarshal(respBytes, &record); err != nil {
		return nil, fmt.Errorf("UpsertDnsRecord: failed to parse response: %w", err)
	}
	return &record, nil
}

// ListDnsRecords sends GET .../dns-zones/{zoneID}/records.
// There is no "get by name" endpoint — the dcapi_dns_record data source calls this and
// filters client-side on (name, type), the record's natural upsert identity within a zone.
func (c *DCAPIClient) ListDnsRecords(ctx context.Context, tenantID, projectID, vnetID, zoneID string) ([]DnsRecordResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/vnets/%s/dns-zones/%s/records", tenantID, projectID, vnetID, zoneID)
	respBytes, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("ListDnsRecords: %w", err)
	}
	var records []DnsRecordResponse
	if err := json.Unmarshal(respBytes, &records); err != nil {
		return nil, fmt.Errorf("ListDnsRecords: failed to parse response: %w", err)
	}
	return records, nil
}

// GetDnsRecord sends GET .../dns-zones/{zoneID}/records/{recordID}.
// Returns (nil, nil) on HTTP 404 — the drift sentinel used across all client files.
func (c *DCAPIClient) GetDnsRecord(ctx context.Context, tenantID, projectID, vnetID, zoneID, recordID string) (*DnsRecordResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/vnets/%s/dns-zones/%s/records/%s", tenantID, projectID, vnetID, zoneID, recordID)
	respBytes, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return nil, nil
		}
		return nil, fmt.Errorf("GetDnsRecord: %w", err)
	}
	var record DnsRecordResponse
	if err := json.Unmarshal(respBytes, &record); err != nil {
		return nil, fmt.Errorf("GetDnsRecord: failed to parse response: %w", err)
	}
	return &record, nil
}

// UpdateDnsRecord sends PUT .../dns-zones/{zoneID}/records/{recordID}. Returns 200 OK (sync).
// Values is a full-replace — send the complete desired values list.
func (c *DCAPIClient) UpdateDnsRecord(ctx context.Context, tenantID, projectID, vnetID, zoneID, recordID string, req DnsRecordUpdateRequest) (*DnsRecordResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/vnets/%s/dns-zones/%s/records/%s", tenantID, projectID, vnetID, zoneID, recordID)
	respBytes, err := c.doRequest(ctx, "PUT", path, req)
	if err != nil {
		return nil, fmt.Errorf("UpdateDnsRecord: %w", err)
	}
	var record DnsRecordResponse
	if err := json.Unmarshal(respBytes, &record); err != nil {
		return nil, fmt.Errorf("UpdateDnsRecord: failed to parse response: %w", err)
	}
	return &record, nil
}

// DeleteDnsRecord sends DELETE .../dns-zones/{zoneID}/records/{recordID}. Returns 204 (sync).
func (c *DCAPIClient) DeleteDnsRecord(ctx context.Context, tenantID, projectID, vnetID, zoneID, recordID string) error {
	path := fmt.Sprintf("/v1/tenants/%s/projects/%s/vnets/%s/dns-zones/%s/records/%s", tenantID, projectID, vnetID, zoneID, recordID)
	_, err := c.doRequest(ctx, "DELETE", path, nil)
	if err != nil {
		return fmt.Errorf("DeleteDnsRecord (zone %q, record %q): %w", zoneID, recordID, err)
	}
	return nil
}
