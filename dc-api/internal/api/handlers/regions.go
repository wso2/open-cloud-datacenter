// Package handlers — regions.go
//
// RegionsHandler serves the multi-region foundation's read + admin surface:
//
//   - GET  /v1/regions
//     Any authenticated caller. Returns every region with its zones and a
//     DERIVED health status (no stored status column — zone health is the
//     age of the zone's dc-agent last_seen, region health is the best of
//     its zones). This is what the cloud-ui dashboard "Regions" card reads.
//
//   - POST /v1/admin/regions/{region}/zones/{zone}/agent-token
//     Platform admin only (middleware.IsAdminFromContext). Mints a bearer
//     token a dc-agent uses to dial GET /v1/agent/ws. The raw token is
//     returned ONCE; only its sha256 digest is persisted.
//
// The WebSocket channel itself lives in agentws.go.
package handlers

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"
	"github.com/wso2/dc-api/internal/api/middleware"
	"github.com/wso2/dc-api/internal/db"
)

// Health windows for the derived zone status. A zone is "up" while its agent
// has checked in within agentUpWindow (the agent pings every 30s), "degraded"
// once it slips past that but is still recent, and "down" beyond
// agentDegradedWindow. A zone whose agent has never connected is "unknown".
const (
	agentUpWindow       = 90 * time.Second
	agentDegradedWindow = 10 * time.Minute
)

// regionZonePattern is the slug rule for region and zone names — lowercase
// ASCII, starts with a letter, ends alphanumeric, 2-32 chars. Mirrors the
// tenant slug convention so the names stay DNS/label-safe.
var regionZonePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}[a-z0-9]$`)

// RegionsHandler handles the regions read endpoint and the admin token mint.
type RegionsHandler struct {
	repo *db.Repository
	log  zerolog.Logger
}

// NewRegionsHandler constructs the handler with injected dependencies.
func NewRegionsHandler(repo *db.Repository, log zerolog.Logger) *RegionsHandler {
	return &RegionsHandler{repo: repo, log: log}
}

// ── Response DTOs (mirror cloud-ui's regions.ts and openapi.yaml) ────────────
// agent / display_name / description are emitted as JSON null (not omitted)
// when absent — the spec types them `… | null` and the dashboard relies on
// `display_name ?? name` for its fallback.

type agentStatusDTO struct {
	Version  string `json:"version"`
	LastSeen string `json:"last_seen"` // RFC3339
}

type zoneDTO struct {
	Name   string          `json:"name"`
	Status string          `json:"status"`
	Agent  *agentStatusDTO `json:"agent"`
}

type regionDTO struct {
	Name        string    `json:"name"`
	DisplayName *string   `json:"display_name"`
	Description *string   `json:"description"`
	Status      string    `json:"status"`
	Zones       []zoneDTO `json:"zones"`
}

type regionListDTO struct {
	Items []regionDTO `json:"items"`
}

// List handles GET /v1/regions.
func (h *RegionsHandler) List(w http.ResponseWriter, r *http.Request) {
	regions, err := h.repo.ListRegionsWithZones(r.Context())
	if err != nil {
		h.log.Error().Err(err).Msg("list regions failed")
		writeError(w, http.StatusInternalServerError, "failed to list regions")
		return
	}

	now := time.Now()
	out := regionListDTO{Items: make([]regionDTO, 0, len(regions))}
	for _, rr := range regions {
		zones := make([]zoneDTO, 0, len(rr.Zones))
		statuses := make([]string, 0, len(rr.Zones))
		for _, z := range rr.Zones {
			st := zoneStatus(z.Agent, now)
			statuses = append(statuses, st)
			var agent *agentStatusDTO
			if z.Agent != nil {
				agent = &agentStatusDTO{
					Version:  z.Agent.Version,
					LastSeen: z.Agent.LastSeen.UTC().Format(time.RFC3339),
				}
			}
			zones = append(zones, zoneDTO{Name: z.Name, Status: st, Agent: agent})
		}
		out.Items = append(out.Items, regionDTO{
			Name:        rr.Name,
			DisplayName: strPtrOrNil(rr.DisplayName),
			Description: strPtrOrNil(rr.Description),
			Status:      regionStatus(statuses),
			Zones:       zones,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// MintAgentToken handles POST /v1/admin/regions/{region}/zones/{zone}/agent-token.
func (h *RegionsHandler) MintAgentToken(w http.ResponseWriter, r *http.Request) {
	// Platform-admin gate. Mirrors admin_tenants.go: admin-tier endpoints are
	// never delegated, so owner/member roles don't grant access.
	if !middleware.IsAdminFromContext(r.Context()) {
		writeError(w, http.StatusForbidden, "insufficient permissions for this action")
		return
	}

	region := chi.URLParam(r, "region")
	zone := chi.URLParam(r, "zone")
	if !regionZonePattern.MatchString(region) {
		writeError(w, http.StatusBadRequest, "region must match ^[a-z][a-z0-9-]{0,30}[a-z0-9]$")
		return
	}
	if !regionZonePattern.MatchString(zone) {
		writeError(w, http.StatusBadRequest, "zone must match ^[a-z][a-z0-9-]{0,30}[a-z0-9]$")
		return
	}

	// Register the region/zone if this is the first token for a freshly
	// bootstrapped cluster. This records metadata only — provisioning the
	// underlying Harvester/Rancher infrastructure remains Terraform's job, so
	// there is no "create region" API; the region/zone simply comes into being
	// when an admin mints the first agent token for it.
	if err := h.repo.EnsureRegionZone(r.Context(), region, zone); err != nil {
		h.log.Error().Err(err).Msg("ensure region/zone failed")
		writeError(w, http.StatusInternalServerError, "failed to mint agent token")
		return
	}

	raw, err := generateAgentToken()
	if err != nil {
		h.log.Error().Err(err).Msg("agent token generation failed")
		writeError(w, http.StatusInternalServerError, "failed to mint agent token")
		return
	}
	sum := sha256.Sum256([]byte(raw))
	hash := hex.EncodeToString(sum[:])

	_, pID, _ := middleware.PrincipalFromContext(r.Context())
	createdBy := "admin:" + pID

	if err := h.repo.CreateAgentToken(r.Context(), region, zone, hash, createdBy); err != nil {
		h.log.Error().Err(err).Msg("store agent token failed")
		writeError(w, http.StatusInternalServerError, "failed to mint agent token")
		return
	}

	h.log.Info().
		Str("region", region).
		Str("zone", zone).
		Str("created_by", createdBy).
		Msg("minted dc-agent token")

	// The raw token is returned exactly once — it is never recoverable after
	// this response (only the sha256 digest is stored).
	writeJSON(w, http.StatusCreated, map[string]string{
		"token":  raw,
		"region": region,
		"zone":   zone,
	})
}

// generateAgentToken returns a "dcagent_"-prefixed token with 32 bytes of
// entropy. The prefix lets the WS handler reject obviously-wrong bearers
// before hashing, and makes the credential self-identifying in logs/leaks.
func generateAgentToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "dcagent_" + hex.EncodeToString(buf), nil
}

// zoneStatus classifies one zone's health from its agent's last_seen age.
func zoneStatus(agent *db.AgentRow, now time.Time) string {
	if agent == nil {
		return "unknown"
	}
	age := now.Sub(agent.LastSeen)
	switch {
	case age < agentUpWindow:
		return "up"
	case age < agentDegradedWindow:
		return "degraded"
	default:
		return "down"
	}
}

// regionStatus rolls a region up to the best status among its zones (a region
// with one healthy zone reads "up"). Empty region → "unknown".
func regionStatus(zoneStatuses []string) string {
	rank := map[string]int{"unknown": 0, "down": 1, "degraded": 2, "up": 3}
	best := "unknown"
	for _, s := range zoneStatuses {
		if rank[s] > rank[best] {
			best = s
		}
	}
	return best
}

// strPtrOrNil returns nil for an empty string so JSON encodes it as null.
func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
