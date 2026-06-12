// Package handlers — vnet.go
//
// VNetHandler implements the /v1/vnets endpoints.
//
// Mirrors the VMHandler pattern exactly:
//   - struct holds repo + provider + log
//   - constructor NewVNetHandler
//   - Create: validate → quota → DB insert PENDING → async goroutine → 202
//   - Get/List: tenant-isolated reads → 200
//   - Delete: DELETING in DB → async goroutine → 202
//
// The VNet is the top-level isolation boundary. KubeOVN sees it as a Vpc CRD;
// DC-API owns the address_space enforcement (KubeOVN Vpc has no CIDR field).
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

// VNetHandler handles all /v1/vnets endpoints.
type VNetHandler struct {
	repo     *db.Repository
	provider providers.NetworkProvider
	nat      providers.VPCNATProvisioner // nil = SNAT disabled (e.g. in tests)
	dns      providers.VPCDNSProvisioner // nil = F20 DNS disabled (e.g. in tests)
	log      zerolog.Logger
}

// NewVNetHandler creates a VNetHandler with injected dependencies.
// nat and dns may be nil — when nil, the respective provisioning is skipped.
func NewVNetHandler(repo *db.Repository, provider providers.NetworkProvider, nat providers.VPCNATProvisioner, dns providers.VPCDNSProvisioner, log zerolog.Logger) *VNetHandler {
	return &VNetHandler{repo: repo, provider: provider, nat: nat, dns: dns, log: log}
}

// ── DTOs ──────────────────────────────────────────────────────────────────────

type createVNetRequest struct {
	Name         string   `json:"name"`
	AddressSpace []string `json:"address_space"`
	Region       string   `json:"region"`
	Description  string   `json:"description"`
}

func (req *createVNetRequest) validate(reserved []ReservedCIDR) error {
	if err := validateResourceName(req.Name); err != nil {
		return err
	}
	if len(req.AddressSpace) == 0 {
		return fmt.Errorf("address_space is required; provide at least one RFC1918 CIDR")
	}
	if len(req.AddressSpace) > 5 {
		return fmt.Errorf("address_space may contain at most 5 CIDRs in M2")
	}
	if req.Region == "" {
		return fmt.Errorf("region is required")
	}
	if len(req.Description) > 256 {
		return fmt.Errorf("description must be 256 characters or fewer")
	}
	for _, cidr := range req.AddressSpace {
		if err := validateRFC1918CIDR(cidr, 8, 28); err != nil {
			return err
		}
		if err := checkNotReserved(cidr, reserved); err != nil {
			return err
		}
	}
	return nil
}

// vnetResponse is the JSON representation of a VNet resource.
// Matches the shape in §4 of the design doc.
type vnetResponse struct {
	ID           string   `json:"id"`
	TenantID     string   `json:"tenant_id"`
	Name         string   `json:"name"`
	Region       string   `json:"region"`
	AddressSpace []string `json:"address_space"`
	Description  string   `json:"description,omitempty"`
	Status       string   `json:"status"`
	ProviderType string   `json:"provider_type"`
	Message      string   `json:"message,omitempty"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
}

func vnetToResponse(v *models.VNet) vnetResponse {
	return vnetResponse{
		ID:           v.ID.String(),
		TenantID:     v.TenantID,
		Name:         v.Name,
		Region:       v.Region,
		AddressSpace: v.AddressSpace,
		Description:  v.Description,
		Status:       string(v.Status),
		ProviderType: v.ProviderType,
		Message:      v.Message,
		CreatedAt:    v.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    v.UpdatedAt.Format(time.RFC3339),
	}
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// Create handles POST /v1/vnets.
// Async: inserts PENDING row, spawns goroutine to call KubeOVN, returns 202.
func (h *VNetHandler) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	if !requireAction(w, r, h.repo, rbac.ActionVNetWrite) {
		return
	}
	userID, _ := middleware.UserFromContext(r.Context())

	var req createVNetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	// Validate region and load reserved CIDRs from the DB.
	rawCIDRs, err := h.repo.GetRegionReservedCIDRs(r.Context(), req.Region)
	if err != nil {
		h.log.Error().Err(err).Str("region", req.Region).Msg("get region reserved CIDRs")
		writeError(w, http.StatusInternalServerError, "failed to validate region")
		return
	}
	if rawCIDRs == nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("region %q is not a known region", req.Region))
		return
	}
	reserved, err := ParseReservedCIDRs(rawCIDRs)
	if err != nil {
		h.log.Error().Err(err).Msg("parse reserved CIDRs")
		writeError(w, http.StatusInternalServerError, "reserved CIDR configuration error")
		return
	}

	if err := req.validate(reserved); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}
	_, projectUUID, okP := lookupProjectUUID(w, r)
	if !okP {
		return
	}
	projectID, _ := middleware.ProjectFromContext(r.Context())

	// Quota check — use project-level quota when available.
	pq, _ := h.repo.GetProjectQuota(r.Context(), projectUUID)
	count, err := h.repo.CountVNetsByProject(r.Context(), projectUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "quota check failed")
		return
	}
	if pq != nil && count >= pq.MaxVNets {
		writeError(w, http.StatusForbidden,
			fmt.Sprintf("VNet quota exceeded: %d/%d VNets in use for this project", count, pq.MaxVNets))
		return
	}

	// Insert PENDING row.
	vnet := &models.VNet{
		TenantID:     tenantID,
		TenantUUID:   tenantUUID,
		ProjectID:    projectID,
		ProjectUUID:  projectUUID,
		Name:         req.Name,
		Region:       req.Region,
		AddressSpace: req.AddressSpace,
		Description:  req.Description,
		Status:       models.StatusPending,
		ProviderType: h.provider.Name(),
	}
	vnet, err = h.repo.CreateVNet(r.Context(), vnet)
	if err != nil {
		h.log.Error().Err(err).Str("tenant", tenantID).Msg("create vnet in DB")
		if strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "duplicate key") {
			writeError(w, http.StatusConflict, fmt.Sprintf("a VNet named %q already exists for this tenant", req.Name))
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to register VNet")
		return
	}


	go h.asyncProvisionVNet(vnet.ID, tenantID, projectID, userID, models.VNetSpec{
		Name:         req.Name,
		AddressSpace: req.AddressSpace,
		Region:       req.Region,
		Description:  req.Description,
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"resource": vnetToResponse(vnet),
		"note":     "VNet is being provisioned. Poll GET /v1/vnets/" + vnet.ID.String() + " for status.",
	})
}

// Get handles GET /v1/tenants/{tid}/projects/{pid}/vnets/{vnet_id}.
func (h *VNetHandler) Get(w http.ResponseWriter, r *http.Request) {
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}
	_, projectUUID, okP := lookupProjectUUID(w, r)
	if !okP {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "vnet_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid VNet ID")
		return
	}
	vnet, err := h.repo.GetVNet(r.Context(), id, tenantUUID, projectUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "VNet not found")
		return
	}
	writeJSON(w, http.StatusOK, vnetToResponse(vnet))
}

// List handles GET /v1/tenants/{tid}/projects/{pid}/vnets.
func (h *VNetHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}
	_, projectUUID, okP := lookupProjectUUID(w, r)
	if !okP {
		return
	}
	vnets, err := h.repo.ListVNetsByProject(r.Context(), tenantUUID, projectUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list VNets")
		return
	}
	resp := make([]vnetResponse, 0, len(vnets))
	for _, v := range vnets {
		resp = append(resp, vnetToResponse(v))
	}
	writeJSON(w, http.StatusOK, resp)
}

// Delete handles DELETE /v1/tenants/{tid}/projects/{pid}/vnets/{vnet_id}.
// Returns 409 if the VNet has active dependents (subnets, route tables, peerings).
func (h *VNetHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if _, ok := middleware.TenantFromContext(r.Context()); !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	if !requireAction(w, r, h.repo, rbac.ActionVNetDelete) {
		return
	}

	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}
	_, projectUUID, okP := lookupProjectUUID(w, r)
	if !okP {
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "vnet_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid VNet ID")
		return
	}
	vnet, err := h.repo.GetVNet(r.Context(), id, tenantUUID, projectUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "VNet not found")
		return
	}

	// Delete guard: reject if active subnets exist.
	subnets, err := h.repo.ListSubnetsByVNet(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check VNet dependencies")
		return
	}
	var activeSubnets []string
	for _, s := range subnets {
		if s.Status != models.StatusFailed {
			activeSubnets = append(activeSubnets, s.ID.String())
		}
	}
	if len(activeSubnets) > 0 {
		writeError(w, http.StatusConflict,
			fmt.Sprintf("VNet has active subnets; delete them first: %s", strings.Join(activeSubnets, ", ")))
		return
	}

	// Delete guard: reject if route tables exist.
	routeTables, err := h.repo.ListRouteTablesByVNet(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check VNet dependencies")
		return
	}
	if len(routeTables) > 0 {
		ids := make([]string, len(routeTables))
		for i, rt := range routeTables {
			ids[i] = rt.ID.String()
		}
		writeError(w, http.StatusConflict,
			fmt.Sprintf("VNet has route tables; delete them first: %s", strings.Join(ids, ", ")))
		return
	}

	// Delete guard: reject if active peerings exist.
	peerings, err := h.repo.ListPeeringsByVNet(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check VNet dependencies")
		return
	}
	if len(peerings) > 0 {
		ids := make([]string, len(peerings))
		for i, p := range peerings {
			ids[i] = p.ID.String()
		}
		writeError(w, http.StatusConflict,
			fmt.Sprintf("VNet has active peerings; delete them first: %s", strings.Join(ids, ", ")))
		return
	}

	// Mark DELETING.
	if err := h.repo.UpdateVNetStatus(r.Context(), id, models.StatusDeleting, "deletion requested", ""); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update VNet status")
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		if vnet.BackendUID == "" {
			_ = h.repo.DeleteVNet(ctx, id)
			return
		}

		// F15: remove NAT resources BEFORE deleting the VPC.
		// Doing it after means the VPC controller may have already torn down
		// dependent objects, leaving orphaned NAT resources or controller confusion.
		// No external-IP release needed — KubeOVN owns the IP and frees it when
		// IptablesEIP is deleted.
		if h.nat != nil {
			if natErr := h.nat.DeleteVpcNAT(ctx, vnet.BackendUID); natErr != nil {
				h.log.Warn().Err(natErr).Str("vpc", vnet.BackendUID).Msg("kubeovn DeleteVpcNAT failed; proceeding with VPC delete anyway")
			}
		}

		// F20: remove the per-VPC CoreDNS Deployment BEFORE deleting the VPC.
		// The Multus secondary NIC is released when the pod terminates; no
		// separate cleanup is needed for the IP pin.
		if h.dns != nil {
			if dnsErr := h.dns.DeleteVpcDNS(ctx, vnet.BackendUID); dnsErr != nil {
				h.log.Warn().Err(dnsErr).Str("vpc", vnet.BackendUID).Msg("kubeovn DeleteVpcDNS failed; proceeding with VPC delete anyway")
			}
		}

		if err := h.provider.DeleteVNet(ctx, vnet.BackendUID); err != nil {
			h.log.Error().Err(err).Str("backend_uid", vnet.BackendUID).Msg("kubeovn DeleteVNet failed")
			_ = h.repo.UpdateVNetStatus(ctx, id, models.StatusFailed, "deletion failed: "+err.Error(), "")
			return
		}
		_ = h.repo.DeleteVNet(ctx, id)
	}()

	w.WriteHeader(http.StatusAccepted)
}

// ── Async Provisioner ────────────────────────────────────────────────────────

func (h *VNetHandler) asyncProvisionVNet(resourceID uuid.UUID, tenantID, projectID, userID string, spec models.VNetSpec) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	fail := func(msg string, err error) {
		h.log.Error().Err(err).Str("vnet", spec.Name).Msg(msg)
		_ = h.repo.UpdateVNetStatus(ctx, resourceID, models.StatusFailed,
			msg+": "+err.Error(), "")
	}

	// ── 1. Create KubeOVN VPC ─────────────────────────────────────────────────
	providerRes, err := h.provider.CreateVNet(ctx, tenantID, projectID, spec)
	if err != nil {
		fail("kubeovn CreateVNet failed", err)
		return
	}
	vpcName := providerRes.BackendUID

	// VPC SNAT is provisioned by the subnet handler when the first subnet on
	// this VPC reaches ACTIVE — not here. VNet creation alone has no subnet
	// to route to, so there's nothing for the NAT chain to wire up yet.

	_ = h.repo.UpdateVNetStatus(ctx, resourceID, models.StatusActive,
		"VNet provisioned", vpcName)
}
