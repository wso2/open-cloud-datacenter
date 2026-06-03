// Package handlers — projects.go
//
// ProjectHandler implements the /v1/tenants/{tenant_id}/projects endpoints.
//
// Auth matrix:
//
//	POST   /v1/tenants/{tid}/projects           → RoleOwner (tenant scope)
//	GET    /v1/tenants/{tid}/projects           → any tenant member
//	GET    /v1/tenants/{tid}/projects/{pid}     → any tenant member OR project member
//	DELETE /v1/tenants/{tid}/projects/{pid}     → RoleOwner (tenant scope); refuses if not empty
//
// On successful POST, the handler synchronously:
//  1. Creates the project row + project_quotas row (db.CreateProject).
//  2. Creates the Kubernetes namespace dc-<tenant>-<project> with standard labels.
//  3. Creates a ResourceQuota mirroring the project capacity quotas.
//
// The K8s namespace creation is best-effort: if it fails the project row is
// still committed (the namespace can be re-created on the next VNet/VM create
// via the provider's ensureNamespace call). A warning is logged.
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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

// ProjectHandler handles all /v1/tenants/{tenant_id}/projects endpoints.
type ProjectHandler struct {
	repo      *db.Repository
	nsProvisioner providers.ProjectNamespaceProvisioner // may be nil in tests
	log       zerolog.Logger
}

// NewProjectHandler creates a ProjectHandler with injected dependencies.
// nsProvisioner may be nil — when nil, K8s namespace creation is skipped
// (acceptable in tests or when running without a Kubernetes backend).
func NewProjectHandler(repo *db.Repository, nsProvisioner providers.ProjectNamespaceProvisioner, log zerolog.Logger) *ProjectHandler {
	return &ProjectHandler{repo: repo, nsProvisioner: nsProvisioner, log: log}
}

// ── DTOs ──────────────────────────────────────────────────────────────────────

type createProjectRequest struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Capacity quotas
	CPUCores  int `json:"cpu_cores"`
	MemoryGB  int `json:"memory_gb"`
	StorageGB int `json:"storage_gb"`
	// Object guardrails (optional — defaults apply when zero)
	MaxVNets    int `json:"max_vnets"`
	MaxClusters int `json:"max_clusters"`
	MaxVolumes  int `json:"max_volumes"`
	MaxPublicIPs int `json:"max_public_ips"`
}

func (req *createProjectRequest) validate() error {
	if err := validateResourceName(req.ID); err != nil {
		return fmt.Errorf("project id: %w", err)
	}
	if req.Name == "" {
		req.Name = req.ID // default display name to slug
	}
	if len(req.Description) > 512 {
		return fmt.Errorf("description must be 512 characters or fewer")
	}
	if req.CPUCores < 0 {
		return fmt.Errorf("cpu_cores must be non-negative")
	}
	if req.MemoryGB < 0 {
		return fmt.Errorf("memory_gb must be non-negative")
	}
	if req.StorageGB < 0 {
		return fmt.Errorf("storage_gb must be non-negative")
	}
	return nil
}

// defaults fills zero guardrails with sensible values.
func (req *createProjectRequest) defaults() {
	if req.CPUCores == 0 {
		req.CPUCores = 20
	}
	if req.MemoryGB == 0 {
		req.MemoryGB = 64
	}
	if req.StorageGB == 0 {
		req.StorageGB = 500
	}
	if req.MaxVNets == 0 {
		req.MaxVNets = 10
	}
	if req.MaxClusters == 0 {
		req.MaxClusters = 2
	}
	if req.MaxVolumes == 0 {
		req.MaxVolumes = 50
	}
	if req.MaxPublicIPs == 0 {
		req.MaxPublicIPs = 3
	}
}

type projectResponse struct {
	ID          string `json:"id"`
	TenantID    string `json:"tenant_id"`
	ProjectUUID string `json:"project_uuid"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	CPUCores    int    `json:"cpu_cores"`
	MemoryGB    int    `json:"memory_gb"`
	StorageGB   int    `json:"storage_gb"`
	// Quotas are included inline for convenience.
	MaxVNets    int    `json:"max_vnets"`
	MaxClusters int    `json:"max_clusters"`
	MaxVolumes  int    `json:"max_volumes"`
	MaxPublicIPs int   `json:"max_public_ips"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	CreatedBy   string `json:"created_by"`
}

func projectToResponse(p *models.Project, q *models.ProjectQuota) projectResponse {
	resp := projectResponse{
		ID:          p.ID,
		TenantID:    p.TenantID,
		ProjectUUID: p.ProjectUUID.String(),
		Name:        p.Name,
		Description: p.Description,
		CPUCores:    p.CPUCores,
		MemoryGB:    p.MemoryGB,
		StorageGB:   p.StorageGB,
		CreatedAt:   p.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   p.UpdatedAt.Format(time.RFC3339),
		CreatedBy:   p.CreatedBy,
	}
	if q != nil {
		resp.MaxVNets = q.MaxVNets
		resp.MaxClusters = q.MaxClusters
		resp.MaxVolumes = q.MaxVolumes
		resp.MaxPublicIPs = q.MaxPublicIPs
	}
	return resp
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// Create handles POST /v1/tenants/{tenant_id}/projects.
// Owner-only. Creates the project row, then best-effort creates the K8s namespace.
func (h *ProjectHandler) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	if !requireAction(w, r, h.repo, rbac.ActionProjectWrite) {
		return
	}
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}
	userID, _ := middleware.UserFromContext(r.Context())

	var req createProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	req.defaults()
	if err := req.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	project := models.Project{
		ID:          req.ID,
		TenantID:    tenantID,
		TenantUUID:  tenantUUID,
		Name:        req.Name,
		Description: req.Description,
		CPUCores:    req.CPUCores,
		MemoryGB:    req.MemoryGB,
		StorageGB:   req.StorageGB,
		CreatedBy:   userID,
	}
	quota := models.ProjectQuota{
		MaxVNets:    req.MaxVNets,
		MaxClusters: req.MaxClusters,
		MaxVolumes:  req.MaxVolumes,
		MaxPublicIPs: req.MaxPublicIPs,
	}

	p, q, usage, err := h.repo.CreateProject(r.Context(), project, quota)
	if err != nil {
		switch {
		case errors.Is(err, db.ErrProjectAlreadyExists):
			writeError(w, http.StatusConflict, fmt.Sprintf("project %q already exists in this tenant", req.ID))
		case errors.Is(err, db.ErrProjectQuotaExceedsTenantCap):
			writeQuotaExceeded(w, "project quotas would exceed tenant cap", usage, models.TenantCap{
				CPUCores: req.CPUCores, MemoryGB: req.MemoryGB, StorageGB: req.StorageGB,
			})
		default:
			h.log.Error().Err(err).Str("tenant", tenantID).Str("project", req.ID).Msg("create project in DB")
			writeError(w, http.StatusInternalServerError, "failed to create project")
		}
		return
	}

	// Best-effort: provision the K8s namespace + ResourceQuota.
	// The project is committed regardless — the namespace can be re-created
	// on first VNet/VM create via the provider's ensureNamespace call.
	if h.nsProvisioner != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if nsErr := h.nsProvisioner.EnsureProjectNamespace(ctx, tenantID, req.ID, p.ProjectUUID, p.CPUCores, p.MemoryGB, p.StorageGB, q.MaxVolumes); nsErr != nil {
				h.log.Warn().Err(nsErr).
					Str("tenant", tenantID).Str("project", req.ID).
					Msg("project namespace provisioning failed (best-effort; will retry on first resource create)")
			}
		}()
	}

	writeJSON(w, http.StatusCreated, projectToResponse(p, q))
}

// List handles GET /v1/tenants/{tenant_id}/projects.
// Any tenant member may list.
func (h *ProjectHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}

	projects, err := h.repo.ListProjects(r.Context(), tenantUUID)
	if err != nil {
		h.log.Error().Err(err).Msg("list projects")
		writeError(w, http.StatusInternalServerError, "failed to list projects")
		return
	}

	// Filter to the projects the caller can actually reach. A tenant-scope grant
	// (or platform admin) covers every project by inheritance and sees them all;
	// a caller with only project-scope grants sees just those, so other projects'
	// names don't leak to them.
	projects, err = h.filterReachableProjects(r, projects)
	if err != nil {
		h.log.Error().Err(err).Msg("filter reachable projects")
		writeError(w, http.StatusInternalServerError, "failed to list projects")
		return
	}

	resp := make([]projectResponse, 0, len(projects))
	for i := range projects {
		q, _ := h.repo.GetProjectQuota(r.Context(), projects[i].ProjectUUID)
		resp = append(resp, projectToResponse(&projects[i], q))
	}
	writeJSON(w, http.StatusOK, resp)
}

// filterReachableProjects narrows a tenant's project list to those the caller
// may access. A platform admin or the holder of a tenant-scope grant on this
// tenant sees all of them (a tenant grant inherits into every project);
// otherwise the caller sees only the projects they hold a project-scope grant
// on. Returns an error only on a genuine lookup failure (fail-closed).
func (h *ProjectHandler) filterReachableProjects(r *http.Request, projects []models.Project) ([]models.Project, error) {
	if middleware.IsAdminFromContext(r.Context()) {
		return projects, nil
	}
	tenantID, _ := middleware.TenantFromContext(r.Context())
	pType, pID, ok := middleware.PrincipalFromContext(r.Context())
	if !ok {
		return nil, fmt.Errorf("no principal in context")
	}
	assignments, err := h.repo.ListRoleAssignmentsForPrincipal(r.Context(), pType, pID)
	if err != nil {
		return nil, err
	}
	granted := make(map[uuid.UUID]bool)
	for _, a := range assignments {
		if a.ScopeType == models.ScopeTypeTenant && a.ScopeID == tenantID {
			return projects, nil // tenant grant inherits to every project
		}
		if a.ScopeType == models.ScopeTypeProject {
			granted[a.ScopeUUID] = true
		}
		// A resource-scope grant makes the resource's project reachable, so a
		// resource-only user sees just that project (to navigate to the
		// resource) without other project names leaking.
		if a.ScopeType == models.ScopeTypeResource {
			_, puuid, found, err := h.repo.GetResourceLocationByUUID(r.Context(), a.ScopeUUID)
			if err != nil {
				return nil, err
			}
			if found {
				granted[puuid] = true
			}
		}
	}
	out := make([]models.Project, 0, len(projects))
	for _, p := range projects {
		if granted[p.ProjectUUID] {
			out = append(out, p)
		}
	}
	return out, nil
}

// Get handles GET /v1/tenants/{tenant_id}/projects/{project_id}.
// Requires tenant-member OR project-member access (enforced by ProjectContext middleware
// before this handler runs).
func (h *ProjectHandler) Get(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := middleware.TenantFromContext(r.Context())
	projectID := chi.URLParam(r, "project_id")

	p, err := h.repo.GetProject(r.Context(), tenantID, projectID)
	if err != nil {
		h.log.Error().Err(err).Str("tenant", tenantID).Str("project", projectID).Msg("get project")
		writeError(w, http.StatusInternalServerError, "failed to get project")
		return
	}
	if p == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	q, _ := h.repo.GetProjectQuota(r.Context(), p.ProjectUUID)
	writeJSON(w, http.StatusOK, projectToResponse(p, q))
}

// Delete handles DELETE /v1/tenants/{tenant_id}/projects/{project_id}.
// Owner-only. Refuses if the project has any active resources.
func (h *ProjectHandler) Delete(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	if !requireAction(w, r, h.repo, rbac.ActionProjectDelete) {
		return
	}
	projectID := chi.URLParam(r, "project_id")

	if err := h.repo.DeleteProject(r.Context(), tenantID, projectID); err != nil {
		if err == db.ErrProjectNotEmpty {
			writeError(w, http.StatusConflict, "project has active resources; delete them first")
			return
		}
		h.log.Error().Err(err).Str("tenant", tenantID).Str("project", projectID).Msg("delete project")
		writeError(w, http.StatusInternalServerError, "failed to delete project")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetTenantCapUsage handles GET /v1/tenants/{tenant_id}/cap-usage.
// Returns the tenant cap, sum of project allocations, and remaining headroom
// — what the cloud-ui RegisterProjectDialog displays inline ("X cpu available")
// and what `dcctl admin tenant cap show` prints. Any tenant member can read;
// mutation goes through PATCH /v1/admin/tenants/{tid} (admin-only).
func (h *ProjectHandler) GetTenantCapUsage(w http.ResponseWriter, r *http.Request) {
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant uuid in context")
		return
	}
	usage, err := h.repo.GetTenantCapAndAllocation(r.Context(), tenantUUID)
	if err != nil {
		h.log.Error().Err(err).Msg("get tenant cap usage")
		writeError(w, http.StatusInternalServerError, "failed to read tenant cap usage")
		return
	}
	if usage == nil {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}
	writeJSON(w, http.StatusOK, usage)
}

// updateProjectQuotaRequest is the PATCH body for project capacity quotas.
// Only capacity fields are mutable. Slug + tenant are immutable; rename
// (name/description) is a separate concern and can be added later as
// distinct PATCH fields if needed.
//
// Any field omitted (zero value) is treated as "no change" — for capacity
// fields the validate step rejects zero explicitly so an accidental omit
// doesn't shrink the project to nothing.
type updateProjectQuotaRequest struct {
	CPUCores  *int `json:"cpu_cores,omitempty"`
	MemoryGB  *int `json:"memory_gb,omitempty"`
	StorageGB *int `json:"storage_gb,omitempty"`
}

// Patch handles PATCH /v1/tenants/{tenant_id}/projects/{project_id}.
// Owner-only (tenant scope). Updates project capacity quotas with both
// invariants enforced inside a single transaction:
//
//  1. New quota >= sum of resources currently in use in this project.
//  2. New quota + other-projects' allocations <= tenant cap.
//
// Quota dimensions omitted from the request keep their current value.
func (h *ProjectHandler) Patch(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	if !requireAction(w, r, h.repo, rbac.ActionProjectWrite) {
		return
	}
	projectID := chi.URLParam(r, "project_id")

	current, err := h.repo.GetProject(r.Context(), tenantID, projectID)
	if err != nil {
		h.log.Error().Err(err).Msg("get project for patch")
		writeError(w, http.StatusInternalServerError, "failed to load project")
		return
	}
	if current == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	var req updateProjectQuotaRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	// Merge: omitted fields keep current value.
	newQuota := models.TenantCap{
		CPUCores:  current.CPUCores,
		MemoryGB:  current.MemoryGB,
		StorageGB: current.StorageGB,
	}
	if req.CPUCores != nil {
		newQuota.CPUCores = *req.CPUCores
	}
	if req.MemoryGB != nil {
		newQuota.MemoryGB = *req.MemoryGB
	}
	if req.StorageGB != nil {
		newQuota.StorageGB = *req.StorageGB
	}

	if newQuota.CPUCores < 1 || newQuota.MemoryGB < 1 || newQuota.StorageGB < 1 {
		writeError(w, http.StatusBadRequest, "cpu_cores, memory_gb, storage_gb must each be >= 1")
		return
	}

	p, usage, err := h.repo.UpdateProjectQuota(r.Context(), current.ProjectUUID, newQuota)
	if err != nil {
		switch {
		case errors.Is(err, db.ErrProjectQuotaBelowUsage):
			// usage.Cap holds the requested values; usage.Allocated holds the in-use sums.
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"error":     "quota_below_usage",
				"message":   "project quota cannot be shrunk below resources currently in use",
				"requested": newQuota,
				"in_use":    usage.Allocated,
			})
		case errors.Is(err, db.ErrProjectQuotaExceedsTenantCap):
			writeQuotaExceeded(w, "project quota would exceed tenant cap", usage, newQuota)
		default:
			h.log.Error().Err(err).Str("project", projectID).Msg("update project quota")
			writeError(w, http.StatusInternalServerError, "failed to update project quota")
		}
		return
	}

	// Update the namespace's k8s ResourceQuota to mirror the new project caps.
	// Best-effort: log on failure but commit the DB change. Operators can
	// re-run via PATCH or kubectl directly. (TODO: surface a follow-up.)
	if h.nsProvisioner != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			currentQuota, _ := h.repo.GetProjectQuota(ctx, p.ProjectUUID)
			maxVolumes := 0
			if currentQuota != nil {
				maxVolumes = currentQuota.MaxVolumes
			}
			if rqErr := h.nsProvisioner.EnsureProjectNamespace(ctx, tenantID, projectID, p.ProjectUUID, p.CPUCores, p.MemoryGB, p.StorageGB, maxVolumes); rqErr != nil {
				h.log.Warn().Err(rqErr).Str("project", projectID).
					Msg("ResourceQuota update after PATCH failed; DB row updated but namespace quota lags")
			}
		}()
	}

	q, _ := h.repo.GetProjectQuota(r.Context(), p.ProjectUUID)
	writeJSON(w, http.StatusOK, projectToResponse(p, q))
}

// lookupProjectUUID extracts project ID and UUID from the request context.
// Returns (id, uuid, true) when the ProjectContext middleware has run.
// Returns ("", uuid.Nil, false) silently when no project context exists —
// callers that need project context mandatory should use requireProjectUUID.
func lookupProjectUUID(w http.ResponseWriter, r *http.Request) (string, uuid.UUID, bool) {
	projectID, ok1 := middleware.ProjectFromContext(r.Context())
	projectUUID, ok2 := middleware.ProjectUUIDFromContext(r.Context())
	if !ok1 || !ok2 {
		return "", uuid.Nil, false
	}
	return projectID, projectUUID, true
}

// requireProjectUUID is like lookupProjectUUID but writes a 500 error and
// returns false when project context is missing. Use for handlers that are only
// reachable via the project-scoped route (after ProjectContext middleware runs).
func requireProjectUUID(w http.ResponseWriter, r *http.Request) (string, uuid.UUID, bool) {
	projectID, ok1 := middleware.ProjectFromContext(r.Context())
	projectUUID, ok2 := middleware.ProjectUUIDFromContext(r.Context())
	if !ok1 || !ok2 {
		writeError(w, http.StatusInternalServerError, "no project in context")
		return "", uuid.Nil, false
	}
	return projectID, projectUUID, true
}
