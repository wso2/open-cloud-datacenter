// Package handlers — keyvault.go
//
// M3 Key Vault — bare CRUD for the logical vault record, plus optional
// integration with the KVI operator (KeyVaultBackend + KeyVaultInstance
// CRDs) when the KVIProvisioner is wired.
//
// Two operating modes:
//
//   1. KVI wired (production):
//        POST   creates a DB row in PENDING, ensures the per-tenant
//               KeyVaultBackend CR, then creates a per-vault
//               KeyVaultInstance CR with the standard dc-api.wso2.com/*
//               labels. The KVI controller provisions OpenBao + mount +
//               AppRole asynchronously; status flips to ACTIVE when
//               the operator reports phase=Ready.
//        GET    overlays the live CR status onto the DB row so the
//               caller sees fresh phase/message without waiting for
//               a reconciler tick.
//        DELETE deletes the KVI CR (operator finalizer handles the
//               OpenBao-side cleanup async) and drops the DB row
//               immediately.
//
//   2. KVI nil (tests / non-Kubernetes deployments):
//        Falls back to the chunk-1+2 behaviour — synchronous DB-only
//        CRUD with status=ACTIVE on create. Integration tests that
//        boot dc-api without a Kubernetes backend still pass.
//
// Tenant scoping is enforced via TenantFromContext (set by Auth middleware).
// RBAC: Create/Delete/Get/List all require the `member` role.
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
	"github.com/wso2/dc-api/internal/providers/common"
	"github.com/wso2/dc-api/internal/rbac"
)

const (
	defaultSoftDeleteDays = 30
	minSoftDeleteDays     = 7
	maxSoftDeleteDays     = 90
)

// KeyVaultHandler handles /v1/keyvaults endpoints.
type KeyVaultHandler struct {
	repo *db.Repository
	// kvi is optional. Nil falls back to chunk-1+2 synchronous DB CRUD.
	kvi providers.KVIProvisioner
	// tenantNS is optional. When wired, driveKVI calls EnsureTenantNamespace
	// before creating the KeyVaultBackend CR — the tenant namespace may not
	// exist yet if the admin skipped POST /v1/admin/tenants (e.g. test fixtures
	// that upsert the DB row directly). Nil means skip the ensure call.
	tenantNS providers.TenantNamespaceProvisioner
	log      zerolog.Logger
}

// NewKeyVaultHandler constructs a KeyVaultHandler. Pass nil for kvi to
// keep the chunk-1+2 behaviour (tests, no-K8s deployments).
// Pass nil for tenantNS to skip the idempotent namespace-ensure step.
func NewKeyVaultHandler(repo *db.Repository, kvi providers.KVIProvisioner, tenantNS providers.TenantNamespaceProvisioner, log zerolog.Logger) *KeyVaultHandler {
	return &KeyVaultHandler{repo: repo, kvi: kvi, tenantNS: tenantNS, log: log}
}

// ── DTOs ──────────────────────────────────────────────────────────────────────

type createKeyVaultRequest struct {
	Name           string `json:"name"`
	SoftDeleteDays int    `json:"soft_delete_days,omitempty"` // optional; 0 → default 30
}

type keyVaultResponse struct {
	ID             string `json:"id"`
	TenantID       string `json:"tenant_id"`
	Name           string `json:"name"`
	SoftDeleteDays int    `json:"soft_delete_days"`
	Status         string `json:"status"`
	Message        string `json:"message,omitempty"`
	// MountPath / EndpointAddress / EndpointPort are populated from the
	// live KVI CR status when the operator integration is wired; otherwise
	// they're empty.
	MountPath       string `json:"mount_path,omitempty"`
	EndpointAddress string `json:"endpoint_address,omitempty"`
	EndpointPort    int    `json:"endpoint_port,omitempty"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

func keyVaultToResponse(kv *models.KeyVault) keyVaultResponse {
	return keyVaultResponse{
		ID:             kv.ID.String(),
		TenantID:       kv.TenantID,
		Name:           kv.Name,
		SoftDeleteDays: kv.SoftDeleteDays,
		Status:         string(kv.Status),
		Message:        kv.Message,
		CreatedAt:      kv.CreatedAt.Format(time.RFC3339),
		UpdatedAt:      kv.UpdatedAt.Format(time.RFC3339),
	}
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// Create handles POST /v1/.../keyvaults.
//
// In KVI mode: creates DB row in PENDING, ensures Backend, creates KVI CR.
// In fallback mode: writes the row with status=ACTIVE synchronously.
func (h *KeyVaultHandler) Create(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionVaultWrite) {
		return
	}

	var req createKeyVaultRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := validateResourceName(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	sdd := req.SoftDeleteDays
	if sdd == 0 {
		sdd = defaultSoftDeleteDays
	}
	if sdd < minSoftDeleteDays || sdd > maxSoftDeleteDays {
		writeError(w, http.StatusBadRequest,
			"soft_delete_days must be between 7 and 90 (omit to use default 30)")
		return
	}

	projectID, projectUUID, _ := lookupProjectUUID(w, r)

	// Initial DB status depends on whether we'll provision the operator-side
	// state. PENDING until the operator reports Ready; ACTIVE in fallback.
	initialStatus := models.StatusActive
	if h.kvi != nil {
		initialStatus = models.StatusPending
	}

	kv, err := h.repo.CreateKeyVault(r.Context(), &models.KeyVault{
		TenantID:       tenantID,
		TenantUUID:     tenantUUID,
		ProjectID:      projectID,
		ProjectUUID:    projectUUID,
		Name:           req.Name,
		SoftDeleteDays: sdd,
		Status:         initialStatus,
	})
	if err != nil {
		if strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "duplicate key") {
			writeError(w, http.StatusConflict, "a key vault named '"+req.Name+"' already exists for this tenant")
			return
		}
		h.log.Error().Err(err).Str("tenant", tenantID).Msg("create key_vault")
		writeError(w, http.StatusInternalServerError, "failed to create key vault")
		return
	}

	actorID, _ := middleware.UserFromContext(r.Context())
	_ = h.repo.AppendAuditEvent(r.Context(), &models.AuditEvent{
		ResourceID: kv.ID, ActorID: actorID, Action: "CREATE", ToStatus: kv.Status,
	})

	// KVI mode: drive the operator's CRDs. Failures here don't roll back
	// the DB row — they leave the row in PENDING with a diagnostic message
	// the caller can surface via GET.
	if h.kvi != nil {
		if err := h.driveKVI(r.Context(), kv, tenantID, projectID, tenantUUID, projectUUID); err != nil {
			h.log.Warn().Err(err).
				Str("tenant", tenantID).
				Str("vault_id", kv.ID.String()).
				Msg("KVI provisioning failed; row left in PENDING with error message")
			// best-effort message capture — surfaced on next GET
			kv.Message = "kvi provisioning failed: " + err.Error()
		}
	}

	writeJSON(w, http.StatusCreated, keyVaultToResponse(kv))
}

// driveKVI runs the operator-CR steps for a freshly-created vault row.
//  0. Idempotently ensure the dc-tenant-<tenantID> Kubernetes namespace
//     exists (the KVI operator's Backend CR lives there; if the admin
//     bypassed POST /v1/admin/tenants, this namespace may not exist yet).
//  1. Ensure the per-tenant Backend CR exists.
//  2. Create the per-vault KVI CR with standard labels.
func (h *KeyVaultHandler) driveKVI(
	ctx context.Context,
	kv *models.KeyVault,
	tenantID, projectID string,
	tenantUUID, projectUUID uuid.UUID,
) error {
	// 0. Ensure the tenant namespace before attempting the Backend CR.
	if h.tenantNS != nil {
		if err := h.tenantNS.EnsureTenantNamespace(ctx, tenantID, tenantUUID); err != nil {
			return fmt.Errorf("ensure tenant namespace: %w", err)
		}
	}

	// 1. Ensure per-tenant Backend CR. Default spec (controller fills in
	// engineConfig defaults). Capacity sizing comes later when we have
	// a per-tenant cap policy for the OpenBao cluster.
	if err := h.kvi.EnsureKeyVaultBackend(ctx, tenantID, tenantUUID, providers.KeyVaultBackendSpec{}); err != nil {
		return err
	}

	// 2. Create the per-vault KVI CR.
	labels := common.StandardLabels(tenantID, projectID, tenantUUID, projectUUID, kv.ID, "keyvault", kv.Name)
	crName := common.NamespaceScopedName("kv", kv.ID)
	return h.kvi.CreateKeyVaultInstance(ctx, providers.KeyVaultInstanceCreateRequest{
		Name:           crName,
		Namespace:      common.NamespaceForProject(tenantID, projectID),
		Labels:         labels,
		BackendName:    h.kvi.BackendName(tenantID),
		BackendNS:      common.NamespaceForTenant(tenantID),
		SoftDeleteDays: kv.SoftDeleteDays,
	})
}

// Get handles GET /v1/.../keyvaults/{id}.
//
// In KVI mode: overlays the live CR status onto the DB row before returning.
func (h *KeyVaultHandler) Get(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionVaultRead) {
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid key vault id")
		return
	}
	kv, err := h.repo.GetKeyVault(r.Context(), id, tenantUUID, projectUUID)
	if errors.Is(err, db.ErrKeyVaultNotFound) {
		writeError(w, http.StatusNotFound, "key vault not found")
		return
	}
	if err != nil {
		h.log.Error().Err(err).Str("id", id.String()).Msg("get key_vault")
		writeError(w, http.StatusInternalServerError, "failed to fetch key vault")
		return
	}
	if kv.TenantUUID != tenantUUID {
		// 404 (not 403) so we don't leak existence to non-owners.
		writeError(w, http.StatusNotFound, "key vault not found")
		return
	}

	resp := keyVaultToResponse(kv)

	// KVI mode: overlay live CR status onto the response.
	if h.kvi != nil {
		ns := common.NamespaceForProject(kv.TenantID, kv.ProjectID)
		crName := common.NamespaceScopedName("kv", kv.ID)
		st, gerr := h.kvi.GetKeyVaultInstance(r.Context(), ns, crName)
		if gerr != nil {
			h.log.Warn().Err(gerr).
				Str("vault_id", kv.ID.String()).
				Msg("read KVI CR status failed; returning DB-only view")
		} else if st != nil {
			if phase := mapCRPhaseToStatus(st.Phase); phase != "" {
				resp.Status = phase
			}
			if st.Message != "" {
				resp.Message = st.Message
			}
			resp.MountPath = st.MountPath
			resp.EndpointAddress = st.EndpointAddress
			resp.EndpointPort = st.EndpointPort
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// List handles GET /v1/.../keyvaults. Overlays the live KVI CR status onto
// each row so the list view shows the same Active/Pending/Failed phase as
// the detail view. Costs one K8s GET per vault — fine for the typical
// vault count per project (dozens at most). If the count grows past that,
// move to a periodic DB-sync reconciler instead.
func (h *KeyVaultHandler) List(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionVaultRead) {
		return
	}
	kvs, err := h.repo.ListKeyVaultsByProject(r.Context(), tenantUUID, projectUUID)
	if err != nil {
		h.log.Error().Err(err).Str("tenant", tenantID).Msg("list key_vaults")
		writeError(w, http.StatusInternalServerError, "failed to list key vaults")
		return
	}
	out := make([]keyVaultResponse, 0, len(kvs))
	for _, kv := range kvs {
		resp := keyVaultToResponse(kv)
		if h.kvi != nil {
			ns := common.NamespaceForProject(kv.TenantID, kv.ProjectID)
			crName := common.NamespaceScopedName("kv", kv.ID)
			st, gerr := h.kvi.GetKeyVaultInstance(r.Context(), ns, crName)
			if gerr != nil {
				h.log.Warn().Err(gerr).
					Str("vault_id", kv.ID.String()).
					Msg("list: read KVI CR status failed; returning DB-only view for this row")
			} else if st != nil {
				if phase := mapCRPhaseToStatus(st.Phase); phase != "" {
					resp.Status = phase
				}
				if st.Message != "" {
					resp.Message = st.Message
				}
				resp.MountPath = st.MountPath
				resp.EndpointAddress = st.EndpointAddress
				resp.EndpointPort = st.EndpointPort
			}
		}
		out = append(out, resp)
	}
	writeJSON(w, http.StatusOK, out)
}

// Delete handles DELETE /v1/.../keyvaults/{id}.
//
// In KVI mode: deletes the KVI CR (operator finalizer runs the upstream
// AppRole/policy/mount cleanup async) and drops the DB row immediately.
func (h *KeyVaultHandler) Delete(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionVaultDelete) {
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid key vault id")
		return
	}

	// Tenant scope guard — fetch first so we don't delete another tenant's row.
	kv, err := h.repo.GetKeyVault(r.Context(), id, tenantUUID, projectUUID)
	if errors.Is(err, db.ErrKeyVaultNotFound) {
		writeError(w, http.StatusNotFound, "key vault not found")
		return
	}
	if err != nil {
		h.log.Error().Err(err).Str("id", id.String()).Msg("get key_vault for delete")
		writeError(w, http.StatusInternalServerError, "failed to fetch key vault")
		return
	}
	if kv.TenantUUID != tenantUUID {
		writeError(w, http.StatusNotFound, "key vault not found")
		return
	}

	// KVI mode: ask the operator to begin teardown. Idempotent on the
	// operator side; NotFound treated as success.
	if h.kvi != nil {
		ns := common.NamespaceForProject(kv.TenantID, kv.ProjectID)
		crName := common.NamespaceScopedName("kv", kv.ID)
		if err := h.kvi.DeleteKeyVaultInstance(r.Context(), ns, crName); err != nil {
			h.log.Error().Err(err).
				Str("vault_id", kv.ID.String()).
				Msg("delete KVI CR")
			writeError(w, http.StatusInternalServerError, "failed to delete vault from operator")
			return
		}
	}

	// Audit before the hard delete — the snapshot resolves only while the
	// row exists.
	actorID, _ := middleware.UserFromContext(r.Context())
	_ = h.repo.AppendAuditEvent(r.Context(), &models.AuditEvent{
		ResourceID: kv.ID, ActorID: actorID, Action: "DELETE", FromStatus: kv.Status,
	})
	if err := h.repo.DeleteKeyVault(r.Context(), id); err != nil {
		if errors.Is(err, db.ErrKeyVaultNotFound) {
			writeError(w, http.StatusNotFound, "key vault not found")
			return
		}
		h.log.Error().Err(err).Str("id", id.String()).Msg("delete key_vault")
		writeError(w, http.StatusInternalServerError, "failed to delete key vault")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Credentials handles GET /v1/.../keyvaults/{id}/credentials.
//
// Shown-once semantics:
//   - First call: returns the AppRole credentials read from the operator's
//     credentials Secret; stamps credentials_consumed_at = NOW() in the DB.
//   - Subsequent calls: returns 410 Gone (the credentials are not retrievable
//     a second time from dc-api — the caller saves them once or rotates).
//
// Pre-conditions:
//   - KVI must be wired (no creds without the operator). Returns 501 otherwise.
//   - The CR's status.phase must be Ready. Returns 409 with a message
//     pointing at the GET /{id} endpoint to poll otherwise.
func (h *KeyVaultHandler) Credentials(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionVaultCredentialsRead) {
		return
	}
	if h.kvi == nil {
		writeError(w, http.StatusNotImplemented,
			"credentials endpoint requires the KVI operator integration; not available on this dc-api deployment")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid key vault id")
		return
	}
	kv, err := h.repo.GetKeyVault(r.Context(), id, tenantUUID, projectUUID)
	if errors.Is(err, db.ErrKeyVaultNotFound) {
		writeError(w, http.StatusNotFound, "key vault not found")
		return
	}
	if err != nil {
		h.log.Error().Err(err).Str("id", id.String()).Msg("get key_vault for credentials")
		writeError(w, http.StatusInternalServerError, "failed to fetch key vault")
		return
	}
	if kv.TenantUUID != tenantUUID {
		writeError(w, http.StatusNotFound, "key vault not found")
		return
	}
	if kv.CredentialsConsumedAt != nil {
		writeError(w, http.StatusGone,
			"credentials already retrieved at "+kv.CredentialsConsumedAt.Format(time.RFC3339)+
				" — they are shown only once; rotate via the operator if you need fresh ones")
		return
	}

	// Read live CR status to find the credentials Secret name.
	ns := common.NamespaceForProject(kv.TenantID, kv.ProjectID)
	crName := common.NamespaceScopedName("kv", kv.ID)
	st, err := h.kvi.GetKeyVaultInstance(r.Context(), ns, crName)
	if err != nil {
		h.log.Error().Err(err).Str("vault_id", kv.ID.String()).Msg("read KVI CR for credentials")
		writeError(w, http.StatusInternalServerError, "failed to read key vault status")
		return
	}
	if st == nil || st.Phase != "Ready" || st.SecretRefName == "" {
		phase := "not yet provisioned"
		if st != nil && st.Phase != "" {
			phase = st.Phase
		}
		writeError(w, http.StatusConflict,
			"key vault is not Ready yet (phase="+phase+
				"); poll GET /v1/.../keyvaults/"+kv.ID.String()+" until status=ACTIVE before requesting credentials")
		return
	}

	data, err := h.kvi.GetCredentialsSecret(r.Context(), ns, st.SecretRefName)
	if err != nil {
		h.log.Error().Err(err).
			Str("vault_id", kv.ID.String()).
			Str("secret", ns+"/"+st.SecretRefName).
			Msg("read credentials secret")
		writeError(w, http.StatusInternalServerError, "failed to read credentials secret")
		return
	}
	if data == nil {
		writeError(w, http.StatusConflict,
			"credentials secret not yet written by the operator — try again shortly")
		return
	}

	// Stamp consumed BEFORE returning. If two callers race, only one wins
	// the UPDATE (WHERE credentials_consumed_at IS NULL); the other gets
	// ErrCredentialsAlreadyConsumed → 410.
	if err := h.repo.MarkKeyVaultCredentialsConsumed(r.Context(), kv.ID); err != nil {
		if errors.Is(err, db.ErrCredentialsAlreadyConsumed) {
			writeError(w, http.StatusGone,
				"credentials already retrieved — they are shown only once; rotate via the operator if you need fresh ones")
			return
		}
		h.log.Error().Err(err).Str("vault_id", kv.ID.String()).Msg("stamp credentials consumed")
		writeError(w, http.StatusInternalServerError, "failed to record credentials consumption")
		return
	}

	port := 0
	if v, ok := data["backend_port"]; ok {
		// stored as string text; parse best-effort.
		for _, b := range v {
			if b < '0' || b > '9' {
				port = 0
				break
			}
			port = port*10 + int(b-'0')
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"role_id":         string(data["role_id"]),
		"secret_id":       string(data["secret_id"]),
		"mount_path":      string(data["mount_path"]),
		"backend_address": string(data["backend_address"]),
		"backend_port":    string(data["backend_port"]),
	})
	_ = port // port string returned as-is for the caller; helper above is defensive
}

// RotateCredentials handles POST /v1/.../keyvaults/{id}/credentials/rotate.
//
// Atomically rotates the AppRole secret_id:
//   1. Resolve vault → role name (kv-<short-uuid>).
//   2. Find the OpenBao leader pod for this tenant.
//   3. List + destroy every existing secret_id_accessor on the role.
//   4. Mint a fresh secret_id.
//   5. Patch the in-cluster credentials Secret (so anything reading it
//      directly sees the new value).
//   6. Stamp credentials_consumed_at = NOW (this rotation IS the
//      shown-once event).
//   7. Return the full KeyVaultCredentials in the response body.
//
// Old secret_ids stop working immediately. Workloads holding the old
// value will hit 403 on the next AppRole login and must be rotated to the
// new value.
func (h *KeyVaultHandler) RotateCredentials(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionVaultWrite) {
		return
	}
	if h.kvi == nil {
		writeError(w, http.StatusNotImplemented,
			"credentials rotation requires the KVI operator integration; not available on this dc-api deployment")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid key vault id")
		return
	}
	kv, err := h.repo.GetKeyVault(r.Context(), id, tenantUUID, projectUUID)
	if errors.Is(err, db.ErrKeyVaultNotFound) {
		writeError(w, http.StatusNotFound, "key vault not found")
		return
	}
	if err != nil {
		h.log.Error().Err(err).Str("id", id.String()).Msg("get key_vault for rotate")
		writeError(w, http.StatusInternalServerError, "failed to fetch key vault")
		return
	}
	if kv.TenantUUID != tenantUUID {
		writeError(w, http.StatusNotFound, "key vault not found")
		return
	}

	// Live CR status: need the credentials-Secret name + confirmation that
	// the AppRole has been provisioned (the mint targets it by role name).
	ns := common.NamespaceForProject(kv.TenantID, kv.ProjectID)
	crName := common.NamespaceScopedName("kv", kv.ID)
	st, err := h.kvi.GetKeyVaultInstance(r.Context(), ns, crName)
	if err != nil {
		h.log.Error().Err(err).Str("vault_id", kv.ID.String()).Msg("read KVI CR for rotate")
		writeError(w, http.StatusInternalServerError, "failed to read key vault status")
		return
	}
	if st == nil || st.Phase != "Ready" || st.SecretRefName == "" {
		phase := "not yet provisioned"
		if st != nil && st.Phase != "" {
			phase = st.Phase
		}
		writeError(w, http.StatusConflict,
			"key vault is not Ready yet (phase="+phase+
				"); poll GET /v1/.../keyvaults/"+kv.ID.String()+" until status=ACTIVE before rotating credentials")
		return
	}

	// AppRole role name convention is kv-<vault-uuid>, matching the
	// operator's Instance reconciler.
	roleName := "kv-" + kv.ID.String()

	podName, err := h.kvi.GetOpenBaoLeaderPod(r.Context(), kv.TenantID)
	if err != nil {
		h.log.Error().Err(err).Str("tenant", kv.TenantID).Msg("find openbao leader for rotate")
		writeError(w, http.StatusServiceUnavailable, "key vault backend not reachable: "+err.Error())
		return
	}
	token, err := h.kvi.ReadDCAPIToken(r.Context(), kv.TenantID)
	if err != nil {
		h.log.Error().Err(err).Str("tenant", kv.TenantID).Msg("read dc-api token for rotate")
		writeError(w, http.StatusServiceUnavailable, "key vault backend credentials unavailable")
		return
	}
	defer func() { token = "" }()

	// (1) List + destroy old accessors. Best-effort: if one destroy fails we
	// continue and still mint, but surface a 500 at the end so the caller
	// knows the rotation wasn't atomic.
	accessors, err := h.kvi.ListSecretIDAccessors(r.Context(), kv.TenantID, podName, roleName, token)
	if err != nil {
		h.log.Error().Err(err).Str("role", roleName).Msg("list secret-id accessors")
		writeError(w, http.StatusInternalServerError, "failed to enumerate existing credentials")
		return
	}
	for _, acc := range accessors {
		if err := h.kvi.DestroySecretIDAccessor(r.Context(), kv.TenantID, podName, roleName, acc, token); err != nil {
			h.log.Error().Err(err).Str("role", roleName).Str("accessor", acc).Msg("destroy secret-id accessor")
			writeError(w, http.StatusInternalServerError, "failed to invalidate existing credentials; rotation aborted")
			return
		}
	}

	// (2) Mint fresh secret_id.
	newSecretID, _, err := h.kvi.GenerateSecretID(r.Context(), kv.TenantID, podName, roleName, token)
	if err != nil {
		h.log.Error().Err(err).Str("role", roleName).Msg("generate secret_id")
		writeError(w, http.StatusInternalServerError, "failed to mint new credentials")
		return
	}

	// (3) Patch in-cluster Secret so anything reading it directly sees
	// the new value. role_id is unchanged; we only update secret_id.
	if err := h.kvi.PatchCredentialsSecret(r.Context(), ns, st.SecretRefName, map[string][]byte{
		"secret_id": []byte(newSecretID),
	}); err != nil {
		h.log.Error().Err(err).
			Str("vault_id", kv.ID.String()).
			Str("secret", ns+"/"+st.SecretRefName).
			Msg("patch credentials secret")
		// We've already minted + destroyed at OpenBao — the new secret_id
		// is valid but the in-cluster Secret is stale. Return 500 so the
		// caller knows to retry; the next rotate will re-list/destroy/mint
		// and re-patch.
		writeError(w, http.StatusInternalServerError,
			"new credentials minted but in-cluster Secret patch failed; retry rotate")
		return
	}

	// (4) Read back the Secret to get the unchanged fields (role_id, mount,
	// backend_address, backend_port) and build the response.
	data, err := h.kvi.GetCredentialsSecret(r.Context(), ns, st.SecretRefName)
	if err != nil || data == nil {
		h.log.Error().Err(err).Str("vault_id", kv.ID.String()).Msg("re-read credentials secret post-rotate")
		writeError(w, http.StatusInternalServerError, "rotated successfully but failed to read back the result")
		return
	}

	// (5) Mark consumed. Unlike create-time GET, rotate is idempotent on
	// the DB stamp — if it was already consumed we just update the
	// timestamp to now. We deliberately bypass MarkKeyVaultCredentialsConsumed
	// (which refuses to re-stamp) by writing directly.
	if err := h.repo.SetKeyVaultCredentialsConsumedNow(r.Context(), kv.ID); err != nil {
		h.log.Error().Err(err).Str("vault_id", kv.ID.String()).Msg("stamp rotated credentials consumed")
		// Don't fail — the rotation succeeded; the stamp is best-effort
		// bookkeeping. Subsequent GET .../credentials will still 410
		// based on the OLD stamp.
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"role_id":         string(data["role_id"]),
		"secret_id":       newSecretID,
		"mount_path":      string(data["mount_path"]),
		"backend_address": string(data["backend_address"]),
		"backend_port":    string(data["backend_port"]),
	})
}

// mapCRPhaseToStatus translates the KVI CR's status.phase into a
// dc-api ResourceStatus. Empty string when phase is unknown / unset
// (caller keeps the DB-row status).
func mapCRPhaseToStatus(phase string) string {
	switch phase {
	case "Pending", "Provisioning":
		return string(models.StatusPending)
	case "Ready":
		return string(models.StatusActive)
	case "Failed":
		return string(models.StatusFailed)
	case "Terminating":
		return string(models.StatusDeleting)
	}
	return ""
}

// ── M3 chunk 2 adapters: TargetLookup + BackendResolver ──────────────────────

// KeyVaultTargetLookup adapts KeyVaultHandler to the generic
// PrivateEndpointHandler.TargetLookup interface — verifies that the parent
// vault exists and is owned by the caller's tenant before any endpoint work.
type KeyVaultTargetLookup struct{ Repo *db.Repository }

// Exists returns true when the vault exists, is owned by tenantUUID, and is in
// a state that allows endpoint operations. ErrKeyVaultNotFound and a tenant
// mismatch both return (false, nil) so the caller emits 404 (no leak).
// Phase 6a: accepts tenantUUID (immutable) instead of the mutable slug.
func (l *KeyVaultTargetLookup) Exists(ctx context.Context, tenantUUID uuid.UUID, id uuid.UUID) (bool, error) {
	// These routes run under ProjectContext middleware, so the project UUID is
	// always present on the request context. Scope the lookup to it so a vault
	// in another project of the same tenant returns not-found (no cross-project
	// leak). A missing project context yields (false, nil) → the caller's 404.
	projectUUID, ok := middleware.ProjectUUIDFromContext(ctx)
	if !ok {
		return false, nil
	}
	kv, err := l.Repo.GetKeyVault(ctx, id, tenantUUID, projectUUID)
	if errors.Is(err, db.ErrKeyVaultNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if kv.TenantUUID != tenantUUID {
		return false, nil
	}
	return true, nil
}

// KeyVaultBackendResolver returns the in-cluster address the proxy should
// forward to for a given vault. In chunk 2 every vault resolves to the same
// shared OpenBao Service — chunk 3 makes this per-(tenant, vault) when the
// OpenBao mount + AppRole lifecycle lands.
type KeyVaultBackendResolver struct {
	Addr string // e.g. "openbao.dc-api-vault.svc.cluster.local"
	Port int    // e.g. 8200
}

// Resolve implements endpoints.BackendResolver.
func (r *KeyVaultBackendResolver) Resolve(_ context.Context, _ uuid.UUID) (string, int, error) {
	return r.Addr, r.Port, nil
}
