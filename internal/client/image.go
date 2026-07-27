// Image-related API calls for the DC-API client.
// Images are scoped to a tenant (not a project) — they are registered once by platform
// operators and referenced by many projects' VMs. Read-only from the provider's perspective:
// no per-image GET endpoint exists upstream, only List and Create.
package client

import (
	"context"
	"encoding/json"
	"fmt"
)

// Image is a VM boot image registered under a tenant.
type Image struct {
	// ID is the composite "namespace/resource-name" identifier — pass this as image_name
	// when creating a dcapi_virtual_machine.
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Namespace   string `json:"namespace"`
}

// ListImages sends GET /v1/tenants/{tenantID}/images. There is no per-image GET endpoint —
// the dcapi_image data source calls this and filters client-side by display_name.
func (c *DCAPIClient) ListImages(ctx context.Context, tenantID string) ([]Image, error) {
	path := fmt.Sprintf("/v1/tenants/%s/images", tenantID)

	respBytes, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("ListImages: %w", err)
	}

	var images []Image
	if err := json.Unmarshal(respBytes, &images); err != nil {
		return nil, fmt.Errorf("ListImages: failed to parse response: %w", err)
	}
	return images, nil
}
