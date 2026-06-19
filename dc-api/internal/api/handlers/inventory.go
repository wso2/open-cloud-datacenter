// Package handlers — inventory.go
//
// GET /v1/admin/regions/{region}/zones/{zone}/inventory returns a zone
// cluster's node/capacity inventory, fetched live from that zone's dc-agent over
// the command channel (the get_inventory op). It is admin-only: node counts and
// capacity are Harvester-internal figures that never reach a tenant (see
// docs/multi-region-protocol-v1.md §11).
package handlers

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"

	"github.com/wso2/dc-api/internal/api/middleware"
)

// inventoryCallTimeout bounds the live RPC to the agent.
const inventoryCallTimeout = 15 * time.Second

// InventoryHandler serves the admin-only zone inventory endpoint by calling the
// connected agent over the command channel.
type InventoryHandler struct {
	registry *Registry
	log      zerolog.Logger
}

// NewInventoryHandler constructs the handler with the shared agent Registry.
func NewInventoryHandler(registry *Registry, log zerolog.Logger) *InventoryHandler {
	return &InventoryHandler{registry: registry, log: log}
}

// Get handles GET /v1/admin/regions/{region}/zones/{zone}/inventory.
func (h *InventoryHandler) Get(w http.ResponseWriter, r *http.Request) {
	// Platform-admin gate, mirroring the token-mint route: admin-tier endpoints
	// are never delegated to owner/member roles.
	if !middleware.IsAdminFromContext(r.Context()) {
		writeError(w, http.StatusForbidden, "insufficient permissions for this action")
		return
	}
	region := chi.URLParam(r, "region")
	zone := chi.URLParam(r, "zone")

	sess, ok := h.registry.Session(region, zone)
	if !ok {
		// No agent is currently connected for this zone — transient.
		h.log.Info().Str("region", region).Str("zone", zone).
			Msg("inventory requested but no agent connected for zone")
		writeError(w, http.StatusServiceUnavailable, "no agent connected for this zone")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), inventoryCallTimeout)
	defer cancel()

	result, err := sess.Call(ctx, opGetInventory, nil)
	if err != nil {
		h.writeCallError(w, region, zone, err)
		return
	}

	// result is the agent's inventory JSON; pass it through verbatim.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result)
}

// writeCallError maps a Session.Call failure to an HTTP status, per the protocol
// error model (design doc §7): transient → 503/504, agent-reported → 5xx/4xx.
func (h *InventoryHandler) writeCallError(w http.ResponseWriter, region, zone string, err error) {
	switch {
	case errors.Is(err, ErrAgentUnavailable):
		h.log.Warn().Str("region", region).Str("zone", zone).
			Msg("inventory call failed: agent channel unavailable")
		writeError(w, http.StatusServiceUnavailable, "agent channel unavailable")
	case errors.Is(err, context.DeadlineExceeded):
		h.log.Warn().Str("region", region).Str("zone", zone).
			Msg("inventory call failed: agent did not respond in time")
		writeError(w, http.StatusGatewayTimeout, "agent did not respond in time")
	default:
		var ae *AgentError
		if errors.As(err, &ae) {
			switch ae.Code {
			case errCodeOpUnsupported:
				writeError(w, http.StatusNotImplemented, "agent does not support inventory")
			case errCodeBadRequest:
				writeError(w, http.StatusBadRequest, ae.Message)
			default: // EXEC_ERROR and anything unrecognised
				h.log.Warn().Str("region", region).Str("zone", zone).Str("code", ae.Code).
					Msg("agent inventory op failed")
				writeError(w, http.StatusBadGateway, "agent failed to read inventory")
			}
			return
		}
		h.log.Warn().Err(err).Str("region", region).Str("zone", zone).Msg("agent inventory call failed")
		writeError(w, http.StatusBadGateway, "agent call failed")
	}
}
