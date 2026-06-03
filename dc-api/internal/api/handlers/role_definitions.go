// Package handlers — role_definitions.go
//
// RoleDefinitionsHandler serves the RBAC v2 role catalog:
//
//	GET /v1/role-definitions          → the full assignable catalog
//	GET /v1/role-definitions/{key}    → one role by its key
//
// The catalog is what a caller picks from when granting access. Today it is the
// in-code built-ins (rbac.BuiltinRoles); tenant-owned custom roles will join the
// same list, with the same shape, when they ship. Any authenticated caller may
// read it: role definitions are non-sensitive metadata, and the web UI needs them
// to render the role picker and a "what this role allows" detail view without
// re-implementing the engine's catalog.
package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"
	"github.com/wso2/dc-api/internal/rbac"
)

// RoleDefinitionsHandler serves the role catalog. It is stateless today (the
// built-ins live in code); the logger is kept for symmetry with the other
// handlers and for when custom roles are read from the repository.
type RoleDefinitionsHandler struct {
	log zerolog.Logger
}

// NewRoleDefinitionsHandler constructs a RoleDefinitionsHandler.
func NewRoleDefinitionsHandler(log zerolog.Logger) *RoleDefinitionsHandler {
	return &RoleDefinitionsHandler{log: log}
}

// roleDefinitionResponse is the JSON shape of a single role definition. The
// action lists let the UI render a role-detail view and seed the future
// custom-role builder; the picker itself needs only key/display_name/description.
type roleDefinitionResponse struct {
	Key            string   `json:"key"`
	DisplayName    string   `json:"display_name"`
	Description    string   `json:"description"`
	Actions        []string `json:"actions"`
	NotActions     []string `json:"not_actions,omitempty"`
	DataActions    []string `json:"data_actions,omitempty"`
	NotDataActions []string `json:"not_data_actions,omitempty"`
	Builtin        bool     `json:"builtin"`
}

func toRoleDefinitionResponse(d rbac.RoleDefinition) roleDefinitionResponse {
	return roleDefinitionResponse{
		Key:            d.Key,
		DisplayName:    d.DisplayName,
		Description:    d.Description,
		Actions:        d.Actions,
		NotActions:     d.NotActions,
		DataActions:    d.DataActions,
		NotDataActions: d.NotDataActions,
		Builtin:        d.Builtin,
	}
}

// List handles GET /v1/role-definitions — the full assignable catalog, sorted by
// key. Any authenticated caller may read it.
func (h *RoleDefinitionsHandler) List(w http.ResponseWriter, r *http.Request) {
	all := rbac.BuiltinRoles()
	resp := make([]roleDefinitionResponse, 0, len(all))
	for _, d := range all {
		resp = append(resp, toRoleDefinitionResponse(d))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"role_definitions": resp})
}

// Get handles GET /v1/role-definitions/{key} — one role by its key, or 404.
func (h *RoleDefinitionsHandler) Get(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	d, ok := rbac.BuiltinRole(key)
	if !ok {
		writeError(w, http.StatusNotFound, "role definition not found")
		return
	}
	writeJSON(w, http.StatusOK, toRoleDefinitionResponse(d))
}
