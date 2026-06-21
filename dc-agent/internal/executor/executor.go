// Package executor is the agent's local-cluster boundary: it runs control-plane
// operations against the zone's own Kubernetes cluster. The cluster credential
// lives here, in the zone, and never travels to dc-api — which is the whole
// point of the agent. See docs/multi-region-protocol-v1.md.
//
// M-A defines one read-only verb (get_inventory). M-B adds the mutating and
// status verbs (apply/delete/get_status/watch_status) against this same
// interface.
package executor

import (
	"context"
	"encoding/json"
)

// Protocol op names. These exact strings are the wire contract with dc-api,
// which defines structurally-identical constants in its own package. Both sides
// match on the string, never on a shared Go symbol.
const (
	// OpGetInventory is the read-only inventory request (M-A).
	OpGetInventory = "get_inventory"

	// M-B mutating/status verbs.
	OpApply       = "apply"        // server-side apply a manifest
	OpDelete      = "delete"       // delete one object (idempotent)
	OpGetStatus   = "get_status"   // read an object's .status once
	OpWatchStatus = "watch_status" // stream status snapshots, then terminate
)

// Inventory is a snapshot of the zone cluster's capacity, returned by
// get_inventory. These node/capacity figures are operator-facing — dc-api
// exposes them only on its admin-gated endpoint (design doc §11).
type Inventory struct {
	Nodes   []Node `json:"nodes"`
	VMCount int    `json:"vm_count"`
}

// Node is one cluster node's readiness and capacity. CPU is in milli-cores,
// memory in MiB; "used" is current usage, "allocatable" the schedulable total.
type Node struct {
	Name             string `json:"name"`
	Ready            bool   `json:"ready"`
	CPUAllocatableM  int64  `json:"cpu_allocatable_m"`
	CPUUsedM         int64  `json:"cpu_used_m"`
	MemAllocatableMB int64  `json:"mem_allocatable_mb"`
	MemUsedMB        int64  `json:"mem_used_mb"`
}

// ResourceRef identifies one Kubernetes object by GVK (api_version + kind),
// namespace, and name. The mutating/status verbs that target an existing object
// all carry this identical shape. GVK form (not GVR) is the wire contract: the
// agent owns the GVK→GVR resolution via a RESTMapper because it — not dc-api —
// has cluster discovery. Namespace is empty for cluster-scoped objects.
type ResourceRef struct {
	APIVersion string `json:"api_version"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name"`
}

// ApplyResult is the result of a server-side apply: the applied object's
// identity plus its post-apply uid and resourceVersion.
type ApplyResult struct {
	APIVersion      string `json:"api_version"`
	Kind            string `json:"kind"`
	Namespace       string `json:"namespace,omitempty"`
	Name            string `json:"name"`
	UID             string `json:"uid"`
	ResourceVersion string `json:"resource_version"`
}

// DeleteResult reports whether the object existed at delete time. A delete of a
// missing object is success with Existed=false (idempotent delete: the desired
// end-state — object gone — is already satisfied).
type DeleteResult struct {
	Existed bool `json:"existed"`
}

// StatusSnapshot is one read of an object's status. It is shared by the
// get_status result AND each watch_status progress-frame payload, so the
// single-read and streaming cases carry one identical shape. Found is false when
// the object is absent (status/resourceVersion omitted); Status is the object's
// .status subobject verbatim (nil/{} when it has none).
type StatusSnapshot struct {
	Found           bool            `json:"found"`
	ResourceVersion string          `json:"resource_version,omitempty"`
	Generation      int64           `json:"generation,omitempty"`
	Status          json.RawMessage `json:"status,omitempty"`
}

// WatchResult is the terminal summary of a watch_status stream: how many
// snapshots were emitted and why the stream stopped.
type WatchResult struct {
	SnapshotsSent int    `json:"snapshots_sent"`
	Reason        string `json:"reason"` // "max_snapshots" | "deadline" | "watch_closed"
}

// Reasons a watch_status stream terminates.
const (
	WatchReasonMaxSnapshots = "max_snapshots"
	WatchReasonDeadline     = "deadline"
	WatchReasonClosed       = "watch_closed"
)

// Executor runs operations against the local zone cluster.
type Executor interface {
	GetInventory(ctx context.Context) (Inventory, error)

	// Apply server-side-applies manifest (a complete Kubernetes object) to the
	// agent's own cluster, owned by fieldManager (defaults to "dc-api" when
	// empty). force takes ownership of conflicting fields.
	Apply(ctx context.Context, manifest json.RawMessage, fieldManager string, force bool) (ApplyResult, error)

	// Delete removes the referenced object. propagationPolicy, when non-empty,
	// must be one of Foreground/Background/Orphan. A missing object is success
	// (DeleteResult{Existed:false}).
	Delete(ctx context.Context, ref ResourceRef, propagationPolicy string) (DeleteResult, error)

	// GetStatus reads the referenced object's status once. A missing object is
	// success (StatusSnapshot{Found:false}).
	GetStatus(ctx context.Context, ref ResourceRef) (StatusSnapshot, error)

	// WatchStatus opens a watch on the single referenced object and calls emit
	// once per observed event (initial added + subsequent modified), then returns
	// a summary when it stops — after maxSnapshots events, when ctx is done, or
	// when the watch closes. emit MUST be safe to call from WatchStatus's
	// goroutine; the dispatcher wires it to a single-writer progress send. The
	// stage passed to emit is the lowercased event type ("added"/"modified").
	WatchStatus(ctx context.Context, ref ResourceRef, maxSnapshots int, emit func(stage string, snap StatusSnapshot)) (WatchResult, error)
}

// BadRequestError marks an executor error as a client/params fault — an
// unparseable manifest, an unresolvable kind (GVK NoMatch), a missing name, or
// an illegal propagation policy. The conn dispatcher detects the BadRequest()
// method (via an interface, no import edge) and reports BAD_REQUEST instead of
// EXEC_ERROR. Construct with badRequest().
type BadRequestError struct{ err error }

func (e *BadRequestError) Error() string { return e.err.Error() }
func (e *BadRequestError) Unwrap() error { return e.err }

// BadRequest reports that this error is a client/params fault. The conn package
// checks for this method through an unexported interface, keeping executor and
// conn decoupled (no import edge).
func (e *BadRequestError) BadRequest() bool { return true }

// badRequest wraps err as a BAD_REQUEST-class fault.
func badRequest(err error) error { return &BadRequestError{err: err} }

// Stub is a no-cluster Executor that returns fixed values (or a configured
// error). It lets the agent's dispatch path run end to end without a real
// cluster and backs unit/dispatcher tests. The per-verb result fields and
// WatchEmits let tests drive each op deterministically.
type Stub struct {
	Inv Inventory
	Err error

	ApplyRes   ApplyResult
	DeleteRes  DeleteResult
	StatusRes  StatusSnapshot
	WatchRes   WatchResult
	WatchEmits []StatusSnapshot // emitted in order (stage "modified") before WatchRes
}

// GetInventory returns the stub's configured inventory or error.
func (s Stub) GetInventory(_ context.Context) (Inventory, error) {
	return s.Inv, s.Err
}

// Apply returns the stub's configured ApplyRes/Err.
func (s Stub) Apply(_ context.Context, _ json.RawMessage, _ string, _ bool) (ApplyResult, error) {
	return s.ApplyRes, s.Err
}

// Delete returns the stub's configured DeleteRes/Err.
func (s Stub) Delete(_ context.Context, _ ResourceRef, _ string) (DeleteResult, error) {
	return s.DeleteRes, s.Err
}

// GetStatus returns the stub's configured StatusRes/Err.
func (s Stub) GetStatus(_ context.Context, _ ResourceRef) (StatusSnapshot, error) {
	return s.StatusRes, s.Err
}

// WatchStatus emits each WatchEmits entry (stage "modified") via emit, then
// returns the configured WatchRes/Err.
func (s Stub) WatchStatus(_ context.Context, _ ResourceRef, _ int, emit func(stage string, snap StatusSnapshot)) (WatchResult, error) {
	for _, snap := range s.WatchEmits {
		emit("modified", snap)
	}
	return s.WatchRes, s.Err
}
