// Package rbac provides pure authorisation logic for DC-API.
//
// It has no knowledge of HTTP or PostgreSQL. The evaluation engine in engine.go
// (Authorize / HasGrantInChain) decides — from a principal's role assignments and
// a target scope chain — whether an action is permitted. The action taxonomy,
// role definitions, and built-in catalog live in actions.go, roledef.go, and
// builtins.go.
//
// ── Circular-import safety ────────────────────────────────────────────────────
//
// This package imports only the standard library and the project's own models
// package — never internal/db. The Repo interface below is satisfied implicitly
// by *db.Repository, so middleware and handlers (which import this package) and
// db never form an import cycle.
package rbac

import (
	"context"

	"github.com/wso2/dc-api/internal/models"
)

// Repo is the narrow data-access interface the authorization callers need: a
// single round-trip returning all of a principal's role assignments across every
// scope. Callers map the rows into engine Assignments and filter them against the
// request's scope chain in memory (a principal holds O(1) assignments).
// *db.Repository satisfies it implicitly; tests use a tiny in-memory fake.
type Repo interface {
	ListRoleAssignmentsForPrincipal(
		ctx context.Context,
		principalType models.PrincipalType,
		principalID string,
	) ([]models.RoleAssignment, error)
}
