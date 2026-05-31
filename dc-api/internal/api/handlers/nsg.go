// Package handlers — nsg.go
//
// NSGHandler implements /v1/security-groups endpoints.
//
// NSGs are SYNCHRONOUS per §7 of the design doc. Create returns 201; rule
// update returns 200; attachment create returns 201; detach returns 204;
// delete returns 204. No reconciler loop needed.
//
// Two-step Create+Update orchestration (per "Driver implementation notes"):
//  1. Insert NSG row → get UUID.
//  2. Call provider.CreateNSG → get backendUID.
//  3. If rules supplied: build composite UID ("<nsgUUID>|<s1>|<s2>|...") and
//     call provider.UpdateNSGRules. With no attachments yet, UpdateNSGRules
//     buffers rules in DC-API DB only.
//
// NSG backendUID encoding (per driver notes):
//   "<nsgUUID>|<subnetUID1>|<subnetUID2>|..."
// When no subnets are attached (no "|"), UpdateNSGRules is a no-op at the
// data plane; rules are buffered in the DB and applied when a subnet attaches.
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

// NSGHandler handles all /v1/security-groups endpoints.
type NSGHandler struct {
	repo     *db.Repository
	provider providers.NetworkProvider
	log      zerolog.Logger
}

// NewNSGHandler creates an NSGHandler with injected dependencies.
func NewNSGHandler(repo *db.Repository, provider providers.NetworkProvider, log zerolog.Logger) *NSGHandler {
	return &NSGHandler{repo: repo, provider: provider, log: log}
}

// ── DTOs ──────────────────────────────────────────────────────────────────────

type nsgRuleDTO struct {
	Name                     string `json:"name"`
	Direction                string `json:"direction"`
	Priority                 int    `json:"priority"`
	Protocol                 string `json:"protocol"`
	SourceAddressPrefix      string `json:"source_address_prefix"`
	SourcePortRange          string `json:"source_port_range"`
	DestinationAddressPrefix string `json:"destination_address_prefix"`
	DestinationPortRange     string `json:"destination_port_range"`
	Action                   string `json:"action"`
}

type createNSGRequest struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Rules       []nsgRuleDTO `json:"rules"`
}

type nsgResponse struct {
	ID           string                  `json:"id"`
	TenantID     string                  `json:"tenant_id"`
	Name         string                  `json:"name"`
	Description  string                  `json:"description,omitempty"`
	Rules        []models.NSGRule        `json:"rules"`
	Attachments  []models.NSGAttachment  `json:"attachments"`
	Status       string                  `json:"status"`
	ProviderType string                  `json:"provider_type"`
	CreatedAt    string                  `json:"created_at"`
	UpdatedAt    string                  `json:"updated_at"`
}

type attachmentResponse struct {
	ID         string `json:"id"`
	SGiD       string `json:"sg_id"`
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	CreatedAt  string `json:"created_at"`
}

func nsgRuleDTOsToModel(dtos []nsgRuleDTO) []models.NSGRule {
	rules := make([]models.NSGRule, len(dtos))
	for i, d := range dtos {
		rules[i] = models.NSGRule{
			Name:                     d.Name,
			Direction:                d.Direction,
			Priority:                 d.Priority,
			Protocol:                 d.Protocol,
			SourceAddressPrefix:      d.SourceAddressPrefix,
			SourcePortRange:          d.SourcePortRange,
			DestinationAddressPrefix: d.DestinationAddressPrefix,
			DestinationPortRange:     d.DestinationPortRange,
			Action:                   d.Action,
		}
	}
	return rules
}

func nsgToResponse(nsg *models.NSG, rules []models.NSGRule, attachments []models.NSGAttachment) nsgResponse {
	if rules == nil {
		rules = []models.NSGRule{}
	}
	if attachments == nil {
		attachments = []models.NSGAttachment{}
	}
	return nsgResponse{
		ID:           nsg.ID.String(),
		TenantID:     nsg.TenantID,
		Name:         nsg.Name,
		Description:  nsg.Description,
		Rules:        rules,
		Attachments:  attachments,
		Status:       string(nsg.Status),
		ProviderType: nsg.ProviderType,
		CreatedAt:    nsg.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    nsg.UpdatedAt.Format(time.RFC3339),
	}
}

// buildBackendUID constructs the composite UID the driver expects:
// "<nsgUUID>|<subnetUID1>|<subnetUID2>|..."
func buildNSGBackendUID(nsgID uuid.UUID, subnetUIDs []string) string {
	parts := append([]string{nsgID.String()}, subnetUIDs...)
	return strings.Join(parts, "|")
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// Create handles POST /v1/security-groups.
// Synchronous: returns 201 Created.
func (h *NSGHandler) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	if !requireAction(w, r, h.repo, rbac.ActionNSGWrite) {
		return
	}

	var req createNSGRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if err := validateResourceName(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.Description) > 256 {
		writeError(w, http.StatusBadRequest, "description must be 256 characters or fewer")
		return
	}

	// Validate rules.
	for _, rule := range req.Rules {
		if err := validateNSGRule(rule); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if err := checkNSGRulePriorityUnique(req.Rules); err != nil {
		writeError(w, http.StatusConflict, err.Error())
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

	// Insert NSG row to get the UUID.
	nsg := &models.NSG{
		TenantID:     tenantID,
		TenantUUID:   tenantUUID,
		ProjectID:    projectID,
		ProjectUUID:  projectUUID,
		Name:         req.Name,
		Description:  req.Description,
		Rules:        nsgRuleDTOsToModel(req.Rules),
		Status:       models.StatusActive,
		ProviderType: h.provider.Name(),
	}
	nsg, err := h.repo.CreateNSG(r.Context(), nsg)
	if err != nil {
		h.log.Error().Err(err).Str("tenant", tenantID).Msg("create NSG in DB")
		if strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "duplicate key") {
			writeError(w, http.StatusConflict, fmt.Sprintf("an NSG named %q already exists for this tenant", req.Name))
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to register NSG")
		return
	}

	// Persist rules in the nsg_rules table.
	if len(req.Rules) > 0 {
		if err := h.repo.ReplaceNSGRules(r.Context(), nsg.ID, nsgRuleDTOsToModel(req.Rules)); err != nil {
			h.log.Error().Err(err).Msg("persist NSG rules")
			_ = h.repo.DeleteNSG(r.Context(), nsg.ID)
			writeError(w, http.StatusInternalServerError, "failed to persist NSG rules")
			return
		}
	}

	// Two-step: CreateNSG driver call.
	spec := models.NSGSpec{
		Name:        req.Name,
		Description: req.Description,
		Rules:       nsgRuleDTOsToModel(req.Rules),
	}
	providerRes, err := h.provider.CreateNSG(r.Context(), tenantID, projectID, spec)
	if err != nil {
		h.log.Error().Err(err).Str("nsg", req.Name).Msg("kubeovn CreateNSG failed")
		// Roll back DB row on driver failure.
		_ = h.repo.DeleteNSG(r.Context(), nsg.ID)
		writeError(w, http.StatusInternalServerError, "failed to create NSG in KubeOVN: "+err.Error())
		return
	}

	// Update backendUID.
	if err := h.repo.UpdateNSGBackendUID(r.Context(), nsg.ID, providerRes.BackendUID); err != nil {
		h.log.Error().Err(err).Str("nsg_id", nsg.ID.String()).
			Str("backend_uid", providerRes.BackendUID).
			Msg("CreateNSG: UpdateNSGBackendUID failed — subsequent rule updates may target the wrong row")
	}
	nsg.BackendUID = providerRes.BackendUID

	// With no attachments, UpdateNSGRules is a no-op at data plane; call anyway
	// so rules are available immediately when the first subnet attaches.
	compositeUID := buildNSGBackendUID(nsg.ID, nil)
	_ = h.provider.UpdateNSGRules(r.Context(), compositeUID, nsgRuleDTOsToModel(req.Rules))

	rules, _ := h.repo.ListNSGRules(r.Context(), nsg.ID)
	writeJSON(w, http.StatusCreated, nsgToResponse(nsg, rules, nil))
}

// Get handles GET /v1/security-groups/{sg_id}.
func (h *NSGHandler) Get(w http.ResponseWriter, r *http.Request) {
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "sg_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid NSG ID")
		return
	}
	nsg, err := h.repo.GetNSG(r.Context(), id)
	if err != nil || nsg.TenantUUID != tenantUUID {
		writeError(w, http.StatusNotFound, "NSG not found")
		return
	}
	rules, _ := h.repo.ListNSGRules(r.Context(), id)
	atts, _ := h.repo.ListNSGAttachments(r.Context(), id)
	writeJSON(w, http.StatusOK, nsgToResponse(nsg, rules, atts))
}

// List handles GET .../security-groups.
func (h *NSGHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}
	_, projectUUID, okP := lookupProjectUUID(w, r)
	if !okP {
		return
	}
	nsgs, err := h.repo.ListNSGsByProject(r.Context(), tenantUUID, projectUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list NSGs")
		return
	}
	resp := make([]nsgResponse, 0, len(nsgs))
	for _, nsg := range nsgs {
		rules, _ := h.repo.ListNSGRules(r.Context(), nsg.ID)
		atts, _ := h.repo.ListNSGAttachments(r.Context(), nsg.ID)
		resp = append(resp, nsgToResponse(nsg, rules, atts))
	}
	writeJSON(w, http.StatusOK, resp)
}

// UpdateRules handles PUT /v1/security-groups/{sg_id}/rules.
// Replaces the complete rule set. Synchronous: returns 200.
func (h *NSGHandler) UpdateRules(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionNSGWrite) {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "sg_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid NSG ID")
		return
	}
	nsg, err := h.repo.GetNSG(r.Context(), id)
	if err != nil || nsg.TenantUUID != tenantUUID {
		writeError(w, http.StatusNotFound, "NSG not found")
		return
	}

	var req struct {
		Rules []nsgRuleDTO `json:"rules"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	for _, rule := range req.Rules {
		if err := validateNSGRule(rule); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if err := checkNSGRulePriorityUnique(req.Rules); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	// Persist new rules in DB first.
	if err := h.repo.ReplaceNSGRules(r.Context(), id, nsgRuleDTOsToModel(req.Rules)); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist NSG rules")
		return
	}

	// Build composite UID with attached subnet KubeOVN backend_uids (NOT DC-API
	// UUIDs — the driver patches Subnet CRDs by their KubeOVN name).
	subnetBUIDs, _ := h.repo.ListAttachmentSubnetBackendUIDs(r.Context(), id)
	compositeUID := buildNSGBackendUID(id, subnetBUIDs)

	// Push updated rules to KubeOVN (no-op if no subnets attached).
	if err := h.provider.UpdateNSGRules(r.Context(), compositeUID, nsgRuleDTOsToModel(req.Rules)); err != nil {
		h.log.Error().Err(err).Str("nsg", nsg.Name).Msg("kubeovn UpdateNSGRules failed")
		writeError(w, http.StatusInternalServerError, "rules persisted but KubeOVN update failed: "+err.Error())
		return
	}

	nsg, _ = h.repo.GetNSG(r.Context(), id)
	rules, _ := h.repo.ListNSGRules(r.Context(), id)
	atts, _ := h.repo.ListNSGAttachments(r.Context(), id)
	writeJSON(w, http.StatusOK, nsgToResponse(nsg, rules, atts))
}

// Delete handles DELETE /v1/security-groups/{sg_id}.
// Synchronous: returns 204. Rejects if attachments exist.
func (h *NSGHandler) Delete(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionNSGDelete) {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "sg_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid NSG ID")
		return
	}
	nsg, err := h.repo.GetNSG(r.Context(), id)
	if err != nil || nsg.TenantUUID != tenantUUID {
		writeError(w, http.StatusNotFound, "NSG not found")
		return
	}

	// Delete guard: reject if attachments exist.
	hasAtts, err := h.repo.HasNSGAttachments(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check NSG attachments")
		return
	}
	if hasAtts {
		atts, _ := h.repo.ListNSGAttachments(r.Context(), id)
		ids := make([]string, len(atts))
		for i, a := range atts {
			ids[i] = a.ID.String()
		}
		writeError(w, http.StatusConflict,
			fmt.Sprintf("NSG has active attachments; detach them first: %s", strings.Join(ids, ", ")))
		return
	}

	if nsg.BackendUID != "" {
		if err := h.provider.DeleteNSG(r.Context(), nsg.BackendUID); err != nil {
			h.log.Error().Err(err).Str("backend_uid", nsg.BackendUID).Msg("kubeovn DeleteNSG failed")
			writeError(w, http.StatusInternalServerError, "failed to delete NSG from KubeOVN: "+err.Error())
			return
		}
	}
	if err := h.repo.DeleteNSG(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete NSG")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Attach handles POST /v1/security-groups/{sg_id}/attachments.
// Synchronous: returns 201.
func (h *NSGHandler) Attach(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionNSGWrite) {
		return
	}
	sgID, err := uuid.Parse(chi.URLParam(r, "sg_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid NSG ID")
		return
	}
	nsg, err := h.repo.GetNSG(r.Context(), sgID)
	if err != nil || nsg.TenantUUID != tenantUUID {
		writeError(w, http.StatusNotFound, "NSG not found")
		return
	}

	var req struct {
		TargetType string `json:"target_type"`
		TargetID   string `json:"target_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	// Per §14: NIC-level NSG attachment is deferred to M3.
	if req.TargetType == "nic" {
		writeError(w, http.StatusBadRequest,
			"NIC-level NSG attachment is deferred to M3 — use target_type: subnet for M2")
		return
	}
	if req.TargetType != "subnet" {
		writeError(w, http.StatusBadRequest, "target_type must be 'subnet' (M2) or 'nic' (M3)")
		return
	}

	targetID, err := uuid.Parse(req.TargetID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid target_id")
		return
	}

	// Verify the subnet belongs to the same tenant.
	subnet, err := h.repo.GetSubnet(r.Context(), targetID)
	if err != nil || subnet.TenantUUID != tenantUUID {
		writeError(w, http.StatusNotFound, "subnet not found")
		return
	}

	// Call driver to write NSG ACLs to the Subnet CRD.
	if err := h.provider.AttachNSGToSubnet(r.Context(), nsg.BackendUID, subnet.BackendUID); err != nil {
		h.log.Error().Err(err).Str("nsg", nsg.Name).Msg("kubeovn AttachNSGToSubnet failed")
		writeError(w, http.StatusInternalServerError, "failed to attach NSG: "+err.Error())
		return
	}

	attachProjectID, attachProjectUUID, _ := lookupProjectUUID(w, r)
	att := &models.NSGAttachment{
		NSGiD:       sgID,
		TenantID:    tenantID,
		TenantUUID:  tenantUUID,
		ProjectID:   attachProjectID,
		ProjectUUID: attachProjectUUID,
		TargetType:  req.TargetType,
		TargetID:    targetID,
	}
	att, err = h.repo.CreateNSGAttachment(r.Context(), att)
	if err != nil {
		if strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "duplicate key") {
			writeError(w, http.StatusConflict, "this NSG is already attached to the specified target")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to persist attachment")
		return
	}

	// Re-apply rules now that a new subnet is attached. Pass the subnet's
	// KubeOVN backend_uid (CRD name), not the DC-API UUID — the driver
	// PATCHes Subnet CRDs by their KubeOVN name.
	rules, _ := h.repo.ListNSGRules(r.Context(), sgID)
	subnetBUIDs, err := h.repo.ListAttachmentSubnetBackendUIDs(r.Context(), sgID)
	if err != nil {
		h.log.Error().Err(err).Str("nsg", sgID.String()).Msg("attach: list attachment subnet backend_uids")
	}
	h.log.Info().Str("nsg", sgID.String()).Int("rules", len(rules)).Int("subnets", len(subnetBUIDs)).
		Strs("subnet_buids", subnetBUIDs).Msg("attach: applying NSG rules to attached subnets")
	compositeUID := buildNSGBackendUID(sgID, subnetBUIDs)
	if err := h.provider.UpdateNSGRules(r.Context(), compositeUID, rules); err != nil {
		h.log.Error().Err(err).Str("nsg", sgID.String()).Msg("attach: kubeovn UpdateNSGRules failed")
	}

	writeJSON(w, http.StatusCreated, attachmentResponse{
		ID:         att.ID.String(),
		SGiD:       att.NSGiD.String(),
		TargetType: att.TargetType,
		TargetID:   att.TargetID.String(),
		CreatedAt:  att.CreatedAt.Format(time.RFC3339),
	})
}

// Detach handles DELETE /v1/security-groups/{sg_id}/attachments/{attachment_id}.
// Synchronous: returns 204.
func (h *NSGHandler) Detach(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionNSGWrite) {
		return
	}
	sgID, err := uuid.Parse(chi.URLParam(r, "sg_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid NSG ID")
		return
	}
	attID, err := uuid.Parse(chi.URLParam(r, "attachment_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid attachment ID")
		return
	}

	nsg, err := h.repo.GetNSG(r.Context(), sgID)
	if err != nil || nsg.TenantUUID != tenantUUID {
		writeError(w, http.StatusNotFound, "NSG not found")
		return
	}

	att, err := h.repo.GetNSGAttachment(r.Context(), attID)
	if err != nil || att.NSGiD != sgID {
		writeError(w, http.StatusNotFound, "attachment not found")
		return
	}

	// Call driver to remove ACLs from the subnet.
	if att.TargetType == "subnet" {
		subnet, subErr := h.repo.GetSubnet(r.Context(), att.TargetID)
		if subErr == nil && subnet != nil {
			if err := h.provider.DetachNSGFromSubnet(r.Context(), nsg.BackendUID, subnet.BackendUID); err != nil {
				h.log.Error().Err(err).Str("nsg", nsg.Name).Msg("kubeovn DetachNSGFromSubnet failed")
				writeError(w, http.StatusInternalServerError, "failed to detach NSG: "+err.Error())
				return
			}
		}
	}

	if err := h.repo.DeleteNSGAttachment(r.Context(), attID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete attachment")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ListAttachments handles a hypothetical GET on attachments sub-resource.
// Not in the spec as a separate route but provided for completeness.
func listAttachmentResponse(atts []models.NSGAttachment) []attachmentResponse {
	resp := make([]attachmentResponse, len(atts))
	for i, a := range atts {
		resp[i] = attachmentResponse{
			ID:         a.ID.String(),
			SGiD:       a.NSGiD.String(),
			TargetType: a.TargetType,
			TargetID:   a.TargetID.String(),
			CreatedAt:  a.CreatedAt.Format(time.RFC3339),
		}
	}
	return resp
}

