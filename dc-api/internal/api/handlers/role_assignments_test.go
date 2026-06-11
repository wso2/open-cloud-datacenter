// Package handlers — role_assignments_test.go
//
// Unit tests for createRoleAssignmentRequest.validate() — the user_email XOR
// user_sub rule introduced with the IdP directory feature, plus the
// role_definition checks. validate() failures are what the Create handler maps
// to HTTP 400.
//
// The Create handler's email-resolution branches (nil provider → 422,
// ErrUserNotFound/ErrAmbiguous → 422, upstream failure → 502, resolved-sub
// persistence, display_alias defaulting) are NOT unit-testable here:
// RoleAssignmentsHandler holds the concrete *db.Repository and Create calls
// requireAction → repo.ListRoleAssignmentsForPrincipal before any directory
// code runs, so the paths need a real database. They are covered end-to-end in
// test/integration/directory_invite_test.go (runs cluster-free with
// DCAPI_TEST_NOP=1).
package handlers

import (
	"testing"
)

func TestCreateRoleAssignmentRequest_Validate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		req     createRoleAssignmentRequest
		wantErr bool
	}{
		{
			name:    "user_sub only is valid",
			req:     createRoleAssignmentRequest{UserSub: "sub-123", RoleDefinition: "Contributor"},
			wantErr: false,
		},
		{
			name:    "user_email only is valid",
			req:     createRoleAssignmentRequest{UserEmail: "alice@example.com", RoleDefinition: "Contributor"},
			wantErr: false,
		},
		{
			name:    "both user_sub and user_email is rejected",
			req:     createRoleAssignmentRequest{UserSub: "sub-123", UserEmail: "alice@example.com", RoleDefinition: "Contributor"},
			wantErr: true,
		},
		{
			name:    "neither user_sub nor user_email is rejected",
			req:     createRoleAssignmentRequest{RoleDefinition: "Contributor"},
			wantErr: true,
		},
		{
			name:    "user_email with a double quote is rejected (SCIM filter injection)",
			req:     createRoleAssignmentRequest{UserEmail: `ali"ce@example.com`, RoleDefinition: "Contributor"},
			wantErr: true,
		},
		{
			name:    "user_email with a backslash is rejected (SCIM filter injection)",
			req:     createRoleAssignmentRequest{UserEmail: `ali\ce@example.com`, RoleDefinition: "Contributor"},
			wantErr: true,
		},
		{
			name:    "missing role_definition is rejected",
			req:     createRoleAssignmentRequest{UserSub: "sub-123"},
			wantErr: true,
		},
		{
			name:    "unknown role_definition is rejected",
			req:     createRoleAssignmentRequest{UserSub: "sub-123", RoleDefinition: "SuperDuperAdmin"},
			wantErr: true,
		},
		{
			name:    "Owner is a known built-in role",
			req:     createRoleAssignmentRequest{UserEmail: "alice@example.com", RoleDefinition: "Owner"},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.req.validate()
			if tc.wantErr && err == nil {
				t.Errorf("expected validate() error, got nil (req=%+v)", tc.req)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected validate() error: %v (req=%+v)", err, tc.req)
			}
		})
	}
}
