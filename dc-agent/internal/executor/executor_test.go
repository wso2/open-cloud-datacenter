package executor

import (
	"context"
	"errors"
	"testing"
)

func TestStubReturnsInventory(t *testing.T) {
	want := Inventory{
		Nodes:   []Node{{Name: "n1", Ready: true, CPUAllocatableM: 64000, MemAllocatableMB: 257000}},
		VMCount: 5,
	}
	got, err := Stub{Inv: want}.GetInventory(context.Background())
	if err != nil {
		t.Fatalf("GetInventory: %v", err)
	}
	if got.VMCount != 5 || len(got.Nodes) != 1 || got.Nodes[0].Name != "n1" {
		t.Errorf("inventory = %+v, want %+v", got, want)
	}
}

func TestStubReturnsError(t *testing.T) {
	if _, err := (Stub{Err: errors.New("down")}).GetInventory(context.Background()); err == nil {
		t.Error("want the configured error, got nil")
	}
}
