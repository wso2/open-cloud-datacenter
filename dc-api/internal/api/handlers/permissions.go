// Package handlers — permissions.go
//
// PermissionsHandler answers "may the caller do these actions here?" so the web
// UI (and CLI) can enable/disable controls WITHOUT ever re-implementing the
// authorization matcher. The engine lives in dc-api; this endpoint runs it on the
// caller's behalf and returns plain booleans.
//
//	POST /v1/tenants/{tenant_id}/permissions:check
//	  { "actions": ["authorization/roleAssignments/write", "compute/virtualMachines/write"] }
//	→ { "results": [ {"action":"authorization/roleAssignments/write","allowed":true}, ... ] }
//
// Each action is evaluated at the request's scope chain (tenant today; project and
// resource scope ride the same handler unchanged once those routes mount it). The
// control vs data plane is inferred per action via rbac.IsDataAction.
package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/rs/zerolog"
	"github.com/wso2/dc-api/internal/api/middleware"
	"github.com/wso2/dc-api/internal/rbac"
)

// maxPermissionChecks caps one check request so a caller can't ask dc-api to
// evaluate an unbounded action list in a single round-trip.
const maxPermissionChecks = 64

// PermissionsHandler evaluates capability checks for the calling principal.
type PermissionsHandler struct {
	repo rbac.Repo
	log  zerolog.Logger
}

// NewPermissionsHandler constructs a PermissionsHandler.
func NewPermissionsHandler(repo rbac.Repo, log zerolog.Logger) *PermissionsHandler {
	return &PermissionsHandler{repo: repo, log: log}
}

type permissionsCheckRequest struct {
	Actions []string `json:"actions"`
}

type permissionResult struct {
	Action  string `json:"action"`
	Allowed bool   `json:"allowed"`
}

type permissionsCheckResponse struct {
	Results []permissionResult `json:"results"`
}

// Check handles POST /v1/tenants/{tenant_id}/permissions:check. It resolves the
// caller's role assignments once, then evaluates each requested action against the
// request's scope chain. Any authenticated caller may ask about their OWN
// permissions — the answer reflects only what they actually hold (admins get
// `true` for everything via the engine's short-circuit).
func (h *PermissionsHandler) Check(w http.ResponseWriter, r *http.Request) {
	pType, pID, ok := middleware.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no principal in context")
		return
	}
	isAdmin := middleware.IsAdminFromContext(r.Context())

	var req permissionsCheckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if len(req.Actions) == 0 {
		writeError(w, http.StatusBadRequest, "actions must not be empty")
		return
	}
	if len(req.Actions) > maxPermissionChecks {
		writeError(w, http.StatusBadRequest, "too many actions in one check request")
		return
	}

	assignments, err := h.repo.ListRoleAssignmentsForPrincipal(r.Context(), pType, pID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "permission check failed")
		return
	}
	ras := make([]rbac.Assignment, 0, len(assignments))
	for _, a := range assignments {
		ras = append(ras, rbac.Assignment{
			RoleDefKey: a.RoleDefinition,
			ScopeType:  a.ScopeType,
			ScopeUUID:  a.ScopeUUID,
		})
	}
	chain := scopeChainFromContext(r.Context())

	results := make([]permissionResult, len(req.Actions))
	for i, action := range req.Actions {
		results[i] = permissionResult{
			Action:  action,
			Allowed: rbac.Authorize(ras, rbac.BuiltinResolver, action, rbac.IsDataAction(action), chain, isAdmin),
		}
	}
	writeJSON(w, http.StatusOK, permissionsCheckResponse{Results: results})
}
