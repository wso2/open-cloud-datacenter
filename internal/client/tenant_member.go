// TenantMember API calls for the DC-API client.
// Members are role assignments scoped to a tenant (owner|member|viewer). The DC-API has no
// GET-by-id or Update endpoint for a single member — List is the only way to read one back,
// and changing a role requires deleting the assignment and re-inviting the user.
package client

import (
	"context"
	"encoding/json"
	"fmt"
)

// TenantMemberCreateRequest maps to POST /v1/tenants/{tenant_id}/members.
type TenantMemberCreateRequest struct {
	UserSub      string `json:"user_sub"`
	Role         string `json:"role"`
	DisplayAlias string `json:"display_alias,omitempty"`
}

// TenantMemberResponse is the JSON shape returned by Create (201) and each item of List (200).
type TenantMemberResponse struct {
	ID            string `json:"id"`
	PrincipalType string `json:"principal_type"`
	PrincipalID   string `json:"principal_id"` // echoes user_sub; also the DELETE path key
	ScopeType     string `json:"scope_type"`
	ScopeID       string `json:"scope_id"`
	Role          string `json:"role"`
	GrantedAt     string `json:"granted_at"`
	GrantedBy     string `json:"granted_by"`
	DisplayAlias  string `json:"display_alias"`
}

// CreateTenantMember sends POST /v1/tenants/{tenantID}/members.
func (c *DCAPIClient) CreateTenantMember(ctx context.Context, tenantID string, req TenantMemberCreateRequest) (*TenantMemberResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/members", tenantID)

	respBytes, err := c.doRequest(ctx, "POST", path, req)
	if err != nil {
		return nil, fmt.Errorf("CreateTenantMember: %w", err)
	}

	var member TenantMemberResponse
	if err := json.Unmarshal(respBytes, &member); err != nil {
		return nil, fmt.Errorf("CreateTenantMember: failed to parse response: %w", err)
	}
	return &member, nil
}

// ListTenantMembers sends GET /v1/tenants/{tenantID}/members. There is no get-by-id endpoint
// for a single member, so the resource's Read resolves one by scanning this list for a
// matching principal_id.
func (c *DCAPIClient) ListTenantMembers(ctx context.Context, tenantID string) ([]TenantMemberResponse, error) {
	path := fmt.Sprintf("/v1/tenants/%s/members", tenantID)

	respBytes, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("ListTenantMembers: %w", err)
	}

	var members []TenantMemberResponse
	if err := json.Unmarshal(respBytes, &members); err != nil {
		return nil, fmt.Errorf("ListTenantMembers: failed to parse response: %w", err)
	}
	return members, nil
}

// DeleteTenantMember sends DELETE /v1/tenants/{tenantID}/members/{principalID}.
// principalID is the OIDC sub string (TenantMemberResponse.PrincipalID) — NOT the
// role_assignment UUID returned in the "id" field.
func (c *DCAPIClient) DeleteTenantMember(ctx context.Context, tenantID, principalID string) error {
	path := fmt.Sprintf("/v1/tenants/%s/members/%s", tenantID, principalID)

	_, err := c.doRequest(ctx, "DELETE", path, nil)
	if err != nil {
		return fmt.Errorf("DeleteTenantMember (tenant %q, principal %q): %w", tenantID, principalID, err)
	}
	return nil
}
