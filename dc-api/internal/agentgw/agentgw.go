// Package agentgw is the neutral seam between provider code (which must reach a
// zone's dc-agent) and the handlers package (which owns the live agent Sessions).
//
// It declares interfaces and the agent-channel WIRE STRUCTS only — no
// implementation, no transport — so BOTH the providers tree and the handlers
// package can import it without an import cycle. The cycle this resolves is:
//
//	handlers → providers → harvester → (would import) handlers     ✗ cycle
//	handlers → agentgw  ;  harvester → clusteraccess → agentgw      ✓ acyclic
//
// *handlers.Session satisfies Session structurally (its Apply/Delete/GetStatus/
// WatchStatus signatures already match), and *handlers.Registry is adapted to
// SessionResolver by a 3-line wrapper in package handlers (agentgw_adapter.go).
//
// The five wire structs (ResourceRef, ApplyResult, DeleteResult, StatusSnapshot,
// WatchResult) and the two sentinels (ErrAgentUnavailable, AgentError) are the
// canonical definitions; handlers/agentchannel.go type-ALIASES them back so the
// JSON wire contract with dc-agent and every M-B call site are byte-identical.
package agentgw

import (
	"context"
	"encoding/json"
	"errors"
)

// Session is the subset of *handlers.Session that clusteraccess.AgentBacked
// needs. *handlers.Session satisfies this by structural match — no change to
// agentchannel.go's method signatures is required.
type Session interface {
	// Apply server-side-applies manifest in the agent's zone cluster.
	Apply(ctx context.Context, manifest json.RawMessage, fieldManager string, force bool) (ApplyResult, error)
	// Delete removes the referenced object (a missing object is a successful,
	// idempotent delete: DeleteResult.Existed == false).
	Delete(ctx context.Context, ref ResourceRef, propagationPolicy string) (DeleteResult, error)
	// GetStatus reads the referenced object's status once (Found == false when
	// the object is absent — not an error).
	GetStatus(ctx context.Context, ref ResourceRef) (StatusSnapshot, error)
	// WatchStatus drives the streaming watch_status op.
	WatchStatus(ctx context.Context, ref ResourceRef, maxSnapshots int, onSnapshot func(stage string, snap StatusSnapshot)) (WatchResult, error)
}

// SessionResolver hands back the live Session for a zone, or false if no agent
// is connected. *handlers.Registry is adapted to this in handlers/agentgw_adapter.go.
type SessionResolver interface {
	Session(region, zone string) (Session, bool)
}

// ── Wire structs (canonical home; aliased back in handlers/agentchannel.go) ───
//
// These mirror dc-agent's executor structs by JSON tag only (no shared Go
// package): the field names below ARE the wire contract. dc-api builds the
// reference from objects it already holds, so it addresses by GVK
// (api_version + kind), not GVR — the agent owns the GVK→GVR mapping.

// ResourceRef identifies one Kubernetes object by GVK + namespace/name. It is
// embedded inline (not nested) in delete/get_status/watch_status params.
type ResourceRef struct {
	APIVersion string `json:"api_version"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name"`
}

// ApplyResult is the apply op's result: the applied object's identity and its
// post-apply version.
type ApplyResult struct {
	APIVersion      string `json:"api_version"`
	Kind            string `json:"kind"`
	Namespace       string `json:"namespace,omitempty"`
	Name            string `json:"name"`
	UID             string `json:"uid"`
	ResourceVersion string `json:"resource_version"`
}

// DeleteResult is the delete op's result. Existed is false when the object was
// already absent (a 404 is a successful, idempotent delete — not an error).
type DeleteResult struct {
	Existed bool `json:"existed"`
}

// StatusSnapshot is the result of get_status AND the payload of each
// watch_status progress frame — one shape for both the single read and the
// stream. Found is false when the object is absent (not an error).
type StatusSnapshot struct {
	Found           bool            `json:"found"`
	ResourceVersion string          `json:"resource_version,omitempty"`
	Generation      int64           `json:"generation,omitempty"`
	Status          json.RawMessage `json:"status,omitempty"`
}

// WatchResult is the terminal summary of a watch_status stream.
type WatchResult struct {
	SnapshotsSent int    `json:"snapshots_sent"`
	Reason        string `json:"reason"`
}

// ── Sentinels (canonical home; aliased back in handlers/agentchannel.go) ──────

// ErrAgentUnavailable is returned when no live agent session exists for a zone,
// or the session dies with a request in flight. It is transient — the caller
// may retry once an agent (re)connects.
var ErrAgentUnavailable = errors.New("no live agent session for the zone")

// ErrOpNotRoutable is returned by clusteraccess.AgentBacked for an op the agent
// cannot perform yet (List, Update, full-object Get), or for an unmapped GVR.
// It is NOT transient — the caller should fall back to the direct path, never
// retry the agent. clusteraccess.Routed treats it as "use Direct".
var ErrOpNotRoutable = errors.New("operation not routable through the agent yet")

// Protocol error codes an agent may return in a res frame. These exact strings
// are the wire contract with dc-agent (a separate Go module). This is the
// canonical home; handlers/agentchannel.go references CodeOpUnsupported via its
// own errCodeOpUnsupported alias, and clusteraccess matches on it too — neither
// can import the other (the import cycle agentgw exists to break), so the shared
// string lives here, the one package both import.
const (
	// CodeOpUnsupported means the agent does not implement the requested op
	// (e.g. an older agent against a newer dc-api verb). Non-retryable.
	CodeOpUnsupported = "OP_UNSUPPORTED"
)

// AgentError is returned when the agent replies with a failure (ok=false). Code
// is one of the protocol error codes (e.g. OP_UNSUPPORTED, BAD_REQUEST,
// EXEC_ERROR); callers map it to an HTTP status or a provider error class.
type AgentError struct {
	Code    string
	Message string
}

func (e *AgentError) Error() string { return e.Code + ": " + e.Message }
