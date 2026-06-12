// Package handlers — dns_zone.go
//
// PrivateDnsZoneHandler implements /v1/vnets/{vnet_id}/dns-zones endpoints
// and the DNS record sub-resource under each zone.
//
// DNS zones are async (202 on create/delete).
// DNS records are synchronous (201/200/204) — they patch a ConfigMap in KubeOVN
// which CoreDNS picks up within seconds.
//
// Cross-tenant zone name collisions are explicitly ALLOWED (§14). The DB unique
// constraint is per (vnet_id, zone_name) only.
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

// PrivateDnsZoneHandler handles all /v1/vnets/{vnet_id}/dns-zones endpoints.
type PrivateDnsZoneHandler struct {
	repo     *db.Repository
	provider providers.NetworkProvider
	log      zerolog.Logger
}

// NewPrivateDnsZoneHandler creates a PrivateDnsZoneHandler with injected dependencies.
func NewPrivateDnsZoneHandler(repo *db.Repository, provider providers.NetworkProvider, log zerolog.Logger) *PrivateDnsZoneHandler {
	return &PrivateDnsZoneHandler{repo: repo, provider: provider, log: log}
}

// ── DTOs — DNS Zone ───────────────────────────────────────────────────────────

type createDNSZoneRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type dnsZoneResponse struct {
	ID           string `json:"id"`
	VNetID       string `json:"vnet_id"`
	TenantID     string `json:"tenant_id"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	Status       string `json:"status"`
	ProviderType string `json:"provider_type"`
	Message      string `json:"message,omitempty"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

func dnsZoneToResponse(z *models.PrivateDnsZone) dnsZoneResponse {
	return dnsZoneResponse{
		ID:           z.ID.String(),
		VNetID:       z.VNetID.String(),
		TenantID:     z.TenantID,
		Name:         z.ZoneName,
		Description:  z.Description,
		Status:       string(z.Status),
		ProviderType: z.ProviderType,
		CreatedAt:    z.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    z.UpdatedAt.Format(time.RFC3339),
	}
}

// ── DTOs — DNS Record ─────────────────────────────────────────────────────────

type createDNSRecordRequest struct {
	Name   string   `json:"name"`
	Type   string   `json:"type"`
	TTL    int      `json:"ttl"`
	Values []string `json:"values"`
}

type dnsRecordResponse struct {
	ID        string   `json:"id"`
	ZoneID    string   `json:"zone_id"`
	TenantID  string   `json:"tenant_id"`
	Type      string   `json:"type"`
	Name      string   `json:"name"`
	Values    []string `json:"values"`
	TTL       int      `json:"ttl"`
	CreatedAt string   `json:"created_at"`
}

func dnsRecordToResponse(rec *models.DnsRecord) dnsRecordResponse {
	return dnsRecordResponse{
		ID:        rec.ID.String(),
		ZoneID:    rec.ZoneID.String(),
		TenantID:  rec.TenantID,
		Type:      rec.RecordType,
		Name:      rec.Name,
		Values:    rec.Values,
		TTL:       rec.TTL,
		CreatedAt: rec.CreatedAt.Format(time.RFC3339),
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// requireActiveVNetForZone fetches + tenant-checks the VNet (via tenantUUID).
// Phase 6a: uses UUID (immutable) instead of slug for the DB WHERE clause.
func (h *PrivateDnsZoneHandler) requireActiveVNetForZone(w http.ResponseWriter, r *http.Request, tenantUUID, projectUUID uuid.UUID) (*models.VNet, bool) {
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
	return vnet, true
}

// requireActiveZone fetches + tenant-checks the DNS zone, enforcing ACTIVE status.
func (h *PrivateDnsZoneHandler) requireActiveZone(w http.ResponseWriter, r *http.Request, vnetID uuid.UUID) (*models.PrivateDnsZone, bool) {
	zoneID, err := uuid.Parse(chi.URLParam(r, "zone_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid DNS zone ID")
		return nil, false
	}
	zone, err := h.repo.GetDNSZone(r.Context(), zoneID)
	if err != nil || zone.VNetID != vnetID {
		writeError(w, http.StatusNotFound, "DNS zone not found")
		return nil, false
	}
	if zone.Status != models.StatusActive {
		writeError(w, http.StatusConflict,
			fmt.Sprintf("DNS zone is %s — wait for it to become ACTIVE before managing records", zone.Status))
		return nil, false
	}
	return zone, true
}

// ── Zone Handlers ─────────────────────────────────────────────────────────────

// CreateZone handles POST /v1/vnets/{vnet_id}/dns-zones.
// Async: returns 202.
func (h *PrivateDnsZoneHandler) CreateZone(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionDNSZoneWrite) {
		return
	}
	userID, _ := middleware.UserFromContext(r.Context())

	vnet, ok := h.requireActiveVNetForZone(w, r, tenantUUID, projectUUID)
	if !ok {
		return
	}
	// DNS zones can only be created on ACTIVE VNets.
	if vnet.Status != models.StatusActive {
		writeError(w, http.StatusConflict,
			fmt.Sprintf("VNet is %s — DNS zones require an ACTIVE VNet", vnet.Status))
		return
	}

	var req createDNSZoneRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if err := validateDNSZoneName(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.Description) > 256 {
		writeError(w, http.StatusBadRequest, "description must be 256 characters or fewer")
		return
	}

	projectID, projectUUID, _ := lookupProjectUUID(w, r)
	zone := &models.PrivateDnsZone{
		VNetID:       vnet.ID,
		TenantID:     tenantID,
		TenantUUID:   tenantUUID,
		ProjectID:    projectID,
		ProjectUUID:  projectUUID,
		ZoneName:     req.Name,
		Description:  req.Description,
		Status:       models.StatusPending,
		ProviderType: h.provider.Name(),
	}
	zone, err := h.repo.CreateDNSZone(r.Context(), zone)
	if err != nil {
		h.log.Error().Err(err).Str("tenant", tenantID).Msg("create dns zone in DB")
		if strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "duplicate key") {
			writeError(w, http.StatusConflict,
				fmt.Sprintf("a DNS zone named %q already exists in this VNet", req.Name))
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to register DNS zone")
		return
	}


	go h.asyncProvisionZone(zone.ID, tenantID, userID, vnet.BackendUID, models.DnsZoneSpec{
		ZoneName:    req.Name,
		Description: req.Description,
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"resource": dnsZoneToResponse(zone),
		"note": fmt.Sprintf("DNS zone is being configured. Poll GET /v1/vnets/%s/dns-zones/%s for status.",
			vnet.ID, zone.ID),
	})
}

// GetZone handles GET /v1/vnets/{vnet_id}/dns-zones/{zone_id}.
func (h *PrivateDnsZoneHandler) GetZone(w http.ResponseWriter, r *http.Request) {
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
	vnet, ok := h.requireActiveVNetForZone(w, r, tenantUUID, projectUUID)
	if !ok {
		return
	}
	zoneID, err := uuid.Parse(chi.URLParam(r, "zone_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid DNS zone ID")
		return
	}
	zone, err := h.repo.GetDNSZone(r.Context(), zoneID)
	if err != nil || zone.VNetID != vnet.ID {
		writeError(w, http.StatusNotFound, "DNS zone not found")
		return
	}
	writeJSON(w, http.StatusOK, dnsZoneToResponse(zone))
}

// ListZones handles GET /v1/vnets/{vnet_id}/dns-zones.
func (h *PrivateDnsZoneHandler) ListZones(w http.ResponseWriter, r *http.Request) {
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
	vnet, ok := h.requireActiveVNetForZone(w, r, tenantUUID, projectUUID)
	if !ok {
		return
	}
	zones, err := h.repo.ListDNSZonesByVNet(r.Context(), vnet.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list DNS zones")
		return
	}
	resp := make([]dnsZoneResponse, 0, len(zones))
	for _, z := range zones {
		resp = append(resp, dnsZoneToResponse(z))
	}
	writeJSON(w, http.StatusOK, resp)
}

// DeleteZone handles DELETE /v1/vnets/{vnet_id}/dns-zones/{zone_id}.
// Async: returns 202.
func (h *PrivateDnsZoneHandler) DeleteZone(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionDNSZoneDelete) {
		return
	}

	vnet, ok := h.requireActiveVNetForZone(w, r, tenantUUID, projectUUID)
	if !ok {
		return
	}
	zoneID, err := uuid.Parse(chi.URLParam(r, "zone_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid DNS zone ID")
		return
	}
	zone, err := h.repo.GetDNSZone(r.Context(), zoneID)
	if err != nil || zone.VNetID != vnet.ID {
		writeError(w, http.StatusNotFound, "DNS zone not found")
		return
	}

	if err := h.repo.UpdateDNSZoneStatus(r.Context(), zoneID, models.StatusDeleting, "deletion requested", ""); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update DNS zone status")
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if zone.BackendUID == "" {
			_ = h.repo.DeleteDNSZone(ctx, zoneID)
			return
		}
		if err := h.provider.DeletePrivateDnsZone(ctx, zone.BackendUID); err != nil {
			h.log.Error().Err(err).Str("backend_uid", zone.BackendUID).Msg("kubeovn DeletePrivateDnsZone failed")
			_ = h.repo.UpdateDNSZoneStatus(ctx, zoneID, models.StatusFailed, "deletion failed: "+err.Error(), "")
			return
		}
		_ = h.repo.DeleteDNSZone(ctx, zoneID)
	}()

	w.WriteHeader(http.StatusAccepted)
}

// ── Zone Async Provisioner ────────────────────────────────────────────────────

func (h *PrivateDnsZoneHandler) asyncProvisionZone(zoneID uuid.UUID, tenantID, userID, vnetUID string, spec models.DnsZoneSpec) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	providerRes, err := h.provider.CreatePrivateDnsZone(ctx, vnetUID, spec)
	if err != nil {
		h.log.Error().Err(err).Str("zone", spec.ZoneName).Msg("kubeovn CreatePrivateDnsZone failed")
		_ = h.repo.UpdateDNSZoneStatus(ctx, zoneID, models.StatusFailed,
			"provisioning failed: "+err.Error(), "")
		return
	}

	if err := h.repo.UpdateDNSZoneStatus(ctx, zoneID, models.StatusActive, "DNS zone provisioned", providerRes.BackendUID); err != nil {
		h.log.Error().Err(err).Str("zone_id", zoneID.String()).Str("backend_uid", providerRes.BackendUID).
			Msg("asyncProvisionZone: UpdateDNSZoneStatus to ACTIVE failed")
		return
	}
	h.log.Info().Str("zone_id", zoneID.String()).Str("backend_uid", providerRes.BackendUID).
		Msg("asyncProvisionZone: zone marked ACTIVE")
}

// ── Record Handlers ───────────────────────────────────────────────────────────

// UpsertRecord handles POST /v1/vnets/{vnet_id}/dns-zones/{zone_id}/records.
// Synchronous: returns 201 (create) or 200 (update, via upsert).
func (h *PrivateDnsZoneHandler) UpsertRecord(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionDNSZoneWrite) {
		return
	}

	vnet, ok := h.requireActiveVNetForZone(w, r, tenantUUID, projectUUID)
	if !ok {
		return
	}
	zone, ok := h.requireActiveZone(w, r, vnet.ID)
	if !ok {
		return
	}

	var req createDNSRecordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	ttl := req.TTL
	if ttl == 0 {
		ttl = 300 // default
	}
	if err := validateDNSRecord(req.Type, req.Name, req.Values, ttl); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	projectID, projectUUID, _ := lookupProjectUUID(w, r)
	rec := &models.DnsRecord{
		ZoneID:      zone.ID,
		TenantID:    tenantID,
		TenantUUID:  tenantUUID,
		ProjectID:   projectID,
		ProjectUUID: projectUUID,
		RecordType:  req.Type,
		Name:        req.Name,
		Values:      req.Values,
		TTL:         ttl,
	}
	rec, err := h.repo.UpsertDNSRecord(r.Context(), rec)
	if err != nil {
		h.log.Error().Err(err).Str("zone", zone.ZoneName).Msg("upsert dns record in DB")
		writeError(w, http.StatusInternalServerError, "failed to persist DNS record")
		return
	}

	// Apply to KubeOVN ConfigMap (synchronous per §12).
	if err := h.provider.UpsertDnsRecord(r.Context(), zone.BackendUID, *rec); err != nil {
		h.log.Error().Err(err).Str("zone", zone.ZoneName).Msg("kubeovn UpsertDnsRecord failed")
		// Record is in DB; KubeOVN update failed — log and surface error.
		writeError(w, http.StatusInternalServerError, "record persisted but KubeOVN update failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, dnsRecordToResponse(rec))
}

// GetRecord handles GET /v1/vnets/{vnet_id}/dns-zones/{zone_id}/records/{record_id}.
func (h *PrivateDnsZoneHandler) GetRecord(w http.ResponseWriter, r *http.Request) {
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
	vnet, ok := h.requireActiveVNetForZone(w, r, tenantUUID, projectUUID)
	if !ok {
		return
	}
	zoneID, err := uuid.Parse(chi.URLParam(r, "zone_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid DNS zone ID")
		return
	}
	zone, err := h.repo.GetDNSZone(r.Context(), zoneID)
	if err != nil || zone.VNetID != vnet.ID {
		writeError(w, http.StatusNotFound, "DNS zone not found")
		return
	}
	recordID, err := uuid.Parse(chi.URLParam(r, "record_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid record ID")
		return
	}
	rec, err := h.repo.GetDNSRecord(r.Context(), recordID)
	if err != nil || rec.ZoneID != zoneID {
		writeError(w, http.StatusNotFound, "DNS record not found")
		return
	}
	writeJSON(w, http.StatusOK, dnsRecordToResponse(rec))
}

// ListRecords handles GET /v1/vnets/{vnet_id}/dns-zones/{zone_id}/records.
func (h *PrivateDnsZoneHandler) ListRecords(w http.ResponseWriter, r *http.Request) {
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
	vnet, ok := h.requireActiveVNetForZone(w, r, tenantUUID, projectUUID)
	if !ok {
		return
	}
	zoneID, err := uuid.Parse(chi.URLParam(r, "zone_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid DNS zone ID")
		return
	}
	zone, err := h.repo.GetDNSZone(r.Context(), zoneID)
	if err != nil || zone.VNetID != vnet.ID {
		writeError(w, http.StatusNotFound, "DNS zone not found")
		return
	}

	records, err := h.repo.ListDNSRecordsByZone(r.Context(), zoneID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list DNS records")
		return
	}
	resp := make([]dnsRecordResponse, 0, len(records))
	for _, rec := range records {
		resp = append(resp, dnsRecordToResponse(rec))
	}
	writeJSON(w, http.StatusOK, resp)
}

// UpdateRecord handles PUT /v1/vnets/{vnet_id}/dns-zones/{zone_id}/records/{record_id}.
// Synchronous: replaces the record values/TTL and returns 200.
func (h *PrivateDnsZoneHandler) UpdateRecord(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionDNSZoneWrite) {
		return
	}
	vnet, ok := h.requireActiveVNetForZone(w, r, tenantUUID, projectUUID)
	if !ok {
		return
	}
	zone, ok := h.requireActiveZone(w, r, vnet.ID)
	if !ok {
		return
	}
	recordID, err := uuid.Parse(chi.URLParam(r, "record_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid record ID")
		return
	}
	existing, err := h.repo.GetDNSRecord(r.Context(), recordID)
	if err != nil || existing.ZoneID != zone.ID {
		writeError(w, http.StatusNotFound, "DNS record not found")
		return
	}

	var req createDNSRecordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	ttl := req.TTL
	if ttl == 0 {
		ttl = existing.TTL
	}
	// Allow updating values/TTL; name+type stay the same as the existing record.
	if err := validateDNSRecord(existing.RecordType, existing.Name, req.Values, ttl); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	updateProjectID, updateProjectUUID, _ := lookupProjectUUID(w, r)
	updated := &models.DnsRecord{
		ZoneID:      zone.ID,
		TenantID:    tenantID,
		TenantUUID:  tenantUUID,
		ProjectID:   updateProjectID,
		ProjectUUID: updateProjectUUID,
		RecordType:  existing.RecordType,
		Name:        existing.Name,
		Values:      req.Values,
		TTL:         ttl,
	}
	updated, err = h.repo.UpsertDNSRecord(r.Context(), updated)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update DNS record")
		return
	}

	if err := h.provider.UpsertDnsRecord(r.Context(), zone.BackendUID, *updated); err != nil {
		h.log.Error().Err(err).Str("record", existing.Name).Msg("kubeovn UpsertDnsRecord (update) failed")
		writeError(w, http.StatusInternalServerError, "record updated in DB but KubeOVN update failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, dnsRecordToResponse(updated))
}

// DeleteRecord handles DELETE /v1/vnets/{vnet_id}/dns-zones/{zone_id}/records/{record_id}.
// Synchronous: returns 204.
func (h *PrivateDnsZoneHandler) DeleteRecord(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionDNSZoneWrite) {
		return
	}
	vnet, ok := h.requireActiveVNetForZone(w, r, tenantUUID, projectUUID)
	if !ok {
		return
	}
	zoneID, err := uuid.Parse(chi.URLParam(r, "zone_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid DNS zone ID")
		return
	}
	zone, err := h.repo.GetDNSZone(r.Context(), zoneID)
	if err != nil || zone.VNetID != vnet.ID {
		writeError(w, http.StatusNotFound, "DNS zone not found")
		return
	}
	recordID, err := uuid.Parse(chi.URLParam(r, "record_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid record ID")
		return
	}
	rec, err := h.repo.GetDNSRecord(r.Context(), recordID)
	if err != nil || rec.ZoneID != zoneID {
		writeError(w, http.StatusNotFound, "DNS record not found")
		return
	}

	if zone.BackendUID != "" {
		if err := h.provider.DeleteDnsRecord(r.Context(), zone.BackendUID, recordID.String()); err != nil {
			h.log.Error().Err(err).Str("record_id", recordID.String()).Msg("kubeovn DeleteDnsRecord failed")
			writeError(w, http.StatusInternalServerError, "failed to delete record from KubeOVN: "+err.Error())
			return
		}
	}

	if err := h.repo.DeleteDNSRecord(r.Context(), recordID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete DNS record")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

