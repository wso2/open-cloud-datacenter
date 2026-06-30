package handlers

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/wso2/dc-api/internal/models"
)

// TestCreateVNetRequest_DecodesOptionalZone proves the optional `zone` selector
// on POST /v1/vnets is decoded onto the DTO, and that its absence leaves the
// field empty (so CreateVNet stamps the local default — byte-identical to the
// prior behaviour).
func TestCreateVNetRequest_DecodesOptionalZone(t *testing.T) {
	t.Run("zone present", func(t *testing.T) {
		var req createVNetRequest
		body := `{"name":"net","address_space":["10.0.0.0/16"],"region":"lk","zone":"zone-2"}`
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if req.Zone != "zone-2" {
			t.Errorf("req.Zone = %q, want %q", req.Zone, "zone-2")
		}
	})

	t.Run("zone omitted stays empty", func(t *testing.T) {
		var req createVNetRequest
		body := `{"name":"net","address_space":["10.0.0.0/16"],"region":"lk"}`
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if req.Zone != "" {
			t.Errorf("req.Zone = %q, want empty when omitted", req.Zone)
		}
	})
}

// TestVNetToResponse_SurfacesZone proves the resolved zone on the VNet model is
// surfaced read-only in the API response, and that an empty zone is omitted from
// the JSON (omitempty) so a single-zone deployment's payload is unchanged.
func TestVNetToResponse_SurfacesZone(t *testing.T) {
	id := uuid.New()
	tUUID := uuid.New()

	t.Run("zone populated", func(t *testing.T) {
		v := &models.VNet{
			ID:           id,
			TenantUUID:   tUUID,
			Name:         "net",
			Region:       "lk",
			Zone:         "zone-2",
			AddressSpace: []string{"10.0.0.0/16"},
			Status:       models.StatusActive,
			ProviderType: "kubeovn",
		}
		resp := vnetToResponse(v)
		if resp.Zone != "zone-2" {
			t.Errorf("resp.Zone = %q, want %q", resp.Zone, "zone-2")
		}
		raw, err := json.Marshal(resp)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if m["zone"] != "zone-2" {
			t.Errorf("JSON zone = %v, want %q", m["zone"], "zone-2")
		}
	})

	t.Run("zone empty omitted", func(t *testing.T) {
		v := &models.VNet{
			ID:           id,
			TenantUUID:   tUUID,
			Name:         "net",
			Region:       "lk",
			AddressSpace: []string{"10.0.0.0/16"},
			Status:       models.StatusActive,
			ProviderType: "kubeovn",
		}
		resp := vnetToResponse(v)
		if resp.Zone != "" {
			t.Errorf("resp.Zone = %q, want empty", resp.Zone)
		}
		raw, err := json.Marshal(resp)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if _, present := m["zone"]; present {
			t.Errorf("zone key present in JSON for empty zone; want omitted")
		}
	})
}
