// Package handlers — database.go
//
// Task 1 v1: Database (DBaaS) HTTP handler. Mirrors the KVI managed-service
// pattern (handlers/keyvault.go) — bespoke handler today, will collapse into
// a managedservice.Register() call when the generic framework lands.
//
// Two operating modes:
//
//  1. Provisioner wired (production):
//       POST   resolves the Multus NAD identity (VPC mode → subnet
//              lookup; legacy mode → pass-through; omitted → project
//              default), inserts a PENDING row, then creates a DBInstance
//              CR. The dbaas controller provisions asynchronously; status
//              flips to ACTIVE when status.phase=Ready.
//       GET    overlays the live CR status onto the DB row so the caller
//              sees fresh phase/endpoint/message without waiting for a
//              reconciler tick.
//       DELETE deletes the DBInstance CR (operator finalizer handles the
//              VM/DataVolume/Secret cleanup async) and drops the DB row
//              immediately.
//
//  2. Provisioner nil (tests / non-Kubernetes deployments):
//       Synchronous DB-only CRUD with status=ACTIVE on create. Credentials
//       endpoint returns 501. Integration tests that boot dc-api without
//       a Kubernetes backend exercise this path.
//
// Tenant scoping is enforced via TenantFromContext (auth middleware) plus a
// post-fetch tenant_uuid match (returns 404 on mismatch — don't leak
// existence to non-owners). RBAC: all endpoints require role `member`.
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

// DatabaseHandler handles /v1/tenants/{tid}/projects/{pid}/databases.
type DatabaseHandler struct {
	repo *db.Repository
	// provisioner is optional. Nil falls back to DB-only synchronous CRUD
	// (used by tests + dc-api deployments without a Kubernetes backend).
	provisioner providers.DatabaseProvisioner
	// osImage is the operator-configured Harvester VM image the controller
	// boots database VMs from (DCAPI_DBAAS_OS_IMAGE). Stamped into every
	// DBInstance CR. Empty leaves the controller's own default.
	osImage string
	log     zerolog.Logger
}

// NewDatabaseHandler constructs a DatabaseHandler. Pass nil for provisioner
// to keep the DB-only fallback behaviour. osImage is the operator-configured
// default OS image ("namespace/name"); empty defers to the controller default.
func NewDatabaseHandler(repo *db.Repository, provisioner providers.DatabaseProvisioner, osImage string, log zerolog.Logger) *DatabaseHandler {
	return &DatabaseHandler{repo: repo, provisioner: provisioner, osImage: osImage, log: log}
}

// ── DTOs ──────────────────────────────────────────────────────────────────────

type createDatabaseRequest struct {
	Name               string  `json:"name"`
	Engine             string  `json:"engine,omitempty"`         // defaults to "postgres"
	EngineVersion      string  `json:"engine_version,omitempty"` // informational in v1
	InstanceClass      string  `json:"instance_class"`
	AllocatedStorageGB int     `json:"allocated_storage_gb"`
	Network            *netReq `json:"network,omitempty"` // omit → project default
}

// netReq is the discriminated network block on createDatabaseRequest.
//
//	{ "mode": "vpc",    "vnet_id": "...", "subnet_id": "..." }
//	{ "mode": "legacy", "nad_ref": "namespace/name" }
type netReq struct {
	Mode     string `json:"mode"`
	VNetID   string `json:"vnet_id,omitempty"`
	SubnetID string `json:"subnet_id,omitempty"`
	NadRef   string `json:"nad_ref,omitempty"`
}

type databaseResponse struct {
	ID          string `json:"id"`
	TenantID    string `json:"tenant_id"`
	ProjectID   string `json:"project_id"`
	Name        string `json:"name"`
	Engine      string `json:"engine"`
	EngineVersion      string `json:"engine_version,omitempty"`
	InstanceClass      string `json:"instance_class"`
	AllocatedStorageGB int    `json:"allocated_storage_gb"`

	NetworkMode string `json:"network_mode"`
	VNetID      string `json:"vnet_id,omitempty"`
	SubnetID    string `json:"subnet_id,omitempty"`
	NadRef      string `json:"nad_ref,omitempty"`

	Status  string `json:"status"`
	Message string `json:"message,omitempty"`

	EndpointAddress string `json:"endpoint_address,omitempty"`
	EndpointPort    int    `json:"endpoint_port,omitempty"`

	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type databaseCredentialsResponse struct {
	Username string `json:"username"`
	Password string `json:"password"`
	CACert   string `json:"ca_cert,omitempty"` // PEM
}

func databaseToResponse(d *models.Database) databaseResponse {
	resp := databaseResponse{
		ID:                 d.ID.String(),
		TenantID:           d.TenantID,
		ProjectID:          d.ProjectID,
		Name:               d.Name,
		Engine:             string(d.Engine),
		EngineVersion:      d.EngineVersion,
		InstanceClass:      d.InstanceClass,
		AllocatedStorageGB: d.AllocatedStorageGB,
		NetworkMode:        string(d.NetworkMode),
		NadRef:             d.NadRef,
		Status:             string(d.Status),
		Message:            d.Message,
		EndpointAddress:    d.EndpointAddress,
		EndpointPort:       d.EndpointPort,
		CreatedAt:          d.CreatedAt.Format(time.RFC3339),
		UpdatedAt:          d.UpdatedAt.Format(time.RFC3339),
	}
	if d.VNetID != nil {
		resp.VNetID = d.VNetID.String()
	}
	if d.SubnetID != nil {
		resp.SubnetID = d.SubnetID.String()
	}
	return resp
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// Create handles POST /v1/tenants/{tid}/projects/{pid}/databases.
//
// In provisioner-wired mode: resolves NAD identity, inserts DB row in PENDING,
// creates the DBInstance CR. Returns 201. The dbaas controller provisions
// asynchronously.
// In fallback mode: writes the row with status=ACTIVE synchronously.
func (h *DatabaseHandler) Create(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionDBServerWrite) {
		return
	}

	var req createDatabaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	// ── Validate basic spec fields ─────────────────────────────────────
	if err := validateResourceName(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Engine == "" {
		req.Engine = string(models.DatabaseEnginePostgres)
	}
	if req.Engine != string(models.DatabaseEnginePostgres) {
		writeError(w, http.StatusBadRequest, "engine must be 'postgres' (v1)")
		return
	}
	if req.InstanceClass == "" {
		writeError(w, http.StatusBadRequest, "instance_class is required")
		return
	}
	if !models.ValidDatabaseInstanceClass(req.InstanceClass) {
		writeError(w, http.StatusBadRequest,
			"instance_class '"+req.InstanceClass+"' is not a known size (see DatabaseInstanceClasses)")
		return
	}
	if req.AllocatedStorageGB <= 0 {
		writeError(w, http.StatusBadRequest, "allocated_storage_gb must be > 0")
		return
	}

	projectID, projectUUID, ok := lookupProjectUUID(w, r)
	if !ok {
		return
	}

	// ── Resolve the network ────────────────────────────────────────────
	networkMode, vnetID, subnetID, nadRef, networkRef, err := h.resolveNetwork(
		r.Context(), req.Network, tenantUUID, projectUUID, tenantID, projectID,
	)
	if err != nil {
		// resolveNetwork already wrote the response on auth/validation failures.
		var herr *httpError
		if errors.As(err, &herr) {
			writeError(w, herr.status, herr.msg)
			return
		}
		h.log.Error().Err(err).Str("tenant", tenantID).Msg("resolve database network")
		writeError(w, http.StatusInternalServerError, "failed to resolve network")
		return
	}

	// ── Initial status ──────────────────────────────────────────────────
	initialStatus := models.StatusActive
	if h.provisioner != nil {
		initialStatus = models.StatusPending
	}

	row := &models.Database{
		TenantID:           tenantID,
		TenantUUID:         tenantUUID,
		ProjectID:          projectID,
		ProjectUUID:        projectUUID,
		Name:               req.Name,
		Engine:             models.DatabaseEnginePostgres,
		EngineVersion:      req.EngineVersion,
		InstanceClass:      req.InstanceClass,
		AllocatedStorageGB: req.AllocatedStorageGB,
		NetworkMode:        networkMode,
		VNetID:             vnetID,
		SubnetID:           subnetID,
		NadRef:             nadRef,
		Status:             initialStatus,
	}

	created, err := h.repo.CreateDatabase(r.Context(), row)
	if err != nil {
		if strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "duplicate key") {
			writeError(w, http.StatusConflict,
				"a database named '"+req.Name+"' already exists in this project")
			return
		}
		h.log.Error().Err(err).Str("tenant", tenantID).Msg("create database row")
		writeError(w, http.StatusInternalServerError, "failed to create database")
		return
	}

	// ── Drive the operator ─────────────────────────────────────────────
	// Failures here don't roll back the DB row — they leave the row in
	// PENDING with a diagnostic message the caller surfaces via GET. Same
	// failure-mode KVI uses.
	if h.provisioner != nil {
		if err := h.driveProvisioner(r.Context(), created, networkRef); err != nil {
			h.log.Warn().Err(err).
				Str("tenant", tenantID).
				Str("database_id", created.ID.String()).
				Msg("DBInstance provisioning failed; row left in PENDING with error message")
			created.Message = "provisioning failed: " + err.Error()
		}
	}

	writeJSON(w, http.StatusCreated, databaseToResponse(created))
}

// driveProvisioner builds the StandardLabels + CR-name and asks the adapter
// to create the DBInstance CR.
func (h *DatabaseHandler) driveProvisioner(
	ctx context.Context,
	d *models.Database,
	networkRef providers.DatabaseNetworkRef,
) error {
	labels := common.StandardLabels(
		d.TenantID, d.ProjectID, d.TenantUUID, d.ProjectUUID, d.ID,
		"database", d.Name,
	)
	crName := common.NamespaceScopedName("db", d.ID)
	ns := common.NamespaceForProject(d.TenantID, d.ProjectID)

	return h.provisioner.CreateDatabaseInstance(ctx, providers.DatabaseInstanceCreateRequest{
		Name:               crName,
		Namespace:          ns,
		Labels:             labels,
		InstanceClass:      d.InstanceClass,
		AllocatedStorageGB: d.AllocatedStorageGB,
		EngineVersion:      d.EngineVersion,
		OSImage:            h.osImage,
		NetworkRef:         networkRef,
	})
}

// Get handles GET /v1/.../databases/{id}.
//
// In provisioner-wired mode: overlays the live CR status onto the DB row
// before returning.
func (h *DatabaseHandler) Get(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionDBServerRead) {
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid database id")
		return
	}
	d, err := h.repo.GetDatabase(r.Context(), id, tenantUUID, projectUUID)
	if errors.Is(err, db.ErrDatabaseNotFound) {
		writeError(w, http.StatusNotFound, "database not found")
		return
	}
	if err != nil {
		h.log.Error().Err(err).Str("id", id.String()).Msg("get database")
		writeError(w, http.StatusInternalServerError, "failed to fetch database")
		return
	}
	if d.TenantUUID != tenantUUID {
		// 404 not 403 — don't leak existence to non-owners.
		writeError(w, http.StatusNotFound, "database not found")
		return
	}

	resp := databaseToResponse(d)
	h.overlayLiveStatus(r.Context(), d, &resp)
	writeJSON(w, http.StatusOK, resp)
}

// List handles GET /v1/.../databases. Overlays the live CR status onto each
// row so the list view shows the same Active/Pending/Failed phase as the
// detail view. Costs one K8s GET per row — fine for the typical database
// count per project (dozens at most). Move to a periodic DB-sync reconciler
// if the count grows past that.
func (h *DatabaseHandler) List(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionDBServerRead) {
		return
	}
	_, projectUUID, ok := lookupProjectUUID(w, r)
	if !ok {
		return
	}

	rows, err := h.repo.ListDatabasesByProject(r.Context(), tenantUUID, projectUUID)
	if err != nil {
		h.log.Error().Err(err).Str("tenant", tenantID).Msg("list databases")
		writeError(w, http.StatusInternalServerError, "failed to list databases")
		return
	}

	out := make([]databaseResponse, 0, len(rows))
	for _, d := range rows {
		resp := databaseToResponse(d)
		h.overlayLiveStatus(r.Context(), d, &resp)
		out = append(out, resp)
	}
	writeJSON(w, http.StatusOK, out)
}

// Delete handles DELETE /v1/.../databases/{id}.
//
// In provisioner-wired mode: deletes the DBInstance CR (the controller's
// finalizer handles VM/DataVolume/Secret teardown async) and drops the DB
// row immediately.
func (h *DatabaseHandler) Delete(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionDBServerDelete) {
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid database id")
		return
	}

	// Tenant scope guard — fetch first so we don't delete another tenant's row.
	d, err := h.repo.GetDatabase(r.Context(), id, tenantUUID, projectUUID)
	if errors.Is(err, db.ErrDatabaseNotFound) {
		writeError(w, http.StatusNotFound, "database not found")
		return
	}
	if err != nil {
		h.log.Error().Err(err).Str("id", id.String()).Msg("get database for delete")
		writeError(w, http.StatusInternalServerError, "failed to fetch database")
		return
	}
	if d.TenantUUID != tenantUUID {
		writeError(w, http.StatusNotFound, "database not found")
		return
	}

	// Operator-side teardown. Idempotent on the adapter (NotFound → success).
	if h.provisioner != nil {
		ns := common.NamespaceForProject(d.TenantID, d.ProjectID)
		crName := common.NamespaceScopedName("db", d.ID)
		if err := h.provisioner.DeleteDatabaseInstance(r.Context(), ns, crName); err != nil {
			h.log.Error().Err(err).
				Str("database_id", d.ID.String()).
				Msg("delete DBInstance CR")
			writeError(w, http.StatusInternalServerError, "failed to delete database from operator")
			return
		}
	}

	if err := h.repo.DeleteDatabase(r.Context(), id); err != nil {
		if errors.Is(err, db.ErrDatabaseNotFound) {
			writeError(w, http.StatusNotFound, "database not found")
			return
		}
		h.log.Error().Err(err).Str("id", id.String()).Msg("delete database row")
		writeError(w, http.StatusInternalServerError, "failed to delete database")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Credentials handles GET /v1/.../databases/{id}/credentials.
//
// Shown-once semantics:
//   - First call: returns username/password/ca_cert read from the operator's
//     credentials Secret; stamps credentials_consumed_at = NOW() in the DB.
//   - Subsequent calls: returns 410 Gone.
//
// Pre-conditions:
//   - Provisioner must be wired (no creds without the operator). 501 otherwise.
//   - The CR's status.phase must be Ready. 409 with a pointer to GET /{id}
//     otherwise.
func (h *DatabaseHandler) Credentials(w http.ResponseWriter, r *http.Request) {
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
	if !requireAction(w, r, h.repo, rbac.ActionDBCredentialsRead) {
		return
	}
	if h.provisioner == nil {
		writeError(w, http.StatusNotImplemented,
			"credentials endpoint requires the dbaas operator integration; not available on this dc-api deployment")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid database id")
		return
	}
	d, err := h.repo.GetDatabase(r.Context(), id, tenantUUID, projectUUID)
	if errors.Is(err, db.ErrDatabaseNotFound) {
		writeError(w, http.StatusNotFound, "database not found")
		return
	}
	if err != nil {
		h.log.Error().Err(err).Str("id", id.String()).Msg("get database for credentials")
		writeError(w, http.StatusInternalServerError, "failed to fetch database")
		return
	}
	if d.TenantUUID != tenantUUID {
		writeError(w, http.StatusNotFound, "database not found")
		return
	}
	if d.CredentialsConsumedAt != nil {
		writeError(w, http.StatusGone,
			"credentials already retrieved at "+d.CredentialsConsumedAt.Format(time.RFC3339)+
				" — they are shown only once; recreate the database or rotate via the operator if you need fresh ones")
		return
	}

	// Read live CR status to find the credentials Secret name.
	ns := common.NamespaceForProject(d.TenantID, d.ProjectID)
	crName := common.NamespaceScopedName("db", d.ID)
	st, err := h.provisioner.GetDatabaseInstance(r.Context(), ns, crName)
	if err != nil {
		h.log.Error().Err(err).Str("database_id", d.ID.String()).Msg("read DBInstance for credentials")
		writeError(w, http.StatusInternalServerError, "failed to read database status")
		return
	}
	if st == nil || st.Phase != "Ready" || st.SecretRefName == "" {
		phase := "not yet provisioned"
		if st != nil && st.Phase != "" {
			phase = st.Phase
		}
		writeError(w, http.StatusConflict,
			"database is not Ready yet (phase="+phase+
				"); poll GET /v1/.../databases/"+d.ID.String()+" until status=ACTIVE before requesting credentials")
		return
	}

	data, err := h.provisioner.GetDatabaseCredentialsSecret(r.Context(), ns, st.SecretRefName)
	if err != nil {
		h.log.Error().Err(err).
			Str("database_id", d.ID.String()).
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
	if err := h.repo.MarkDatabaseCredentialsConsumed(r.Context(), d.ID); err != nil {
		if errors.Is(err, db.ErrCredentialsAlreadyConsumed) {
			writeError(w, http.StatusGone,
				"credentials already retrieved — they are shown only once; recreate the database or rotate via the operator if you need fresh ones")
			return
		}
		h.log.Error().Err(err).Str("database_id", d.ID.String()).Msg("stamp credentials consumed")
		writeError(w, http.StatusInternalServerError, "failed to record credentials consumption")
		return
	}

	// dbaas controller writes admin_user/admin_password/ca_cert; map to the
	// canonical username/password/ca_cert response shape.
	writeJSON(w, http.StatusOK, databaseCredentialsResponse{
		Username: string(data["admin_user"]),
		Password: string(data["admin_password"]),
		CACert:   string(data["ca_cert"]),
	})
}

// ── helpers ───────────────────────────────────────────────────────────────────

// overlayLiveStatus mutates resp in place with the controller's live status.
// No-op when the provisioner is nil. Errors are logged + swallowed so the
// caller still gets a DB-only view rather than a 500.
func (h *DatabaseHandler) overlayLiveStatus(ctx context.Context, d *models.Database, resp *databaseResponse) {
	if h.provisioner == nil {
		return
	}
	ns := common.NamespaceForProject(d.TenantID, d.ProjectID)
	crName := common.NamespaceScopedName("db", d.ID)
	st, err := h.provisioner.GetDatabaseInstance(ctx, ns, crName)
	if err != nil {
		h.log.Warn().Err(err).
			Str("database_id", d.ID.String()).
			Msg("read DBInstance CR status failed; returning DB-only view")
		return
	}
	if st == nil {
		return
	}
	if phase := mapDatabasePhaseToStatus(st.Phase); phase != "" {
		resp.Status = phase
	}
	if st.Message != "" {
		resp.Message = st.Message
	}
	if st.EndpointAddress != "" {
		resp.EndpointAddress = st.EndpointAddress
	}
	if st.EndpointPort != 0 {
		resp.EndpointPort = st.EndpointPort
	}
}

// mapDatabasePhaseToStatus translates the framework-canonical phase enum
// (already normalised by the adapter via mapRDSPhase) into dc-api's
// ResourceStatus. Empty string when phase is unknown / unset (caller keeps
// the DB-row status).
func mapDatabasePhaseToStatus(phase string) string {
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

// ── network resolution ────────────────────────────────────────────────────────

// httpError carries an HTTP-mapped error from resolveNetwork back to Create.
// Used instead of a plain error so the handler can preserve the status code
// the resolver chose (400 for missing subnet, 409 for inactive subnet, ...).
type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

// resolveNetwork turns the (optional) network block on the request into a
// concrete NAD identity. Returns:
//   - networkMode, vnetID, subnetID, nadRef → DB row values (some nil per mode)
//   - networkRef → identity passed to the provisioner
//
// On user-input errors, returns an *httpError with the right HTTP status.
func (h *DatabaseHandler) resolveNetwork(
	ctx context.Context,
	in *netReq,
	tenantUUID, projectUUID uuid.UUID,
	tenantID, projectID string,
) (
	mode models.DatabaseNetworkMode,
	vnetID, subnetID *uuid.UUID,
	nadRef string,
	networkRef providers.DatabaseNetworkRef,
	err error,
) {
	// Default path: caller omitted the network block → pick the project's
	// first ACTIVE subnet in its first ACTIVE VPC. VPC mode.
	if in == nil {
		sub, derr := h.findProjectDefaultSubnet(ctx, tenantUUID, projectUUID)
		if derr != nil {
			return "", nil, nil, "", providers.DatabaseNetworkRef{}, derr
		}
		vid := sub.VNetID
		sid := sub.ID
		return models.DatabaseNetworkModeVPC, &vid, &sid, "",
			providers.DatabaseNetworkRef{
				Namespace:   common.NamespaceForProject(tenantID, projectID),
				Name:        sub.BackendUID,
				DNSServerIP: h.vnetDNSServerIP(ctx, vid),
			}, nil
	}

	switch in.Mode {
	case "vpc":
		if in.VNetID == "" || in.SubnetID == "" {
			return "", nil, nil, "", providers.DatabaseNetworkRef{},
				&httpError{http.StatusBadRequest, "network.mode='vpc' requires both vnet_id and subnet_id"}
		}
		vid, perr := uuid.Parse(in.VNetID)
		if perr != nil {
			return "", nil, nil, "", providers.DatabaseNetworkRef{},
				&httpError{http.StatusBadRequest, "vnet_id is not a valid UUID"}
		}
		sid, perr := uuid.Parse(in.SubnetID)
		if perr != nil {
			return "", nil, nil, "", providers.DatabaseNetworkRef{},
				&httpError{http.StatusBadRequest, "subnet_id is not a valid UUID"}
		}

		sub, gerr := h.repo.GetSubnet(ctx, sid)
		if gerr != nil {
			return "", nil, nil, "", providers.DatabaseNetworkRef{},
				&httpError{http.StatusBadRequest, "subnet not found"}
		}
		// Scope checks: same tenant, same project, references the supplied vnet,
		// and is ACTIVE.
		if sub.TenantUUID != tenantUUID || sub.ProjectUUID != projectUUID {
			return "", nil, nil, "", providers.DatabaseNetworkRef{},
				&httpError{http.StatusBadRequest, "subnet not found in this project"}
		}
		if sub.VNetID != vid {
			return "", nil, nil, "", providers.DatabaseNetworkRef{},
				&httpError{http.StatusBadRequest, "subnet does not belong to the supplied vnet_id"}
		}
		if sub.Status != models.StatusActive {
			return "", nil, nil, "", providers.DatabaseNetworkRef{},
				&httpError{http.StatusConflict,
					"subnet is not ACTIVE (status=" + string(sub.Status) + ")"}
		}
		if sub.BackendUID == "" {
			return "", nil, nil, "", providers.DatabaseNetworkRef{},
				&httpError{http.StatusConflict,
					"subnet has not been provisioned by the network backend yet — try again shortly"}
		}
		return models.DatabaseNetworkModeVPC, &vid, &sid, "",
			providers.DatabaseNetworkRef{
				Namespace:   common.NamespaceForProject(tenantID, projectID),
				Name:        sub.BackendUID,
				DNSServerIP: h.vnetDNSServerIP(ctx, vid),
			}, nil

	case "legacy":
		if in.NadRef == "" {
			return "", nil, nil, "", providers.DatabaseNetworkRef{},
				&httpError{http.StatusBadRequest, "network.mode='legacy' requires nad_ref"}
		}
		parts := strings.SplitN(in.NadRef, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", nil, nil, "", providers.DatabaseNetworkRef{},
				&httpError{http.StatusBadRequest,
					"nad_ref must be 'namespace/name' (got '" + in.NadRef + "')"}
		}
		return models.DatabaseNetworkModeLegacy, nil, nil, in.NadRef,
			providers.DatabaseNetworkRef{Namespace: parts[0], Name: parts[1]}, nil

	default:
		return "", nil, nil, "", providers.DatabaseNetworkRef{},
			&httpError{http.StatusBadRequest,
				"network.mode must be 'vpc' or 'legacy' (got '" + in.Mode + "')"}
	}
}

// vnetDNSServerIP returns the per-VPC CoreDNS address for a VNet (the F20
// dns_server_ip), or "" when unset/unavailable. Best-effort: a lookup error is
// logged and swallowed — the database is still created; if the per-VPC DNS is
// genuinely missing the VM install would fail the same way regardless. Used to
// populate the DBInstance's dnsServerIP so the VM resolves through a
// VPC-reachable resolver (defeats the KubeVirt-on-OVN DHCP DNS race).
func (h *DatabaseHandler) vnetDNSServerIP(ctx context.Context, vnetID uuid.UUID) string {
	v, err := h.repo.GetVNetInternal(ctx, vnetID)
	if err != nil {
		h.log.Warn().Err(err).Str("vnet_id", vnetID.String()).
			Msg("resolve vnet dns_server_ip failed; proceeding without VM dnsConfig")
		return ""
	}
	return v.DNSServerIP
}

// findProjectDefaultSubnet returns the first ACTIVE subnet in the project's
// first ACTIVE VPC. Returns *httpError 400 with a useful pointer when no
// such subnet exists — the caller has to create a VPC + subnet first (or
// explicitly pass network.mode='legacy' if their database should attach to
// a pre-existing bridge NAD).
func (h *DatabaseHandler) findProjectDefaultSubnet(
	ctx context.Context,
	tenantUUID, projectUUID uuid.UUID,
) (*models.Subnet, error) {
	vnets, err := h.repo.ListVNetsByProject(ctx, tenantUUID, projectUUID)
	if err != nil {
		return nil, fmt.Errorf("list project vnets: %w", err)
	}
	for _, v := range vnets {
		if v.Status != models.StatusActive {
			continue
		}
		subs, err := h.repo.ListSubnetsByVNet(ctx, v.ID)
		if err != nil {
			return nil, fmt.Errorf("list subnets for vnet %s: %w", v.ID, err)
		}
		for _, s := range subs {
			if s.Status == models.StatusActive && s.BackendUID != "" {
				return s, nil
			}
		}
	}
	return nil, &httpError{http.StatusBadRequest,
		"no default network available — this project has no ACTIVE VPC/subnet. " +
			"Create one via POST /vnets and POST /vnets/{id}/subnets, or pass " +
			"network.mode='legacy' with a nad_ref."}
}
