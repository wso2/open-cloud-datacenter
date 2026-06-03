// Package handlers — route_table.go
//
// RouteTableHandler implements /v1/vnets/{vnet_id}/route-tables endpoints.
//
// Route tables are SYNCHRONOUS (201/200/204) per §6 of the design doc because
// the KubeOVN operation is a single PATCH to the Vpc CRD that completes in
// under 2 seconds.
//
// Two-step Create+Update orchestration (per "Driver implementation notes"):
//  1. Insert DB row (gets UUID for the composite backendUID).
//  2. Call provider.CreateRouteTable → returns VNet backendUID.
//  3. Immediately call provider.UpdateRouteTableRoutes("<vnetUID>/<rtUUID>", routes).
//  All three happen synchronously in the handler (no background goroutine).
//
// Association returns a warning because per-subnet routing is informational
// only in M2 (all routes apply at the VPC level per §13 Decision 3).
package handlers

import (
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

// RouteTableHandler handles all /v1/vnets/{vnet_id}/route-tables endpoints.
type RouteTableHandler struct {
	repo     *db.Repository
	provider providers.NetworkProvider
	log      zerolog.Logger
}

// NewRouteTableHandler creates a RouteTableHandler with injected dependencies.
func NewRouteTableHandler(repo *db.Repository, provider providers.NetworkProvider, log zerolog.Logger) *RouteTableHandler {
	return &RouteTableHandler{repo: repo, provider: provider, log: log}
}

// ── DTOs ──────────────────────────────────────────────────────────────────────

// routeRuleDTO mirrors models.RouteRule but with json tags for request decoding.
type routeRuleDTO struct {
	Name            string `json:"name"`
	DestinationCIDR string `json:"destination_cidr"`
	NextHopType     string `json:"next_hop_type"`
	NextHopIP       string `json:"next_hop_ip,omitempty"`
}

type createRouteTableRequest struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Routes      []routeRuleDTO `json:"routes"`
}

type routeTableResponse struct {
	ID           string                  `json:"id"`
	VNetID       string                  `json:"vnet_id"`
	TenantID     string                  `json:"tenant_id"`
	Name         string                  `json:"name"`
	Description  string                  `json:"description,omitempty"`
	Routes       []models.RouteRule      `json:"routes"`
	Status       string                  `json:"status"`
	ProviderType string                  `json:"provider_type"`
	CreatedAt    string                  `json:"created_at"`
	UpdatedAt    string                  `json:"updated_at"`
}

func rtToResponse(rt *models.RouteTable) routeTableResponse {
	routes := rt.Routes
	if routes == nil {
		routes = []models.RouteRule{}
	}
	return routeTableResponse{
		ID:           rt.ID.String(),
		VNetID:       rt.VNetID.String(),
		TenantID:     rt.TenantID,
		Name:         rt.Name,
		Description:  rt.Description,
		Routes:       routes,
		Status:       string(rt.Status),
		ProviderType: rt.ProviderType,
		CreatedAt:    rt.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    rt.UpdatedAt.Format(time.RFC3339),
	}
}

func dtosToRouteRules(dtos []routeRuleDTO) []models.RouteRule {
	rules := make([]models.RouteRule, len(dtos))
	for i, d := range dtos {
		rules[i] = models.RouteRule{
			Name:            d.Name,
			DestinationCIDR: d.DestinationCIDR,
			NextHopType:     d.NextHopType,
			NextHopIP:       d.NextHopIP,
		}
	}
	return rules
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// requireActiveVNet fetches the VNet, enforces tenant isolation by UUID, and
// returns 409 if not ACTIVE. Phase 6a: uses tenantUUID (immutable) for the DB
// WHERE clause instead of the mutable slug.
func (h *RouteTableHandler) requireActiveVNet(w http.ResponseWriter, r *http.Request, tenantUUID, projectUUID uuid.UUID) (*models.VNet, bool) {
	vnetID, err := uuid.Parse(chi.URLParam(r, "vnet_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid VNet ID")
		return nil, false
	}
	vnet, err := h.repo.GetVNet(r.Context(), vnetID, tenantUUID, projectUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "VNet not found")
		return nil, false
	}
	if vnet.Status != models.StatusActive {
		writeError(w, http.StatusConflict,
			fmt.Sprintf("VNet is %s — route tables can only be created on ACTIVE VNets", vnet.Status))
		return nil, false
	}
	return vnet, true
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// Create handles POST /v1/vnets/{vnet_id}/route-tables.
// Synchronous: returns 201 Created.
func (h *RouteTableHandler) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}
	projectUUID, ok := middleware.ProjectUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "no project UUID in context")
		return
	}
	if !requireAction(w, r, h.repo, rbac.ActionRouteTableWrite) {
		return
	}

	vnet, ok := h.requireActiveVNet(w, r, tenantUUID, projectUUID)
	if !ok {
		return
	}

	var req createRouteTableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if err := validateResourceName(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Load subnet CIDRs in this VNet for next_hop_ip validation.
	subnetCIDRs, err := h.repo.ListActiveSubnetCIDRsByVNet(r.Context(), vnet.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to validate routes")
		return
	}

	for _, route := range req.Routes {
		if err := validateRouteRule(route, subnetCIDRs); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	// Insert DB row — this gives us the UUID for the composite backendUID.
	projectID, projectUUID, _ := lookupProjectUUID(w, r)
	rt := &models.RouteTable{
		VNetID:       vnet.ID,
		TenantID:     tenantID,
		TenantUUID:   tenantUUID,
		ProjectID:    projectID,
		ProjectUUID:  projectUUID,
		Name:         req.Name,
		Description:  req.Description,
		Routes:       dtosToRouteRules(req.Routes),
		Status:       models.StatusActive,
		ProviderType: h.provider.Name(),
	}
	rt, err = h.repo.CreateRouteTable(r.Context(), rt)
	if err != nil {
		h.log.Error().Err(err).Str("tenant", tenantID).Msg("create route table in DB")
		if strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "duplicate key") {
			writeError(w, http.StatusConflict, fmt.Sprintf("a route table named %q already exists in this VNet", req.Name))
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to register route table")
		return
	}

	// Two-step orchestration per driver notes:
	// Step 1: CreateRouteTable → returns VNet backendUID (routes not yet written).
	rtSpec := models.RouteTableSpec{
		Name:        req.Name,
		Description: req.Description,
		Routes:      dtosToRouteRules(req.Routes),
	}
	providerRes, err := h.provider.CreateRouteTable(r.Context(), vnet.BackendUID, rtSpec)
	if err != nil {
		h.log.Error().Err(err).Str("route_table", req.Name).Msg("kubeovn CreateRouteTable failed")
		_ = h.repo.DeleteRouteTable(r.Context(), rt.ID)
		writeError(w, http.StatusInternalServerError, "failed to create route table in KubeOVN: "+err.Error())
		return
	}

	// Composite backendUID: "<vnetUID>/<rtUUID>"
	compositeUID := providerRes.BackendUID + "/" + rt.ID.String()

	// Step 2: UpdateRouteTableRoutes to write the actual route entries.
	if len(req.Routes) > 0 {
		if err := h.provider.UpdateRouteTableRoutes(r.Context(), compositeUID, dtosToRouteRules(req.Routes)); err != nil {
			h.log.Error().Err(err).Str("route_table", req.Name).Msg("kubeovn UpdateRouteTableRoutes failed")
			// Persist the RT but mark failed so the operator can retry.
			_ = h.repo.UpdateRouteTableRoutes(r.Context(), rt.ID, []models.RouteRule{}, "")
			writeError(w, http.StatusInternalServerError, "route table created but route entries could not be applied: "+err.Error())
			return
		}
	}

	// Persist the composite backendUID and routes.
	_ = h.repo.UpdateRouteTableRoutes(r.Context(), rt.ID, dtosToRouteRules(req.Routes), compositeUID)

	// Re-read to pick up updated_at.
	rt, _ = h.repo.GetRouteTable(r.Context(), rt.ID)
	writeJSON(w, http.StatusCreated, rtToResponse(rt))
}

// Get handles GET /v1/vnets/{vnet_id}/route-tables/{rt_id}.
func (h *RouteTableHandler) Get(w http.ResponseWriter, r *http.Request) {
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}
	projectUUID, ok := middleware.ProjectUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "no project UUID in context")
		return
	}
	vnetID, err := uuid.Parse(chi.URLParam(r, "vnet_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid VNet ID")
		return
	}
	rtID, err := uuid.Parse(chi.URLParam(r, "rt_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid route table ID")
		return
	}

	_, err = h.repo.GetVNet(r.Context(), vnetID, tenantUUID, projectUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "VNet not found")
		return
	}

	rt, err := h.repo.GetRouteTable(r.Context(), rtID)
	if err != nil || rt.VNetID != vnetID {
		writeError(w, http.StatusNotFound, "route table not found")
		return
	}
	writeJSON(w, http.StatusOK, rtToResponse(rt))
}

// List handles GET /v1/vnets/{vnet_id}/route-tables.
func (h *RouteTableHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}
	projectUUID, ok := middleware.ProjectUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "no project UUID in context")
		return
	}
	vnetID, err := uuid.Parse(chi.URLParam(r, "vnet_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid VNet ID")
		return
	}

	_, err = h.repo.GetVNet(r.Context(), vnetID, tenantUUID, projectUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "VNet not found")
		return
	}

	rts, err := h.repo.ListRouteTablesByVNet(r.Context(), vnetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list route tables")
		return
	}
	resp := make([]routeTableResponse, 0, len(rts))
	for _, rt := range rts {
		resp = append(resp, rtToResponse(rt))
	}
	writeJSON(w, http.StatusOK, resp)
}

// UpdateRoutes handles PUT /v1/vnets/{vnet_id}/route-tables/{rt_id}.
// Synchronous: replaces all route entries and returns 200.
func (h *RouteTableHandler) UpdateRoutes(w http.ResponseWriter, r *http.Request) {
	_, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}
	projectUUID, ok := middleware.ProjectUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "no project UUID in context")
		return
	}
	if !requireAction(w, r, h.repo, rbac.ActionRouteTableWrite) {
		return
	}
	vnetID, err := uuid.Parse(chi.URLParam(r, "vnet_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid VNet ID")
		return
	}
	rtID, err := uuid.Parse(chi.URLParam(r, "rt_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid route table ID")
		return
	}

	_, err = h.repo.GetVNet(r.Context(), vnetID, tenantUUID, projectUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "VNet not found")
		return
	}

	rt, err := h.repo.GetRouteTable(r.Context(), rtID)
	if err != nil || rt.VNetID != vnetID {
		writeError(w, http.StatusNotFound, "route table not found")
		return
	}

	var req struct {
		Routes []routeRuleDTO `json:"routes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	subnetCIDRs, _ := h.repo.ListActiveSubnetCIDRsByVNet(r.Context(), vnetID)
	for _, route := range req.Routes {
		if err := validateRouteRule(route, subnetCIDRs); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	// Call driver to update routes on the VPC CRD.
	if err := h.provider.UpdateRouteTableRoutes(r.Context(), rt.BackendUID, dtosToRouteRules(req.Routes)); err != nil {
		h.log.Error().Err(err).Str("rt_id", rtID.String()).Msg("kubeovn UpdateRouteTableRoutes failed")
		writeError(w, http.StatusInternalServerError, "failed to update routes: "+err.Error())
		return
	}

	// Persist updated routes in DB.
	if err := h.repo.UpdateRouteTableRoutes(r.Context(), rtID, dtosToRouteRules(req.Routes), ""); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist route updates")
		return
	}

	rt, _ = h.repo.GetRouteTable(r.Context(), rtID)
	writeJSON(w, http.StatusOK, rtToResponse(rt))
}

// Delete handles DELETE /v1/vnets/{vnet_id}/route-tables/{rt_id}.
// Synchronous: returns 204 No Content.
func (h *RouteTableHandler) Delete(w http.ResponseWriter, r *http.Request) {
	_, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}
	projectUUID, ok := middleware.ProjectUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "no project UUID in context")
		return
	}
	if !requireAction(w, r, h.repo, rbac.ActionRouteTableDelete) {
		return
	}
	vnetID, err := uuid.Parse(chi.URLParam(r, "vnet_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid VNet ID")
		return
	}
	rtID, err := uuid.Parse(chi.URLParam(r, "rt_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid route table ID")
		return
	}

	_, err = h.repo.GetVNet(r.Context(), vnetID, tenantUUID, projectUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "VNet not found")
		return
	}

	rt, err := h.repo.GetRouteTable(r.Context(), rtID)
	if err != nil || rt.VNetID != vnetID {
		writeError(w, http.StatusNotFound, "route table not found")
		return
	}

	if rt.BackendUID != "" {
		if err := h.provider.DeleteRouteTable(r.Context(), rt.BackendUID); err != nil {
			h.log.Error().Err(err).Str("backend_uid", rt.BackendUID).Msg("kubeovn DeleteRouteTable failed")
			writeError(w, http.StatusInternalServerError, "failed to remove routes from KubeOVN: "+err.Error())
			return
		}
	}

	if err := h.repo.DeleteRouteTable(r.Context(), rtID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete route table")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// Associate handles POST /v1/vnets/{vnet_id}/route-tables/{rt_id}/associations.
// Returns 201 with a warning that per-subnet routing is informational only in M2.
func (h *RouteTableHandler) Associate(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}
	projectUUID, ok := middleware.ProjectUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "no project UUID in context")
		return
	}
	if !requireAction(w, r, h.repo, rbac.ActionRouteTableWrite) {
		return
	}
	vnetID, err := uuid.Parse(chi.URLParam(r, "vnet_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid VNet ID")
		return
	}
	rtID, err := uuid.Parse(chi.URLParam(r, "rt_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid route table ID")
		return
	}

	_, err = h.repo.GetVNet(r.Context(), vnetID, tenantUUID, projectUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "VNet not found")
		return
	}

	rt, err := h.repo.GetRouteTable(r.Context(), rtID)
	if err != nil || rt.VNetID != vnetID {
		writeError(w, http.StatusNotFound, "route table not found")
		return
	}

	var req struct {
		SubnetID string `json:"subnet_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	subnetID, err := uuid.Parse(req.SubnetID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid subnet_id")
		return
	}

	// Verify subnet belongs to the same VNet.
	subnet, err := h.repo.GetSubnet(r.Context(), subnetID)
	if err != nil || subnet.VNetID != vnetID {
		writeError(w, http.StatusBadRequest, "subnet must belong to the same VNet as the route table")
		return
	}

	// Call driver for informational association (no-op in M2 at the data plane).
	_ = h.provider.AssociateRouteTable(r.Context(), rt.BackendUID, subnet.BackendUID)

	assoc, err := h.repo.CreateRouteTableAssociation(r.Context(), &db.RouteTableAssociation{
		RouteTableID: rtID,
		SubnetID:     subnetID,
		TenantID:     tenantID,
	})
	if err != nil {
		if strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "duplicate key") {
			writeError(w, http.StatusConflict, "this subnet already has a route table associated — disassociate the existing one first")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create association")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":              assoc.ID.String(),
		"route_table_id":  rtID.String(),
		"subnet_id":       subnetID.String(),
		"created_at":      assoc.CreatedAt.Format(time.RFC3339),
		"warning":         "per-subnet route differentiation is informational in M2 — all routes apply at the VPC level. Slated for M2.5 (OVN policy routes).",
	})
}

// Disassociate handles DELETE /v1/vnets/{vnet_id}/route-tables/{rt_id}/associations/{assoc_id}.
func (h *RouteTableHandler) Disassociate(w http.ResponseWriter, r *http.Request) {
	_, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}
	projectUUID, ok := middleware.ProjectUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "no project UUID in context")
		return
	}
	if !requireAction(w, r, h.repo, rbac.ActionRouteTableWrite) {
		return
	}
	vnetID, err := uuid.Parse(chi.URLParam(r, "vnet_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid VNet ID")
		return
	}
	rtID, err := uuid.Parse(chi.URLParam(r, "rt_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid route table ID")
		return
	}
	assocID, err := uuid.Parse(chi.URLParam(r, "assoc_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid association ID")
		return
	}

	_, err = h.repo.GetVNet(r.Context(), vnetID, tenantUUID, projectUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "VNet not found")
		return
	}

	rt, err := h.repo.GetRouteTable(r.Context(), rtID)
	if err != nil || rt.VNetID != vnetID {
		writeError(w, http.StatusNotFound, "route table not found")
		return
	}

	assoc, err := h.repo.GetRouteTableAssociation(r.Context(), assocID)
	if err != nil || assoc.RouteTableID != rtID {
		writeError(w, http.StatusNotFound, "association not found")
		return
	}

	subnet, _ := h.repo.GetSubnet(r.Context(), assoc.SubnetID)
	if subnet != nil {
		_ = h.provider.DisassociateRouteTable(r.Context(), rt.BackendUID, subnet.BackendUID)
	}

	if err := h.repo.DeleteRouteTableAssociation(r.Context(), assocID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove association")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
