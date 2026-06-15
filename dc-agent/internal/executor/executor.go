// Package executor is the agent's local-cluster boundary: it runs control-plane
// operations against the zone's own Kubernetes cluster. The cluster credential
// lives here, in the zone, and never travels to dc-api — which is the whole
// point of the agent. See docs/multi-region-protocol-v1.md.
//
// M-A defines one read-only verb (get_inventory). The mutating verbs
// (apply/delete) are added in M-B against this same interface.
package executor

import "context"

// OpGetInventory is the protocol op name for the read-only inventory request.
const OpGetInventory = "get_inventory"

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

// Executor runs operations against the local zone cluster.
type Executor interface {
	GetInventory(ctx context.Context) (Inventory, error)
}

// Stub is a no-cluster Executor that returns a fixed Inventory (or error). It
// lets the agent's dispatch path run end to end before the real
// Kubernetes-backed executor (M-A step 4) is wired in, and backs unit tests.
type Stub struct {
	Inv Inventory
	Err error
}

// GetInventory returns the stub's configured inventory or error.
func (s Stub) GetInventory(_ context.Context) (Inventory, error) {
	return s.Inv, s.Err
}
