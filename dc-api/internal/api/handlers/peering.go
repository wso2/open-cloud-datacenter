// Package handlers — peering.go
//
// PeeringHandler implements /v1/vnets/{vnet_id}/peerings endpoints.
//
// Peerings are async (202 on create/delete) because KubeOVN must create the
// VpcPeering CRD and patch reciprocal staticRoutes on both Vpc CRDs.
//
// Key constraints (§8, §14):
//   - peer_vnet_id must belong to the same tenant (cross-tenant → 404)
//   - Both VNets must be ACTIVE
//   - Address spaces must not overlap
//   - Both VNets must be in the same region (multi-region peering deferred)
//   - allow_forwarded_traffic is accepted but no-op in M2; response includes a warning
//
// BackendUID: The KubeOVN VpcPeering CRD name returned by provider.CreatePeering.
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/wso2/dc-api/internal/api/middleware"
	"github.com/wso2/dc-api/internal/db"
	"github.com/wso2/dc-api/internal/models"
	"github.com/wso2/dc-api/internal/providers"
	"github.com/wso2/dc-api/internal/rbac"
)

// PeeringHandler handles all /v1/vnets/{vnet_id}/peerings endpoints.
type PeeringHandler struct {
	repo     *db.Repository
	provider providers.NetworkProvider
	log      zerolog.Logger
}

// NewPeeringHandler creates a PeeringHandler with injected dependencies.
func NewPeeringHandler(repo *db.Repository, provider providers.NetworkProvider, log zerolog.Logger) *PeeringHandler {
	return &PeeringHandler{repo: repo, provider: provider, log: log}
}

// ── DTOs ──────────────────────────────────────────────────────────────────────

type createPeeringRequest struct {
	Name                  string `json:"name"`
	PeerVNetID            string `json:"peer_vnet_id"`
	AllowForwardedTraffic bool   `json:"allow_forwarded_traffic"`
}

type peeringResponse struct {
	ID                    string `json:"id"`
	VNetID                string `json:"vnet_id"`
	PeerVNetID            string `json:"peer_vnet_id"`
	TenantID              string `json:"tenant_id"`
	Name                  string `json:"name"`
	AllowForwardedTraffic bool   `json:"allow_forwarded_traffic"`
	Status                string `json:"status"`
	ProviderType          string `json:"provider_type"`
	Message               string `json:"message,omitempty"`
	CreatedAt             string `json:"created_at"`
	UpdatedAt             string `json:"updated_at"`
	// Warning is populated when allow_forwarded_traffic = true (no-op in M2).
	Warning string `json:"warning,omitempty"`
}

func peeringToResponse(p *models.Peering) peeringResponse {
	resp := peeringResponse{
		ID:                    p.ID.String(),
		VNetID:                p.VNetID.String(),
		PeerVNetID:            p.PeerVNetID.String(),
		TenantID:              p.TenantID,
		Name:                  p.Name,
		AllowForwardedTraffic: p.AllowForwardedTraffic,
		Status:                string(p.Status),
		ProviderType:          p.ProviderType,
		CreatedAt:             p.CreatedAt.Format(time.RFC3339),
		UpdatedAt:             p.UpdatedAt.Format(time.RFC3339),
	}
	if p.AllowForwardedTraffic {
		resp.Warning = "allow_forwarded_traffic is accepted but not yet enforced — slated for M2.5"
	}
	return resp
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// Create handles POST /v1/vnets/{vnet_id}/peerings.
// Async: inserts PENDING, spawns goroutine, returns 202.
func (h *PeeringHandler) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	if !requireAction(w, r, h.repo, rbac.ActionPeeringWrite) {
		return
	}
	userID, _ := middleware.UserFromContext(r.Context())

	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}

	vnetID, err := uuid.Parse(chi.URLParam(r, "vnet_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid VNet ID")
		return
	}

	// Fetch initiating VNet.
	vnet, err := h.repo.GetVNetByTenant(r.Context(), vnetID, tenantUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "VNet not found")
		return
	}
	if vnet.Status != models.StatusActive {
		writeError(w, http.StatusConflict,
			fmt.Sprintf("VNet is %s — only ACTIVE VNets can participate in peerings", vnet.Status))
		return
	}

	var req createPeeringRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if err := validateResourceName(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.PeerVNetID == "" {
		writeError(w, http.StatusBadRequest, "peer_vnet_id is required")
		return
	}
	peerID, err := uuid.Parse(req.PeerVNetID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "peer_vnet_id is not a valid UUID")
		return
	}
	if peerID == vnetID {
		writeError(w, http.StatusBadRequest, "peer_vnet_id must be a different VNet")
		return
	}

	// Fetch peer VNet — must belong to the same tenant (§14 Decision 4).
	// Phase 6a: tenantUUID enforces isolation; cross-tenant VNet IDs return 404
	// to prevent enumeration.
	peerVNet, err := h.repo.GetVNetByTenant(r.Context(), peerID, tenantUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "peer VNet not found")
		return
	}
	if peerVNet.Status != models.StatusActive {
		writeError(w, http.StatusConflict,
			fmt.Sprintf("peer VNet is %s — only ACTIVE VNets can participate in peerings", peerVNet.Status))
		return
	}

	// Same region check (multi-region peering deferred).
	if vnet.Region != peerVNet.Region {
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("cross-region peering is not supported in M2 (VNet region: %s, peer VNet region: %s)",
				vnet.Region, peerVNet.Region))
		return
	}

	// Address space overlap check.
	for _, a := range vnet.AddressSpace {
		for _, b := range peerVNet.AddressSpace {
			if cidrsOverlap(a, b) {
				writeError(w, http.StatusBadRequest,
					fmt.Sprintf("VNet address spaces overlap: %s (%s) and %s (%s)",
						a, vnet.Name, b, peerVNet.Name))
				return
			}
		}
	}

	// Duplicate peering check (either direction).
	exists, err := h.repo.PeeringExistsBetween(r.Context(), vnetID, peerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check existing peerings")
		return
	}
	if exists {
		writeError(w, http.StatusConflict, "a peering already exists between these two VNets")
		return
	}

	// Insert PENDING row.
	projectID, projectUUID, _ := lookupProjectUUID(w, r)
	peering := &models.Peering{
		VNetID:                vnetID,
		PeerVNetID:            peerID,
		TenantID:              tenantID,
		TenantUUID:            tenantUUID,
		ProjectID:             projectID,
		ProjectUUID:           projectUUID,
		Name:                  req.Name,
		AllowForwardedTraffic: req.AllowForwardedTraffic,
		Status:                models.StatusPending,
		ProviderType:          h.provider.Name(),
	}
	peering, err = h.repo.CreatePeering(r.Context(), peering)
	if err != nil {
		h.log.Error().Err(err).Str("tenant", tenantID).Msg("create peering in DB")
		if strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "duplicate key") {
			writeError(w, http.StatusConflict, fmt.Sprintf("a peering named %q already exists for this VNet", req.Name))
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to register peering")
		return
	}

	_ = h.repo.AppendAuditEvent(r.Context(), &models.AuditEvent{
		ResourceID: peering.ID,
		ActorID:    userID,
		Action:     "CREATE",
		ToStatus:   models.StatusPending,
		Message:    fmt.Sprintf("vnet_id=%s peer_vnet_id=%s", vnetID, peerID),
	})

	// F6: allocate the transit /24 BEFORE the async goroutine fires. Doing it
	// inline lets us surface "pool exhausted" or DB errors as a synchronous
	// 5xx instead of a silent PENDING→FAILED. Idempotent: a retried create
	// returns the same CIDR.
	transitCIDR, err := h.repo.AllocateTransitCIDR(r.Context(), peering.ID)
	if err != nil {
		h.log.Error().Err(err).Str("peering", peering.ID.String()).Msg("allocate transit CIDR")
		writeError(w, http.StatusInternalServerError, "failed to allocate peering transit CIDR")
		return
	}

	spec := models.PeeringSpec{
		Name:                  req.Name,
		PeerVNetID:            peerID,
		AllowForwardedTraffic: req.AllowForwardedTraffic,
		AddressSpace:          vnet.AddressSpace,
		PeerAddressSpace:      peerVNet.AddressSpace,
		TransitCIDR:           transitCIDR,
	}
	go h.asyncProvisionPeering(peering.ID, tenantID, userID, vnet.BackendUID, peerVNet.BackendUID, spec)

	resp := peeringToResponse(peering)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"resource": resp,
		"note": fmt.Sprintf("Peering is being configured. Poll GET /v1/vnets/%s/peerings/%s for status.",
			vnetID, peering.ID),
	})
}

// Get handles GET /v1/vnets/{vnet_id}/peerings/{peering_id}.
func (h *PeeringHandler) Get(w http.ResponseWriter, r *http.Request) {
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}
	vnetID, err := uuid.Parse(chi.URLParam(r, "vnet_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid VNet ID")
		return
	}
	peeringID, err := uuid.Parse(chi.URLParam(r, "peering_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid peering ID")
		return
	}

	_, err = h.repo.GetVNetByTenant(r.Context(), vnetID, tenantUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "VNet not found")
		return
	}

	peering, err := h.repo.GetPeering(r.Context(), peeringID)
	if err != nil || peering.VNetID != vnetID {
		writeError(w, http.StatusNotFound, "peering not found")
		return
	}
	writeJSON(w, http.StatusOK, peeringToResponse(peering))
}

// List handles GET /v1/vnets/{vnet_id}/peerings.
func (h *PeeringHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}
	vnetID, err := uuid.Parse(chi.URLParam(r, "vnet_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid VNet ID")
		return
	}

	_, err = h.repo.GetVNetByTenant(r.Context(), vnetID, tenantUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "VNet not found")
		return
	}

	peerings, err := h.repo.ListPeeringsByVNet(r.Context(), vnetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list peerings")
		return
	}
	resp := make([]peeringResponse, 0, len(peerings))
	for _, p := range peerings {
		resp = append(resp, peeringToResponse(p))
	}
	writeJSON(w, http.StatusOK, resp)
}

// Delete handles DELETE /v1/vnets/{vnet_id}/peerings/{peering_id}.
// Async: marks DELETING, spawns goroutine, returns 202.
func (h *PeeringHandler) Delete(w http.ResponseWriter, r *http.Request) {
	_, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	if !requireAction(w, r, h.repo, rbac.ActionPeeringDelete) {
		return
	}
	userID, _ := middleware.UserFromContext(r.Context())

	vnetID, err := uuid.Parse(chi.URLParam(r, "vnet_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid VNet ID")
		return
	}
	peeringID, err := uuid.Parse(chi.URLParam(r, "peering_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid peering ID")
		return
	}

	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}

	vnet, err := h.repo.GetVNetByTenant(r.Context(), vnetID, tenantUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "VNet not found")
		return
	}

	peering, err := h.repo.GetPeering(r.Context(), peeringID)
	if err != nil || peering.VNetID != vnetID {
		writeError(w, http.StatusNotFound, "peering not found")
		return
	}

	// Fetch both VNets' address-spaces so DeletePeering can identify which
	// staticRoutes to remove. Both VNets must still exist (they are not deleted
	// as part of a peering delete, only as part of a full VNet delete).
	// Use GetVNetInternal here — we need the peer regardless of who owns it.
	peerVNet, peerErr := h.repo.GetVNetInternal(r.Context(), peering.PeerVNetID)
	if peerErr != nil {
		// Not fatal — we can still remove the peering CRD entry; route cleanup
		// will be best-effort with empty slices (will remove nothing, which is safe
		// since those routes reference a VNet that may itself be gone).
		h.log.Warn().Err(peerErr).Str("peer_vnet_id", peering.PeerVNetID.String()).
			Msg("delete peering: could not fetch peer VNet; route cleanup will be skipped")
	}
	localCIDRs := vnet.AddressSpace // always valid — checked above
	var peerCIDRs []string
	if peerVNet != nil {
		peerCIDRs = peerVNet.AddressSpace
	}

	if err := h.repo.UpdatePeeringStatus(r.Context(), peeringID, models.StatusDeleting, "deletion requested", ""); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update peering status")
		return
	}
	_ = h.repo.AppendAuditEvent(r.Context(), &models.AuditEvent{
		ResourceID: peeringID, ActorID: userID, Action: "DELETE",
		FromStatus: peering.Status, ToStatus: models.StatusDeleting,
	})

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if peering.BackendUID == "" {
			// Nothing on the kubeovn side, just clear the DB rows. The
			// transit_cidrs row has ON DELETE CASCADE on peering_id so this
			// also frees the index for reuse.
			_ = h.repo.DeletePeering(ctx, peeringID)
			return
		}
		if err := h.provider.DeletePeering(ctx, peering.BackendUID, localCIDRs, peerCIDRs); err != nil {
			h.log.Error().Err(err).Str("backend_uid", peering.BackendUID).Msg("kubeovn DeletePeering failed")
			_ = h.repo.UpdatePeeringStatus(ctx, peeringID, models.StatusFailed, "deletion failed: "+err.Error(), "")
			return
		}
		// F6: explicit release before DeletePeering so the slot is freed even
		// if peerings.id has its CASCADE removed in some future schema change.
		// Idempotent — no row means no-op.
		if err := h.repo.ReleaseTransitCIDR(ctx, peeringID); err != nil {
			h.log.Warn().Err(err).Str("peering", peeringID.String()).
				Msg("release transit CIDR (non-fatal — CASCADE will clean up)")
		}
		_ = h.repo.DeletePeering(ctx, peeringID)
	}()

	w.WriteHeader(http.StatusAccepted)
}

// ── Async Provisioner ────────────────────────────────────────────────────────

func (h *PeeringHandler) asyncProvisionPeering(
	peeringID uuid.UUID, tenantID, userID, vnetUID, peerVNetUID string,
	spec models.PeeringSpec,
) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	providerRes, err := h.provider.CreatePeering(ctx, vnetUID, peerVNetUID, spec)
	if err != nil {
		h.log.Error().Err(err).Str("peering", spec.Name).Msg("kubeovn CreatePeering failed")
		_ = h.repo.UpdatePeeringStatus(ctx, peeringID, models.StatusFailed,
			"provisioning failed: "+err.Error(), "")
		_ = h.repo.AppendAuditEvent(ctx, &models.AuditEvent{
			ResourceID: peeringID, ActorID: userID, Action: "STATUS_CHANGE",
			FromStatus: models.StatusPending, ToStatus: models.StatusFailed, Message: err.Error(),
		})
		return
	}

	if err := h.repo.UpdatePeeringStatus(ctx, peeringID, models.StatusActive,
		"peering established", providerRes.BackendUID); err != nil {
		h.log.Error().Err(err).Str("peering_id", peeringID.String()).
			Str("backend_uid", providerRes.BackendUID).
			Msg("asyncProvisionPeering: UpdatePeeringStatus to ACTIVE failed")
		return
	}
	h.log.Info().Str("peering_id", peeringID.String()).
		Str("backend_uid", providerRes.BackendUID).
		Msg("asyncProvisionPeering: peering marked ACTIVE")
	_ = h.repo.AppendAuditEvent(ctx, &models.AuditEvent{
		ResourceID: peeringID, ActorID: userID, Action: "STATUS_CHANGE",
		FromStatus: models.StatusPending, ToStatus: models.StatusActive,
	})
}
