package handlers

// agentchannel_list_test.go — unit tests for the protocol-v1 generic LIST verb
// on the dc-api side: Session.List. Reuses the in-process channel harness from
// agentchannel_test.go (newTestChannel + runAgent): the test drives the agent end
// of the socket and returns a crafted res carrying a collection. No live cluster,
// no dc-agent package — only the JSON wire contract.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestSessionList_Success(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()

	type seen struct {
		op  string
		ref ListRef
	}
	got := make(chan seen, 1)
	// The agent returns a two-item collection of raw VM objects.
	const result = `{"items":[` +
		`{"apiVersion":"kubevirt.io/v1","kind":"VirtualMachine","metadata":{"name":"vm-1","namespace":"dc-t-p"}},` +
		`{"apiVersion":"kubevirt.io/v1","kind":"VirtualMachine","metadata":{"name":"vm-2","namespace":"dc-t-p"}}` +
		`]}`
	tc.runAgent(func(req wsReq) string {
		var ref ListRef
		_ = json.Unmarshal(req.Params, &ref)
		got <- seen{op: req.Op, ref: ref}
		return resOK(req.ID, result)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ref := ListRef{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine", Namespace: "dc-t-p", LabelSelector: "dc-api/managed=true"}
	res, err := tc.sess.List(ctx, ref)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(res.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(res.Items))
	}
	// The raw objects round-tripped.
	var obj0 struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(res.Items[0], &obj0); err != nil {
		t.Fatalf("item[0] did not round-trip: %v", err)
	}
	if obj0.Metadata.Name != "vm-1" {
		t.Errorf("item[0] name = %q, want vm-1", obj0.Metadata.Name)
	}

	// The agent saw op=="list" with the full ListRef (GVK + namespace + selector).
	s := <-got
	if s.op != opList {
		t.Errorf("agent saw op %q, want %q", s.op, opList)
	}
	if s.ref != ref {
		t.Errorf("ref = %+v, want %+v", s.ref, ref)
	}
}

func TestSessionList_Empty(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()
	tc.runAgent(func(req wsReq) string {
		return resOK(req.ID, `{"items":[]}`)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ref := ListRef{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine", Namespace: "dc-t-p"}
	res, err := tc.sess.List(ctx, ref)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(res.Items) != 0 {
		t.Errorf("items = %d, want 0", len(res.Items))
	}
}

func TestSessionList_AgentError(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()
	tc.runAgent(func(req wsReq) string {
		return resErr(req.ID, errCodeExecError, "list failed")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ref := ListRef{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine", Namespace: "dc-t-p"}
	_, err := tc.sess.List(ctx, ref)
	var ae *AgentError
	if !errors.As(err, &ae) {
		t.Fatalf("want *AgentError, got %v", err)
	}
	if ae.Code != errCodeExecError {
		t.Errorf("code = %q, want %q", ae.Code, errCodeExecError)
	}
}

// TestSessionList_OpUnsupported confirms an older agent that doesn't advertise
// "list" replies ok=false/OP_UNSUPPORTED and the driver surfaces *AgentError with
// that code — the back-compat safety net for the additive op.
func TestSessionList_OpUnsupported(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()
	tc.runAgent(func(req wsReq) string {
		return resErr(req.ID, errCodeOpUnsupported, "unknown op")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ref := ListRef{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine", Namespace: "dc-t-p"}
	_, err := tc.sess.List(ctx, ref)
	var ae *AgentError
	if !errors.As(err, &ae) {
		t.Fatalf("want *AgentError, got %v", err)
	}
	if ae.Code != errCodeOpUnsupported {
		t.Errorf("code = %q, want %q", ae.Code, errCodeOpUnsupported)
	}
}
