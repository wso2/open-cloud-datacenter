// Package handlers contains the HTTP request handlers for DC-API.
//
// ── DESIGN PATTERN: Dependency Injection (Handler Struct) ─────────────────────
//
// VMHandler is a struct that holds all of its dependencies:
//   - repo:     the ResourceRepository (for PostgreSQL)
//   - provider: the ComputeProvider (for Harvester)
//   - log:      the structured logger
//
// These are injected at startup time (in router.go) via the constructor:
//   h := handlers.NewVMHandler(repo, provider, logger)
//
// Why this matters for testing:
//   In a test, you pass a MockRepository and a MockProvider — no PostgreSQL,
//   no Harvester, no network calls. The handler code is identical.
//   This is the core benefit of Dependency Injection: the handler doesn't
//   hard-code WHERE its dependencies come from; it receives them.
//
// Compare to the anti-pattern (global variables):
//   var DB *pgxpool.Pool  // package-level global
//   var Provider providers.ComputeProvider
//   This makes testing impossible without a real DB and a real Harvester.
package handlers

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
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
	"golang.org/x/crypto/ssh"
)

// VMHandler handles all /v1/virtual-machines endpoints.
type VMHandler struct {
	repo            *db.Repository
	provider        providers.ComputeProvider
	dnsSearchDomain string // from DCAPI_VPC_DNS_SEARCH_DOMAIN; empty = no extra search domain
	// reservedNADs is the set of `namespace/nad-name` references that tenant
	// VMs MUST NOT attach to via the legacy `network_name` path. F21 — these
	// are bridges claimed by KubeOVN's ProviderNetwork or otherwise reserved
	// for cluster infrastructure. Built from DCAPI_INFRA_RESERVED_NADS.
	reservedNADs map[string]bool
	log          zerolog.Logger
}

// NewVMHandler creates a VMHandler with injected dependencies.
// dnsSearchDomain is the optional DNS search domain injected into VPC VMs (F20).
// reservedNADs blocks tenant VM creates on infra-claimed bridges (F21).
func NewVMHandler(
	repo *db.Repository,
	provider providers.ComputeProvider,
	dnsSearchDomain string,
	reservedNADs map[string]bool,
	log zerolog.Logger,
) *VMHandler {
	return &VMHandler{
		repo:            repo,
		provider:        provider,
		dnsSearchDomain: dnsSearchDomain,
		reservedNADs:    reservedNADs,
		log:             log,
	}
}

// ─────────────────────────── Request / Response DTOs ────────────────────────
//
// DTOs (Data Transfer Objects) are separate from domain models.
// The API request/response shape is allowed to differ from the internal model.
// For example: CreateVMRequest does NOT include tenant_id (that comes from the JWT),
// and does NOT include ssh_public_key (DC-API generates it, the caller never provides it).

// CreateVMRequest is the JSON body for POST /v1/virtual-machines.
//
// Network attachment is mutually exclusive:
//   - Legacy bridge path:  set NetworkName only.
//   - VPC path (M2):       set VNetID + SubnetID only.
//
// Providing both or neither results in a 400.
type CreateVMRequest struct {
	Name        string `json:"name"`
	Size        string `json:"size"`        // "small" | "medium" | "large" | "xlarge"
	DiskGB      int    `json:"disk_gb"`     // optional override; 0 → use size default
	ImageName   string `json:"image_name"`
	NetworkName string `json:"network_name"` // legacy bridge — mutually exclusive with VNetID+SubnetID
	VNetID      string `json:"vnet_id"`      // M2 VPC path — mutually exclusive with NetworkName
	SubnetID    string `json:"subnet_id"`    // M2 VPC path — must accompany VNetID
}

func (r *CreateVMRequest) validate() error {
	if r.Name == "" {
		return errors.New("name is required")
	}
	if err := validateResourceName(r.Name); err != nil {
		return err
	}
	if _, ok := models.Sizes[r.Size]; !ok {
		return fmt.Errorf("size must be one of: %s", strings.Join(models.ValidSizeNames(), ", "))
	}
	if r.DiskGB != 0 && r.DiskGB < 10 {
		return errors.New("disk_gb must be at least 10 when specified")
	}
	if r.ImageName == "" {
		return errors.New("image_name is required")
	}

	// Network attachment: exactly one path must be specified.
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
	return nil
}

// VMResponse is the JSON response for VM operations.
type VMResponse struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Size         string `json:"size,omitempty"`
	Status       string `json:"status"`
	TenantID     string `json:"tenant_id"`
	ProviderType string `json:"provider_type"`
	IPAddress    string `json:"ip_address,omitempty"`
	Message      string `json:"message,omitempty"`
	CreatedAt    string `json:"created_at"`
}

func vmToResponse(r *models.Resource) VMResponse {
	return VMResponse{
		ID:           r.ID.String(),
		Name:         r.Name,
		Size:         r.Size,
		Status:       string(r.Status),
		TenantID:     r.TenantID,
		ProviderType: r.ProviderType,
		IPAddress:    r.IPAddress,
		Message:      r.Message,
		CreatedAt:    r.CreatedAt.Format(time.RFC3339),
	}
}

// ─────────────────────────── Handlers ───────────────────────────────────────

// Create handles POST /v1/virtual-machines.
//
// This is an ATOMIC operation from the caller's perspective:
//  1. Validate the request.
//  2. Check quota — reject early if the tenant is at their limit.
//  3. Generate an SSH key pair (DC-API manages the key, caller gets the private key in response).
//  4. Create the resource record in PostgreSQL (status: PENDING).
//  5. Kick off async provisioning in Harvester (background goroutine).
//  6. Return 202 Accepted immediately — the VM is not yet ready.
//
// The caller polls GET /v1/virtual-machines/{id} to track progress.
// When Harvester reports the VM as running, the async worker updates status to ACTIVE.
func (h *VMHandler) Create(w http.ResponseWriter, r *http.Request) {
	// ── Step 0: extract tenant and user from context (set by auth middleware) ──
	tenantID, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	if !requireAction(w, r, h.repo, rbac.ActionVMWrite) {
		return
	}
	userID, _ := middleware.UserFromContext(r.Context())

	// ── Step 1: decode and validate request body ──────────────────────────────
	var req CreateVMRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := req.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// ── F21: refuse legacy-bridge VM creates on infra-claimed NADs ───────────
	// Bridges claimed by KubeOVN's ProviderNetwork (e.g. mgmt-br after F15)
	// reject new bridge-NAD attachments at the OVS layer; the recovery
	// disrupted ARP for every kube-vip-served VIP on the LAN. dc-api
	// short-circuits these requests so tenants get a useful error instead
	// of causing a platform-wide outage.
	if req.NetworkName != "" && h.reservedNADs[req.NetworkName] {
		writeError(w, http.StatusBadRequest, fmt.Sprintf(
			"Network %q is reserved for cluster infrastructure. Tenant VMs must use a VPC — pass vnet_id + subnet_id instead.",
			req.NetworkName))
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

	// ── Step 2: enforce quota ─────────────────────────────────────────────────
	// We check BEFORE hitting Harvester — fail fast, no wasted API calls.
	quota, err := h.repo.GetQuota(r.Context(), tenantID)
	if err != nil {
		h.log.Error().Err(err).Str("tenant", tenantID).Msg("get quota")
		writeError(w, http.StatusInternalServerError, "quota check failed")
		return
	}
	count, err := h.repo.CountByTenant(r.Context(), tenantUUID, models.ResourceTypeVM)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "quota check failed")
		return
	}
	if count >= quota.MaxVMs {
		writeError(w, http.StatusForbidden,
			fmt.Sprintf("VM quota exceeded: %d/%d VMs in use", count, quota.MaxVMs))
		return
	}

	// ── Step 3: generate SSH key pair ─────────────────────────────────────────
	// DC-API generates the key pair. The private key is returned to the caller
	// in this response only — we do NOT store the private key.
	// The public key is injected into the VM at boot via cloud-init.
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

	// ── Step 4: resolve VPC attachment and size/disk ─────────────────────────
	vmSize := models.Sizes[req.Size]
	diskGB := req.DiskGB
	if diskGB == 0 {
		diskGB = vmSize.DefaultDiskGB
	}

	// When the caller provides vnet_id + subnet_id look up both rows BEFORE
	// writing the resource row.  This way if the lookup fails (wrong tenant,
	// not ACTIVE, wrong VNet) we return 4xx without creating an orphan row.
	// The handler is the only layer that talks to the DB — the provider never
	// queries the DB directly (single-responsibility principle).
	var vnetBackendUID, subnetBackendUID string
	var vnetUUIDPtr, subnetUUIDPtr *uuid.UUID // F41: persisted on the Resource row when VPC path is used
	if req.VNetID != "" {
		vnetUUID, _ := uuid.Parse(req.VNetID)    // already validated above
		subnetUUID, _ := uuid.Parse(req.SubnetID) // already validated above

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

	// ── Step 4a: read VPC DNS server IP (F20) ────────────────────────────────
	// When the VM is on a VPC subnet, look up the VNet row's dns_server_ip so
	// the harvester driver can inject dnsPolicy=None + dnsConfig.nameservers.
	// This silences KubeVirt's bridge-mode internal DHCP race (see memory
	// project_f20_spike_outcome.md — critical gotcha section).
	// Empty string is fine for the legacy bridge path (no VPC, no F20 DNS).
	var dnsSrvIP string
	if req.VNetID != "" {
		vnetUUID2, _ := uuid.Parse(req.VNetID)
		if vnet2, err2 := h.repo.GetVNet(r.Context(), vnetUUID2, tenantUUID, projectUUID); err2 == nil && vnet2.DNSServerIP != "" {
			dnsSrvIP = vnet2.DNSServerIP
		}
	}

	// ── Step 4b: create PENDING resource in PostgreSQL ────────────────────────
	// We write the record BEFORE calling Harvester. This means:
	//   a. The resource is immediately visible to GET requests (status: PENDING).
	//   b. If DC-API crashes mid-provisioning, the orphan record is detectable.
	projectID, projectUUID, _ := lookupProjectUUID(w, r) // ok=false means no project context; zero values are fine
	resource, err := h.repo.Create(r.Context(), &models.Resource{
		TenantID:     tenantID,
		TenantUUID:   tenantUUID,
		ProjectID:    projectID,
		ProjectUUID:  projectUUID,
		OwnerID:      userID,
		Name:         req.Name,
		Type:         models.ResourceTypeVM,
		Size:         req.Size,
		Status:       models.StatusPending,
		ProviderType: h.provider.Name(),
		VNetID:       vnetUUIDPtr,
		SubnetID:     subnetUUIDPtr,
	})
	if err != nil {
		h.log.Error().Err(err).Str("tenant", tenantID).Msg("create resource in DB")
		if strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "duplicate key") {
			writeError(w, http.StatusConflict,
				"a VM named '"+req.Name+"' already exists (PENDING, ACTIVE, or DELETING) — wait for it to finish or delete it first")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to register resource")
		return
	}

	// Record the creation event in the audit log.

	// ── Step 5: trigger async provisioning ───────────────────────────────────
	// We return 202 immediately and provision in the background.
	// The goroutine captures the resource ID and spec by value — safe for concurrency.
	spec := models.VMSpec{
		Name:             req.Name,
		CPU:              vmSize.CPU,
		MemoryGB:         vmSize.MemoryGB,
		DiskGB:           diskGB,
		ImageName:        req.ImageName,
		NetworkName:      req.NetworkName,
		VNetBackendUID:   vnetBackendUID,
		SubnetBackendUID: subnetBackendUID,
		SSHPublicKey:     publicKey,
		Password:         password,
		ResourceID:       resource.ID.String(),
		DNSServerIP:      dnsSrvIP,
		DNSSearchDomain:  h.dnsSearchDomain,
	}
	go h.asyncProvision(resource.ID, tenantID, projectID, userID, spec)

	// ── Step 6: return 202 Accepted ───────────────────────────────────────────
	// Credentials are returned ONCE here — never stored server-side.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"resource":         vmToResponse(resource),
		"private_key":      privateKeyPEM,
		"console_password": password,
		"note":             "VM is being provisioned. Poll GET /v1/virtual-machines/" + resource.ID.String() + " for status.",
	})
}

// Get handles GET /v1/virtual-machines/{id}.
func (h *VMHandler) Get(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}

	writeJSON(w, http.StatusOK, vmToResponse(resource))
}

// List handles GET /v1/virtual-machines.
func (h *VMHandler) List(w http.ResponseWriter, r *http.Request) {
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

	resources, err := h.repo.ListByProject(r.Context(), tenantUUID, projectUUID, models.ResourceTypeVM)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list VMs")
		return
	}

	responses := make([]VMResponse, 0, len(resources))
	for _, res := range resources {
		responses = append(responses, vmToResponse(res))
	}
	writeJSON(w, http.StatusOK, responses)
}

// Delete handles DELETE /v1/virtual-machines/{id}.
func (h *VMHandler) Delete(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionVMDelete) {
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
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}

	// Mark as DELETING in DB — visible to pollers immediately.
	if err := h.repo.UpdateStatus(r.Context(), id, models.StatusDeleting, "deletion requested", ""); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update resource status")
		return
	}

	// Async: call Harvester delete. The reconciler detects the 404 from Harvester
	// and removes the DB row — consistent with how cluster deletion works.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		// If BackendUID is empty the VM never reached Harvester (failed during
		// provisioning). Remove from DB directly — reconciler won't find it either.
		if resource.BackendUID == "" {
			_ = h.repo.Delete(ctx, id)
			return
		}

		if err := h.provider.DeleteVM(ctx, resource.BackendUID); err != nil {
			h.log.Error().Err(err).Str("backend_uid", resource.BackendUID).Msg("harvester delete VM failed")
			_ = h.repo.UpdateStatus(ctx, id, models.StatusFailed, "deletion failed: "+err.Error(), "")
		}
		// Leave the row as DELETING. The reconciler polls it every 60s and removes
		// the row once Harvester returns 404 (VM fully gone).
	}()

	w.WriteHeader(http.StatusAccepted)
}

// ─────────────────────────── Async Provisioner ──────────────────────────────

// asyncProvision calls Harvester to create the VM and updates the DB on completion.
// This runs in a goroutine so the HTTP handler returns 202 immediately.
func (h *VMHandler) asyncProvision(resourceID uuid.UUID, tenantID, projectID, userID string, spec models.VMSpec) {
	// Use a background context — the HTTP request context is cancelled when the
	// response is sent, but provisioning continues for minutes.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	providerResource, err := h.provider.CreateVM(ctx, tenantID, projectID, spec)
	if err != nil {
		h.log.Error().Err(err).Str("vm", spec.Name).Msg("harvester CreateVM failed")
		_ = h.repo.UpdateStatus(ctx, resourceID, models.StatusFailed,
			"provisioning failed: "+err.Error(), "")
		return
	}

	// Update the DB record with the provider's BackendUID.
	// The background reconciler will poll Harvester and flip status to ACTIVE
	// once the VM is actually running.
	if err := h.repo.UpdateStatus(ctx, resourceID, models.StatusPending,
		"provisioning submitted to Harvester", providerResource.BackendUID); err != nil {
		h.log.Error().Err(err).Msg("update resource BackendUID")
	}
}

// ─────────────────────────── SSH Key Generation ─────────────────────────────

// generatePassword creates a random 20-character alphanumeric console password.
// Used for cloud-init chpasswd — gives serial-console access without an SSH key.
// The password is returned to the caller ONCE and never stored server-side.
func generatePassword() (string, error) {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate password: %w", err)
	}
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b), nil
}

// generateSSHKeyPair creates an ECDSA key pair (P-256).
// Returns (authorizedKeyLine, privateKeyPEM, error).
// ECDSA is preferred over RSA for new keys: smaller keys, same security level.
func generateSSHKeyPair() (string, string, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate ECDSA key: %w", err)
	}

	// Convert to SSH authorized_keys format for the VM.
	pubKey, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		return "", "", fmt.Errorf("marshal SSH public key: %w", err)
	}
	authorizedKey := string(ssh.MarshalAuthorizedKey(pubKey))

	// Encode private key as PEM for the caller.
	privBlock, err := ssh.MarshalPrivateKey(privateKey, "")
	if err != nil {
		return "", "", fmt.Errorf("marshal SSH private key: %w", err)
	}

	return authorizedKey, string(pem.EncodeToMemory(privBlock)), nil
}

// ListNetworks handles GET /v1/networks.
func (h *VMHandler) ListNetworks(w http.ResponseWriter, r *http.Request) {
	networks, err := h.provider.ListNetworks(r.Context())
	if err != nil {
		h.log.Error().Err(err).Msg("list networks")
		writeError(w, http.StatusInternalServerError, "failed to list networks")
		return
	}
	writeJSON(w, http.StatusOK, networks)
}

// ListImages handles GET /v1/images.
func (h *VMHandler) ListImages(w http.ResponseWriter, r *http.Request) {
	images, err := h.provider.ListImages(r.Context())
	if err != nil {
		h.log.Error().Err(err).Msg("list images")
		writeError(w, http.StatusInternalServerError, "failed to list images")
		return
	}
	writeJSON(w, http.StatusOK, images)
}

// CreateImage handles POST /v1/images.
func (h *VMHandler) CreateImage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DisplayName string `json:"display_name"`
		URL         string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.DisplayName == "" {
		writeError(w, http.StatusBadRequest, "display_name is required")
		return
	}
	if req.URL == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}

	image, err := h.provider.CreateImage(r.Context(), req.DisplayName, req.URL)
	if err != nil {
		h.log.Error().Err(err).Str("name", req.DisplayName).Msg("create image")
		writeError(w, http.StatusInternalServerError, "failed to create image: "+err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, image)
}

// JSON-response + error helpers moved to response.go.
