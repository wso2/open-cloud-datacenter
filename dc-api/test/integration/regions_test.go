//go:build integration

// regions_test.go — multi-region foundation (phase 0).
//
// Pure-DB + WebSocket coverage that runs cluster-free (DCAPI_TEST_NOP=1):
//   - GET /v1/regions derives zone/region health from agent last_seen.
//   - POST /v1/admin/regions/{region}/zones/{zone}/agent-token gates on admin,
//     404s an unknown zone, and stores only the sha256 digest.
//   - GET /v1/agent/ws runs the protocol-v0 handshake (hello/hello_ack/ping/
//     pong); a connected agent flips its zone to "up". Bad/absent bearers 401.
package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// ── Response shapes (mirror openapi.yaml's RegionList / Region / Zone) ────────

type tRegionList struct {
	Items []tRegion `json:"items"`
}

type tRegion struct {
	Name        string  `json:"name"`
	DisplayName *string `json:"display_name"`
	Description *string `json:"description"`
	Status      string  `json:"status"`
	Zones       []tZone `json:"zones"`
}

type tZone struct {
	Name   string  `json:"name"`
	Status string  `json:"status"`
	Agent  *tAgent `json:"agent"`
}

type tAgent struct {
	Version  string `json:"version"`
	LastSeen string `json:"last_seen"`
}

// rawReq fires an HTTP request with an optional bearer token and returns the
// body + status. token "" omits the Authorization header.
func rawReq(t *testing.T, method, url, token string, body io.Reader) ([]byte, int) {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return b, resp.StatusCode
}

func findRegion(list tRegionList, name string) (tRegion, bool) {
	for _, r := range list.Items {
		if r.Name == name {
			return r, true
		}
	}
	return tRegion{}, false
}

func findZone(r tRegion, name string) (tZone, bool) {
	for _, z := range r.Zones {
		if z.Name == name {
			return z, true
		}
	}
	return tZone{}, false
}

// TestRegionsList asserts the read endpoint is open to any authenticated caller
// and derives "unknown" for a zone whose agent has never connected.
func TestRegionsList(t *testing.T) {
	ctx := context.Background()

	// An isolated region/zone with no agent — immune to the WS test's mutation
	// of lk/zone-1, so its derived status is deterministically "unknown".
	if _, err := env.DB.Pool().Exec(ctx,
		`INSERT INTO regions (name, description) VALUES ('rtest', 'isolated test region')
		 ON CONFLICT (name) DO NOTHING`); err != nil {
		t.Fatalf("seed region: %v", err)
	}
	if _, err := env.DB.Pool().Exec(ctx,
		`INSERT INTO zones (region_name, name, description) VALUES ('rtest', 'ztest', 'isolated zone')
		 ON CONFLICT (region_name, name) DO NOTHING`); err != nil {
		t.Fatalf("seed zone: %v", err)
	}

	token, err := env.JWT.MintTokenWithGroups("member-sub", "member", nil)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}

	body, status := rawReq(t, http.MethodGet, env.BaseURL+"/v1/regions", token, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /v1/regions: status %d, body %s", status, body)
	}
	var list tRegionList
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode regions: %v (body %s)", err, body)
	}

	// Seeded lk region is always present.
	if _, ok := findRegion(list, "lk"); !ok {
		t.Errorf("expected seeded region 'lk' in %s", body)
	}

	reg, ok := findRegion(list, "rtest")
	if !ok {
		t.Fatalf("expected region 'rtest' in %s", body)
	}
	if reg.Status != "unknown" {
		t.Errorf("rtest status = %q, want unknown", reg.Status)
	}
	z, ok := findZone(reg, "ztest")
	if !ok {
		t.Fatalf("expected zone 'ztest' under rtest")
	}
	if z.Status != "unknown" {
		t.Errorf("ztest status = %q, want unknown", z.Status)
	}
	if z.Agent != nil {
		t.Errorf("ztest agent = %+v, want nil (no agent ever connected)", z.Agent)
	}
}

// TestAgentTokenMint covers the admin-only token mint: 403 for non-admins, 404
// for an unknown zone, 201 + a dcagent_ token (with only the digest stored) for
// a valid request.
func TestAgentTokenMint(t *testing.T) {
	ctx := context.Background()
	const url = "/v1/admin/regions/lk/zones/zone-1/agent-token"

	memberToken, err := env.JWT.MintTokenWithGroups("member-sub", "member", nil)
	if err != nil {
		t.Fatalf("mint member token: %v", err)
	}
	adminToken, err := env.JWT.MintTokenWithGroups("admin-sub", "admin", []string{"dc-admin"})
	if err != nil {
		t.Fatalf("mint admin token: %v", err)
	}

	// Non-admin → 403.
	if _, status := rawReq(t, http.MethodPost, env.BaseURL+url, memberToken, nil); status != http.StatusForbidden {
		t.Errorf("non-admin mint: status %d, want 403", status)
	}

	// Admin, unknown zone → 404.
	if _, status := rawReq(t, http.MethodPost, env.BaseURL+"/v1/admin/regions/lk/zones/nope/agent-token", adminToken, nil); status != http.StatusNotFound {
		t.Errorf("unknown-zone mint: status %d, want 404", status)
	}

	// Admin, valid zone → 201 + token once.
	body, status := rawReq(t, http.MethodPost, env.BaseURL+url, adminToken, nil)
	if status != http.StatusCreated {
		t.Fatalf("admin mint: status %d, body %s", status, body)
	}
	var resp struct {
		Token  string `json:"token"`
		Region string `json:"region"`
		Zone   string `json:"zone"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode token resp: %v (body %s)", err, body)
	}
	if !strings.HasPrefix(resp.Token, "dcagent_") {
		t.Errorf("token %q missing dcagent_ prefix", resp.Token)
	}
	if resp.Region != "lk" || resp.Zone != "zone-1" {
		t.Errorf("token scope = %s/%s, want lk/zone-1", resp.Region, resp.Zone)
	}

	// Only the sha256 digest is persisted — the raw token must hash to a stored row.
	sum := sha256.Sum256([]byte(resp.Token))
	var count int
	if err := env.DB.Pool().QueryRow(ctx,
		`SELECT COUNT(*) FROM agent_tokens WHERE token_hash = $1 AND region_name = 'lk' AND zone_name = 'zone-1'`,
		hex.EncodeToString(sum[:]),
	).Scan(&count); err != nil {
		t.Fatalf("query agent_tokens: %v", err)
	}
	if count != 1 {
		t.Errorf("agent_tokens rows for digest = %d, want 1", count)
	}
}

// TestAgentWSHandshake mints a token, runs the protocol-v0 handshake over the
// WebSocket, and asserts the zone flips to "up" with the agent's version.
func TestAgentWSHandshake(t *testing.T) {
	ctx := context.Background()

	adminToken, err := env.JWT.MintTokenWithGroups("admin-sub", "admin", []string{"dc-admin"})
	if err != nil {
		t.Fatalf("mint admin token: %v", err)
	}
	body, status := rawReq(t, http.MethodPost, env.BaseURL+"/v1/admin/regions/lk/zones/zone-1/agent-token", adminToken, nil)
	if status != http.StatusCreated {
		t.Fatalf("mint agent token: status %d, body %s", status, body)
	}
	var minted struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &minted); err != nil {
		t.Fatalf("decode token: %v", err)
	}

	wsURL := "ws" + strings.TrimPrefix(env.BaseURL, "http") + "/v1/agent/ws"
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + minted.Token}},
	})
	if err != nil {
		t.Fatalf("dial agent ws: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	// hello → hello_ack.
	const agentVersion = "test-0.1"
	writeWS(t, ctx, c, map[string]string{"type": "hello", "region": "lk", "zone": "zone-1", "version": agentVersion})
	ack := readWS(t, ctx, c)
	if ack["type"] != "hello_ack" {
		t.Fatalf("expected hello_ack, got %v", ack)
	}
	if ack["agent_id"] == "" {
		t.Errorf("hello_ack missing agent_id")
	}

	// ping → pong.
	writeWS(t, ctx, c, map[string]string{"type": "ping", "ts": time.Now().UTC().Format(time.RFC3339)})
	pong := readWS(t, ctx, c)
	if pong["type"] != "pong" {
		t.Fatalf("expected pong, got %v", pong)
	}

	// The connected agent flips lk/zone-1 to "up".
	regBody, regStatus := rawReq(t, http.MethodGet, env.BaseURL+"/v1/regions", adminToken, nil)
	if regStatus != http.StatusOK {
		t.Fatalf("GET /v1/regions: status %d, body %s", regStatus, regBody)
	}
	var list tRegionList
	if err := json.Unmarshal(regBody, &list); err != nil {
		t.Fatalf("decode regions: %v", err)
	}
	reg, ok := findRegion(list, "lk")
	if !ok {
		t.Fatalf("region lk not found in %s", regBody)
	}
	z, ok := findZone(reg, "zone-1")
	if !ok {
		t.Fatalf("zone zone-1 not found under lk")
	}
	if z.Status != "up" {
		t.Errorf("zone-1 status = %q, want up", z.Status)
	}
	if z.Agent == nil || z.Agent.Version != agentVersion {
		t.Errorf("zone-1 agent = %+v, want version %q", z.Agent, agentVersion)
	}
	if reg.Status != "up" {
		t.Errorf("region lk status = %q, want up (best of zones)", reg.Status)
	}
}

// TestAgentWSUnauthorized asserts the channel rejects a missing, malformed, or
// non-agent bearer with HTTP 401 at the upgrade.
func TestAgentWSUnauthorized(t *testing.T) {
	ctx := context.Background()
	wsURL := "ws" + strings.TrimPrefix(env.BaseURL, "http") + "/v1/agent/ws"

	cases := []struct {
		name   string
		header http.Header
	}{
		{"absent bearer", nil},
		{"non-agent bearer", http.Header{"Authorization": []string{"Bearer not-a-dcagent-token"}}},
		{"unknown agent token", http.Header{"Authorization": []string{"Bearer dcagent_deadbeef"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			c, resp, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{HTTPHeader: tc.header})
			if err == nil {
				_ = c.Close(websocket.StatusNormalClosure, "")
				t.Fatalf("expected dial to fail with 401")
			}
			if resp == nil || resp.StatusCode != http.StatusUnauthorized {
				gotStatus := 0
				if resp != nil {
					gotStatus = resp.StatusCode
				}
				t.Errorf("status = %d, want 401", gotStatus)
			}
		})
	}
}

// writeWS marshals v and writes it as a text frame with a bounded deadline.
func writeWS(t *testing.T, ctx context.Context, c *websocket.Conn, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal ws frame: %v", err)
	}
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := c.Write(wctx, websocket.MessageText, b); err != nil {
		t.Fatalf("write ws frame: %v", err)
	}
}

// readWS reads one text frame and decodes it into a string map.
func readWS(t *testing.T, ctx context.Context, c *websocket.Conn) map[string]string {
	t.Helper()
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, data, err := c.Read(rctx)
	if err != nil {
		t.Fatalf("read ws frame: %v", err)
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("decode ws frame: %v (raw %s)", err, data)
	}
	return m
}
