package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"

	"github.com/wso2/dc-api/internal/api/middleware"
)

// invRequest builds a GET inventory request for (region, zone) with chi path
// params, optionally carrying the platform-admin flag.
func invRequest(region, zone string, admin bool) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/regions/"+region+"/zones/"+zone+"/inventory", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("region", region)
	rctx.URLParams.Add("zone", zone)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	if admin {
		ctx = context.WithValue(ctx, middleware.ContextKeyIsAdmin, true)
	}
	return req.WithContext(ctx)
}

func TestInventoryHandler_Success(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()
	tc.runAgent(func(req wsReq) string {
		if req.Op != opGetInventory {
			return `{"type":"res","id":"` + req.ID + `","ok":false,"error":{"code":"OP_UNSUPPORTED","message":"no"}}`
		}
		return resOK(req.ID, `{"nodes":[{"name":"n1","ready":true}],"vm_count":4}`)
	})
	reg := NewRegistry()
	reg.register(tc.sess) // tc.sess is region "lk", zone "zone-1"

	rec := httptest.NewRecorder()
	NewInventoryHandler(reg, zerolog.Nop()).Get(rec, invRequest("lk", "zone-1", true))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"vm_count":4`) {
		t.Errorf("body = %s, want vm_count:4", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
}

func TestInventoryHandler_NonAdminForbidden(t *testing.T) {
	rec := httptest.NewRecorder()
	NewInventoryHandler(NewRegistry(), zerolog.Nop()).Get(rec, invRequest("lk", "zone-1", false))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestInventoryHandler_NoAgentUnavailable(t *testing.T) {
	rec := httptest.NewRecorder()
	NewInventoryHandler(NewRegistry(), zerolog.Nop()).Get(rec, invRequest("lk", "zone-1", true))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestInventoryHandler_AgentExecError(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()
	tc.runAgent(func(req wsReq) string {
		return `{"type":"res","id":"` + req.ID + `","ok":false,"error":{"code":"EXEC_ERROR","message":"boom"}}`
	})
	reg := NewRegistry()
	reg.register(tc.sess)

	rec := httptest.NewRecorder()
	NewInventoryHandler(reg, zerolog.Nop()).Get(rec, invRequest("lk", "zone-1", true))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502, body = %s", rec.Code, rec.Body.String())
	}
}

func TestInventoryHandler_OpUnsupported(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()
	tc.runAgent(func(req wsReq) string {
		return `{"type":"res","id":"` + req.ID + `","ok":false,"error":{"code":"OP_UNSUPPORTED","message":"old agent"}}`
	})
	reg := NewRegistry()
	reg.register(tc.sess)

	rec := httptest.NewRecorder()
	NewInventoryHandler(reg, zerolog.Nop()).Get(rec, invRequest("lk", "zone-1", true))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501, body = %s", rec.Code, rec.Body.String())
	}
}
