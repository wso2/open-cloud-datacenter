// Package handlers — private_endpoint.go
//
// M3 chunk 2: generic Private Endpoint HTTP handler.
//
// One handler implementation, parameterised by:
//   - target_type     ("key_vault" today; future "database", "cache", "registry")
//   - target lookup   (a small per-service shim that fetches the row and verifies
//                      tenant ownership + ACTIVE state)
//   - BackendResolver (per-service: returns the in-cluster address the proxy
//                      should forward to)
//
// The same handler instance is wired under each service's URL prefix
// (e.g. /v1/keyvaults/{id}/private-endpoints,
//        /v1/databases/{id}/private-endpoints, …) and behaves identically.
//
// Async vs sync: provisioning is *synchronous in the handler* because the
// network primitive (Vip allocation + pod ready + Corefile patch) finishes in
// a few seconds. If that ever stretches enough to time out, we'll flip to
// 202 + reconciler in a separate chunk.
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
	"github.com/wso2/dc-api/internal/providers/common"
	"github.com/wso2/dc-api/internal/providers/endpoints"
	"github.com/wso2/dc-api/internal/rbac"
)

// TargetLookup is the per-service shim that confirms the parent resource
// (Key Vault, future Database, …) exists AND is owned by the caller's tenant.
// Returning false means 404 — same convention as the rest of the API.
// Phase 6a: accepts tenantUUID (immutable) instead of the mutable slug.
type TargetLookup interface {
	Exists(ctx context.Context, tenantUUID uuid.UUID, id uuid.UUID) (bool, error)
}

// PrivateEndpointHandler wires a target-agnostic CRUD endpoint set to one
// specific service type. Each managed service gets its own instance built
// from the same struct definition.
type PrivateEndpointHandler struct {
	repo         *db.Repository
	provisioner  endpoints.Provisioner
	targetType   models.PrivateEndpointTargetType
	serviceClass string                     // "kv" | "db" | ...
	targetParam  string                     // path param name for the target ID (e.g. "id")
	target       TargetLookup               // per-service ownership / existence check
	resolver     endpoints.BackendResolver  // per-service backend address
	log          zerolog.Logger
}

// NewPrivateEndpointHandler constructs a per-service handler. Call once per
// service type and bind it under the service's `/private-endpoints` route.
func NewPrivateEndpointHandler(
	repo *db.Repository,
	provisioner endpoints.Provisioner,
	targetType models.PrivateEndpointTargetType,
	serviceClass string,
	targetParam string,
	target TargetLookup,
	resolver endpoints.BackendResolver,
	log zerolog.Logger,
) *PrivateEndpointHandler {
	return &PrivateEndpointHandler{
		repo:         repo,
		provisioner:  provisioner,
		targetType:   targetType,
		serviceClass: serviceClass,
		targetParam:  targetParam,
		target:       target,
		resolver:     resolver,
		log:          log,
	}
}

// ── DTOs ──────────────────────────────────────────────────────────────────────

type createPrivateEndpointRequest struct {
	Name     string `json:"name"`
	VNetID   string `json:"vnet_id"`
	SubnetID string `json:"subnet_id"`
}

type privateEndpointResponse struct {
	ID         string `json:"id"`
	TenantID   string `json:"tenant_id"`
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	VNetID     string `json:"vnet_id"`
	SubnetID   string `json:"subnet_id"`
	Name       string `json:"name"`
	IPAddress  string `json:"ip_address,omitempty"`
	Hostname   string `json:"hostname,omitempty"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

func privateEndpointToResponse(ep *models.PrivateEndpoint) privateEndpointResponse {
	return privateEndpointResponse{
		ID:         ep.ID.String(),
		TenantID:   ep.TenantID,
		TargetType: string(ep.TargetType),
		TargetID:   ep.TargetID.String(),
		VNetID:     ep.VNetID.String(),
		SubnetID:   ep.SubnetID.String(),
		Name:       ep.Name,
		IPAddress:  ep.IPAddress,
		Hostname:   ep.Hostname,
		Status:     string(ep.Status),
		Message:    ep.Message,
		CreatedAt:  ep.CreatedAt.Format(time.RFC3339),
		UpdatedAt:  ep.UpdatedAt.Format(time.RFC3339),
	}
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// Create handles POST /v1/<service>/{target_id}/private-endpoints. Synchronous
// — returns 201 with status=ACTIVE once the proxy is up.
func (h *PrivateEndpointHandler) Create(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionPrivateEndpointWrite) {
		return
	}

	targetID, err := uuid.Parse(chi.URLParam(r, h.targetParam))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid target id")
		return
	}

	// Ownership / existence check on the target (e.g. the Key Vault).
	// Phase 6a: pass tenantUUID (immutable) instead of slug.
	exists, err := h.target.Exists(r.Context(), tenantUUID, targetID)
	if err != nil {
		h.log.Error().Err(err).Msg("private-endpoint: target lookup")
		writeError(w, http.StatusInternalServerError, "failed to verify target")
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, string(h.targetType)+" not found")
		return
	}

	var req createPrivateEndpointRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := validateResourceName(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	vnetUUID, err := uuid.Parse(req.VNetID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "vnet_id must be a UUID")
		return
	}
	subnetUUID, err := uuid.Parse(req.SubnetID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "subnet_id must be a UUID")
		return
	}

	// Verify VNet + subnet ownership and state.
	// Phase 6a: GetVNet now takes tenantUUID for WHERE clause isolation.
	vnet, err := h.repo.GetVNet(r.Context(), vnetUUID, tenantUUID, projectUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "vnet not found")
		return
	}
	if vnet.Status != models.StatusActive {
		writeError(w, http.StatusConflict, "vnet is not ACTIVE")
		return
	}
	subnet, err := h.repo.GetSubnet(r.Context(), subnetUUID)
	if err != nil || subnet.TenantUUID != tenantUUID {
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

	// Resolve the backend address via the per-service resolver.
	backendAddr, backendPort, err := h.resolver.Resolve(r.Context(), targetID)
	if err != nil {
		h.log.Error().Err(err).Msg("private-endpoint: backend resolve")
		writeError(w, http.StatusInternalServerError, "failed to resolve backend address")
		return
	}

	// Insert PENDING row first. The provisioner uses the row's UUID as the
	// kube-ovn resource-name seed, so the DB write has to happen up front.
	projectID, projectUUID, _ := lookupProjectUUID(w, r)
	ep, err := h.repo.CreatePrivateEndpoint(r.Context(), &models.PrivateEndpoint{
		TenantID:    tenantID,
		TenantUUID:  tenantUUID,
		ProjectID:   projectID,
		ProjectUUID: projectUUID,
		TargetType:  h.targetType,
		TargetID:    targetID,
		VNetID:      vnetUUID,
		SubnetID:    subnetUUID,
		Name:        req.Name,
		BackendAddr: fmt.Sprintf("%s:%d", backendAddr, backendPort),
		Status:      models.StatusPending,
	})
	if err != nil {
		if strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "duplicate key") {
			writeError(w, http.StatusConflict, "a private endpoint for this resource already exists in that VNet")
			return
		}
		h.log.Error().Err(err).Msg("private-endpoint: insert row")
		writeError(w, http.StatusInternalServerError, "failed to register private endpoint")
		return
	}

	actorID, _ := middleware.UserFromContext(r.Context())
	_ = h.repo.AppendAuditEvent(r.Context(), &models.AuditEvent{
		ResourceID: ep.ID, ActorID: actorID, Action: "CREATE", ToStatus: models.StatusPending,
	})

	// Gather sibling host records (other endpoints in the same VPC, ACTIVE).
	siblings, err := h.repo.ListPrivateEndpointsByVNet(r.Context(), vnetUUID)
	if err != nil {
		h.log.Error().Err(err).Msg("private-endpoint: list siblings")
		_ = h.repo.DeletePrivateEndpoint(r.Context(), ep.ID)
		writeError(w, http.StatusInternalServerError, "failed to enumerate sibling endpoints")
		return
	}
	siblingHosts := make([]endpoints.HostRecord, 0, len(siblings))
	for _, s := range siblings {
		if s.ID == ep.ID {
			continue
		}
		if s.Hostname == "" || s.IPAddress == "" {
			continue
		}
		siblingHosts = append(siblingHosts, endpoints.HostRecord{
			Hostname:  s.Hostname,
			IPAddress: s.IPAddress,
		})
	}

	// Run the provisioner.
	tenantNS := common.NamespaceForProject(tenantID, projectID)
	res, err := h.provisioner.Provision(r.Context(), endpoints.ProvisionInput{
		EndpointID:       ep.ID,
		TenantID:         tenantID,
		VNetBackendUID:   vnet.BackendUID,
		SubnetBackendUID: subnet.BackendUID,
		SubnetCIDR:       subnet.CIDR,
		TenantNS:         tenantNS,
		BackendAddr:      backendAddr,
		BackendPort:      backendPort,
		Name:             req.Name,
		ServiceClass:     h.serviceClass,
		SiblingHosts:     siblingHosts,
	})
	if err != nil {
		h.log.Error().Err(err).Str("endpoint", ep.ID.String()).Msg("private-endpoint: provision failed")
		_ = h.repo.UpdatePrivateEndpointStatus(r.Context(), ep.ID, models.StatusFailed, err.Error(), "", "", "")
		_ = h.repo.AppendAuditEvent(r.Context(), &models.AuditEvent{
			ResourceID: ep.ID, ActorID: actorID, Action: "STATUS_CHANGE",
			FromStatus: models.StatusPending, ToStatus: models.StatusFailed, Message: err.Error(),
		})
		writeError(w, http.StatusInternalServerError, "provisioning failed: "+err.Error())
		return
	}

	if err := h.repo.UpdatePrivateEndpointStatus(r.Context(), ep.ID, models.StatusActive,
		"", res.IPAddress, res.Hostname, res.ProxyPodName); err != nil {
		h.log.Error().Err(err).Msg("private-endpoint: status update")
		// Provisioner succeeded but DB update failed — caller will see a stale
		// PENDING. Don't roll back the proxy; let the reconciler heal it.
	}
	_ = h.repo.AppendAuditEvent(r.Context(), &models.AuditEvent{
		ResourceID: ep.ID, ActorID: actorID, Action: "STATUS_CHANGE",
		FromStatus: models.StatusPending, ToStatus: models.StatusActive,
	})

	final, err := h.repo.GetPrivateEndpoint(r.Context(), ep.ID)
	if err != nil {
		h.log.Error().Err(err).Msg("private-endpoint: re-read after provision")
		writeError(w, http.StatusInternalServerError, "endpoint created but re-read failed")
		return
	}
	writeJSON(w, http.StatusCreated, privateEndpointToResponse(final))
}

// List handles GET /v1/<service>/{target_id}/private-endpoints.
func (h *PrivateEndpointHandler) List(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionPrivateEndpointRead) {
		return
	}
	targetID, err := uuid.Parse(chi.URLParam(r, h.targetParam))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid target id")
		return
	}
	exists, err := h.target.Exists(r.Context(), tenantUUID, targetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to verify target")
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, string(h.targetType)+" not found")
		return
	}

	eps, err := h.repo.ListPrivateEndpointsByTarget(r.Context(), h.targetType, targetID)
	if err != nil {
		h.log.Error().Err(err).Msg("private-endpoint: list")
		writeError(w, http.StatusInternalServerError, "failed to list private endpoints")
		return
	}
	out := make([]privateEndpointResponse, 0, len(eps))
	for _, ep := range eps {
		out = append(out, privateEndpointToResponse(ep))
	}
	writeJSON(w, http.StatusOK, out)
}

// Get handles GET /v1/<service>/{target_id}/private-endpoints/{ep_id}.
func (h *PrivateEndpointHandler) Get(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionPrivateEndpointRead) {
		return
	}

	epID, err := uuid.Parse(chi.URLParam(r, "ep_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid endpoint id")
		return
	}
	ep, err := h.repo.GetPrivateEndpoint(r.Context(), epID)
	if errors.Is(err, db.ErrPrivateEndpointNotFound) {
		writeError(w, http.StatusNotFound, "private endpoint not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fetch endpoint")
		return
	}
	if ep.TenantUUID != tenantUUID || string(ep.TargetType) != string(h.targetType) {
		writeError(w, http.StatusNotFound, "private endpoint not found")
		return
	}
	writeJSON(w, http.StatusOK, privateEndpointToResponse(ep))
}

// Delete handles DELETE /v1/<service>/{target_id}/private-endpoints/{ep_id}.
// Synchronous — runs Teardown then deletes the row.
func (h *PrivateEndpointHandler) Delete(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionPrivateEndpointDelete) {
		return
	}

	epID, err := uuid.Parse(chi.URLParam(r, "ep_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid endpoint id")
		return
	}
	ep, err := h.repo.GetPrivateEndpoint(r.Context(), epID)
	if errors.Is(err, db.ErrPrivateEndpointNotFound) {
		writeError(w, http.StatusNotFound, "private endpoint not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fetch endpoint")
		return
	}
	if ep.TenantUUID != tenantUUID || string(ep.TargetType) != string(h.targetType) {
		writeError(w, http.StatusNotFound, "private endpoint not found")
		return
	}

	// Compute the remaining sibling list (everything in the VPC except this one).
	siblings, err := h.repo.ListPrivateEndpointsByVNet(r.Context(), ep.VNetID)
	if err != nil {
		h.log.Error().Err(err).Msg("private-endpoint: list siblings for teardown")
		writeError(w, http.StatusInternalServerError, "failed to enumerate sibling endpoints")
		return
	}
	remaining := make([]endpoints.HostRecord, 0, len(siblings))
	for _, s := range siblings {
		if s.ID == ep.ID {
			continue
		}
		if s.Hostname == "" || s.IPAddress == "" {
			continue
		}
		remaining = append(remaining, endpoints.HostRecord{
			Hostname:  s.Hostname,
			IPAddress: s.IPAddress,
		})
	}

	// We need the VPC's BackendUID for Corefile lookup; fetch the row.
	// Phase 6a: use GetVNetInternal — we already verified tenant ownership above via TenantUUID.
	vnet, err := h.repo.GetVNetInternal(r.Context(), ep.VNetID)
	if err != nil {
		h.log.Error().Err(err).Msg("private-endpoint: fetch vnet for teardown")
		writeError(w, http.StatusInternalServerError, "failed to look up vnet")
		return
	}

	if err := h.provisioner.Teardown(r.Context(), endpoints.TeardownInput{
		EndpointID:     ep.ID,
		VNetBackendUID: vnet.BackendUID,
		ProxyPodName:   ep.ProxyPodName,
		VipName:        ep.ProxyPodName, // same name in our naming scheme
		RemainingHosts: remaining,
	}); err != nil {
		h.log.Error().Err(err).Str("endpoint", ep.ID.String()).Msg("private-endpoint: teardown failed")
		writeError(w, http.StatusInternalServerError, "teardown failed: "+err.Error())
		return
	}

	// Audit before the hard delete — the snapshot resolves only while the
	// row exists.
	delActor, _ := middleware.UserFromContext(r.Context())
	_ = h.repo.AppendAuditEvent(r.Context(), &models.AuditEvent{
		ResourceID: ep.ID, ActorID: delActor, Action: "DELETE", FromStatus: ep.Status,
	})
	if err := h.repo.DeletePrivateEndpoint(r.Context(), ep.ID); err != nil {
		h.log.Error().Err(err).Msg("private-endpoint: delete row")
		writeError(w, http.StatusInternalServerError, "failed to delete endpoint row")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
