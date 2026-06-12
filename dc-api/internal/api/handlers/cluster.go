// Package handlers — Cluster handler.
//
// ClusterHandler follows the exact same pattern as VMHandler.
// Reading vm.go first will help you understand this file.
//
// Notable difference: cluster creation triggers Rancher (not Harvester).
// The handler itself is identical in structure — this is the Strategy Pattern
// in action. The handler code says "create a cluster" and the injected
// ClusterProvider (Rancher) handles the backend-specific details.
//
// Node pool management (R5):
//
//	POST   .../clusters/{id}/node-pools          — AddNodePool
//	GET    .../clusters/{id}/node-pools          — ListNodePools
//	PATCH  .../clusters/{id}/node-pools/{name}   — ScaleNodePool or UpdateNodePoolTaintsLabels
//	DELETE .../clusters/{id}/node-pools/{name}   — RemoveNodePool
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/wso2/dc-api/internal/api/middleware"
	"github.com/wso2/dc-api/internal/db"
	"github.com/wso2/dc-api/internal/models"
	"github.com/wso2/dc-api/internal/providers"
	"github.com/wso2/dc-api/internal/providers/common"
	"github.com/wso2/dc-api/internal/rbac"
)

// ClusterHandler handles all /v1/clusters endpoints.
type ClusterHandler struct {
	repo     *db.Repository
	provider providers.ClusterProvider
	log      zerolog.Logger
}

// NewClusterHandler creates a ClusterHandler with injected dependencies.
func NewClusterHandler(repo *db.Repository, provider providers.ClusterProvider, log zerolog.Logger) *ClusterHandler {
	return &ClusterHandler{repo: repo, provider: provider, log: log}
}

// ─────────────────────────── Request / Response DTOs ────────────────────────

// CreateClusterRequest is the JSON body for POST /v1/clusters.
//
// Network attachment is mutually exclusive:
//   - Legacy bridge path: set NetworkName only.
//   - VPC path (F32):     set VNetID + SubnetID only.
//
// Providing both or neither results in a 400.
//
// WorkerPools is optional: one or more worker pools to provision at the same
// time as the cluster. Each follows AddNodePoolRequest validation rules. Worker
// pools can also be added after cluster creation via POST .../node-pools.
type CreateClusterRequest struct {
	Name        string               `json:"name"`
	K8sVersion  string               `json:"k8s_version"`
	ImageName   string               `json:"image_name"`
	SystemPool  *SystemPoolSpec      `json:"system_pool"`
	WorkerPools []AddNodePoolRequest `json:"worker_pools,omitempty"` // optional; max 10
	NetworkName string               `json:"network_name"`            // legacy bridge — mutually exclusive with VNetID+SubnetID
	VNetID      string               `json:"vnet_id"`                 // F32 VPC path — mutually exclusive with NetworkName
	SubnetID    string               `json:"subnet_id"`               // F32 VPC path — must accompany VNetID
}

// SystemPoolSpec is the create-time spec for the cluster's system node pool.
// Name and role are server-set ("system" and cp+etcd); only size, count, and
// optional disk_gb are user-supplied. Count must be 1, 3, or 5 (etcd quorum).
type SystemPoolSpec struct {
	Size   string `json:"size"`
	Count  int    `json:"count"`
	DiskGB int    `json:"disk_gb,omitempty"`
}

func (r *CreateClusterRequest) validate() error {
	if r.Name == "" {
		return errors.New("name is required")
	}
	if err := validateResourceName(r.Name); err != nil {
		return err
	}
	if r.K8sVersion == "" {
		return errors.New("k8s_version is required (e.g., v1.33.10+rke2r3)")
	}
	if r.SystemPool == nil {
		return errors.New("system_pool is required")
	}
	if _, ok := models.Sizes[r.SystemPool.Size]; !ok {
		return fmt.Errorf("system_pool.size must be one of: %s", strings.Join(models.ValidSizeNames(), ", "))
	}
	switch r.SystemPool.Count {
	case 1, 3, 5:
		// ok — etcd quorum constraints
	default:
		return errors.New("system_pool.count must be 1, 3, or 5 (etcd quorum)")
	}
	if r.ImageName == "" {
		return errors.New("image_name is required")
	}

	hasLegacy := r.NetworkName != ""
	hasVPC := r.VNetID != "" || r.SubnetID != ""
	if hasLegacy && hasVPC {
		return errors.New("use either network_name OR vnet_id+subnet_id, not both")
	}
	if !hasLegacy && !hasVPC {
		return errors.New("network_name or vnet_id+subnet_id is required")
	}
	if hasVPC {
		if r.VNetID == "" {
			return errors.New("vnet_id is required when subnet_id is set")
		}
		if r.SubnetID == "" {
			return errors.New("subnet_id is required when vnet_id is set")
		}
		if _, err := uuid.Parse(r.VNetID); err != nil {
			return errors.New("vnet_id must be a valid UUID")
		}
		if _, err := uuid.Parse(r.SubnetID); err != nil {
			return errors.New("subnet_id must be a valid UUID")
		}
	}

	// Validate optional worker pools.
	if len(r.WorkerPools) > 10 {
		return fmt.Errorf("worker_pools: at most 10 pools may be specified at create time (got %d)", len(r.WorkerPools))
	}
	seenNames := make(map[string]struct{}, len(r.WorkerPools))
	for i, wp := range r.WorkerPools {
		if err := wp.validate(); err != nil {
			return fmt.Errorf("worker_pools[%d] (%q): %w", i, wp.Name, err)
		}
		if _, dup := seenNames[wp.Name]; dup {
			return fmt.Errorf("worker_pools: duplicate pool name %q", wp.Name)
		}
		seenNames[wp.Name] = struct{}{}
	}

	return nil
}

// ClusterResponse is the JSON response for cluster operations.
type ClusterResponse struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Status          string            `json:"status"`
	TenantID        string            `json:"tenant_id"`
	ProviderType    string            `json:"provider_type"`
	Message         string            `json:"message,omitempty"`
	CreatedAt       string            `json:"created_at"`
	SystemPool      *NodePoolResponse `json:"system_pool,omitempty"`
	WorkerPoolCount int               `json:"worker_pool_count"`
	TotalNodeCount  int               `json:"total_node_count"`
}

// clusterToResponse builds the wire response from a Resource row plus its
// pool aggregates. The system pool is optional — if the system_pool row hasn't
// been persisted yet (very brief window during create), the response omits it.
func clusterToResponse(r *models.Resource, systemPool *models.NodePool, workerCount, totalNodes int) ClusterResponse {
	resp := ClusterResponse{
		ID:              r.ID.String(),
		Name:            r.Name,
		Status:          string(r.Status),
		TenantID:        r.TenantID,
		ProviderType:    r.ProviderType,
		Message:         r.Message,
		CreatedAt:       r.CreatedAt.Format(time.RFC3339),
		WorkerPoolCount: workerCount,
		TotalNodeCount:  totalNodes,
	}
	if systemPool != nil {
		sp := poolToResponse(systemPool)
		resp.SystemPool = &sp
	}
	return resp
}

// ── Node pool DTOs ────────────────────────────────────────────────────────────

// AddNodePoolRequest is the JSON body for POST .../node-pools.
type AddNodePoolRequest struct {
	// Name is a DNS-label for the pool within this cluster (≤40 chars).
	Name string `json:"name"`
	// Size is the node size ("small" | "medium" | "large" | "xlarge").
	Size string `json:"size"`
	// Count is the desired replica count (1..50 for workers).
	Count int `json:"count"`
	// DiskGB overrides the size default. Omit or 0 to use the catalog default.
	DiskGB int `json:"disk_gb,omitempty"`
	// ImageName is the Harvester VM image to use for nodes in this pool.
	// Omit to reuse the image already set in the cluster's system pool.
	// Format: "namespace/image-name" (same as CreateCluster.image_name).
	ImageName string `json:"image_name,omitempty"`
	// Taints is the optional list of Kubernetes taints applied to every node.
	Taints []models.NodePoolTaint `json:"taints,omitempty"`
	// Labels is the optional set of Kubernetes node labels.
	Labels map[string]string `json:"labels,omitempty"`
}

var poolNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,38}[a-z0-9]$|^[a-z]$`)

func (r *AddNodePoolRequest) validate() error {
	if r.Name == "" {
		return errors.New("name is required")
	}
	if r.Name == "system" {
		return errors.New("pool name 'system' is reserved; choose a different name")
	}
	if !poolNameRE.MatchString(r.Name) {
		return errors.New("name must be 1-40 lowercase alphanumeric characters or hyphens, starting with a letter")
	}
	if _, ok := models.Sizes[r.Size]; !ok {
		return fmt.Errorf("size must be one of: %s", strings.Join(models.ValidSizeNames(), ", "))
	}
	if r.Count < 1 || r.Count > 50 {
		return errors.New("count must be between 1 and 50")
	}
	for _, t := range r.Taints {
		if !isValidTaintEffect(t.Effect) {
			return fmt.Errorf("taint effect %q must be NoSchedule, PreferNoSchedule, or NoExecute", t.Effect)
		}
	}
	return nil
}

// PatchNodePoolRequest is the JSON body for PATCH .../node-pools/{name}.
// All fields are optional; only set fields are applied.
type PatchNodePoolRequest struct {
	// Count, if > 0, triggers a scale operation.
	Count int `json:"count,omitempty"`
	// Taints, if non-nil (even empty slice), replaces the pool's taint set.
	Taints *[]models.NodePoolTaint `json:"taints,omitempty"`
	// Labels, if non-nil (even empty map), replaces the pool's label set.
	Labels *map[string]string `json:"labels,omitempty"`
}

func (r *PatchNodePoolRequest) validate() error {
	if r.Count < 0 {
		return errors.New("count must be a positive integer when set")
	}
	if r.Count > 50 {
		return errors.New("count must be between 1 and 50")
	}
	if r.Taints != nil {
		for _, t := range *r.Taints {
			if !isValidTaintEffect(t.Effect) {
				return fmt.Errorf("taint effect %q must be NoSchedule, PreferNoSchedule, or NoExecute", t.Effect)
			}
		}
	}
	return nil
}

// NodePoolResponse is the JSON shape for a single pool in list/get responses.
type NodePoolResponse struct {
	ID          string                 `json:"id"`
	Name        string                 `json:"name"`
	Role        string                 `json:"role"`
	Size        string                 `json:"size"`
	Count       int                    `json:"count"`
	DiskGB      *int                   `json:"disk_gb,omitempty"`
	Taints      []models.NodePoolTaint `json:"taints,omitempty"`
	Labels      map[string]string      `json:"labels,omitempty"`
	Status      string                 `json:"status"`
	Message     string                 `json:"message,omitempty"`
	CreatedAt   string                 `json:"created_at"`
}

func poolToResponse(p *models.NodePool) NodePoolResponse {
	return NodePoolResponse{
		ID:        p.ID.String(),
		Name:      p.Name,
		Role:      string(p.Role),
		Size:      p.Size,
		Count:     p.Count,
		DiskGB:    p.DiskGB,
		Taints:    p.Taints,
		Labels:    p.Labels,
		Status:    string(p.Status),
		Message:   p.Message,
		CreatedAt: p.CreatedAt.Format(time.RFC3339),
	}
}

func isValidTaintEffect(e string) bool {
	switch e {
	case "NoSchedule", "PreferNoSchedule", "NoExecute":
		return true
	}
	return false
}

// ─────────────────────────── Handlers ───────────────────────────────────────

// Create handles POST .../clusters.
//
// Flow:
//  1. Validate request.
//  2. Check cluster quota for tenant.
//  3. Create PENDING record in PostgreSQL.
//  4. Pre-generate HarvesterConfigName and persist the system pool row.
//  5. Call Rancher async to provision the RKE2 cluster.
//  6. Return 202 Accepted.
func (h *ClusterHandler) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	if !requireAction(w, r, h.repo, rbac.ActionClusterWrite) {
		return
	}
	userID, _ := middleware.UserFromContext(r.Context())

	var req CreateClusterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := req.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
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

	// Quota check
	quota, err := h.repo.GetQuota(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "quota check failed")
		return
	}
	count, err := h.repo.CountByTenant(r.Context(), tenantUUID, models.ResourceTypeCluster)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "quota check failed")
		return
	}
	if count >= quota.MaxClusters {
		writeError(w, http.StatusForbidden,
			fmt.Sprintf("cluster quota exceeded: %d/%d clusters in use", count, quota.MaxClusters))
		return
	}

	// Resolve system-pool node size → concrete numbers.
	nodeSize := models.Sizes[req.SystemPool.Size]
	nodeDiskGB := req.SystemPool.DiskGB
	if nodeDiskGB == 0 {
		nodeDiskGB = nodeSize.DefaultDiskGB
	}

	// Resolve VPC attachment (F32): when vnet_id + subnet_id are present,
	// look up both rows to obtain their backend CRD names before writing the
	// resource row. This mirrors the VM handler's approach exactly so a
	// failed lookup returns 4xx without creating an orphan resource row.
	var vnetBackendUID, subnetBackendUID string
	var vnetUUIDPtr, subnetUUIDPtr *uuid.UUID // F41: persisted on the Resource row when VPC path is used
	if req.VNetID != "" {
		vnetUUID, _ := uuid.Parse(req.VNetID)
		subnetUUID, _ := uuid.Parse(req.SubnetID)

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

		vnetBackendUID = vnet.BackendUID
		subnetBackendUID = subnet.BackendUID
		vnetUUIDPtr = &vnetUUID
		subnetUUIDPtr = &subnetUUID
	}

	// Create PENDING record
	projectID, projectUUID, _ := lookupProjectUUID(w, r)
	resource, err := h.repo.Create(r.Context(), &models.Resource{
		TenantID:     tenantID,
		TenantUUID:   tenantUUID,
		ProjectID:    projectID,
		ProjectUUID:  projectUUID,
		OwnerID:      userID,
		Name:         req.Name,
		Type:         models.ResourceTypeCluster,
		Size:         req.SystemPool.Size,
		Status:       models.StatusPending,
		VNetID:       vnetUUIDPtr,
		SubnetID:     subnetUUIDPtr,
		ProviderType: h.provider.Name(),
	})
	if err != nil {
		h.log.Error().Err(err).Msg("create cluster resource in DB")
		writeError(w, http.StatusInternalServerError, "failed to register cluster resource")
		return
	}


	// Pre-generate the HarvesterConfig CR name so the provider can use it
	// deterministically and the reconciler knows which CR to cascade-clean.
	// Persist the system pool row now so the pool is visible even while the
	// cluster is PENDING.
	hcName := common.HarvesterConfigName(req.Name, "system")
	diskGBCopy := nodeDiskGB
	systemPool := &models.NodePool{
		ClusterID:           resource.ID,
		Name:                "system",
		Role:                models.NodePoolRoleSystem,
		Size:                req.SystemPool.Size,
		Count:               req.SystemPool.Count,
		DiskGB:              &diskGBCopy,
		HarvesterConfigName: hcName,
		Status:              models.NodePoolStatusProvisioning,
	}
	if err := h.repo.CreateNodePool(r.Context(), systemPool); err != nil {
		// Pool row creation failure is non-fatal: the cluster resource row
		// was already committed. Log and continue — the pool row can be
		// backfilled by the reconciler when it sees a healthy cluster.
		h.log.Error().Err(err).Str("cluster", req.Name).Msg("persist system pool row")
	}

	// Persist each worker pool row and generate HarvesterConfigNames.
	// TODO(quota): per-pool node count quota enforcement is not implemented here;
	// only the cluster-level quota (max_clusters) is enforced today. Add per-project
	// node count enforcement when project-level compute quota is implemented.
	workerPools := make([]*models.NodePool, 0, len(req.WorkerPools))
	totalWorkerNodeCount := 0
	for i := range req.WorkerPools {
		wp := &req.WorkerPools[i]
		wpHCName := common.HarvesterConfigName(req.Name, wp.Name)
		wpDiskGB := wp.DiskGB
		if wpDiskGB == 0 {
			wpDiskGB = models.Sizes[wp.Size].DefaultDiskGB
		}
		wpDiskCopy := wpDiskGB
		pool := &models.NodePool{
			ClusterID:           resource.ID,
			Name:                wp.Name,
			Role:                models.NodePoolRoleWorker,
			Size:                wp.Size,
			Count:               wp.Count,
			DiskGB:              &wpDiskCopy,
			Taints:              wp.Taints,
			Labels:              wp.Labels,
			HarvesterConfigName: wpHCName,
			Status:              models.NodePoolStatusProvisioning,
		}
		if err := h.repo.CreateNodePool(r.Context(), pool); err != nil {
			// Non-fatal: the cluster row is already committed. Log and continue;
			// the reconciler can backfill the pool row when the cluster becomes Active.
			h.log.Error().Err(err).
				Str("cluster", req.Name).
				Str("pool", wp.Name).
				Msg("persist worker pool row at create time")
			continue
		}
		workerPools = append(workerPools, pool)
		totalWorkerNodeCount += wp.Count
	}

	spec := models.ClusterSpec{
		Name:             req.Name,
		K8sVersion:       req.K8sVersion,
		ImageName:        req.ImageName,
		NetworkName:      req.NetworkName,
		VNetBackendUID:   vnetBackendUID,
		SubnetBackendUID: subnetBackendUID,
		SystemPool: &models.NodePool{
			Name:                "system",
			Role:                models.NodePoolRoleSystem,
			Size:                req.SystemPool.Size,
			Count:               req.SystemPool.Count,
			DiskGB:              &diskGBCopy,
			HarvesterConfigName: hcName,
		},
		WorkerPools: workerPools,
	}
	go h.asyncProvision(resource.ID, tenantID, projectID, userID, spec)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"resource": clusterToResponse(resource, systemPool, len(workerPools), req.SystemPool.Count+totalWorkerNodeCount),
		"note":     "Cluster is being provisioned. Poll GET /v1/clusters/" + resource.ID.String() + " for status.",
	})
}

// Get handles GET .../clusters/{id}.
func (h *ClusterHandler) Get(w http.ResponseWriter, r *http.Request) {
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

	rawID := chi.URLParam(r, "id")
	id, err := uuid.Parse(rawID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid resource ID format")
		return
	}

	resource, err := h.repo.GetForProject(r.Context(), id, tenantUUID, projectUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "cluster not found")
		return
	}

	systemPool, _ := h.repo.GetNodePool(r.Context(), resource.ID, "system")
	workerCount, totalNodes, _ := h.repo.CountNodePools(r.Context(), resource.ID)
	writeJSON(w, http.StatusOK, clusterToResponse(resource, systemPool, workerCount, totalNodes))
}

// GetKubeconfig handles GET .../clusters/{id}/kubeconfig.
// Returns the kubeconfig YAML for an active cluster.
func (h *ClusterHandler) GetKubeconfig(w http.ResponseWriter, r *http.Request) {
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

	rawID := chi.URLParam(r, "id")
	id, err := uuid.Parse(rawID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid resource ID format")
		return
	}

	resource, err := h.repo.GetForProject(r.Context(), id, tenantUUID, projectUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "cluster not found")
		return
	}

	if resource.Status != models.StatusActive {
		writeError(w, http.StatusConflict, "cluster is not yet active (status: "+string(resource.Status)+")")
		return
	}

	kubeconfig, err := h.provider.GetKubeconfig(r.Context(), resource.BackendUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to retrieve kubeconfig")
		return
	}

	// Return as plain text — callers pipe this directly to a file.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, kubeconfig)
}

// List handles GET .../clusters.
func (h *ClusterHandler) List(w http.ResponseWriter, r *http.Request) {
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

	resources, err := h.repo.ListByProject(r.Context(), tenantUUID, projectUUID, models.ResourceTypeCluster)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list clusters")
		return
	}

	responses := make([]ClusterResponse, 0, len(resources))
	for _, res := range resources {
		systemPool, _ := h.repo.GetNodePool(r.Context(), res.ID, "system")
		workerCount, totalNodes, _ := h.repo.CountNodePools(r.Context(), res.ID)
		responses = append(responses, clusterToResponse(res, systemPool, workerCount, totalNodes))
	}
	writeJSON(w, http.StatusOK, responses)
}

// Delete handles DELETE .../clusters/{id}.
func (h *ClusterHandler) Delete(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionClusterDelete) {
		return
	}

	rawID := chi.URLParam(r, "id")
	id, err := uuid.Parse(rawID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid resource ID format")
		return
	}

	resource, err := h.repo.GetForProject(r.Context(), id, tenantUUID, projectUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "cluster not found")
		return
	}

	_ = h.repo.UpdateStatus(r.Context(), id, models.StatusDeleting, "deletion requested", "")

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := h.provider.DeleteCluster(ctx, resource.BackendUID); err != nil {
			h.log.Error().Err(err).Str("cluster", resource.Name).Msg("delete cluster failed")
			_ = h.repo.UpdateStatus(ctx, id, models.StatusFailed, "deletion failed: "+err.Error(), "")
		}
	}()

	w.WriteHeader(http.StatusAccepted)
}

// ─────────────────────────── Node pool handlers ─────────────────────────────

// ListNodePools handles GET .../clusters/{id}/node-pools.
func (h *ClusterHandler) ListNodePools(w http.ResponseWriter, r *http.Request) {
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}

	clusterID, clusterName, ok := h.resolveCluster(w, r, tenantUUID)
	if !ok {
		return
	}
	_ = clusterName

	pools, err := h.repo.ListNodePools(r.Context(), clusterID)
	if err != nil {
		h.log.Error().Err(err).Str("cluster_id", clusterID.String()).Msg("list node pools")
		writeError(w, http.StatusInternalServerError, "failed to list node pools")
		return
	}

	resp := make([]NodePoolResponse, 0, len(pools))
	for i := range pools {
		resp = append(resp, poolToResponse(&pools[i]))
	}
	writeJSON(w, http.StatusOK, resp)
}

// AddNodePool handles POST .../clusters/{id}/node-pools.
//
// Flow:
//  1. Validate request.
//  2. Refuse if cluster is not ACTIVE.
//  3. Refuse if pool name already exists.
//  4. Pre-generate HarvesterConfigName, persist pool row (status=provisioning).
//  5. Call provider.AddNodePool async.
//  6. Return 202 Accepted.
func (h *ClusterHandler) AddNodePool(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	if !requireAction(w, r, h.repo, rbac.ActionClusterWrite) {
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

	var req AddNodePoolRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := req.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	clusterID, clusterName, ok := h.resolveCluster(w, r, tenantUUID)
	if !ok {
		return
	}

	// Cluster must be ACTIVE before we can add a pool.
	resource, err := h.repo.GetForProject(r.Context(), clusterID, tenantUUID, projectUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "cluster not found")
		return
	}
	if resource.Status != models.StatusActive {
		writeError(w, http.StatusConflict, "cluster must be ACTIVE before adding node pools (status: "+string(resource.Status)+")")
		return
	}

	// Resolve node size.
	nodeSize := models.Sizes[req.Size]
	diskGB := req.DiskGB
	if diskGB == 0 {
		diskGB = nodeSize.DefaultDiskGB
	}

	// Pre-generate HarvesterConfig name and persist pool row.
	hcName := common.HarvesterConfigName(clusterName, req.Name)
	diskGBCopy := diskGB
	pool := &models.NodePool{
		ClusterID:           clusterID,
		Name:                req.Name,
		Role:                models.NodePoolRoleWorker,
		Size:                req.Size,
		Count:               req.Count,
		DiskGB:              &diskGBCopy,
		Taints:              req.Taints,
		Labels:              req.Labels,
		HarvesterConfigName: hcName,
		Status:              models.NodePoolStatusProvisioning,
	}

	if err := h.repo.CreateNodePool(r.Context(), pool); err != nil {
		if errors.Is(err, db.ErrNodePoolAlreadyExists) {
			writeError(w, http.StatusConflict, "a pool named "+req.Name+" already exists in this cluster")
			return
		}
		h.log.Error().Err(err).Str("cluster", clusterName).Str("pool", req.Name).Msg("persist node pool row")
		writeError(w, http.StatusInternalServerError, "failed to create node pool")
		return
	}

	// Retrieve additional context for the provider call (NAD names etc.).
	// The mgmtNAD and tenantSubnetNAD are resolved from the cluster's VPC
	// attachment. If the cluster was on the legacy bridge path (no VPC), both
	// are empty and the provider falls back to the bridge settings.
	mgmtNAD, tenantSubnetNAD, vmNamespace := h.resolveClusterNetworkContext(r.Context(), resource, tenantID)

	go h.asyncAddPool(clusterID, clusterName, pool, mgmtNAD, tenantSubnetNAD, vmNamespace, req.ImageName)

	writeJSON(w, http.StatusAccepted, poolToResponse(pool))
}

// GetNodePool handles GET .../clusters/{id}/node-pools/{pool_name}.
func (h *ClusterHandler) GetNodePool(w http.ResponseWriter, r *http.Request) {
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}

	poolName := chi.URLParam(r, "pool_name")
	if poolName == "" {
		writeError(w, http.StatusBadRequest, "pool_name is required")
		return
	}

	clusterID, _, ok := h.resolveCluster(w, r, tenantUUID)
	if !ok {
		return
	}

	pool, err := h.repo.GetNodePool(r.Context(), clusterID, poolName)
	if err != nil {
		if errors.Is(err, db.ErrNodePoolNotFound) {
			writeError(w, http.StatusNotFound, "pool not found")
			return
		}
		h.log.Error().Err(err).Str("pool", poolName).Msg("get node pool")
		writeError(w, http.StatusInternalServerError, "failed to get pool")
		return
	}

	writeJSON(w, http.StatusOK, poolToResponse(pool))
}

// ScaleOrUpdateNodePool handles PATCH .../clusters/{id}/node-pools/{pool_name}.
//
// If the request contains count it's a scale. If it contains taints/labels it
// updates those. If it contains both it does both in a single PUT to Rancher.
// System pool taints/labels are refused (system pool must remain taint-free so
// the cluster can always schedule system workloads).
func (h *ClusterHandler) ScaleOrUpdateNodePool(w http.ResponseWriter, r *http.Request) {
	_, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	if !requireAction(w, r, h.repo, rbac.ActionClusterWrite) {
		return
	}

	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}

	var req PatchNodePoolRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := req.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Count == 0 && req.Taints == nil && req.Labels == nil {
		writeError(w, http.StatusBadRequest, "at least one of count, taints, or labels must be provided")
		return
	}

	poolName := chi.URLParam(r, "pool_name")
	if poolName == "" {
		writeError(w, http.StatusBadRequest, "pool_name is required")
		return
	}

	clusterID, clusterName, ok := h.resolveCluster(w, r, tenantUUID)
	if !ok {
		return
	}

	// Refuse taint/label changes on the system pool.
	if poolName == "system" && (req.Taints != nil || req.Labels != nil) {
		writeError(w, http.StatusForbidden, "taints and labels cannot be set on the system pool")
		return
	}

	// Fetch existing pool row.
	pool, err := h.repo.GetNodePool(r.Context(), clusterID, poolName)
	if err != nil {
		if errors.Is(err, db.ErrNodePoolNotFound) {
			writeError(w, http.StatusNotFound, "pool not found")
			return
		}
		h.log.Error().Err(err).Str("cluster", clusterName).Str("pool", poolName).Msg("get node pool")
		writeError(w, http.StatusInternalServerError, "failed to get pool")
		return
	}

	// Enforce system-pool count invariants: must be in {1, 3, 5} and may only
	// grow by +2 (1->3, 3->5). Etcd quorum cannot tolerate shrinks; Rancher's
	// provisioner does not coordinate etcd member removal.
	if poolName == "system" && req.Count > 0 && req.Count != pool.Count {
		switch {
		case pool.Count == 1 && req.Count == 3:
		case pool.Count == 3 && req.Count == 5:
		default:
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("system pool count may only grow from %d (allowed transitions: 1->3, 3->5)", pool.Count))
			return
		}
	}

	// Apply requested mutations to the in-memory pool.
	newCount := pool.Count
	if req.Count > 0 {
		newCount = req.Count
		pool.Count = newCount
	}
	newTaints := pool.Taints
	if req.Taints != nil {
		newTaints = *req.Taints
		pool.Taints = newTaints
	}
	newLabels := pool.Labels
	if req.Labels != nil {
		newLabels = *req.Labels
		pool.Labels = newLabels
	}

	// Determine new status.
	newStatus := models.NodePoolStatusScaling
	if req.Count == 0 {
		newStatus = pool.Status // taint/label only — keep existing status
	}
	pool.Status = newStatus

	if err := h.repo.UpdateNodePool(r.Context(), pool); err != nil {
		h.log.Error().Err(err).Str("cluster", clusterName).Str("pool", poolName).Msg("update node pool in DB")
		writeError(w, http.StatusInternalServerError, "failed to update pool")
		return
	}

	// Async: apply changes to Rancher.
	go h.asyncPatchPool(clusterName, pool, req.Count > 0, newCount, req.Taints != nil || req.Labels != nil, newTaints, newLabels)

	writeJSON(w, http.StatusAccepted, poolToResponse(pool))
}

// RemoveNodePool handles DELETE .../clusters/{id}/node-pools/{pool_name}.
//
// Refused for the system pool. Worker pool removal is async: the row is marked
// deleting immediately, the goroutine calls provider.RemoveNodePool which drains
// then removes the pool from the Rancher CR, then deletes the DB row.
func (h *ClusterHandler) RemoveNodePool(w http.ResponseWriter, r *http.Request) {
	_, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	if !requireAction(w, r, h.repo, rbac.ActionClusterWrite) {
		return
	}

	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}

	poolName := chi.URLParam(r, "pool_name")
	if poolName == "" {
		writeError(w, http.StatusBadRequest, "pool_name is required")
		return
	}
	if poolName == "system" {
		writeError(w, http.StatusForbidden, "the system pool cannot be removed; delete the entire cluster instead")
		return
	}

	clusterID, clusterName, ok := h.resolveCluster(w, r, tenantUUID)
	if !ok {
		return
	}

	pool, err := h.repo.GetNodePool(r.Context(), clusterID, poolName)
	if err != nil {
		if errors.Is(err, db.ErrNodePoolNotFound) {
			writeError(w, http.StatusNotFound, "pool not found")
			return
		}
		h.log.Error().Err(err).Str("cluster", clusterName).Str("pool", poolName).Msg("get node pool for delete")
		writeError(w, http.StatusInternalServerError, "failed to get pool")
		return
	}

	// Mark deleting immediately so the UI shows feedback.
	pool.Status = models.NodePoolStatusDeleting
	if err := h.repo.UpdateNodePool(r.Context(), pool); err != nil {
		h.log.Error().Err(err).Str("pool", poolName).Msg("mark pool deleting")
	}

	go h.asyncRemovePool(clusterID, clusterName, pool)

	w.WriteHeader(http.StatusAccepted)
}

// ─────────────────────────── Async helpers ──────────────────────────────────

func (h *ClusterHandler) asyncProvision(resourceID uuid.UUID, tenantID, projectID, userID string, spec models.ClusterSpec) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	providerResource, err := h.provider.CreateCluster(ctx, tenantID, projectID, spec)
	if err != nil {
		h.log.Error().Err(err).Str("cluster", spec.Name).Msg("rancher CreateCluster failed")
		_ = h.repo.UpdateStatus(ctx, resourceID, models.StatusFailed,
			"provisioning failed: "+err.Error(), "")
		// Mark system pool failed too.
		if sysPool, pErr := h.repo.GetNodePool(ctx, resourceID, "system"); pErr == nil {
			sysPool.Status = models.NodePoolStatusFailed
			sysPool.Message = err.Error()
			_ = h.repo.UpdateNodePool(ctx, sysPool)
		}
		return
	}

	_ = h.repo.UpdateStatus(ctx, resourceID, models.StatusPending,
		"provisioning submitted to Rancher", providerResource.BackendUID)
}

// asyncAddPool calls provider.AddNodePool and updates the pool row on completion.
func (h *ClusterHandler) asyncAddPool(
	clusterID uuid.UUID,
	clusterName string,
	pool *models.NodePool,
	mgmtNAD, tenantSubnetNAD, vmNamespace, nodeImage string,
) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if err := h.provider.AddNodePool(ctx, clusterName, pool, mgmtNAD, tenantSubnetNAD, vmNamespace, nodeImage); err != nil {
		h.log.Error().Err(err).Str("cluster", clusterName).Str("pool", pool.Name).Msg("AddNodePool failed")
		// Fetch fresh pool row to avoid stale update.
		if p, gErr := h.repo.GetNodePool(ctx, clusterID, pool.Name); gErr == nil {
			p.Status = models.NodePoolStatusFailed
			p.Message = err.Error()
			_ = h.repo.UpdateNodePool(ctx, p)
		}
	}
	// On success the reconciler will flip the pool to ready when Rancher reports it.
}

// asyncPatchPool applies scale / taint / label changes to Rancher.
func (h *ClusterHandler) asyncPatchPool(
	clusterName string,
	pool *models.NodePool,
	doScale bool, newCount int,
	doTL bool, taints []models.NodePoolTaint, labels map[string]string,
) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if doScale {
		if err := h.provider.ScaleNodePool(ctx, clusterName, pool.Name, newCount); err != nil {
			h.log.Error().Err(err).Str("cluster", clusterName).Str("pool", pool.Name).Msg("ScaleNodePool failed")
			if p, gErr := h.repo.GetNodePool(ctx, pool.ClusterID, pool.Name); gErr == nil {
				p.Status = models.NodePoolStatusFailed
				p.Message = "scale failed: " + err.Error()
				_ = h.repo.UpdateNodePool(ctx, p)
			}
			return
		}
	}
	if doTL {
		if err := h.provider.UpdateNodePoolTaintsLabels(ctx, clusterName, pool.Name, taints, labels); err != nil {
			h.log.Error().Err(err).Str("cluster", clusterName).Str("pool", pool.Name).Msg("UpdateNodePoolTaintsLabels failed")
			if p, gErr := h.repo.GetNodePool(ctx, pool.ClusterID, pool.Name); gErr == nil {
				p.Status = models.NodePoolStatusFailed
				p.Message = "taint/label update failed: " + err.Error()
				_ = h.repo.UpdateNodePool(ctx, p)
			}
		}
	}
	// On success the reconciler flips the pool back to ready.
}

// asyncRemovePool drains and removes the pool from Rancher, then deletes the DB row.
func (h *ClusterHandler) asyncRemovePool(clusterID uuid.UUID, clusterName string, pool *models.NodePool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if err := h.provider.RemoveNodePool(ctx, clusterName, pool.Name, pool.HarvesterConfigName); err != nil {
		h.log.Error().Err(err).Str("cluster", clusterName).Str("pool", pool.Name).Msg("RemoveNodePool failed")
		if p, gErr := h.repo.GetNodePool(ctx, clusterID, pool.Name); gErr == nil {
			p.Status = models.NodePoolStatusFailed
			p.Message = "removal failed: " + err.Error()
			_ = h.repo.UpdateNodePool(ctx, p)
		}
		return
	}

	// Provider removed the pool from Rancher; now delete the DB row.
	if err := h.repo.DeleteNodePool(ctx, clusterID, pool.Name); err != nil && !errors.Is(err, db.ErrNodePoolNotFound) {
		h.log.Error().Err(err).Str("pool", pool.Name).Msg("delete node pool row after removal")
	}
}

// ─────────────────────────── Internal helpers ────────────────────────────────

// resolveCluster looks up a cluster resource by the {id} URL parameter, validates
// it belongs to the caller's tenant, and returns its ID and name.
// On failure it writes an appropriate error response and returns ok=false.
func (h *ClusterHandler) resolveCluster(w http.ResponseWriter, r *http.Request, tenantUUID uuid.UUID) (clusterID uuid.UUID, clusterName string, ok bool) {
	projectUUID, ok := middleware.ProjectUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "no project UUID in context")
		return uuid.Nil, "", false
	}

	rawID := chi.URLParam(r, "id")
	id, err := uuid.Parse(rawID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid cluster ID format")
		return uuid.Nil, "", false
	}

	resource, err := h.repo.GetForProject(r.Context(), id, tenantUUID, projectUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "cluster not found")
		return uuid.Nil, "", false
	}
	if resource.Type != models.ResourceTypeCluster {
		writeError(w, http.StatusNotFound, "cluster not found")
		return uuid.Nil, "", false
	}

	return id, resource.Name, true
}

// resolveClusterNetworkContext extracts the NAD names and VM namespace used
// when adding a node pool to an existing cluster. This information comes from
// the cluster's VPC attachment (resources.vnet_id / subnet_id) or from the
// legacy bridge path (no VPC). The provider uses these to configure the
// second NIC on each new node VM.
//
// For the legacy bridge path, all three values are empty; the provider falls
// back to its configured DCAPI_CLUSTER_MGMT_NAD / DCAPI_CLUSTER_NETWORK_NAD
// defaults.
func (h *ClusterHandler) resolveClusterNetworkContext(ctx context.Context, resource *models.Resource, tenantID string) (mgmtNAD, tenantSubnetNAD, vmNamespace string) {
	if resource.SubnetID == nil {
		// Legacy bridge path — let the provider use its default config.
		return "", "", ""
	}
	subnet, err := h.repo.GetSubnet(ctx, *resource.SubnetID)
	if err != nil {
		h.log.Warn().Err(err).Str("cluster", resource.Name).Msg("resolveClusterNetworkContext: subnet lookup failed")
		return "", "", ""
	}
	// The NAD name is the subnet's BackendUID in the project namespace.
	// For RKE2-on-VPC, the VMs are in the project namespace derived from the
	// cluster's projectID. The mgmtNAD is empty; the provider picks it from config.
	tenantSubnetNAD = tenantID + "/" + subnet.BackendUID
	vmNamespace = common.NamespaceForProject(resource.TenantID, resource.ProjectID)
	return "", tenantSubnetNAD, vmNamespace
}
