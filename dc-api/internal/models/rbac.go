// Package models — rbac.go
//
// Domain types for M1.5 RBAC: role assignments, service accounts, and the
// scope/principal enumerations used throughout the authorisation layer.
//
// Design notes:
//   - PrincipalType and ScopeType are string-typed enums so new values can be
//     added (in M5) without a schema migration — they are stored as TEXT in
//     PostgreSQL rather than as a ENUM type.
//   - ScopeType today only takes the value "tenant". When M5 lands
//     ("subscription", "resource_group", "resource") the constant list grows
//     but existing DB rows are unaffected.
package models

import (
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────── Enumerations ───────────────────────────────────

// PrincipalType distinguishes human users (identified by JWT sub) from
// DC-API-issued service accounts (identified by service_account.id).
type PrincipalType string

const (
	PrincipalTypeUser           PrincipalType = "user"
	PrincipalTypeServiceAccount PrincipalType = "service_account"
)

// ScopeType identifies the hierarchical level at which a role is granted.
// M1.5 only uses ScopeTypeTenant. The remaining values are placeholders that
// will become valid in M5 when the Organisation → Subscription → Resource Group
// → Resource hierarchy is implemented. No schema migration will be required
// when they land — they are stored as plain TEXT.
type ScopeType string

const (
	ScopeTypeTenant  ScopeType = "tenant"
	ScopeTypeProject ScopeType = "project"
	// ScopeTypeResource is the narrowest scope (RBAC v2): a role bound to a
	// single resource by its UUID. See docs/rbac-v2.md §5.3.
	ScopeTypeResource ScopeType = "resource"
)

// Role is the permission level granted within a scope.
// platform-admin is handled by the Asgardeo group short-circuit in the
// middleware (dc-admin group) — it does not appear as a role_assignments row.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleMember Role = "member"
	RoleViewer Role = "viewer"
)

// ─────────────────────── RBAC v2 storage bridge ─────────────────────────────
//
// RBAC v2 stores a role-definition KEY in role_assignments.role_definition
// instead of the v1 owner/member/viewer rank. These three keys are the built-in
// roles the v1 ranks map onto (see docs/rbac-v2.md §4.1); the literals mirror
// the rbac package's built-in catalog (rbac.RoleOwner / RoleContributor /
// RoleReader) — a unit test asserts they stay in sync. The mapping helpers are a
// transitional bridge: the in-memory model still carries the v1 Role rank
// (consumed by the rank-based enforcement path), while persistence speaks the
// v2 key. P1 retires this bridge when handlers adopt action-based checks and the
// model carries the key directly.
const (
	RoleDefOwner       = "Owner"
	RoleDefContributor = "Contributor"
	RoleDefReader      = "Reader"
)

// RoleDefinitionForRole maps a v1 rank role to its v2 built-in role-definition
// key (the value persisted in role_assignments.role_definition). Returns "" for
// an unknown role.
func RoleDefinitionForRole(r Role) string {
	switch r {
	case RoleOwner:
		return RoleDefOwner
	case RoleMember:
		return RoleDefContributor
	case RoleViewer:
		return RoleDefReader
	default:
		return ""
	}
}

// RoleForRoleDefinition maps a persisted role-definition key back to the v1 rank
// role used by the (transitional) rank-based enforcement path. Keys outside the
// three built-ins above — future per-resource-type or custom roles — map to ""
// (rank 0); those are only meaningful to the v2 action engine, not the rank path.
func RoleForRoleDefinition(key string) Role {
	switch key {
	case RoleDefOwner:
		return RoleOwner
	case RoleDefContributor:
		return RoleMember
	case RoleDefReader:
		return RoleViewer
	default:
		return ""
	}
}

// ─────────────────────────── Role Assignment ────────────────────────────────

// RoleAssignment is the canonical DC-API representation of a single role
// binding. One row in the role_assignments table.
//
// The UNIQUE constraint (principal_type, principal_id, scope_type, scope_id,
// role) means a principal can hold multiple roles at different scopes, or even
// multiple roles at the same scope (e.g., both owner and viewer), though the
// authorisation helpers collapse them to the most permissive.
type RoleAssignment struct {
	ID            uuid.UUID     `json:"id"`
	PrincipalType PrincipalType `json:"principal_type"`
	PrincipalID   string        `json:"principal_id"`
	ScopeType     ScopeType     `json:"scope_type"`
	ScopeID       string        `json:"scope_id"`
	// ScopeUUID is the immutable identity for the scope (Phase 6a).
	// For scope_type='tenant' this is tenants.tenant_uuid. Populated on write;
	// read back on every SELECT. M5 will use it for other scope_types too.
	ScopeUUID     uuid.UUID     `json:"scope_uuid"`
	Role          Role          `json:"role"`
	// RoleDefinition is the RBAC v2 role-definition key persisted in the
	// role_definition column — a built-in key ("Contributor",
	// "VirtualMachineContributor", …) or a custom role_definitions.id. Populated
	// on read; the v2 action engine resolves it. Role above is the rank derived
	// from it for the transitional rank-based path (retired as handlers move to
	// requireAction).
	RoleDefinition string       `json:"role_definition,omitempty"`
	GrantedAt     time.Time     `json:"granted_at"`
	GrantedBy     string        `json:"granted_by"` // principal_id of the granter
	// DisplayAlias is an admin-set mnemonic shown by cloud-ui instead of
	// the opaque sub. Optional. No PII is sourced from the IdP — purely
	// what the inviter typed when they added the principal.
	DisplayAlias string `json:"display_alias,omitempty"`
}

// ─────────────────────────── Service Account ────────────────────────────────

// ServiceAccount is a DC-API-managed principal for non-human callers (CI/CD
// pipelines, automation scripts). It authenticates via a long-lived token
// rather than an OIDC JWT. The raw token is returned exactly once on creation
// and is never stored; only its bcrypt hash (token_hash) lives in the DB.
//
// Token format: dcapi_sa_<lookup_id>_<secret>
//   - lookup_id (12 chars): stored in plaintext as token_lookup_id; used to find
//     the candidate row in O(1) via an indexed column lookup.
//   - secret (32 chars): the part that is bcrypt-hashed and stored in token_hash.
//
// This two-part design avoids an O(N) bcrypt scan across all service accounts on
// every authenticated request, while keeping the security properties of bcrypt.
//
// ServiceAccount rows appear in role_assignments with
// principal_type = PrincipalTypeServiceAccount and
// principal_id   = ServiceAccount.ID.String().
type ServiceAccount struct {
	ID          uuid.UUID `json:"id"`
	TenantID    string    `json:"tenant_id"`
	TenantUUID  uuid.UUID `json:"tenant_uuid"`
	ProjectID   string    `json:"project_id"`
	ProjectUUID uuid.UUID `json:"project_uuid"`
	Name          string     `json:"name"`
	TokenLookupID string     `json:"-"`                    // first 12 chars of raw token; stored plain for indexed lookup
	TokenHash     string     `json:"-"`                    // bcrypt hash of the secret portion; never serialised to callers
	Description   string     `json:"description,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	LastUsed      *time.Time `json:"last_used,omitempty"` // nil until the token is used for the first time
}
