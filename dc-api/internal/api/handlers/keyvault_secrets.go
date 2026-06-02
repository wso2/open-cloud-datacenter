// Package handlers — keyvault_secrets.go
//
// Four endpoints that let authenticated dc-api users (via Asgardeo JWT or
// service-account token) read and write secrets inside an existing Key Vault
// without ever touching the OpenBao backend or its AppRole credentials
// directly. dc-api is the trusted broker:
//
//	user → JWT → dc-api ─── RBAC check (RequireRole)
//	                    └── proxy to OpenBao using the per-Backend root token
//
// SECURITY INVARIANT: the root token MUST NEVER appear in any HTTP response,
// log line, or error message. Every code path that obtains a root token must
// pass it as a local variable and not log or serialise it.
//
// RBAC:
//   - viewer  → listKeyVaultSecrets only
//   - member  → listKeyVaultSecrets + getKeyVaultSecret + putKeyVaultSecret + deleteKeyVaultSecret
//   - owner   → same as member
//
// Mount path per vault: "tenants/<tenant_uuid>/<vault_uuid>"
// This is derived from the vault's DB row and matches the KVI operator's
// mount naming convention.
package handlers

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/wso2/dc-api/internal/api/middleware"
	"github.com/wso2/dc-api/internal/db"
	"github.com/wso2/dc-api/internal/models"
	"github.com/wso2/dc-api/internal/providers"
	"github.com/wso2/dc-api/internal/providers/kvi"
	"github.com/wso2/dc-api/internal/rbac"
)

const (
	// defaultSecretsPageSize is the default page size for listKeyVaultSecrets.
	defaultSecretsPageSize = 100
	// maxSecretsPageSize is the maximum allowed page size.
	maxSecretsPageSize = 500
	// secretMaxValueSize is the maximum allowed plaintext value size (1 MiB).
	// Mirrors OpenBao's default per-secret cap.
	secretMaxValueSize = 1 << 20 // 1 MiB
)

// secretKeyPattern matches valid secret key names.
var secretKeyPattern = regexp.MustCompile(`^[a-z0-9._-]{1,256}$`)

// KeyVaultSecretsHandler handles the four secret-CRUD endpoints. It depends
// on the same KeyVaultHandler for vault lookup and RBAC but delegates the
// actual OpenBao proxying to the KVIProvisioner.
type KeyVaultSecretsHandler struct {
	repo *db.Repository
	kvi  providers.KVIProvisioner
	log  zerolog.Logger
}

// NewKeyVaultSecretsHandler constructs the handler.
// kvi MUST be non-nil — these endpoints are unavailable when the KVI
// operator integration is not wired. The router registers them only when
// kvi is non-nil (enforced in router.go).
func NewKeyVaultSecretsHandler(repo *db.Repository, kvi providers.KVIProvisioner, log zerolog.Logger) *KeyVaultSecretsHandler {
	return &KeyVaultSecretsHandler{repo: repo, kvi: kvi, log: log}
}

// ── DTOs ──────────────────────────────────────────────────────────────────────

type keyVaultSecretSummary struct {
	Name          string  `json:"name"`
	LatestVersion int     `json:"latest_version"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
	DeletedAt     *string `json:"deleted_at,omitempty"`
}

type keyVaultSecretListResponse struct {
	Items      []keyVaultSecretSummary `json:"items"`
	NextCursor *string                 `json:"next_cursor,omitempty"`
	TotalCount int                     `json:"total_count"`
}

type keyVaultSecretResponse struct {
	Key       string            `json:"key"`
	Value     string            `json:"value"`
	Version   int               `json:"version"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt string            `json:"created_at"`
	DeletedAt *string           `json:"deleted_at,omitempty"`
}

type putKeyVaultSecretRequest struct {
	Value    string            `json:"value"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// validateSecretKey returns an error if key does not match the allowed pattern.
func validateSecretKey(key string) error {
	if !secretKeyPattern.MatchString(key) {
		return fmt.Errorf("invalid secret key %q: must match ^[a-z0-9._-]{1,256}$", key)
	}
	return nil
}

// mountPath derives the KV-v2 mount path for a vault:
//
//	tenants/<tenant_uuid>/<vault_uuid>
func mountPath(tenantUUID, vaultUUID uuid.UUID) string {
	return "tenants/" + tenantUUID.String() + "/" + vaultUUID.String()
}

// resolveVault fetches the vault from the DB, validates tenant ownership, and
// returns the vault. Writes the appropriate error response and returns nil on
// failure — the caller must return immediately.
func (h *KeyVaultSecretsHandler) resolveVault(
	w http.ResponseWriter, r *http.Request, tenantUUID uuid.UUID,
) *models.KeyVault {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid key vault id")
		return nil
	}
	kv, err := h.repo.GetKeyVault(r.Context(), id)
	if errors.Is(err, db.ErrKeyVaultNotFound) {
		writeError(w, http.StatusNotFound, "key vault not found")
		return nil
	}
	if err != nil {
		h.log.Error().Err(err).Str("id", id.String()).Msg("get key vault for secret op")
		writeError(w, http.StatusInternalServerError, "failed to fetch key vault")
		return nil
	}
	if kv.TenantUUID != tenantUUID {
		writeError(w, http.StatusNotFound, "key vault not found")
		return nil
	}
	return kv
}

// getLeaderAndToken fetches the OpenBao leader pod name and root token for the
// tenant. Writes the appropriate error response and returns ("", "") on
// failure — the caller must return immediately.
//
// SECURITY: the returned token MUST NOT be logged or included in responses.
func (h *KeyVaultSecretsHandler) getLeaderAndToken(
	w http.ResponseWriter, r *http.Request, tenantSlug string,
) (podName, token string) {
	pod, err := h.kvi.GetOpenBaoLeaderPod(r.Context(), tenantSlug)
	if err != nil {
		h.log.Error().Err(err).Str("tenant", tenantSlug).Msg("find openbao leader pod")
		writeError(w, http.StatusServiceUnavailable, "key vault backend not reachable: "+err.Error())
		return "", ""
	}
	tok, err := h.kvi.ReadDCAPIToken(r.Context(), tenantSlug)
	if err != nil {
		h.log.Error().Err(err).Str("tenant", tenantSlug).Msg("read openbao dc-api token")
		writeError(w, http.StatusServiceUnavailable, "key vault backend credentials unavailable")
		return "", ""
	}
	return pod, tok
}

// handleOpenBaoErr translates OpenBao-specific errors into dc-api HTTP
// responses. Returns true when an error response was written (caller must
// stop). Returns false when err is nil.
func handleOpenBaoErr(w http.ResponseWriter, err error, key string) bool {
	if err == nil {
		return false
	}
	var notFound kvi.ErrOpenBaoNotFound
	var unavail kvi.ErrOpenBaoUnavailable
	switch {
	case errors.As(err, &notFound):
		writeError(w, http.StatusNotFound, fmt.Sprintf("secret %q not found in this vault", key))
		return true
	case errors.As(err, &unavail):
		writeError(w, http.StatusServiceUnavailable, "key vault backend unavailable: "+unavail.Message)
		return true
	}
	return false
}

// isVersionDeleted returns true when the given deletion_time string indicates
// that the version has already been soft-deleted (deletion_time is in the past).
// Returns false when deletion_time is empty (no scheduled or actual deletion)
// or when it is in the future (scheduled via delete_version_after, not yet deleted).
func isVersionDeleted(deletionTime string) bool {
	if deletionTime == "" || deletionTime == "0001-01-01T00:00:00Z" {
		return false
	}
	t, err := time.Parse(time.RFC3339Nano, deletionTime)
	if err != nil {
		// Can't parse — treat as not deleted to avoid false 410s.
		return false
	}
	return t.Before(time.Now().UTC())
}

// formatTime converts an OpenBao RFC-3339 timestamp string to RFC-3339 (it
// is already RFC-3339, but we normalise the format and handle empty).
// Returns empty string for zero/empty input.
func formatTime(ts string) string {
	if ts == "" || ts == "0001-01-01T00:00:00Z" {
		return ""
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return ts
	}
	return t.UTC().Format(time.RFC3339)
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// ListKeyVaultSecrets handles GET .../keyvaults/{id}/secrets.
//
// RBAC: viewer (minimum).
// Returns a cursor-paginated list of secret name summaries. Values are never
// included. Soft-deleted secrets appear with a non-null deleted_at.
func (h *KeyVaultSecretsHandler) ListKeyVaultSecrets(w http.ResponseWriter, r *http.Request) {
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
	// readMetadata is the minimum action for list — no value is returned.
	if !requireAction(w, r, h.repo, rbac.ActionSecretReadMetadata) {
		return
	}

	kv := h.resolveVault(w, r, tenantUUID)
	if kv == nil {
		return
	}

	// Parse pagination params.
	limit := defaultSecretsPageSize
	if lStr := r.URL.Query().Get("limit"); lStr != "" {
		if n, err := strconv.Atoi(lStr); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > maxSecretsPageSize {
		limit = maxSecretsPageSize
	}

	cursorStr := r.URL.Query().Get("cursor")
	var afterKey string
	if cursorStr != "" {
		b, err := base64.StdEncoding.DecodeString(cursorStr)
		if err == nil {
			afterKey = string(b)
		}
	}

	// Get the OpenBao leader and root token.
	podName, token := h.getLeaderAndToken(w, r, tenantID)
	if podName == "" {
		return
	}
	// Clear token reference at end to prevent accidental use after return.
	defer func() { token = "" }()

	mount := mountPath(tenantUUID, kv.ID)

	keys, err := h.kvi.ListSecretKeys(r.Context(), tenantID, podName, mount, token)
	if err != nil {
		if handleOpenBaoErr(w, err, "") {
			return
		}
		h.log.Error().Err(err).Str("vault", kv.ID.String()).Msg("list secret keys")
		writeError(w, http.StatusInternalServerError, "failed to list secrets")
		return
	}

	// Sort deterministically (OpenBao returns sorted, but be defensive).
	sort.Strings(keys)
	totalCount := len(keys)

	// Apply cursor: skip all keys up to and including afterKey.
	if afterKey != "" {
		start := 0
		for i, k := range keys {
			if k > afterKey {
				start = i
				break
			}
			start = i + 1
		}
		keys = keys[start:]
	}

	// Slice to page size.
	var nextCursor *string
	if len(keys) > limit {
		nc := base64.StdEncoding.EncodeToString([]byte(keys[limit-1]))
		nextCursor = &nc
		keys = keys[:limit]
	}

	// Fetch metadata for each key in the page.
	items := make([]keyVaultSecretSummary, 0, len(keys))
	for _, k := range keys {
		meta, err := h.kvi.GetSecretMetadata(r.Context(), tenantID, podName, mount, k, token)
		if err != nil {
			// On metadata-fetch failure, include the key with zero timestamps
			// rather than failing the entire list. Log at warn.
			h.log.Warn().Err(err).Str("key", k).Msg("list: get secret metadata failed; skipping timestamps")
			items = append(items, keyVaultSecretSummary{
				Name:          k,
				LatestVersion: 0,
			})
			continue
		}
		// Determine deleted_at from the latest version's deletion_time.
		// Only set when the deletion_time is in the past (actually deleted now),
		// not when it is a future scheduled deletion (delete_version_after).
		var deletedAt *string
		if lv, ok := meta.VersionMeta[strconv.Itoa(meta.CurrentVersion)]; ok && isVersionDeleted(lv.DeletionTime) {
			if formatted := formatTime(lv.DeletionTime); formatted != "" {
				deletedAt = &formatted
			}
		}
		sum := keyVaultSecretSummary{
			Name:          k,
			LatestVersion: meta.CurrentVersion,
			CreatedAt:     formatTime(meta.CreatedTime),
			UpdatedAt:     formatTime(meta.UpdatedTime),
			DeletedAt:     deletedAt,
		}
		items = append(items, sum)
	}

	writeJSON(w, http.StatusOK, keyVaultSecretListResponse{
		Items:      items,
		NextCursor: nextCursor,
		TotalCount: totalCount,
	})
}

// GetKeyVaultSecret handles GET .../keyvaults/{id}/secrets/{key}.
//
// RBAC: member.
// Returns the secret value and version metadata.
// Returns 410 Gone if the latest version is soft-deleted and no ?version was given.
func (h *KeyVaultSecretsHandler) GetKeyVaultSecret(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionSecretRead) {
		return
	}

	key := chi.URLParam(r, "key")
	if err := validateSecretKey(key); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Parse optional version query param.
	var versionReq int
	if vStr := r.URL.Query().Get("version"); vStr != "" {
		n, err := strconv.Atoi(vStr)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "version must be a positive integer")
			return
		}
		versionReq = n
	}

	kv := h.resolveVault(w, r, tenantUUID)
	if kv == nil {
		return
	}

	podName, token := h.getLeaderAndToken(w, r, tenantID)
	if podName == "" {
		return
	}
	defer func() { token = "" }()

	mount := mountPath(tenantUUID, kv.ID)

	res, err := h.kvi.ReadSecret(r.Context(), tenantID, podName, mount, key, token, versionReq)
	if err != nil {
		var notFound kvi.ErrOpenBaoNotFound
		if errors.As(err, &notFound) {
			// OpenBao returns 404 for both "key never existed" AND "latest version
			// is soft-deleted". Disambiguate via metadata.
			// If versionReq > 0, always 404 (specific version not accessible).
			if versionReq > 0 {
				writeError(w, http.StatusNotFound, fmt.Sprintf("secret %q version %d not found", key, versionReq))
				return
			}
			// No explicit version — check metadata to see if the key exists but is deleted.
			meta, metaErr := h.kvi.GetSecretMetadata(r.Context(), tenantID, podName, mount, key, token)
			if metaErr != nil || meta == nil {
				// Metadata not found either — key truly doesn't exist.
				writeError(w, http.StatusNotFound, fmt.Sprintf("secret %q not found in this vault", key))
				return
			}
			// Key exists in metadata — latest version is soft-deleted.
			writeError(w, http.StatusGone,
				fmt.Sprintf("secret %q is soft-deleted; specify ?version=N to read a prior version", key))
			return
		}
		var unavail kvi.ErrOpenBaoUnavailable
		if errors.As(err, &unavail) {
			writeError(w, http.StatusServiceUnavailable, "key vault backend unavailable: "+unavail.Message)
			return
		}
		h.log.Error().Err(err).Str("vault", kv.ID.String()).Str("key", key).Msg("read secret")
		writeError(w, http.StatusInternalServerError, "failed to read secret")
		return
	}

	// If the latest version has a past deletion_time (actually deleted, not
	// future-scheduled via delete_version_after), return 410 Gone.
	// Note: OpenBao with delete_version_after sets a future deletion_time on
	// every new write — that is a scheduled expiry, not a soft-delete.
	if versionReq == 0 && isVersionDeleted(res.DeletionTime) {
		writeError(w, http.StatusGone,
			fmt.Sprintf("secret %q is soft-deleted; specify ?version=N to read a prior version", key))
		return
	}

	resp := keyVaultSecretResponse{
		Key:      key,
		Value:    res.Value,
		Version:  res.Version,
		Metadata: res.Metadata,
		CreatedAt: formatTime(res.CreatedTime),
	}
	// Only set deleted_at when the version is actually deleted (past timestamp),
	// not when it has a future scheduled deletion_time (delete_version_after).
	if isVersionDeleted(res.DeletionTime) {
		if dt := formatTime(res.DeletionTime); dt != "" {
			resp.DeletedAt = &dt
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// PutKeyVaultSecret handles PUT .../keyvaults/{id}/secrets/{key}.
//
// RBAC: member.
// Writes a new KV-v2 version. Returns 201 on first write, 200 on updates.
//
// NOTE: metadata is stored inline in the KV-v2 data map as
// {"value": "...", "metadata": {...}}. This avoids a second
// POST metadata/<key> round-trip for v1. A future v2 can use
// KV-v2's native custom-metadata path (POST metadata/<key>).
func (h *KeyVaultSecretsHandler) PutKeyVaultSecret(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionSecretWrite) {
		return
	}

	key := chi.URLParam(r, "key")
	if err := validateSecretKey(key); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Enforce value size limit before JSON decode.
	if r.ContentLength > secretMaxValueSize+512 { // 512 bytes slack for JSON framing
		writeError(w, http.StatusBadRequest, "request body exceeds 1 MiB limit")
		return
	}

	var req putKeyVaultSecretRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if len(req.Value) > secretMaxValueSize {
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("secret value exceeds 1 MiB limit (%d bytes)", len(req.Value)))
		return
	}

	kv := h.resolveVault(w, r, tenantUUID)
	if kv == nil {
		return
	}

	podName, token := h.getLeaderAndToken(w, r, tenantID)
	if podName == "" {
		return
	}
	defer func() { token = "" }()

	mount := mountPath(tenantUUID, kv.ID)

	version, isNew, err := h.kvi.WriteSecret(r.Context(), tenantID, podName, mount, key, token, req.Value, req.Metadata)
	if err != nil {
		if handleOpenBaoErr(w, err, key) {
			return
		}
		h.log.Error().Err(err).Str("vault", kv.ID.String()).Str("key", key).Msg("write secret")
		writeError(w, http.StatusInternalServerError, "failed to write secret")
		return
	}

	resp := keyVaultSecretResponse{
		Key:       key,
		Value:     req.Value,
		Version:   version,
		Metadata:  req.Metadata,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	statusCode := http.StatusOK
	if isNew {
		statusCode = http.StatusCreated
	}
	writeJSON(w, statusCode, resp)
}

// DeleteKeyVaultSecret handles DELETE .../keyvaults/{id}/secrets/{key}.
//
// RBAC: member.
// Soft-deletes the latest version. Returns:
//   - 204 on success
//   - 404 if the key never existed
//   - 409 if the latest version is already soft-deleted
func (h *KeyVaultSecretsHandler) DeleteKeyVaultSecret(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionSecretDelete) {
		return
	}

	key := chi.URLParam(r, "key")
	if err := validateSecretKey(key); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	kv := h.resolveVault(w, r, tenantUUID)
	if kv == nil {
		return
	}

	podName, token := h.getLeaderAndToken(w, r, tenantID)
	if podName == "" {
		return
	}
	defer func() { token = "" }()

	mount := mountPath(tenantUUID, kv.ID)

	// Pre-check: is the key known? Is it already deleted?
	meta, err := h.kvi.GetSecretMetadata(r.Context(), tenantID, podName, mount, key, token)
	if err != nil {
		if handleOpenBaoErr(w, err, key) {
			return
		}
		h.log.Error().Err(err).Str("vault", kv.ID.String()).Str("key", key).Msg("get metadata pre-delete")
		writeError(w, http.StatusInternalServerError, "failed to read secret metadata")
		return
	}

	// Check if latest version is already actually deleted (not just
	// future-scheduled via delete_version_after).
	if lv, ok := meta.VersionMeta[strconv.Itoa(meta.CurrentVersion)]; ok {
		if isVersionDeleted(lv.DeletionTime) || lv.Destroyed {
			writeError(w, http.StatusConflict,
				fmt.Sprintf("secret %q latest version is already deleted or destroyed", key))
			return
		}
	}

	if err := h.kvi.DeleteSecret(r.Context(), tenantID, podName, mount, key, token); err != nil {
		if handleOpenBaoErr(w, err, key) {
			return
		}
		h.log.Error().Err(err).Str("vault", kv.ID.String()).Str("key", key).Msg("delete secret")
		writeError(w, http.StatusInternalServerError, "failed to delete secret")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// RestoreKeyVaultSecret reverses a soft-delete on the latest deleted version.
//
// Returns:
//   - 204 No Content on success
//   - 404 if the key never existed
//   - 409 if the latest version is not in the deleted state
//   - 410 if the latest version has been hard-destroyed (irrecoverable)
func (h *KeyVaultSecretsHandler) RestoreKeyVaultSecret(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionSecretWrite) {
		return
	}

	key := chi.URLParam(r, "key")
	if err := validateSecretKey(key); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	kv := h.resolveVault(w, r, tenantUUID)
	if kv == nil {
		return
	}

	podName, token := h.getLeaderAndToken(w, r, tenantID)
	if podName == "" {
		return
	}
	defer func() { token = "" }()

	mount := mountPath(tenantUUID, kv.ID)

	// Pre-check the metadata to decide which version to restore + reject
	// 409/410 cases.
	meta, err := h.kvi.GetSecretMetadata(r.Context(), tenantID, podName, mount, key, token)
	if err != nil {
		if handleOpenBaoErr(w, err, key) {
			return
		}
		h.log.Error().Err(err).Str("vault", kv.ID.String()).Str("key", key).Msg("get metadata pre-restore")
		writeError(w, http.StatusInternalServerError, "failed to read secret metadata")
		return
	}

	lv, hasLatest := meta.VersionMeta[strconv.Itoa(meta.CurrentVersion)]
	if !hasLatest {
		writeError(w, http.StatusNotFound, fmt.Sprintf("secret %q has no readable version metadata", key))
		return
	}
	if lv.Destroyed {
		writeError(w, http.StatusGone,
			fmt.Sprintf("secret %q latest version is permanently destroyed and cannot be restored", key))
		return
	}
	if !isVersionDeleted(lv.DeletionTime) {
		writeError(w, http.StatusConflict,
			fmt.Sprintf("secret %q latest version is not soft-deleted; nothing to restore", key))
		return
	}

	if err := h.kvi.UndeleteSecretVersion(r.Context(), tenantID, podName, mount, key, token, meta.CurrentVersion); err != nil {
		if handleOpenBaoErr(w, err, key) {
			return
		}
		h.log.Error().Err(err).Str("vault", kv.ID.String()).Str("key", key).Int("version", meta.CurrentVersion).Msg("undelete secret")
		writeError(w, http.StatusInternalServerError, "failed to restore secret")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

