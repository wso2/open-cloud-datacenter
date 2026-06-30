package handlers

// agentchannel_getobject_test.go — unit tests for the protocol-v1 generic
// full-object GET verb on the dc-api side: Session.GetObject. Reuses the
// in-process channel harness from agentchannel_test.go (newTestChannel +
// runAgent): the test drives the agent end of the socket and returns a crafted
// res carrying one object (verbatim). No live cluster, no dc-agent package —
// only the JSON wire contract.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestSessionGetObject_Found(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()

	type seen struct {
		op  string
		ref ResourceRef
	}
	got := make(chan seen, 1)
	// The agent returns the whole object (spec + status + metadata), wrapped in a
	// get_object result with found=true.
	const result = `{"found":true,"object":` +
		`{"apiVersion":"kubevirt.io/v1","kind":"VirtualMachine",` +
		`"metadata":{"name":"vm-1","namespace":"dc-t-p","resourceVersion":"42"},` +
		`"spec":{"running":true},"status":{"printableStatus":"Running"}}}`
	tc.runAgent(func(req wsReq) string {
		var ref ResourceRef
		_ = json.Unmarshal(req.Params, &ref)
		got <- seen{op: req.Op, ref: ref}
		return resOK(req.ID, result)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ref := ResourceRef{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine", Namespace: "dc-t-p", Name: "vm-1"}
	res, err := tc.sess.GetObject(ctx, ref)
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	if !res.Found {
		t.Fatal("Found = false, want true")
	}

	// The whole object round-tripped: spec AND status are present.
	var obj struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Spec   map[string]any `json:"spec"`
		Status struct {
			PrintableStatus string `json:"printableStatus"`
		} `json:"status"`
	}
	if err := json.Unmarshal(res.Object, &obj); err != nil {
		t.Fatalf("object did not round-trip: %v", err)
	}
	if obj.Metadata.Name != "vm-1" {
		t.Errorf("object name = %q, want vm-1", obj.Metadata.Name)
	}
	if obj.Spec["running"] != true {
		t.Errorf("object spec.running = %v, want true (full object must carry .spec)", obj.Spec["running"])
	}
	if obj.Status.PrintableStatus != "Running" {
		t.Errorf("object status.printableStatus = %q, want Running", obj.Status.PrintableStatus)
	}

	// The agent saw op=="get_object" with the full ResourceRef (GVK + ns/name).
	s := <-got
	if s.op != opGetObject {
		t.Errorf("agent saw op %q, want %q", s.op, opGetObject)
	}
	if s.ref != ref {
		t.Errorf("ref = %+v, want %+v", s.ref, ref)
	}
}

func TestSessionGetObject_Absent(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()
	tc.runAgent(func(req wsReq) string {
		return resOK(req.ID, `{"found":false}`)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ref := ResourceRef{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine", Namespace: "dc-t-p", Name: "ghost"}
	res, err := tc.sess.GetObject(ctx, ref)
	if err != nil {
		t.Fatalf("GetObject of absent object returned error %v, want nil (not-found-is-success)", err)
	}
	if res.Found {
		t.Errorf("Found = true for an absent object, want false")
	}
	if len(res.Object) != 0 {
		t.Errorf("absent result carries an object %s, want none", res.Object)
	}
}

func TestSessionGetObject_AgentError(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()
	tc.runAgent(func(req wsReq) string {
		return resErr(req.ID, errCodeExecError, "get failed")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ref := ResourceRef{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine", Namespace: "dc-t-p", Name: "vm-1"}
	_, err := tc.sess.GetObject(ctx, ref)
	var ae *AgentError
	if !errors.As(err, &ae) {
		t.Fatalf("want *AgentError, got %v", err)
	}
	if ae.Code != errCodeExecError {
		t.Errorf("code = %q, want %q", ae.Code, errCodeExecError)
	}
}

// TestSessionGetObject_OpUnsupported confirms an older agent that doesn't
// advertise "get_object" replies ok=false/OP_UNSUPPORTED and the driver surfaces
// *AgentError with that code — this is the signal AgentBacked.Get keys on to fall
// back to the status-only get_status during a rolling deploy.
func TestSessionGetObject_OpUnsupported(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()
	tc.runAgent(func(req wsReq) string {
		return resErr(req.ID, errCodeOpUnsupported, "unknown op")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ref := ResourceRef{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine", Namespace: "dc-t-p", Name: "vm-1"}
	_, err := tc.sess.GetObject(ctx, ref)
	var ae *AgentError
	if !errors.As(err, &ae) {
		t.Fatalf("want *AgentError, got %v", err)
	}
	if ae.Code != errCodeOpUnsupported {
		t.Errorf("code = %q, want %q", ae.Code, errCodeOpUnsupported)
	}
}
