package common

import "fmt"

// NotFoundError is the typed not-found sentinel provider drivers return when
// the backend object for a resource does not exist (Harvester VM gone,
// Rancher cluster 404). The reconciler detects it structurally via
// errors.As(err, &interface{ NotFound() bool }) instead of substring-matching
// "not found" in error text.
//
// It lives in common (not the providers package) because the drivers cannot
// import providers — factory.go there imports the drivers, which would be an
// import cycle. providers re-exports it as a type alias so callers outside
// the driver tree keep a single import.
type NotFoundError struct {
	Kind string // resource kind, e.g. "VM", "cluster"
	Name string // the backendUID / object name that was looked up
	// Msg preserves the driver's original human-readable error text so log
	// messages stay familiar. Empty falls back to a generic form.
	Msg string
}

// Error returns the driver's original message when set, else a generic form.
func (e *NotFoundError) Error() string {
	if e.Msg != "" {
		return e.Msg
	}
	return fmt.Sprintf("%s %q not found", e.Kind, e.Name)
}

// NotFound marks this error as a structural not-found for errors.As probing.
func (e *NotFoundError) NotFound() bool { return true }
