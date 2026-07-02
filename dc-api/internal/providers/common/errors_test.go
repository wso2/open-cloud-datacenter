package common

import (
	"errors"
	"fmt"
	"testing"
)

func TestNotFoundError_ErrorPreservesDriverMessage(t *testing.T) {
	e := &NotFoundError{Kind: "VM", Name: "vm-abc", Msg: "VM vm-abc not found in Harvester"}
	if got := e.Error(); got != "VM vm-abc not found in Harvester" {
		t.Fatalf("Error() = %q, want the driver's original message", got)
	}
}

func TestNotFoundError_ErrorFallsBackWhenMsgEmpty(t *testing.T) {
	e := &NotFoundError{Kind: "cluster", Name: "c-xyz"}
	if got, want := e.Error(), `cluster "c-xyz" not found`; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestNotFoundError_StructurallyDetectableThroughWrapping(t *testing.T) {
	// The reconciler probes errors.As(err, &interface{ NotFound() bool }) —
	// the sentinel must survive fmt.Errorf %w wrapping.
	wrapped := fmt.Errorf("get VM for reconcile: %w",
		&NotFoundError{Kind: "VM", Name: "vm-abc", Msg: "VM vm-abc not found in Harvester"})

	var nf interface{ NotFound() bool }
	if !errors.As(wrapped, &nf) {
		t.Fatal("errors.As failed to find the NotFound() sentinel through wrapping")
	}
	if !nf.NotFound() {
		t.Fatal("NotFound() = false, want true")
	}
}
