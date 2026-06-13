// Package db — regions.go
//
// Repository methods for the multi-region foundation (phase 0): the regions
// catalog, its zones, the dc-agents that dial in per zone, and the bearer
// tokens those agents authenticate with.
//
// Health is DERIVED, never stored: GET /v1/regions reads agents.last_seen and
// the handler classifies the age into up/degraded/down/unknown. There is no
// status column to keep in sync — an agent that stops checking in simply ages
// out. SQL lives here; handlers call these methods, never the pool directly.
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// RegionRow is one region with its zones, assembled for GET /v1/regions.
// DisplayName/Description are "" when NULL in the catalog.
type RegionRow struct {
	Name        string
	DisplayName string
	Description string
	Zones       []ZoneRow
}

// ZoneRow is one availability zone within a region. Agent is nil when no
// dc-agent has ever connected for this zone (status "unknown").
type ZoneRow struct {
	Name        string
	Description string
	Agent       *AgentRow
}

// AgentRow is the latest dc-agent seen for a zone. LastSeen drives the derived
// health classification in the handler.
type AgentRow struct {
	Version  string
	LastSeen time.Time
}

// ListRegionsWithZones returns every region, each with its zones and (when
// present) the single dc-agent record for that zone. The agents table carries
// a UNIQUE (region_name, zone_name) index, so the LEFT JOIN yields at most one
// agent row per zone. Regions and zones are ordered by name for stable output.
func (r *Repository) ListRegionsWithZones(ctx context.Context) ([]RegionRow, error) {
	const regionsQ = `
		SELECT name, COALESCE(display_name, ''), COALESCE(description, '')
		FROM   regions
		ORDER  BY name`
	rows, err := r.pool.Query(ctx, regionsQ)
	if err != nil {
		return nil, fmt.Errorf("db list regions: %w", err)
	}
	defer rows.Close()

	regions := []RegionRow{}
	indexByName := map[string]int{}
	for rows.Next() {
		var rr RegionRow
		if err := rows.Scan(&rr.Name, &rr.DisplayName, &rr.Description); err != nil {
			return nil, fmt.Errorf("db scan region: %w", err)
		}
		rr.Zones = []ZoneRow{}
		indexByName[rr.Name] = len(regions)
		regions = append(regions, rr)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db iterate regions: %w", err)
	}

	const zonesQ = `
		SELECT z.region_name, z.name, COALESCE(z.description, ''), a.version, a.last_seen
		FROM   zones z
		LEFT   JOIN agents a
		       ON a.region_name = z.region_name AND a.zone_name = z.name
		ORDER  BY z.region_name, z.name`
	zrows, err := r.pool.Query(ctx, zonesQ)
	if err != nil {
		return nil, fmt.Errorf("db list zones: %w", err)
	}
	defer zrows.Close()

	for zrows.Next() {
		var regionName string
		var zr ZoneRow
		var version *string
		var lastSeen *time.Time
		if err := zrows.Scan(&regionName, &zr.Name, &zr.Description, &version, &lastSeen); err != nil {
			return nil, fmt.Errorf("db scan zone: %w", err)
		}
		if lastSeen != nil {
			v := ""
			if version != nil {
				v = *version
			}
			zr.Agent = &AgentRow{Version: v, LastSeen: *lastSeen}
		}
		if i, ok := indexByName[regionName]; ok {
			regions[i].Zones = append(regions[i].Zones, zr)
		}
	}
	if err := zrows.Err(); err != nil {
		return nil, fmt.Errorf("db iterate zones: %w", err)
	}
	return regions, nil
}

// EnsureRegionZone records a region and its zone if they don't already exist,
// so an admin minting the first token for a freshly bootstrapped cluster
// implicitly registers it. This is metadata only — provisioning the underlying
// Harvester/Rancher infrastructure stays with Terraform, so there is no
// separate "create region" path; a region/zone exists once a token is minted
// for it (or an agent connects). Both inserts are idempotent.
func (r *Repository) EnsureRegionZone(ctx context.Context, region, zone string) error {
	const regionQ = `INSERT INTO regions (name) VALUES ($1) ON CONFLICT (name) DO NOTHING`
	if _, err := r.pool.Exec(ctx, regionQ, region); err != nil {
		return fmt.Errorf("db ensure region: %w", err)
	}
	const zoneQ = `INSERT INTO zones (region_name, name) VALUES ($1, $2) ON CONFLICT (region_name, name) DO NOTHING`
	if _, err := r.pool.Exec(ctx, zoneQ, region, zone); err != nil {
		return fmt.Errorf("db ensure zone: %w", err)
	}
	return nil
}

// CreateAgentToken stores the sha256 hex digest of a freshly minted agent
// token, scoped to (region, zone). The raw token is never persisted — the
// handler returns it to the caller exactly once.
func (r *Repository) CreateAgentToken(ctx context.Context, region, zone, tokenHash, createdBy string) error {
	const q = `
		INSERT INTO agent_tokens (region_name, zone_name, token_hash, created_by)
		VALUES ($1, $2, $3, $4)`
	if _, err := r.pool.Exec(ctx, q, region, zone, tokenHash, createdBy); err != nil {
		return fmt.Errorf("db create agent token: %w", err)
	}
	return nil
}

// LookupAgentToken resolves a token's sha256 hex digest to the (region, zone)
// it authorises. found is false (with nil error) when no token matches — the
// WS handler maps that to 401.
func (r *Repository) LookupAgentToken(ctx context.Context, tokenHash string) (region, zone string, found bool, err error) {
	const q = `SELECT region_name, zone_name FROM agent_tokens WHERE token_hash = $1`
	err = r.pool.QueryRow(ctx, q, tokenHash).Scan(&region, &zone)
	if err == pgx.ErrNoRows {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("db lookup agent token: %w", err)
	}
	return region, zone, true, nil
}

// MarkAgentTokenUsed bumps last_used_at on the token a connecting agent
// presented. Called once per successful authentication (not per frame) so the
// column records the last time the credential opened a channel.
func (r *Repository) MarkAgentTokenUsed(ctx context.Context, tokenHash string) error {
	const q = `UPDATE agent_tokens SET last_used_at = NOW() WHERE token_hash = $1`
	if _, err := r.pool.Exec(ctx, q, tokenHash); err != nil {
		return fmt.Errorf("db mark agent token used: %w", err)
	}
	return nil
}

// UpsertAgent records (or refreshes) the single agents row for a zone when an
// agent sends its hello frame. connected_at and last_seen are reset to now on
// every (re)connect; the returned id is the agent_id echoed back in hello_ack.
func (r *Repository) UpsertAgent(ctx context.Context, region, zone, version, remoteAddr string) (uuid.UUID, error) {
	const q = `
		INSERT INTO agents (region_name, zone_name, version, remote_addr, connected_at, last_seen)
		VALUES ($1, $2, $3, $4, NOW(), NOW())
		ON CONFLICT (region_name, zone_name)
		DO UPDATE SET version      = EXCLUDED.version,
		              remote_addr  = EXCLUDED.remote_addr,
		              connected_at = NOW(),
		              last_seen    = NOW()
		RETURNING id`
	var id uuid.UUID
	if err := r.pool.QueryRow(ctx, q, region, zone, version, remoteAddr).Scan(&id); err != nil {
		return uuid.Nil, fmt.Errorf("db upsert agent: %w", err)
	}
	return id, nil
}

// TouchAgent bumps last_seen for a connected agent. Called on every inbound
// frame (ping and beyond) so the derived health stays fresh while the channel
// is alive.
func (r *Repository) TouchAgent(ctx context.Context, id uuid.UUID) error {
	const q = `UPDATE agents SET last_seen = NOW() WHERE id = $1`
	if _, err := r.pool.Exec(ctx, q, id); err != nil {
		return fmt.Errorf("db touch agent: %w", err)
	}
	return nil
}
