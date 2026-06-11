// Package directory provides an optional, read-only lookup facade over the
// deployment's IdP user/group directory (SCIM2 today; the Provider interface
// keeps the door open for others).
//
// Hard rules this package enforces — do not relax them when extending it:
//
//   - Proxy, don't store. Nothing read from the IdP is ever persisted by this
//     package or its callers. dc-api stores only the OIDC `sub` (and, on an
//     email-based invite, the email the inviter typed as the display_alias
//     default) — both on the role_assignments row, never sourced from a SCIM
//     response beyond the sub itself.
//   - Minimal fields only. The SCIM responses are decoded into minimal structs
//     that carry exactly the fields exposed here (sub/email/display_name for
//     users, id/name for groups). Groups' membership, phone numbers, addresses,
//     raw attributes — none of it is read out of the response.
//   - Read-only credential. The configured IdP client must hold user/group
//     VIEW scopes only. This package never issues anything but GETs.
//
// The feature is dark when unconfigured: main.go passes a nil Provider into
// the router and the handlers answer 501 (directory endpoints) / 422
// (invite-by-email) per the OpenAPI contract.
package directory

import (
	"context"
	"errors"
)

// User is the minimal directory user record: just enough for an invite picker.
type User struct {
	// Sub is the OIDC `sub` — for Asgardeo this equals the SCIM user `id`.
	Sub string
	// Email is the user's primary email/username.
	Email string
	// DisplayName is the human-readable name, when the IdP has one.
	DisplayName string
}

// Group is the minimal directory group record.
type Group struct {
	ID   string
	Name string
}

// Sentinel errors callers branch on with errors.Is.
var (
	// ErrNotConfigured is returned by code paths that require a directory
	// provider when none is configured on this deployment.
	ErrNotConfigured = errors.New("no directory provider is configured on this deployment")
	// ErrUserNotFound means an email point-lookup matched zero IdP users.
	ErrUserNotFound = errors.New("no IdP user matches that email")
	// ErrAmbiguous means an email point-lookup matched more than one IdP user.
	ErrAmbiguous = errors.New("email matches more than one IdP user")
	// ErrBadFilter means the caller-supplied filter/email contains characters
	// that cannot be safely embedded in a SCIM filter (defence against SCIM
	// filter injection). Handlers map it to a 4xx, never to a 502.
	ErrBadFilter = errors.New("filter value contains characters not allowed in a directory query")
)

// Provider is the read-only directory lookup interface the handlers depend on.
// Implementations must respect the package-level hard rules above.
type Provider interface {
	// LookupUserByEmail resolves an email to exactly one directory user.
	// Returns ErrUserNotFound (zero matches), ErrAmbiguous (>1 match), or
	// ErrBadFilter (unsafe input). Any other error is an upstream failure
	// (handlers map it to 502).
	LookupUserByEmail(ctx context.Context, email string) (*User, error)

	// SearchUsers returns one page of users matching filter (case-insensitive
	// substring against username/email/display name; empty filter lists all),
	// plus the total number of matches across all pages (SCIM2 totalResults).
	SearchUsers(ctx context.Context, filter string, limit, offset int) ([]User, int, error)

	// ListGroups returns one page of groups plus the total count across all
	// pages (SCIM2 totalResults).
	ListGroups(ctx context.Context, limit, offset int) ([]Group, int, error)
}
