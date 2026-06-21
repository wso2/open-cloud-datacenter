package executor

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
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

// TestStubMutatingVerbs verifies the Stub returns its configured result for each
// M-B request/response verb. The Stub is what dispatcher e2e tests drive, so a
// drifted return wiring would silently break those.
func TestStubMutatingVerbs(t *testing.T) {
	s := Stub{
		ApplyRes:  ApplyResult{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine", Namespace: "tenant-abc", Name: "vm-1", UID: "u-1", ResourceVersion: "12345"},
		DeleteRes: DeleteResult{Existed: true},
		StatusRes: StatusSnapshot{Found: true, ResourceVersion: "12345", Generation: 7, Status: json.RawMessage(`{"phase":"Running"}`)},
	}

	t.Run("apply", func(t *testing.T) {
		got, err := s.Apply(context.Background(), json.RawMessage(`{}`), "dc-api", false)
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if !reflect.DeepEqual(got, s.ApplyRes) {
			t.Errorf("Apply = %+v, want %+v", got, s.ApplyRes)
		}
	})
	t.Run("delete", func(t *testing.T) {
		got, err := s.Delete(context.Background(), ResourceRef{Name: "vm-1"}, "")
		if err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if got != s.DeleteRes {
			t.Errorf("Delete = %+v, want %+v", got, s.DeleteRes)
		}
	})
	t.Run("get_status", func(t *testing.T) {
		got, err := s.GetStatus(context.Background(), ResourceRef{Name: "vm-1"})
		if err != nil {
			t.Fatalf("GetStatus: %v", err)
		}
		if got.Found != s.StatusRes.Found || got.ResourceVersion != s.StatusRes.ResourceVersion ||
			got.Generation != s.StatusRes.Generation || string(got.Status) != string(s.StatusRes.Status) {
			t.Errorf("GetStatus = %+v, want %+v", got, s.StatusRes)
		}
	})
}

// TestStubMutatingVerbsError verifies the configured error propagates through the
// mutating verbs (the watch verb returns it as its terminal error too).
func TestStubMutatingVerbsError(t *testing.T) {
	s := Stub{Err: errors.New("down")}
	if _, err := s.Apply(context.Background(), nil, "", false); err == nil {
		t.Error("Apply: want configured error, got nil")
	}
	if _, err := s.Delete(context.Background(), ResourceRef{}, ""); err == nil {
		t.Error("Delete: want configured error, got nil")
	}
	if _, err := s.GetStatus(context.Background(), ResourceRef{}); err == nil {
		t.Error("GetStatus: want configured error, got nil")
	}
	if _, err := s.WatchStatus(context.Background(), ResourceRef{}, 0, func(string, StatusSnapshot) {}); err == nil {
		t.Error("WatchStatus: want configured error, got nil")
	}
}

// TestStubWatchStatusEmitsInOrder verifies the Stub emits each configured
// snapshot, in order, before returning its terminal summary — the behavior
// dispatcher streaming tests rely on to produce a deterministic frame sequence.
func TestStubWatchStatusEmitsInOrder(t *testing.T) {
	s := Stub{
		WatchEmits: []StatusSnapshot{
			{Found: true, ResourceVersion: "1", Status: json.RawMessage(`{"phase":"Pending"}`)},
			{Found: true, ResourceVersion: "2", Status: json.RawMessage(`{"phase":"Running"}`)},
		},
		WatchRes: WatchResult{SnapshotsSent: 2, Reason: WatchReasonMaxSnapshots},
	}

	var gotStages []string
	var gotRVs []string
	res, err := s.WatchStatus(context.Background(), ResourceRef{Name: "vm-1"}, 2, func(stage string, snap StatusSnapshot) {
		gotStages = append(gotStages, stage)
		gotRVs = append(gotRVs, snap.ResourceVersion)
	})
	if err != nil {
		t.Fatalf("WatchStatus: %v", err)
	}
	if res != s.WatchRes {
		t.Errorf("WatchResult = %+v, want %+v", res, s.WatchRes)
	}
	// The Stub emits every entry with stage "modified" (it has no event source).
	if !reflect.DeepEqual(gotStages, []string{"modified", "modified"}) {
		t.Errorf("emit stages = %v, want two modified", gotStages)
	}
	if !reflect.DeepEqual(gotRVs, []string{"1", "2"}) {
		t.Errorf("emit order (by resourceVersion) = %v, want [1 2]", gotRVs)
	}
}
