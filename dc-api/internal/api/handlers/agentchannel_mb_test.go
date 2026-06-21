package handlers

// agentchannel_mb_test.go — unit tests for the protocol-v1 M-B mutating/status
// verbs on the dc-api side: Session.{Apply,Delete,GetStatus,WatchStatus}.
//
// These reuse the in-process channel harness from agentchannel_test.go
// (newTestChannel + runAgent): an httptest server runs the server-side
// Session.Serve loop and the test drives the agent end of the socket, returning
// crafted res/progress JSON. No live cluster, no dc-agent package — only the
// JSON wire contract.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// resErr crafts a terminal res with ok=false and a {code,message} error, the
// agent's failure reply for any op.
func resErr(id, code, msg string) string {
	return `{"type":"res","id":"` + id + `","ok":false,"error":{"code":"` + code + `","message":"` + msg + `"}}`
}

// progressFrame crafts a watch_status progress frame correlated by id, carrying
// a status snapshot in data (the §2.5 encoding).
func progressFrame(id, stage, dataJSON string) string {
	return `{"type":"progress","id":"` + id + `","stage":"` + stage + `","data":` + dataJSON + `}`
}

// runAgentMulti plays the agent for streaming ops: for each req it calls
// respond(req) for an ordered slice of frame strings, written in sequence under
// one write mutex. This is the multi-frame analogue of runAgent (which sends a
// single reply) — used to emit N progress frames followed by a terminal res for
// one watch_status req, with the frame ordering the wire guarantees.
func (tc *testChannel) runAgentMulti(respond func(req wsReq) []string) {
	var writeMu sync.Mutex
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			_, data, err := tc.client.Read(ctx)
			cancel()
			if err != nil {
				return
			}
			var req wsReq
			if json.Unmarshal(data, &req) != nil {
				continue
			}
			go func(req wsReq) {
				frames := respond(req)
				for _, f := range frames {
					if f == "" {
						continue
					}
					wctx, wcancel := context.WithTimeout(context.Background(), 3*time.Second)
					writeMu.Lock()
					_ = tc.client.Write(wctx, websocket.MessageText, []byte(f))
					writeMu.Unlock()
					wcancel()
				}
			}(req)
		}
	}()
}

// ── Apply ────────────────────────────────────────────────────────────────────

func TestSessionApply_Success(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()

	type seen struct {
		op     string
		params applyParams
	}
	got := make(chan seen, 1)
	const result = `{"api_version":"kubevirt.io/v1","kind":"VirtualMachine","namespace":"tenant-abc","name":"vm-1","uid":"abc-123","resource_version":"12345"}`
	tc.runAgent(func(req wsReq) string {
		var p applyParams
		_ = json.Unmarshal(req.Params, &p)
		got <- seen{op: req.Op, params: p}
		return resOK(req.ID, result)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	manifest := json.RawMessage(`{"apiVersion":"kubevirt.io/v1","kind":"VirtualMachine","metadata":{"name":"vm-1","namespace":"tenant-abc"}}`)
	res, err := tc.sess.Apply(ctx, manifest, "dc-api", true)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Assert the result unmarshaled.
	want := ApplyResult{
		APIVersion:      "kubevirt.io/v1",
		Kind:            "VirtualMachine",
		Namespace:       "tenant-abc",
		Name:            "vm-1",
		UID:             "abc-123",
		ResourceVersion: "12345",
	}
	if res != want {
		t.Errorf("ApplyResult = %+v, want %+v", res, want)
	}

	// Assert the agent saw op=="apply" and the params carried manifest/field_manager/force.
	s := <-got
	if s.op != opApply {
		t.Errorf("agent saw op %q, want %q", s.op, opApply)
	}
	if s.params.FieldManager != "dc-api" {
		t.Errorf("field_manager = %q, want dc-api", s.params.FieldManager)
	}
	if !s.params.Force {
		t.Error("force = false, want true")
	}
	if !json.Valid(s.params.Manifest) || !strings.Contains(string(s.params.Manifest), `"kind":"VirtualMachine"`) {
		t.Errorf("manifest not passed through: %s", s.params.Manifest)
	}
}

func TestSessionApply_AgentError(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()
	tc.runAgent(func(req wsReq) string {
		return resErr(req.ID, errCodeExecError, "ssa conflict")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := tc.sess.Apply(ctx, json.RawMessage(`{}`), "", false)
	var ae *AgentError
	if !errors.As(err, &ae) {
		t.Fatalf("want *AgentError, got %v", err)
	}
	if ae.Code != errCodeExecError || ae.Message != "ssa conflict" {
		t.Errorf("AgentError = %+v", ae)
	}
}

// TestSessionApply_BadRequest covers the agent-side BAD_REQUEST: a manifest that
// is valid JSON but not a usable Kubernetes object (e.g. missing kind) reaches
// the agent, which rejects it. The driver surfaces *AgentError{Code:BAD_REQUEST}.
// Note: a manifest that is not even valid JSON fails earlier, at the dc-api
// marshal boundary (json.RawMessage) — see TestSessionApply_InvalidManifestJSON.
func TestSessionApply_BadRequest(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()
	tc.runAgent(func(req wsReq) string {
		return resErr(req.ID, errCodeBadRequest, "manifest missing kind")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Valid JSON, but not a usable manifest (no apiVersion/kind) — the agent
	// rejects it after it arrives.
	_, err := tc.sess.Apply(ctx, json.RawMessage(`{"foo":"bar"}`), "", false)
	var ae *AgentError
	if !errors.As(err, &ae) {
		t.Fatalf("want *AgentError, got %v", err)
	}
	if ae.Code != errCodeBadRequest {
		t.Errorf("code = %q, want %q", ae.Code, errCodeBadRequest)
	}
}

// TestSessionApply_InvalidManifestJSON documents the local-marshal boundary: a
// manifest that is not valid JSON fails inside Apply when it marshals
// applyParams (a json.RawMessage must be valid JSON), before any agent round
// trip. The error is a marshal error, NOT an *AgentError — there is no agent
// involved, so it cannot be a BAD_REQUEST reply.
func TestSessionApply_InvalidManifestJSON(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()
	tc.runAgent(func(req wsReq) string {
		t.Error("agent received a req for an invalid-JSON manifest; it should fail locally")
		return resOK(req.ID, `{}`)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := tc.sess.Apply(ctx, json.RawMessage(`not-json`), "", false)
	if err == nil {
		t.Fatal("want a local marshal error, got nil")
	}
	var ae *AgentError
	if errors.As(err, &ae) {
		t.Fatalf("want a local marshal error, got *AgentError %v", err)
	}
}

// ── Delete ───────────────────────────────────────────────────────────────────

func TestSessionDelete_Existed(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()

	type seen struct {
		op     string
		params deleteParams
	}
	got := make(chan seen, 1)
	tc.runAgent(func(req wsReq) string {
		var p deleteParams
		_ = json.Unmarshal(req.Params, &p)
		got <- seen{op: req.Op, params: p}
		return resOK(req.ID, `{"existed":true}`)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ref := ResourceRef{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine", Namespace: "tenant-abc", Name: "vm-1"}
	res, err := tc.sess.Delete(ctx, ref, "Foreground")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !res.Existed {
		t.Error("Existed = false, want true")
	}

	s := <-got
	if s.op != opDelete {
		t.Errorf("agent saw op %q, want %q", s.op, opDelete)
	}
	if s.params.ResourceRef != ref {
		t.Errorf("ref = %+v, want %+v", s.params.ResourceRef, ref)
	}
	if s.params.PropagationPolicy != "Foreground" {
		t.Errorf("propagation_policy = %q, want Foreground", s.params.PropagationPolicy)
	}
}

// TestSessionDelete_Absent covers the idempotent-delete decision: a missing
// object is success with existed:false, NOT an error.
func TestSessionDelete_Absent(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()
	tc.runAgent(func(req wsReq) string {
		return resOK(req.ID, `{"existed":false}`)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ref := ResourceRef{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine", Namespace: "tenant-abc", Name: "gone"}
	res, err := tc.sess.Delete(ctx, ref, "")
	if err != nil {
		t.Fatalf("Delete of absent object errored: %v", err)
	}
	if res.Existed {
		t.Error("Existed = true, want false for an already-absent object")
	}
}

func TestSessionDelete_BadRequest(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()
	tc.runAgent(func(req wsReq) string {
		return resErr(req.ID, errCodeBadRequest, "invalid propagation policy")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ref := ResourceRef{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine", Namespace: "tenant-abc", Name: "vm-1"}
	_, err := tc.sess.Delete(ctx, ref, "Nope")
	var ae *AgentError
	if !errors.As(err, &ae) {
		t.Fatalf("want *AgentError, got %v", err)
	}
	if ae.Code != errCodeBadRequest {
		t.Errorf("code = %q, want %q", ae.Code, errCodeBadRequest)
	}
}

// ── GetStatus ──────────────────────────────────────────────────────────────────

func TestSessionGetStatus_Found(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()

	gotOp := make(chan string, 1)
	gotRef := make(chan ResourceRef, 1)
	const result = `{"found":true,"resource_version":"12345","generation":7,"status":{"phase":"Running","ready":true}}`
	tc.runAgent(func(req wsReq) string {
		var ref ResourceRef
		_ = json.Unmarshal(req.Params, &ref)
		gotOp <- req.Op
		gotRef <- ref
		return resOK(req.ID, result)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ref := ResourceRef{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine", Namespace: "tenant-abc", Name: "vm-1"}
	res, err := tc.sess.GetStatus(ctx, ref)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if !res.Found {
		t.Error("Found = false, want true")
	}
	if res.ResourceVersion != "12345" {
		t.Errorf("ResourceVersion = %q, want 12345", res.ResourceVersion)
	}
	if res.Generation != 7 {
		t.Errorf("Generation = %d, want 7", res.Generation)
	}
	var status struct {
		Phase string `json:"phase"`
		Ready bool   `json:"ready"`
	}
	if err := json.Unmarshal(res.Status, &status); err != nil {
		t.Fatalf("status did not round-trip: %v", err)
	}
	if status.Phase != "Running" || !status.Ready {
		t.Errorf("status = %+v, want {Running true}", status)
	}

	if op := <-gotOp; op != opGetStatus {
		t.Errorf("agent saw op %q, want %q", op, opGetStatus)
	}
	if r := <-gotRef; r != ref {
		t.Errorf("ref = %+v, want %+v", r, ref)
	}
}

// TestSessionGetStatus_Absent covers the not-found-is-success decision: a
// missing object yields Found:false with no error, so a reconciler can poll for
// "gone yet?" without treating a 404 as a failure.
func TestSessionGetStatus_Absent(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()
	tc.runAgent(func(req wsReq) string {
		return resOK(req.ID, `{"found":false}`)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ref := ResourceRef{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine", Namespace: "tenant-abc", Name: "gone"}
	res, err := tc.sess.GetStatus(ctx, ref)
	if err != nil {
		t.Fatalf("GetStatus of absent object errored: %v", err)
	}
	if res.Found {
		t.Error("Found = true, want false for an absent object")
	}
	if res.ResourceVersion != "" || res.Generation != 0 || res.Status != nil {
		t.Errorf("absent snapshot carried data: %+v", res)
	}
}

func TestSessionGetStatus_AgentError(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()
	tc.runAgent(func(req wsReq) string {
		return resErr(req.ID, errCodeExecError, "read failed")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ref := ResourceRef{APIVersion: "v1", Kind: "ConfigMap", Namespace: "ns", Name: "cm"}
	_, err := tc.sess.GetStatus(ctx, ref)
	var ae *AgentError
	if !errors.As(err, &ae) {
		t.Fatalf("want *AgentError, got %v", err)
	}
	if ae.Code != errCodeExecError {
		t.Errorf("code = %q, want %q", ae.Code, errCodeExecError)
	}
}

// ── Error pass-through (shared mapping inputs) ─────────────────────────────────

// TestSessionMutating_OpUnsupported confirms an agent that doesn't advertise a
// verb replies ok=false/OP_UNSUPPORTED and the driver surfaces *AgentError with
// that code (which writeCallError maps to 501) — the v0/M-A-agent safety net.
func TestSessionMutating_OpUnsupported(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()
	tc.runAgent(func(req wsReq) string {
		return resErr(req.ID, errCodeOpUnsupported, "unknown op")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := tc.sess.Apply(ctx, json.RawMessage(`{}`), "", false)
	var ae *AgentError
	if !errors.As(err, &ae) {
		t.Fatalf("want *AgentError, got %v", err)
	}
	if ae.Code != errCodeOpUnsupported {
		t.Errorf("code = %q, want %q", ae.Code, errCodeOpUnsupported)
	}
}

// TestSessionMutating_Timeout: an agent that reads but never replies → the
// driver returns context.DeadlineExceeded (not a hang).
func TestSessionMutating_Timeout(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()
	tc.runAgent(func(req wsReq) string { return "" }) // reads but never replies

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	ref := ResourceRef{APIVersion: "v1", Kind: "ConfigMap", Name: "cm"}
	_, err := tc.sess.GetStatus(ctx, ref)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded, got %v", err)
	}
}

// TestSessionMutating_SessionClosed: the channel dies mid-call → the driver
// returns ErrAgentUnavailable promptly instead of waiting out its deadline.
func TestSessionMutating_SessionClosed(t *testing.T) {
	tc := newTestChannel(t)
	tc.runAgent(func(req wsReq) string { return "" }) // never replies

	errc := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		ref := ResourceRef{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine", Namespace: "tenant-abc", Name: "vm-1"}
		_, err := tc.sess.Delete(ctx, ref, "")
		errc <- err
	}()

	time.Sleep(50 * time.Millisecond) // let the call register and send its req
	tc.client.CloseNow()              // kill the channel → server Serve returns → close() fails pending

	select {
	case err := <-errc:
		if !errors.Is(err, ErrAgentUnavailable) {
			t.Fatalf("want ErrAgentUnavailable, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Delete did not return after the session closed")
	}
	tc.srv.Close()
}

// ── WatchStatus (streaming) ────────────────────────────────────────────────────

// TestSessionWatchStatus_Streaming is the key new test: the fake agent writes N
// progress frames (each a status snapshot) followed by the terminal res, in
// order. Assert onSnapshot fired N times with the right stage+snapshot, in
// order, and that WatchStatus returned the summary.
func TestSessionWatchStatus_Streaming(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()

	gotOp := make(chan string, 1)
	gotParams := make(chan watchStatusParams, 1)
	tc.runAgentMulti(func(req wsReq) []string {
		var p watchStatusParams
		_ = json.Unmarshal(req.Params, &p)
		gotOp <- req.Op
		gotParams <- p
		return []string{
			progressFrame(req.ID, "added", `{"found":true,"resource_version":"100","generation":1,"status":{"phase":"Pending"}}`),
			progressFrame(req.ID, "modified", `{"found":true,"resource_version":"101","generation":1,"status":{"phase":"Scheduling"}}`),
			progressFrame(req.ID, "modified", `{"found":true,"resource_version":"102","generation":2,"status":{"phase":"Running"}}`),
			resOK(req.ID, `{"snapshots_sent":3,"reason":"max_snapshots"}`),
		}
	})

	type snap struct {
		stage string
		snap  StatusSnapshot
	}
	var mu sync.Mutex
	var snaps []snap
	onSnapshot := func(stage string, s StatusSnapshot) {
		mu.Lock()
		snaps = append(snaps, snap{stage: stage, snap: s})
		mu.Unlock()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ref := ResourceRef{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine", Namespace: "tenant-abc", Name: "vm-1"}
	res, err := tc.sess.WatchStatus(ctx, ref, 3, onSnapshot)
	if err != nil {
		t.Fatalf("WatchStatus: %v", err)
	}

	// Terminal summary.
	if res.SnapshotsSent != 3 || res.Reason != "max_snapshots" {
		t.Errorf("WatchResult = %+v, want {3 max_snapshots}", res)
	}

	// onSnapshot fired 3× with the right stages in order.
	mu.Lock()
	defer mu.Unlock()
	if len(snaps) != 3 {
		t.Fatalf("onSnapshot fired %d times, want 3", len(snaps))
	}
	wantStages := []string{"added", "modified", "modified"}
	wantRV := []string{"100", "101", "102"}
	for i, s := range snaps {
		if s.stage != wantStages[i] {
			t.Errorf("snapshot[%d] stage = %q, want %q", i, s.stage, wantStages[i])
		}
		if !s.snap.Found {
			t.Errorf("snapshot[%d] Found = false, want true", i)
		}
		if s.snap.ResourceVersion != wantRV[i] {
			t.Errorf("snapshot[%d] rv = %q, want %q", i, s.snap.ResourceVersion, wantRV[i])
		}
		if len(s.snap.Status) == 0 {
			t.Errorf("snapshot[%d] carried no status", i)
		}
	}

	// The agent saw op=="watch_status" with the ref + max_snapshots.
	if op := <-gotOp; op != opWatchStatus {
		t.Errorf("agent saw op %q, want %q", op, opWatchStatus)
	}
	p := <-gotParams
	if p.ResourceRef != ref {
		t.Errorf("ref = %+v, want %+v", p.ResourceRef, ref)
	}
	if p.MaxSnapshots != 3 {
		t.Errorf("max_snapshots = %d, want 3", p.MaxSnapshots)
	}
}

// TestSessionWatchStatus_NoSnapshots covers a watch that yields zero events
// before terminating (object absent + deadline): snapshots_sent:0, a non-error
// terminal res, and onSnapshot never fires.
func TestSessionWatchStatus_NoSnapshots(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()
	tc.runAgentMulti(func(req wsReq) []string {
		return []string{resOK(req.ID, `{"snapshots_sent":0,"reason":"deadline"}`)}
	})

	var fired int
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ref := ResourceRef{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine", Namespace: "tenant-abc", Name: "never"}
	res, err := tc.sess.WatchStatus(ctx, ref, 10, func(string, StatusSnapshot) { fired++ })
	if err != nil {
		t.Fatalf("WatchStatus: %v", err)
	}
	if res.SnapshotsSent != 0 || res.Reason != "deadline" {
		t.Errorf("WatchResult = %+v, want {0 deadline}", res)
	}
	if fired != 0 {
		t.Errorf("onSnapshot fired %d times, want 0", fired)
	}
}

func TestSessionWatchStatus_AgentError(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()
	tc.runAgentMulti(func(req wsReq) []string {
		return []string{resErr(req.ID, errCodeBadRequest, "max_snapshots < 0")}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ref := ResourceRef{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine", Namespace: "tenant-abc", Name: "vm-1"}
	_, err := tc.sess.WatchStatus(ctx, ref, -1, func(string, StatusSnapshot) {
		t.Error("onSnapshot fired on an error reply")
	})
	var ae *AgentError
	if !errors.As(err, &ae) {
		t.Fatalf("want *AgentError, got %v", err)
	}
	if ae.Code != errCodeBadRequest {
		t.Errorf("code = %q, want %q", ae.Code, errCodeBadRequest)
	}
}

// TestSessionWatchStatus_TeardownOnClose: kill the channel mid-watch after some
// progress → WatchStatus returns ErrAgentUnavailable, and no onSnapshot fires
// after close (the streams entry is removed in close()).
func TestSessionWatchStatus_TeardownOnClose(t *testing.T) {
	tc := newTestChannel(t)

	firstSent := make(chan struct{}, 1)
	// The agent sends one progress frame, signals, then stalls (no terminal res)
	// so the test can close the channel mid-stream.
	tc.runAgentMulti(func(req wsReq) []string {
		// Single frame; the goroutine returns after writing it, leaving the watch
		// open with no terminal res.
		defer func() { firstSent <- struct{}{} }()
		return []string{progressFrame(req.ID, "added", `{"found":true,"resource_version":"1","status":{}}`)}
	})

	var mu sync.Mutex
	var afterClose int
	closed := make(chan struct{})
	onSnapshot := func(string, StatusSnapshot) {
		select {
		case <-closed:
			mu.Lock()
			afterClose++
			mu.Unlock()
		default:
		}
	}

	errc := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		ref := ResourceRef{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine", Namespace: "tenant-abc", Name: "vm-1"}
		_, err := tc.sess.WatchStatus(ctx, ref, 10, onSnapshot)
		errc <- err
	}()

	<-firstSent                       // first progress delivered
	time.Sleep(50 * time.Millisecond) // let deliverProgress run
	close(closed)
	tc.client.CloseNow() // kill the channel → Serve returns → close() fails pending + drops streams

	select {
	case err := <-errc:
		if !errors.Is(err, ErrAgentUnavailable) {
			t.Fatalf("want ErrAgentUnavailable, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WatchStatus did not return after the session closed")
	}

	mu.Lock()
	defer mu.Unlock()
	if afterClose != 0 {
		t.Errorf("onSnapshot fired %d times after close, want 0", afterClose)
	}
	tc.srv.Close()
}

// TestSessionWatchStatus_CtxCancel: cancel the caller ctx mid-stream →
// WatchStatus returns ctx.Err(); a late terminal res for that id is dropped
// (deliver finds no waiter), no panic.
func TestSessionWatchStatus_CtxCancel(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()

	firstSent := make(chan struct{}, 1)
	release := make(chan struct{})
	tc.runAgentMulti(func(req wsReq) []string {
		// First frame, then block until released, then a (late) terminal res that
		// must be safely dropped after the caller cancelled.
		frames := []string{progressFrame(req.ID, "added", `{"found":true,"resource_version":"1","status":{}}`)}
		go func() {
			firstSent <- struct{}{}
			<-release
			wctx, wcancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer wcancel()
			_ = tc.client.Write(wctx, websocket.MessageText, []byte(resOK(req.ID, `{"snapshots_sent":1,"reason":"watch_closed"}`)))
		}()
		return frames
	})

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		ref := ResourceRef{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine", Namespace: "tenant-abc", Name: "vm-1"}
		_, err := tc.sess.WatchStatus(ctx, ref, 10, func(string, StatusSnapshot) {})
		errc <- err
	}()

	<-firstSent
	time.Sleep(50 * time.Millisecond)
	cancel() // cancel the caller ctx mid-stream

	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WatchStatus did not return after ctx cancel")
	}

	// Release the late terminal res; deliver must find no waiter and drop it
	// without panicking. Give the read loop a moment to process it.
	close(release)
	time.Sleep(100 * time.Millisecond)
}

// TestSessionWatchStatus_NoSnapshotAfterReturn is the regression guard for the
// head-of-line / use-after-return hazard: onSnapshot must NEVER fire after
// WatchStatus has returned. We cancel the caller ctx so WatchStatus returns,
// THEN have the agent deliver a late progress frame for that same id, and assert
// onSnapshot was never invoked for it (and nothing panics).
//
// In the old model deliverProgress looked up a sink under the lock and called it
// inline on the Serve read goroutine — so a progress frame arriving after
// WatchStatus's deferred delete(streams,id) was at best dropped, but if it raced
// the delete it ran onSnapshot after return (a send-on-closed-channel hazard for
// real callers). Now progress is routed through a buffered channel drained only
// inside WatchStatus's own select loop; once that loop exits and the deferred
// cleanup removes the channel, deliverProgress finds no entry and drops the frame.
func TestSessionWatchStatus_NoSnapshotAfterReturn(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()

	reqSeen := make(chan string, 1)   // carries the watch req id to the test
	deliverLate := make(chan struct{}) // test → agent: send the late progress now
	lateDone := make(chan struct{})    // agent → test: late frame written

	// The agent records the watch req id and signals; it sends NO terminal res
	// (the watch stays open) until the test releases deliverLate, at which point it
	// writes a single late progress frame for that id — after the caller has
	// already cancelled and WatchStatus has returned.
	tc.runAgentMulti(func(req wsReq) []string {
		if req.Op != opWatchStatus {
			return nil
		}
		go func() {
			reqSeen <- req.ID
			<-deliverLate
			wctx, wcancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer wcancel()
			_ = tc.client.Write(wctx, websocket.MessageText,
				[]byte(progressFrame(req.ID, "modified", `{"found":true,"resource_version":"99","status":{"phase":"Running"}}`)))
			close(lateDone)
		}()
		return nil // no frames inline; the goroutine drives the late write
	})

	var mu sync.Mutex
	var calls int
	onSnapshot := func(string, StatusSnapshot) {
		mu.Lock()
		calls++
		mu.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		ref := ResourceRef{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine", Namespace: "tenant-abc", Name: "vm-1"}
		_, err := tc.sess.WatchStatus(ctx, ref, 10, onSnapshot)
		errc <- err
	}()

	// Wait until the agent has the watch req (so streams[id] is registered), then
	// cancel the caller ctx so WatchStatus returns BEFORE any progress arrives.
	<-reqSeen
	cancel()

	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WatchStatus did not return after ctx cancel")
	}

	// Now — after WatchStatus has returned — let the agent deliver the late
	// progress frame. deliverProgress must find no registered stream (the deferred
	// cleanup removed it) and drop it; onSnapshot must not fire and nothing panics.
	close(deliverLate)
	select {
	case <-lateDone:
	case <-time.After(2 * time.Second):
		t.Fatal("agent never wrote the late progress frame")
	}
	time.Sleep(100 * time.Millisecond) // give the Serve loop time to process+drop it

	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Errorf("onSnapshot fired %d times after WatchStatus returned, want 0", calls)
	}
}

// TestSessionWatchStatus_SlowConsumerNoHeadOfLineBlock is the regression guard
// for the head-of-line-blocking half of the fix: a slow onSnapshot must NOT
// stall the single Serve read goroutine and thereby block unrelated Calls.
//
// One watch has a deliberately blocking onSnapshot; while it is blocked on a
// progress frame, an unrelated get_inventory Call is issued on the same session.
// In the old model deliverProgress ran the sink inline on the Serve goroutine, so
// the blocked onSnapshot froze the only reader and the Call's res frame could
// never be read — a deadlock until a deadline. In the channel model
// deliverProgress only does a non-blocking buffered send, so the Serve reader
// stays free and the Call completes promptly. We assert the Call returns well
// inside its deadline, then release the watch.
func TestSessionWatchStatus_SlowConsumerNoHeadOfLineBlock(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()

	blockSnapshot := make(chan struct{}) // released at the end to unblock onSnapshot
	snapshotEntered := make(chan struct{}, 1)

	// The agent answers a watch req with a single progress frame (no terminal res
	// — the watch stays open, blocked in onSnapshot), and answers get_inventory
	// normally. The frame orderings the wire guarantees are preserved by
	// runAgentMulti's per-req write serialization.
	tc.runAgentMulti(func(req wsReq) []string {
		switch req.Op {
		case opWatchStatus:
			return []string{progressFrame(req.ID, "modified", `{"found":true,"resource_version":"1","status":{}}`)}
		case opGetInventory:
			return []string{resOK(req.ID, `{"vm_count":42}`)}
		default:
			return nil
		}
	})

	// Start the watch; its onSnapshot blocks (simulating a slow consumer) until the
	// test releases it.
	watchErr := make(chan error, 1)
	watchCtx, watchCancel := context.WithCancel(context.Background())
	defer watchCancel()
	go func() {
		ref := ResourceRef{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine", Namespace: "tenant-abc", Name: "slow"}
		_, err := tc.sess.WatchStatus(watchCtx, ref, 10, func(string, StatusSnapshot) {
			select {
			case snapshotEntered <- struct{}{}:
			default:
			}
			<-blockSnapshot // block the consumer
		})
		watchErr <- err
	}()

	// Wait until onSnapshot is actually blocked, so the watch is mid-consume.
	select {
	case <-snapshotEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("watch onSnapshot never ran")
	}

	// With the watch's onSnapshot blocked, an unrelated Call must still complete
	// quickly — the Serve reader must not be head-of-line-blocked by the slow
	// consumer. Old model: this deadlocks until the deadline.
	callDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		res, err := tc.sess.Call(ctx, opGetInventory, nil)
		if err == nil && string(res) != `{"vm_count":42}` {
			err = errors.New("unexpected inventory result: " + string(res))
		}
		callDone <- err
	}()

	select {
	case err := <-callDone:
		if err != nil {
			t.Fatalf("unrelated Call failed while a watch consumer was blocked: %v", err)
		}
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("unrelated Call was head-of-line-blocked by a slow watch consumer")
	}

	// Release the watch and tear down.
	close(blockSnapshot)
	watchCancel()
	select {
	case <-watchErr:
	case <-time.After(2 * time.Second):
		t.Fatal("WatchStatus did not return after release+cancel")
	}
}

// TestSessionDeliverProgress_UnknownID guards the stray-progress / old-dc-api
// compat path: a progress frame whose id no WatchStatus registered is dropped
// (debug-logged) with no panic and no effect on an unrelated in-flight Call.
func TestSessionDeliverProgress_UnknownID(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()

	tc.runAgentMulti(func(req wsReq) []string {
		// For a normal Call, emit a stray progress frame for an unrelated id
		// BEFORE the terminal res, then the terminal res. The stray frame must be
		// dropped and the Call must still succeed.
		return []string{
			progressFrame("no-such-id", "modified", `{"found":true,"resource_version":"9","status":{}}`),
			resOK(req.ID, `{"vm_count":1}`),
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := tc.sess.Call(ctx, opGetInventory, nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if string(result) != `{"vm_count":1}` {
		t.Errorf("result = %s, want {\"vm_count\":1}", result)
	}
}
