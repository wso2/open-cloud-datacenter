package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"

	"github.com/wso2/dc-agent/internal/conn"
	"github.com/wso2/dc-agent/internal/executor"
	"github.com/wso2/dc-agent/internal/protocol"
)

// recordingExecutor wraps an executor.Stub and captures the arguments each verb
// received, so a dispatcher test can assert the params decoded off the wire (the
// JSON field names in §1–§2 of the wire contract) reach the executor intact.
type recordingExecutor struct {
	executor.Stub

	applyManifest json.RawMessage
	applyFM        string
	applyForce     bool

	deleteRef    executor.ResourceRef
	deletePolicy string

	statusRef executor.ResourceRef

	watchRef executor.ResourceRef
	watchMax int
}

func (r *recordingExecutor) Apply(ctx context.Context, manifest json.RawMessage, fm string, force bool) (executor.ApplyResult, error) {
	r.applyManifest, r.applyFM, r.applyForce = manifest, fm, force
	return r.Stub.Apply(ctx, manifest, fm, force)
}

func (r *recordingExecutor) Delete(ctx context.Context, ref executor.ResourceRef, policy string) (executor.DeleteResult, error) {
	r.deleteRef, r.deletePolicy = ref, policy
	return r.Stub.Delete(ctx, ref, policy)
}

func (r *recordingExecutor) GetStatus(ctx context.Context, ref executor.ResourceRef) (executor.StatusSnapshot, error) {
	r.statusRef = ref
	return r.Stub.GetStatus(ctx, ref)
}

func (r *recordingExecutor) WatchStatus(ctx context.Context, ref executor.ResourceRef, max int, emit func(string, executor.StatusSnapshot)) (executor.WatchResult, error) {
	r.watchRef, r.watchMax = ref, max
	return r.Stub.WatchStatus(ctx, ref, max, emit)
}

// dispatchHarness drives buildDispatchers(exec) through the real conn.Runner loop
// against an in-process WebSocket server. The server-side function it takes runs
// after the handshake and exchanges req/res frames over the live socket — exactly
// how dc-api drives the agent — then must return to end the test.
func dispatchHarness(t *testing.T, exec executor.Executor, serverExchange func(t *testing.T, ctx context.Context, c *websocket.Conn)) {
	t.Helper()
	disp, stream := buildDispatchers(exec, zerolog.Nop())

	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		c, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "test over")
		ctx := req.Context()

		if _, _, err := c.Read(ctx); err != nil { // hello
			return
		}
		ack, _ := protocol.Encode(&protocol.HelloAck{Type: protocol.TypeHelloAck, AgentID: "agent-1"})
		if err := c.Write(ctx, websocket.MessageText, ack); err != nil {
			return
		}

		serverExchange(t, ctx, c)
		close(done)
		<-ctx.Done()
	}))
	defer srv.Close()

	r := conn.NewRunner(conn.Config{
		Endpoint:         "ws" + strings.TrimPrefix(srv.URL, "http"),
		Token:            "dcagent_x",
		Region:           "region-a",
		Zone:             "zone-1",
		Version:          "test",
		Logger:           zerolog.Nop(),
		PingInterval:     time.Hour, // keep pings out of the req/res exchange
		Dispatcher:       disp,
		StreamDispatcher: stream,
	})

	// Run (not runSession) is the exported entry point reachable from package
	// main. It maintains the session and would reconnect on a drop, but the server
	// holds the connection open until the test cancels, so exactly one session
	// serves the whole exchange.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runDone := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(runDone)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("timed out waiting for the dispatch exchange to complete")
	}

	// Unwind the connection loop and confirm it returns promptly.
	cancel()
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

// sendReq writes a req and reads back the correlated terminal res. It fails the
// test on any transport error or correlation mismatch.
func sendReq(t *testing.T, ctx context.Context, c *websocket.Conn, id, op string, params json.RawMessage) *protocol.Res {
	t.Helper()
	b, _ := protocol.Encode(&protocol.Req{Type: protocol.TypeReq, ID: id, Op: op, Params: params})
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("write req %q: %v", op, err)
	}
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read res for %q: %v", op, err)
	}
	f, err := protocol.Decode(data)
	if err != nil {
		t.Fatalf("decode res for %q: %v", op, err)
	}
	res, ok := f.(*protocol.Res)
	if !ok {
		t.Fatalf("response for %q is %T, want *protocol.Res", op, f)
	}
	if res.ID != id {
		t.Errorf("res id = %q, want %q (correlation broken)", res.ID, id)
	}
	return res
}

// TestBuildDispatchers_Apply drives the apply op end to end: the server sends an
// apply req with a manifest/field_manager/force, and the test asserts the stub's
// ApplyResult comes back as the res result AND that the executor saw the params
// decoded from the wire field names.
func TestBuildDispatchers_Apply(t *testing.T) {
	exec := &recordingExecutor{Stub: executor.Stub{
		ApplyRes: executor.ApplyResult{
			APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine",
			Namespace: "tenant-abc", Name: "vm-1", UID: "u-1", ResourceVersion: "12345",
		},
	}}

	dispatchHarness(t, exec, func(t *testing.T, ctx context.Context, c *websocket.Conn) {
		params := json.RawMessage(`{"manifest":{"apiVersion":"kubevirt.io/v1","kind":"VirtualMachine","metadata":{"name":"vm-1","namespace":"tenant-abc"}},"field_manager":"dc-api","force":true}`)
		res := sendReq(t, ctx, c, "id-apply", executor.OpApply, params)
		if !res.Ok {
			t.Fatalf("apply res not ok: %+v", res)
		}
		var got executor.ApplyResult
		if err := json.Unmarshal(res.Result, &got); err != nil {
			t.Fatalf("unmarshal apply result: %v", err)
		}
		if !reflect.DeepEqual(got, exec.ApplyRes) {
			t.Errorf("apply result = %+v, want %+v", got, exec.ApplyRes)
		}
	})

	// The executor must have received the params decoded from the wire.
	if exec.applyFM != "dc-api" || !exec.applyForce {
		t.Errorf("executor saw field_manager=%q force=%v, want dc-api/true", exec.applyFM, exec.applyForce)
	}
	var manifest map[string]any
	if err := json.Unmarshal(exec.applyManifest, &manifest); err != nil {
		t.Fatalf("executor manifest not valid JSON: %v", err)
	}
	if manifest["kind"] != "VirtualMachine" {
		t.Errorf("executor manifest kind = %v, want VirtualMachine", manifest["kind"])
	}
}

// TestBuildDispatchers_Delete drives the delete op and asserts the Existed result
// and that the ref + propagation_policy reached the executor.
func TestBuildDispatchers_Delete(t *testing.T) {
	exec := &recordingExecutor{Stub: executor.Stub{DeleteRes: executor.DeleteResult{Existed: true}}}

	dispatchHarness(t, exec, func(t *testing.T, ctx context.Context, c *websocket.Conn) {
		params := json.RawMessage(`{"api_version":"kubevirt.io/v1","kind":"VirtualMachine","namespace":"tenant-abc","name":"vm-1","propagation_policy":"Foreground"}`)
		res := sendReq(t, ctx, c, "id-del", executor.OpDelete, params)
		if !res.Ok {
			t.Fatalf("delete res not ok: %+v", res)
		}
		var got executor.DeleteResult
		if err := json.Unmarshal(res.Result, &got); err != nil {
			t.Fatalf("unmarshal delete result: %v", err)
		}
		if !got.Existed {
			t.Errorf("delete result Existed = false, want true")
		}
	})

	want := executor.ResourceRef{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine", Namespace: "tenant-abc", Name: "vm-1"}
	if exec.deleteRef != want {
		t.Errorf("executor delete ref = %+v, want %+v", exec.deleteRef, want)
	}
	if exec.deletePolicy != "Foreground" {
		t.Errorf("executor propagation policy = %q, want Foreground", exec.deletePolicy)
	}
}

// TestBuildDispatchers_GetStatus drives get_status and asserts the snapshot
// round-trips and the ref reached the executor.
func TestBuildDispatchers_GetStatus(t *testing.T) {
	exec := &recordingExecutor{Stub: executor.Stub{StatusRes: executor.StatusSnapshot{
		Found: true, ResourceVersion: "12345", Generation: 7, Status: json.RawMessage(`{"phase":"Running"}`),
	}}}

	dispatchHarness(t, exec, func(t *testing.T, ctx context.Context, c *websocket.Conn) {
		params := json.RawMessage(`{"api_version":"kubevirt.io/v1","kind":"VirtualMachine","namespace":"tenant-abc","name":"vm-1"}`)
		res := sendReq(t, ctx, c, "id-status", executor.OpGetStatus, params)
		if !res.Ok {
			t.Fatalf("get_status res not ok: %+v", res)
		}
		var got executor.StatusSnapshot
		if err := json.Unmarshal(res.Result, &got); err != nil {
			t.Fatalf("unmarshal status result: %v", err)
		}
		if !got.Found || got.ResourceVersion != "12345" || got.Generation != 7 || string(got.Status) != `{"phase":"Running"}` {
			t.Errorf("status snapshot = %+v, want found rv=12345 gen=7 phase=Running", got)
		}
	})

	want := executor.ResourceRef{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine", Namespace: "tenant-abc", Name: "vm-1"}
	if exec.statusRef != want {
		t.Errorf("executor status ref = %+v, want %+v", exec.statusRef, want)
	}
}

// TestBuildDispatchers_WatchStatus drives the streaming watch_status op: the stub
// emits two snapshots, and the test asserts two progress frames (with data)
// precede the terminal summary res, all correlated, and that max_snapshots reached
// the executor.
func TestBuildDispatchers_WatchStatus(t *testing.T) {
	exec := &recordingExecutor{Stub: executor.Stub{
		WatchEmits: []executor.StatusSnapshot{
			{Found: true, ResourceVersion: "1", Status: json.RawMessage(`{"phase":"Pending"}`)},
			{Found: true, ResourceVersion: "2", Status: json.RawMessage(`{"phase":"Running"}`)},
		},
		WatchRes: executor.WatchResult{SnapshotsSent: 2, Reason: executor.WatchReasonMaxSnapshots},
	}}

	dispatchHarness(t, exec, func(t *testing.T, ctx context.Context, c *websocket.Conn) {
		params := json.RawMessage(`{"api_version":"kubevirt.io/v1","kind":"VirtualMachine","namespace":"tenant-abc","name":"vm-1","max_snapshots":5}`)
		b, _ := protocol.Encode(&protocol.Req{Type: protocol.TypeReq, ID: "id-watch", Op: executor.OpWatchStatus, Params: params})
		if err := c.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatalf("write watch req: %v", err)
		}

		var progresses []*protocol.Progress
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				t.Fatalf("read stream frame: %v", err)
			}
			f, err := protocol.Decode(data)
			if err != nil {
				t.Fatalf("decode stream frame: %v", err)
			}
			switch fr := f.(type) {
			case *protocol.Progress:
				if fr.ID != "id-watch" {
					t.Errorf("progress id = %q, want id-watch", fr.ID)
				}
				progresses = append(progresses, fr)
			case *protocol.Res:
				if fr.ID != "id-watch" || !fr.Ok {
					t.Fatalf("terminal res = %+v, want ok id-watch", fr)
				}
				var summary executor.WatchResult
				if err := json.Unmarshal(fr.Result, &summary); err != nil {
					t.Fatalf("unmarshal watch summary: %v", err)
				}
				if summary.SnapshotsSent != 2 || summary.Reason != executor.WatchReasonMaxSnapshots {
					t.Errorf("watch summary = %+v, want sent=2 reason=max_snapshots", summary)
				}
				if len(progresses) != 2 {
					t.Fatalf("got %d progress frames, want 2 before the res", len(progresses))
				}
				// Each progress frame's data is a marshaled StatusSnapshot.
				var snap0 executor.StatusSnapshot
				if err := json.Unmarshal(progresses[0].Data, &snap0); err != nil {
					t.Fatalf("unmarshal progress[0] data: %v", err)
				}
				if snap0.ResourceVersion != "1" || string(snap0.Status) != `{"phase":"Pending"}` {
					t.Errorf("progress[0] snapshot = %+v, want rv=1 phase=Pending", snap0)
				}
				return
			default:
				t.Fatalf("unexpected frame %T in stream", fr)
			}
		}
	})

	if exec.watchMax != 5 {
		t.Errorf("executor max_snapshots = %d, want 5", exec.watchMax)
	}
	want := executor.ResourceRef{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine", Namespace: "tenant-abc", Name: "vm-1"}
	if exec.watchRef != want {
		t.Errorf("executor watch ref = %+v, want %+v", exec.watchRef, want)
	}
}

// TestBuildDispatchers_BadParams asserts that malformed params for each verb map
// to a BAD_REQUEST res (the conn.BadRequest path), not EXEC_ERROR — exercising the
// real unmarshal-failure handling in the main.go closures.
func TestBuildDispatchers_BadParams(t *testing.T) {
	exec := &recordingExecutor{}

	dispatchHarness(t, exec, func(t *testing.T, ctx context.Context, c *websocket.Conn) {
		bad := json.RawMessage(`"not an object"`)
		for _, op := range []string{executor.OpApply, executor.OpDelete, executor.OpGetStatus, executor.OpWatchStatus} {
			res := sendReq(t, ctx, c, "id-"+op, op, bad)
			if res.Ok || res.Error == nil || res.Error.Code != protocol.ErrCodeBadRequest {
				t.Errorf("%s with bad params res = %+v, want BAD_REQUEST", op, res)
			}
		}
	})
}

// TestBuildDispatchers_RegistersAllOps asserts buildDispatchers wires exactly the
// five M-B ops: the four request/response verbs in Dispatcher and watch_status in
// StreamDispatcher. The hello advertisement (the merge of these two maps, sorted)
// is covered in the conn package's TestAdvertisedOps; here we pin the registration
// so a verb added to the executor without a handler is caught.
func TestBuildDispatchers_RegistersAllOps(t *testing.T) {
	disp, stream := buildDispatchers(&recordingExecutor{}, zerolog.Nop())

	wantRR := []string{executor.OpApply, executor.OpDelete, executor.OpGetInventory, executor.OpGetStatus}
	for _, op := range wantRR {
		if _, ok := disp[op]; !ok {
			t.Errorf("Dispatcher missing request/response op %q", op)
		}
	}
	if len(disp) != len(wantRR) {
		t.Errorf("Dispatcher has %d ops, want %d (%v)", len(disp), len(wantRR), wantRR)
	}
	if _, ok := stream[executor.OpWatchStatus]; !ok {
		t.Errorf("StreamDispatcher missing %q", executor.OpWatchStatus)
	}
	if len(stream) != 1 {
		t.Errorf("StreamDispatcher has %d ops, want 1 (watch_status)", len(stream))
	}
}
