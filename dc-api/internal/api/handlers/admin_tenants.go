// Package handlers — admin_tenants.go
//
// AdminTenantHandler implements POST /v1/admin/tenants — the endpoint a
// platform admin uses to register an empty tenant in the local registry
// so it becomes visible to GET /v1/tenants before any user with the
// matching Asgardeo group has logged in.
//
// Auth: platform admin only (checked via middleware.IsAdminFromContext).
// Non-admin callers receive 403 even if they hold owner roles on every
// existing tenant — admin-tier endpoints are not delegated.
//
// Behaviour:
//   - 201 Created when the row is inserted
//   - 409 Conflict when a tenant with the same id already exists
//   - 400 Bad Request on validation failures (empty/malformed id)
//   - 403 Forbidden for non-admin callers
//
// This handler does NOT create the underlying Asgardeo group — operator
// owns that step today. Phase 2 (Asgardeo Admin API integration) will
// close the loop so `dcctl create tenant` provisions the group too.
package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"

	"github.com/rs/zerolog"
	"github.com/wso2/dc-api/internal/api/middleware"
	"github.com/wso2/dc-api/internal/db"
	"github.com/wso2/dc-api/internal/models"
	"github.com/wso2/dc-api/internal/providers"
)

// tenantIDPattern is the canonical slug regex. Mirrors the openapi spec
// for CreateTenantRequest.id. Lowercase ASCII letters/digits/dashes,
// starts with a letter, ends with letter or digit, 2-32 chars total.
var tenantIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}[a-z0-9]$`)

// AdminTenantHandler handles POST /v1/admin/tenants.
type AdminTenantHandler struct {
	repo     *db.Repository
	tenantNS providers.TenantNamespaceProvisioner // may be nil in tests
	log      zerolog.Logger
}

// NewAdminTenantHandler constructs the handler with injected dependencies.
// tenantNS is the per-tenant namespace provisioner — when nil (test fixture
// / no Kubernetes backend), the namespace creation step is skipped and the
// tenant row is still committed.
func NewAdminTenantHandler(
	repo *db.Repository,
	tenantNS providers.TenantNamespaceProvisioner,
	log zerolog.Logger,
) *AdminTenantHandler {
	return &AdminTenantHandler{
		repo:     repo,
		tenantNS: tenantNS,
		log:      log,
	}
}

// createTenantRequest matches the openapi spec's CreateTenantRequest schema.
//
// Capacity caps are optional — omitted/zero falls through to the schema
// defaults (80 cpu / 256 GB ram / 2 TB storage). Admin sets them per
// tenant based on the team's stated need; they can be bumped later via
// PATCH /v1/admin/tenants/{tenant_id}.
type createTenantRequest struct {
	ID           string `json:"id"`
	Name         string `json:"name,omitempty"`
	Description  string `json:"description,omitempty"`
	CPUCoresCap  int    `json:"cpu_cores_cap,omitempty"`
	MemoryGBCap  int    `json:"memory_gb_cap,omitempty"`
	StorageGBCap int    `json:"storage_gb_cap,omitempty"`
}

// updateTenantCapRequest is the PATCH body for /v1/admin/tenants/{tid}.
// All fields are pointers so we can distinguish "not present" (keep current)
// from "present and zero" (which we reject as invalid — cap of 0 freezes
// the tenant and is the wrong API verb for that).
type updateTenantCapRequest struct {
	CPUCoresCap  *int `json:"cpu_cores_cap,omitempty"`
	MemoryGBCap  *int `json:"memory_gb_cap,omitempty"`
	StorageGBCap *int `json:"storage_gb_cap,omitempty"`
}

// Create handles POST /v1/admin/tenants.
func (h *AdminTenantHandler) Create(w http.ResponseWriter, r *http.Request) {
	if !middleware.IsAdminFromContext(r.Context()) {
		writeError(w, http.StatusForbidden, "platform admin role required")
		return
	}

	var req createTenantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "malformed JSON body: "+err.Error())
		return
	}
	if !tenantIDPattern.MatchString(req.ID) {
		writeError(w, http.StatusBadRequest,
			"id must match ^[a-z][a-z0-9-]{0,30}[a-z0-9]$ (lowercase, starts with letter, 2-32 chars)")
		return
	}

	// Default name to id when omitted.
	name := req.Name
	if name == "" {
		name = req.ID
	}

	// Identify the caller for the audit field.
	_, pID, _ := middleware.PrincipalFromContext(r.Context())
	createdBy := "admin:" + pID

	// Reject obviously-bad caps now; repo passes 0 through as "use default."
	if req.CPUCoresCap < 0 || req.MemoryGBCap < 0 || req.StorageGBCap < 0 {
		writeError(w, http.StatusBadRequest, "capacity caps must be >= 0 (0 = use default)")
		return
	}

	t := models.Tenant{
		ID:          req.ID,
		Name:        name,
		Description: req.Description,
		CPUCoresCap:   req.CPUCoresCap,
		MemoryGBCap:   req.MemoryGBCap,
		StorageGBCap:  req.StorageGBCap,
		CreatedBy:     createdBy,
	}

	out, err := h.repo.CreateTenant(r.Context(), t)
	if err != nil {
		if errors.Is(err, db.ErrTenantAlreadyExists) {
			writeError(w, http.StatusConflict, "tenant with this id already exists")
			return
		}
		h.log.Error().Err(err).Str("tenant_id", req.ID).Msg("create tenant failed")
		writeError(w, http.StatusInternalServerError, "failed to create tenant")
		return
	}

	h.log.Info().
		Str("tenant_id", out.ID).
		Str("created_by", out.CreatedBy).
		Msg("admin registered tenant")

	// Eagerly create the per-tenant Kubernetes namespace so the first
	// managed-service Backend create (KVI, future DBI/cache/registry)
	// doesn't have to be defensive about whether the ns exists. Best-
	// effort: a failure here is logged but doesn't roll back the tenant
	// row — the next Backend create will retry the namespace.
	if h.tenantNS != nil {
		if err := h.tenantNS.EnsureTenantNamespace(r.Context(), out.ID, out.TenantUUID); err != nil {
			h.log.Warn().
				Err(err).
				Str("tenant_id", out.ID).
				Msg("tenant ns provision failed (non-fatal; will retry on first managed-service create)")
		}
	}

	writeJSON(w, http.StatusCreated, out)
}

// PatchCap handles PATCH /v1/admin/tenants/{tenant_id} — admin-only adjust
// of the tenant capacity ceiling. Fields omitted from the request keep
// their current value; pass any subset of {cpu_cores_cap, memory_gb_cap,
// storage_gb_cap} to update only those.
//
// Refused with 400 when the new cap would shrink any dimension below the
// already-allocated project sum. The error body carries `cap` (requested),
// `allocated` (current sum across this tenant's projects), and `available`
// (negative — by how much the proposed cap falls short) so the operator
// can see exactly what blocked the change.
func (h *AdminTenantHandler) PatchCap(w http.ResponseWriter, r *http.Request) {
	if !middleware.IsAdminFromContext(r.Context()) {
		writeError(w, http.StatusForbidden, "platform admin role required")
		return
	}

	tenantID, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant uuid in context")
		return
	}

	// Load current to merge with the partial PATCH body.
	current, err := h.repo.GetTenant(r.Context(), tenantID)
	if err != nil {
		h.log.Error().Err(err).Str("tenant_id", tenantID).Msg("load tenant for cap patch")
		writeError(w, http.StatusInternalServerError, "failed to load tenant")
		return
	}
	if current == nil {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	var req updateTenantCapRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "malformed JSON body: "+err.Error())
		return
	}

	newCap := models.TenantCap{
		CPUCores:  current.CPUCoresCap,
		MemoryGB:  current.MemoryGBCap,
		StorageGB: current.StorageGBCap,
	}
	if req.CPUCoresCap != nil {
		newCap.CPUCores = *req.CPUCoresCap
	}
	if req.MemoryGBCap != nil {
		newCap.MemoryGB = *req.MemoryGBCap
	}
	if req.StorageGBCap != nil {
		newCap.StorageGB = *req.StorageGBCap
	}
	if newCap.CPUCores < 1 || newCap.MemoryGB < 1 || newCap.StorageGB < 1 {
		writeError(w, http.StatusBadRequest, "cpu_cores_cap, memory_gb_cap, storage_gb_cap must each be >= 1")
		return
	}

	updated, usage, err := h.repo.UpdateTenantCap(r.Context(), tenantUUID, newCap)
	if err != nil {
		if errors.Is(err, db.ErrCapBelowAllocated) {
			writeQuotaExceeded(w, "tenant cap cannot be shrunk below already-allocated project quotas", usage, newCap)
			return
		}
		h.log.Error().Err(err).Str("tenant_id", tenantID).Msg("update tenant cap")
		writeError(w, http.StatusInternalServerError, "failed to update tenant cap")
		return
	}

	h.log.Info().
		Str("tenant_id", tenantID).
		Int("cpu_cores_cap", updated.CPUCoresCap).
		Int("memory_gb_cap", updated.MemoryGBCap).
		Int("storage_gb_cap", updated.StorageGBCap).
		Msg("admin updated tenant cap")
	writeJSON(w, http.StatusOK, updated)
}
