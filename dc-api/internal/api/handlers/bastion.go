package handlers

import (
	"context"
	"encoding/json"
	"errors"
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

// BastionHandler handles all /v1/bastions endpoints (F10).
//
// A bastion is mechanically a dual-NIC KubeVirt VM (mgmt + OVN), provisioned
// through the same Harvester ComputeProvider as regular VMs. The only difference
// is that:
//   - the DB row carries Type=BASTION (so reconciler + handlers can filter)
//   - the VMSpec sets MgmtNAD so the harvester driver renders dual-NIC + dual-NIC
//     cloud-init netplan bootcmd
//   - size + image are operator-controlled (fixed at "small" + DCAPI_BASTION_IMAGE)
//     so tenants don't choose
//
// Quota: bastions count against the same max_vms quota — they ARE VMs at the
// backend. A separate max_bastions could land later if usage justifies.
type BastionHandler struct {
	repo            *db.Repository
	provider        providers.ComputeProvider
	bastionImage    string // DCAPI_BASTION_IMAGE
	bastionMgmtNAD  string // DCAPI_BASTION_MGMT_NAD
	dnsSearchDomain string // DCAPI_VPC_DNS_SEARCH_DOMAIN — same as VMHandler
	log             zerolog.Logger
}

// NewBastionHandler creates a BastionHandler with injected dependencies.
func NewBastionHandler(
	repo *db.Repository,
	provider providers.ComputeProvider,
	bastionImage, bastionMgmtNAD, dnsSearchDomain string,
	log zerolog.Logger,
) *BastionHandler {
	return &BastionHandler{
		repo:            repo,
		provider:        provider,
		bastionImage:    bastionImage,
		bastionMgmtNAD:  bastionMgmtNAD,
		dnsSearchDomain: dnsSearchDomain,
		log:             log,
	}
}

// ─────────────────────────── Request / Response DTOs ────────────────────────

// CreateBastionRequest is the JSON body for POST /v1/bastions.
type CreateBastionRequest struct {
	Name        string `json:"name"`
	VNetID      string `json:"vnet_id"`
	SubnetID    string `json:"subnet_id"`
	Description string `json:"description,omitempty"`
}

func (r *CreateBastionRequest) validate() error {
	if r.Name == "" {
		return errors.New("name is required")
	}
	if err := validateResourceName(r.Name); err != nil {
		return err
	}
	if r.VNetID == "" {
		return errors.New("vnet_id is required")
	}
	if r.SubnetID == "" {
		return errors.New("subnet_id is required")
	}
	if _, err := uuid.Parse(r.VNetID); err != nil {
		return errors.New("vnet_id must be a UUID")
	}
	if _, err := uuid.Parse(r.SubnetID); err != nil {
		return errors.New("subnet_id must be a UUID")
	}
	if len(r.Description) > 256 {
		return errors.New("description must be 256 chars or fewer")
	}
	return nil
}

// BastionResponse is the JSON body for GET responses + the resource section of
// the create response. Mirrors the OpenAPI `Bastion` schema.
type BastionResponse struct {
	ID           uuid.UUID             `json:"id"`
	Name         string                `json:"name"`
	Status       models.ResourceStatus `json:"status"`
	TenantID     string                `json:"tenant_id"`
	// vnet_id/subnet_id are populated only for VPC-attached bastions.
	// Legacy bridge-mode bastions omit them entirely so spec-typed clients
	// don't try to decode "" into a uuid field.
	VNetID       string                `json:"vnet_id,omitempty"`
	SubnetID     string                `json:"subnet_id,omitempty"`
	ProviderType string                `json:"provider_type"`
	MgmtIP       string                `json:"mgmt_ip,omitempty"`
	InternalIP   string                `json:"internal_ip,omitempty"`
	Description  string                `json:"description,omitempty"`
	Message      string                `json:"message,omitempty"`
	CreatedAt    time.Time             `json:"created_at"`
}

func bastionToResponse(res *models.Resource, description string) BastionResponse {
	out := BastionResponse{
		ID:           res.ID,
		Name:         res.Name,
		Status:       res.Status,
		TenantID:     res.TenantID,
		ProviderType: res.ProviderType,
		MgmtIP:       res.MgmtIP,
		InternalIP:   res.IPAddress,
		Description:  description,
		Message:      res.Message,
		CreatedAt:    res.CreatedAt,
	}
	if res.VNetID != nil {
		out.VNetID = res.VNetID.String()
	}
	if res.SubnetID != nil {
		out.SubnetID = res.SubnetID.String()
	}
	return out
}

// ─────────────────────────── Create ─────────────────────────────────────────

func (h *BastionHandler) Create(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionBastionWrite) {
		return
	}
	userID, _ := middleware.UserFromContext(r.Context())

	var req CreateBastionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := req.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Bastions share max_vms quota — they are VMs at the backend.
	quota, err := h.repo.GetQuota(r.Context(), tenantID)
	if err != nil {
		h.log.Error().Err(err).Str("tenant", tenantID).Msg("get quota")
		writeError(w, http.StatusInternalServerError, "quota check failed")
		return
	}
	vmCount, err := h.repo.CountByTenant(r.Context(), tenantUUID, models.ResourceTypeVM)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "quota check failed")
		return
	}
	bastionCount, err := h.repo.CountByTenant(r.Context(), tenantUUID, models.ResourceTypeBastion)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "quota check failed")
		return
	}
	if vmCount+bastionCount >= quota.MaxVMs {
		writeError(w, http.StatusForbidden,
			fmt.Sprintf("VM quota exceeded: %d/%d VMs+bastions in use", vmCount+bastionCount, quota.MaxVMs))
		return
	}

	// Resolve VPC attachment — same shape as VM handler.
	vnetUUID, _ := uuid.Parse(req.VNetID)
	subnetUUID, _ := uuid.Parse(req.SubnetID)

	vnet, err := h.repo.GetVNetByTenant(r.Context(), vnetUUID, tenantUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "vnet not found")
		return
	}
	if vnet.Status != models.StatusActive {
		writeError(w, http.StatusConflict, "vnet is not ACTIVE")
		return
	}
	subnet, err := h.repo.GetSubnet(r.Context(), subnetUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "subnet not found")
		return
	}
	if subnet.VNetID != vnetUUID {
		writeError(w, http.StatusBadRequest, "subnet does not belong to the specified vnet")
		return
	}
	if subnet.Status != models.StatusActive {
		writeError(w, http.StatusConflict, "subnet is not ACTIVE")
		return
	}

	// SSH key (same flow as VM — private key returned once, never stored).
	publicKey, privateKeyPEM, err := generateSSHKeyPair()
	if err != nil {
		h.log.Error().Err(err).Msg("generate SSH key pair")
		writeError(w, http.StatusInternalServerError, "failed to generate SSH keys")
		return
	}
	password, err := generatePassword()
	if err != nil {
		h.log.Error().Err(err).Msg("generate console password")
		writeError(w, http.StatusInternalServerError, "failed to generate password")
		return
	}

	// Fixed sizing — bastions are tiny.
	sz := models.Sizes["small"]

	// DB row first (PENDING).
	projectID, projectUUID, _ := lookupProjectUUID(w, r)
	resource, err := h.repo.Create(r.Context(), &models.Resource{
		TenantID:     tenantID,
		TenantUUID:   tenantUUID,
		ProjectID:    projectID,
		ProjectUUID:  projectUUID,
		OwnerID:      userID,
		Name:         req.Name,
		Type:         models.ResourceTypeBastion,
		Size:         "small",
		Status:       models.StatusPending,
		ProviderType: h.provider.Name(),
		VNetID:       &vnet.ID,
		SubnetID:     &subnet.ID,
	})
	if err != nil {
		h.log.Error().Err(err).Str("tenant", tenantID).Msg("create bastion resource in DB")
		if strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "duplicate key") {
			writeError(w, http.StatusConflict,
				"a bastion named '"+req.Name+"' already exists — wait for it to finish or delete it first")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to register bastion")
		return
	}

	_ = h.repo.AppendAuditEvent(r.Context(), &models.AuditEvent{
		ResourceID: resource.ID,
		ActorID:    userID,
		Action:     "CREATE",
		ToStatus:   models.StatusPending,
	})

	// VPC DNS server IP (F20) so the bastion's resolv.conf matches the VPC.
	dnsSrvIP := ""
	if vnet.DNSServerIP != "" {
		dnsSrvIP = vnet.DNSServerIP
	}

	spec := models.VMSpec{
		Name:             req.Name,
		CPU:              sz.CPU,
		MemoryGB:         sz.MemoryGB,
		DiskGB:           sz.DefaultDiskGB,
		ImageName:        h.bastionImage,
		VNetBackendUID:   vnet.BackendUID,
		SubnetBackendUID: subnet.BackendUID,
		SSHPublicKey:     publicKey,
		Password:         password,
		ResourceID:       resource.ID.String(),
		DNSServerIP:      dnsSrvIP,
		DNSSearchDomain:  h.dnsSearchDomain,
		MgmtNAD:          h.bastionMgmtNAD,
	}
	go h.asyncProvision(resource.ID, tenantID, projectID, userID, spec, req.Description)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"resource":         bastionToResponse(resource, req.Description),
		"private_key":      privateKeyPEM,
		"console_password": password,
		"note":             "Bastion is being provisioned. Poll GET /v1/bastions/" + resource.ID.String() + " for status.",
	})
}

// ─────────────────────────── Get / List / Delete ─────────────────────────────

func (h *BastionHandler) Get(w http.ResponseWriter, r *http.Request) {
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid resource ID format")
		return
	}
	resource, err := h.repo.Get(r.Context(), id, tenantUUID)
	if err != nil || resource.Type != models.ResourceTypeBastion {
		writeError(w, http.StatusNotFound, "bastion not found")
		return
	}
	writeJSON(w, http.StatusOK, bastionToResponse(resource, ""))
}

func (h *BastionHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}
	resources, err := h.repo.ListByTenant(r.Context(), tenantUUID, models.ResourceTypeBastion)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list bastions")
		return
	}
	responses := make([]BastionResponse, 0, len(resources))
	for _, res := range resources {
		responses = append(responses, bastionToResponse(res, ""))
	}
	writeJSON(w, http.StatusOK, responses)
}

func (h *BastionHandler) Delete(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionBastionDelete) {
		return
	}
	userID, _ := middleware.UserFromContext(r.Context())

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid resource ID format")
		return
	}
	resource, err := h.repo.Get(r.Context(), id, tenantUUID)
	if err != nil || resource.Type != models.ResourceTypeBastion {
		writeError(w, http.StatusNotFound, "bastion not found")
		return
	}

	if err := h.repo.UpdateStatus(r.Context(), id, models.StatusDeleting, "deletion requested", ""); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update bastion status")
		return
	}
	_ = h.repo.AppendAuditEvent(r.Context(), &models.AuditEvent{
		ResourceID: id,
		ActorID:    userID,
		Action:     "DELETE",
		FromStatus: resource.Status,
		ToStatus:   models.StatusDeleting,
	})

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if resource.BackendUID == "" {
			_ = h.repo.Delete(ctx, id)
			return
		}
		if err := h.provider.DeleteVM(ctx, resource.BackendUID); err != nil {
			h.log.Error().Err(err).Str("backend_uid", resource.BackendUID).Msg("harvester delete bastion VM failed")
			_ = h.repo.UpdateStatus(ctx, id, models.StatusFailed, "deletion failed: "+err.Error(), "")
		}
	}()

	w.WriteHeader(http.StatusAccepted)
}

// ─────────────────────────── Async Provisioner ──────────────────────────────

func (h *BastionHandler) asyncProvision(resourceID uuid.UUID, tenantID, projectID, userID string, spec models.VMSpec, _ string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	providerResource, err := h.provider.CreateVM(ctx, tenantID, projectID, spec)
	if err != nil {
		h.log.Error().Err(err).Str("bastion", spec.Name).Msg("harvester CreateVM (bastion) failed")
		_ = h.repo.UpdateStatus(ctx, resourceID, models.StatusFailed,
			"provisioning failed: "+err.Error(), "")
		_ = h.repo.AppendAuditEvent(ctx, &models.AuditEvent{
			ResourceID: resourceID,
			ActorID:    userID,
			Action:     "STATUS_CHANGE",
			FromStatus: models.StatusPending,
			ToStatus:   models.StatusFailed,
			Message:    err.Error(),
		})
		return
	}

	if err := h.repo.UpdateStatus(ctx, resourceID, models.StatusPending,
		"provisioning submitted to Harvester", providerResource.BackendUID); err != nil {
		h.log.Error().Err(err).Msg("update bastion BackendUID")
	}
}
