// Package rbac — roledef.go
//
// RoleDefinition is the v2 unit of permission: a named set of action patterns.
// Built-in roles (builtins.go) and future custom roles share this exact shape
// and are evaluated identically, so custom roles never become a forked code
// path. See docs/rbac-v2.md §4, §8.1.
package rbac

// RoleDefinition is a named set of allow/deny action patterns, split across the
// control plane (Actions/NotActions) and the data plane (DataActions/
// NotDataActions). Patterns may use the `*` wildcard (see MatchAction).
type RoleDefinition struct {
	// Key is the stable identifier: a reserved PascalCase name for built-ins
	// (e.g. "VirtualMachineContributor") or a role_definitions.id UUID string
	// for custom roles.
	Key            string
	DisplayName    string
	Description    string
	Actions        []string
	NotActions     []string
	DataActions    []string
	NotDataActions []string
	// Builtin is true for the system catalog, false for tenant-owned custom roles.
	Builtin bool
}

// MatchAction reports whether a concrete action matches a pattern. `*` is the
// only wildcard and matches any sequence of characters, including `/` (Azure
// semantics). A pattern with no `*` is an exact match; `"*"` matches everything;
// `"*/read"` matches `compute/virtualMachines/read`; `"compute/*"` matches
// `compute/virtualMachines/write`.
//
// Implemented as the classic linear-time wildcard match with backtracking on the
// most recent `*` — correct for any arrangement of literals and stars.
func MatchAction(pattern, action string) bool {
	p, a := 0, 0
	star := -1 // index in pattern of the most recent '*'
	match := 0 // index in action that the most recent '*' is currently absorbing up to
	for a < len(action) {
		switch {
		case p < len(pattern) && pattern[p] == action[a]:
			p++
			a++
		case p < len(pattern) && pattern[p] == '*':
			star = p
			match = a
			p++
		case star != -1:
			// Backtrack: let the last '*' absorb one more character of action.
			p = star + 1
			match++
			a = match
		default:
			return false
		}
	}
	// Trailing stars in the pattern can match the empty string.
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

// Permits reports whether this single role definition grants the action. The
// data plane (isData=true → DataActions/NotDataActions) and control plane
// (isData=false → Actions/NotActions) never cross. Grant requires a matching
// allow pattern AND no matching deny pattern, evaluated within this one role.
//
// NotActions/NotDataActions are subtractions *within* a role, not standalone
// deny rules — so a permission this role subtracts can still be granted by
// another role the principal holds (see Authorize). This is Azure's semantics.
func (d RoleDefinition) Permits(action string, isData bool) bool {
	allow, deny := d.Actions, d.NotActions
	if isData {
		allow, deny = d.DataActions, d.NotDataActions
	}
	matched := false
	for _, pat := range allow {
		if MatchAction(pat, action) {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}
	for _, pat := range deny {
		if MatchAction(pat, action) {
			return false
		}
	}
	return true
}
