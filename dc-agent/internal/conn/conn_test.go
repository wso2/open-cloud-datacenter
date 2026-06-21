package conn

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"

	"github.com/wso2/dc-agent/internal/protocol"
)

// TestBackoffBounds verifies the backoff schedule: exponential from 1s,
// capped at 60s, with equal jitter (result in [d/2, d]).
func TestBackoffBounds(t *testing.T) {
	cases := []struct {
		attempt  int
		min, max time.Duration
	}{
		{0, 500 * time.Millisecond, 1 * time.Second},
		{1, 1 * time.Second, 2 * time.Second},
		{2, 2 * time.Second, 4 * time.Second},
		{5, 16 * time.Second, 32 * time.Second},
		{6, 30 * time.Second, 60 * time.Second},       // 64s pre-cap → capped at 60s
		{7, 30 * time.Second, 60 * time.Second},       // stays at the cap
		{50, 30 * time.Second, 60 * time.Second},      // no overflow at large attempts
		{-3, 500 * time.Millisecond, 1 * time.Second}, // negative clamps to first attempt
	}
	for _, tc := range cases {
		// Jitter is random — sample repeatedly to exercise the range.
		for i := 0; i < 200; i++ {
			got := Backoff(tc.attempt)
			if got < tc.min || got > tc.max {
				t.Fatalf("Backoff(%d) = %v, want within [%v, %v]", tc.attempt, got, tc.min, tc.max)
			}
		}
	}
}

// TestBackoffJitters verifies the jitter actually varies — a constant
// backoff would reconnect a whole agent fleet in lockstep.
func TestBackoffJitters(t *testing.T) {
	seen := map[time.Duration]bool{}
	for i := 0; i < 100; i++ {
		seen[Backoff(4)] = true
	}
	if len(seen) < 2 {
		t.Error("Backoff(4) returned the same value 100 times; jitter appears broken")
	}
}

// TestSessionHandshakeAndPing runs a full protocol v0 session against an
// in-process WebSocket server: dial with bearer token, hello → hello_ack,
// ping → pong on the (shrunk) ticker, then context-driven shutdown.
func TestSessionHandshakeAndPing(t *testing.T) {
	const token = "dcagent_testtoken"

	gotAuth := make(chan string, 1)
	gotHello := make(chan *protocol.Hello, 1)
	gotPing := make(chan *protocol.Ping, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotAuth <- req.Header.Get("Authorization")
		c, err := websocket.Accept(w, req, nil)
		if err != nil {
			t.Errorf("server accept: %v", err)
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "test over")
		ctx := req.Context()

		// Expect hello, reply hello_ack.
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Errorf("server read hello: %v", err)
			return
		}
		frame, err := protocol.Decode(data)
		if err != nil {
			t.Errorf("server decode hello: %v", err)
			return
		}
		hello, ok := frame.(*protocol.Hello)
		if !ok {
			t.Errorf("first frame is %T, want *protocol.Hello", frame)
			return
		}
		gotHello <- hello

		ackBytes, _ := protocol.Encode(&protocol.HelloAck{
			Type:    protocol.TypeHelloAck,
			AgentID: "11111111-2222-4333-8444-555555555555",
		})
		if err := c.Write(ctx, websocket.MessageText, ackBytes); err != nil {
			t.Errorf("server write hello_ack: %v", err)
			return
		}

		// Steady state: answer pings with pongs until the client leaves.
		first := true
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return // client disconnected — normal end of test
			}
			frame, err := protocol.Decode(data)
			if err != nil {
				t.Errorf("server decode steady-state frame: %v", err)
				return
			}
			ping, ok := frame.(*protocol.Ping)
			if !ok {
				t.Errorf("steady-state frame is %T, want *protocol.Ping", frame)
				return
			}
			if first {
				gotPing <- ping
				first = false
			}
			pongBytes, _ := protocol.Encode(&protocol.Pong{Type: protocol.TypePong, TS: ping.TS})
			if err := c.Write(ctx, websocket.MessageText, pongBytes); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	r := NewRunner(Config{
		Endpoint:     "ws" + strings.TrimPrefix(srv.URL, "http"),
		Token:        token,
		Region:       "region-a",
		Zone:         "zone-1",
		Version:      "test",
		Logger:       zerolog.Nop(),
		PingInterval: 50 * time.Millisecond, // shrink the 30s production cadence for the test
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sessionDone := make(chan error, 1)
	go func() {
		established, err := r.runSession(ctx)
		if !established {
			t.Errorf("runSession established=false, want true (err=%v)", err)
		}
		sessionDone <- err
	}()

	select {
	case auth := <-gotAuth:
		if auth != "Bearer "+token {
			t.Errorf("Authorization header = %q, want %q", auth, "Bearer "+token)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for the dial to reach the server")
	}

	select {
	case hello := <-gotHello:
		want := protocol.Hello{Type: protocol.TypeHello, Region: "region-a", Zone: "zone-1", Version: "test"}
		if !reflect.DeepEqual(*hello, want) {
			t.Errorf("hello = %+v, want %+v", hello, want)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for hello")
	}

	select {
	case ping := <-gotPing:
		if _, err := time.Parse(time.RFC3339, ping.TS); err != nil {
			t.Errorf("ping ts %q is not RFC3339: %v", ping.TS, err)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for the first ping")
	}

	// Cancel and confirm the session unwinds promptly (graceful shutdown).
	cancel()
	select {
	case <-sessionDone:
	case <-time.After(3 * time.Second):
		t.Fatal("runSession did not return after context cancellation")
	}
}

// TestSessionDispatch runs the v1 request path against an in-process server:
// the agent advertises its ops in hello, then answers get_inventory with the
// handler's result, an unknown op with OP_UNSUPPORTED, and a failing handler
// with EXEC_ERROR — each response correlated to its request id.
func TestSessionDispatch(t *testing.T) {
	disp := Dispatcher{
		"get_inventory": func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"vm_count":3}`), nil
		},
		"failing_op": func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
			return nil, errors.New("kaboom")
		},
	}

	gotOps := make(chan []string, 1)
	done := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		c, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "test over")
		ctx := req.Context()

		// hello → capture advertised ops → hello_ack.
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		frame, _ := protocol.Decode(data)
		hello, ok := frame.(*protocol.Hello)
		if !ok {
			t.Errorf("first frame %T, want *protocol.Hello", frame)
			return
		}
		gotOps <- hello.Ops
		ack, _ := protocol.Encode(&protocol.HelloAck{Type: protocol.TypeHelloAck, AgentID: "agent-1"})
		if err := c.Write(ctx, websocket.MessageText, ack); err != nil {
			return
		}

		// send writes a req and reads back the correlated res.
		send := func(id, op string) *protocol.Res {
			b, _ := protocol.Encode(&protocol.Req{Type: protocol.TypeReq, ID: id, Op: op})
			if err := c.Write(ctx, websocket.MessageText, b); err != nil {
				t.Errorf("server write req %q: %v", op, err)
				return nil
			}
			_, data, err := c.Read(ctx)
			if err != nil {
				t.Errorf("server read res for %q: %v", op, err)
				return nil
			}
			f, err := protocol.Decode(data)
			if err != nil {
				t.Errorf("server decode res for %q: %v", op, err)
				return nil
			}
			res, ok := f.(*protocol.Res)
			if !ok {
				t.Errorf("response is %T, want *protocol.Res", f)
				return nil
			}
			if res.ID != id {
				t.Errorf("res id = %q, want %q (correlation broken)", res.ID, id)
			}
			return res
		}

		if res := send("id-ok", "get_inventory"); res != nil {
			if !res.Ok || string(res.Result) != `{"vm_count":3}` {
				t.Errorf("get_inventory res = %+v, want ok with result {\"vm_count\":3}", res)
			}
		}
		if res := send("id-unknown", "no_such_op"); res != nil {
			if res.Ok || res.Error == nil || res.Error.Code != protocol.ErrCodeOpUnsupported {
				t.Errorf("unknown op res = %+v, want OP_UNSUPPORTED", res)
			}
		}
		if res := send("id-fail", "failing_op"); res != nil {
			if res.Ok || res.Error == nil || res.Error.Code != protocol.ErrCodeExecError {
				t.Errorf("failing op res = %+v, want EXEC_ERROR", res)
			}
		}

		close(done)
		<-ctx.Done() // hold the conn open until the client disconnects
	}))
	defer srv.Close()

	r := NewRunner(Config{
		Endpoint:     "ws" + strings.TrimPrefix(srv.URL, "http"),
		Token:        "dcagent_x",
		Region:       "region-a",
		Zone:         "zone-1",
		Version:      "test",
		Logger:       zerolog.Nop(),
		PingInterval: time.Hour, // keep pings out of the req/res exchange
		Dispatcher:   disp,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go r.runSession(ctx)

	select {
	case ops := <-gotOps:
		want := []string{"failing_op", "get_inventory"} // sorted
		if !reflect.DeepEqual(ops, want) {
			t.Errorf("hello.ops = %v, want %v", ops, want)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for hello")
	}

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("timed out waiting for the request exchanges to complete")
	}
}

// TestAdvertisedOps verifies the hello advertisement merges the request/response
// and streaming dispatchers: sorted, de-duplicated, and nil when both are empty.
func TestAdvertisedOps(t *testing.T) {
	noop := func(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, nil }
	noopStream := func(context.Context, json.RawMessage, Emitter) (json.RawMessage, error) { return nil, nil }

	cases := []struct {
		name   string
		disp   Dispatcher
		stream StreamDispatcher
		want   []string
	}{
		{
			name: "both empty",
			want: nil,
		},
		{
			name: "request/response only",
			disp: Dispatcher{"get_inventory": noop, "apply": noop},
			want: []string{"apply", "get_inventory"},
		},
		{
			name:   "streaming only",
			stream: StreamDispatcher{"watch_status": noopStream},
			want:   []string{"watch_status"},
		},
		{
			name:   "union sorted",
			disp:   Dispatcher{"get_inventory": noop, "delete": noop, "apply": noop, "get_status": noop},
			stream: StreamDispatcher{"watch_status": noopStream},
			want:   []string{"apply", "delete", "get_inventory", "get_status", "watch_status"},
		},
		{
			name:   "name in both maps listed once",
			disp:   Dispatcher{"shared": noop},
			stream: StreamDispatcher{"shared": noopStream},
			want:   []string{"shared"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Config{Dispatcher: tc.disp, StreamDispatcher: tc.stream}.advertisedOps()
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("advertisedOps() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSessionStreamDispatch runs the v1 streaming path against an in-process
// server: a StreamHandler emits two progress frames, then returns its terminal
// res. The test asserts (1) the streaming op is advertised in hello alongside the
// request/response op, (2) the two progress frames arrive before the terminal res,
// in order, all correlated to the request id, and (3) the terminal res carries the
// handler's result. This pins the single-writer ordering guarantee: progress
// frames never land after the res that ends the request.
func TestSessionStreamDispatch(t *testing.T) {
	stream := StreamDispatcher{
		"watch_status": func(_ context.Context, _ json.RawMessage, emit Emitter) (json.RawMessage, error) {
			emit("added", json.RawMessage(`{"found":true,"resource_version":"1"}`))
			emit("modified", json.RawMessage(`{"found":true,"resource_version":"2"}`))
			return json.RawMessage(`{"snapshots_sent":2,"reason":"max_snapshots"}`), nil
		},
	}
	disp := Dispatcher{
		"get_inventory": func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"vm_count":0}`), nil
		},
	}

	gotOps := make(chan []string, 1)
	done := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		c, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "test over")
		ctx := req.Context()

		// hello → capture ops → hello_ack.
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		frame, _ := protocol.Decode(data)
		hello, ok := frame.(*protocol.Hello)
		if !ok {
			t.Errorf("first frame %T, want *protocol.Hello", frame)
			return
		}
		gotOps <- hello.Ops
		ack, _ := protocol.Encode(&protocol.HelloAck{Type: protocol.TypeHelloAck, AgentID: "agent-1"})
		if err := c.Write(ctx, websocket.MessageText, ack); err != nil {
			return
		}

		// Issue the watch_status req and read frames until the terminal res.
		reqBytes, _ := protocol.Encode(&protocol.Req{Type: protocol.TypeReq, ID: "watch-1", Op: "watch_status"})
		if err := c.Write(ctx, websocket.MessageText, reqBytes); err != nil {
			t.Errorf("server write watch req: %v", err)
			return
		}

		var progresses []*protocol.Progress
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				t.Errorf("server read stream frame: %v", err)
				return
			}
			f, err := protocol.Decode(data)
			if err != nil {
				t.Errorf("server decode stream frame: %v", err)
				return
			}
			switch fr := f.(type) {
			case *protocol.Progress:
				progresses = append(progresses, fr)
			case *protocol.Res:
				// Terminal res: validate ordering and contents, then finish.
				if fr.ID != "watch-1" {
					t.Errorf("res id = %q, want watch-1", fr.ID)
				}
				if !fr.Ok || string(fr.Result) != `{"snapshots_sent":2,"reason":"max_snapshots"}` {
					t.Errorf("terminal res = %+v, want ok with the watch summary", fr)
				}
				if len(progresses) != 2 {
					t.Fatalf("got %d progress frames before res, want 2", len(progresses))
				}
				if progresses[0].Stage != "added" || string(progresses[0].Data) != `{"found":true,"resource_version":"1"}` {
					t.Errorf("progress[0] = %+v, want added/rv-1", progresses[0])
				}
				if progresses[1].Stage != "modified" || string(progresses[1].Data) != `{"found":true,"resource_version":"2"}` {
					t.Errorf("progress[1] = %+v, want modified/rv-2", progresses[1])
				}
				for i, p := range progresses {
					if p.ID != "watch-1" {
						t.Errorf("progress[%d] id = %q, want watch-1 (correlation broken)", i, p.ID)
					}
				}
				close(done)
				<-ctx.Done()
				return
			default:
				t.Errorf("unexpected frame %T in stream", fr)
				return
			}
		}
	}))
	defer srv.Close()

	r := NewRunner(Config{
		Endpoint:         "ws" + strings.TrimPrefix(srv.URL, "http"),
		Token:            "dcagent_x",
		Region:           "region-a",
		Zone:             "zone-1",
		Version:          "test",
		Logger:           zerolog.Nop(),
		PingInterval:     time.Hour, // keep pings out of the stream exchange
		Dispatcher:       disp,
		StreamDispatcher: stream,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go r.runSession(ctx)

	select {
	case ops := <-gotOps:
		want := []string{"get_inventory", "watch_status"} // merged + sorted
		if !reflect.DeepEqual(ops, want) {
			t.Errorf("hello.ops = %v, want %v", ops, want)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for hello")
	}

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("timed out waiting for the streaming exchange to complete")
	}
}

// TestSessionDispatchBadRequest verifies a handler error wrapped with BadRequest
// (the path main.go uses for params-unmarshal failures) surfaces as a BAD_REQUEST
// res — distinct from the EXEC_ERROR a plain handler error yields — for both a
// request/response handler and a streaming handler.
func TestSessionDispatchBadRequest(t *testing.T) {
	disp := Dispatcher{
		"bad_params": func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return nil, BadRequest(errors.New("unparseable params"))
		},
	}
	stream := StreamDispatcher{
		"bad_stream_params": func(context.Context, json.RawMessage, Emitter) (json.RawMessage, error) {
			return nil, BadRequest(errors.New("unparseable stream params"))
		},
	}

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

		send := func(id, op string) *protocol.Res {
			b, _ := protocol.Encode(&protocol.Req{Type: protocol.TypeReq, ID: id, Op: op})
			if err := c.Write(ctx, websocket.MessageText, b); err != nil {
				t.Errorf("server write req %q: %v", op, err)
				return nil
			}
			_, data, err := c.Read(ctx)
			if err != nil {
				t.Errorf("server read res for %q: %v", op, err)
				return nil
			}
			f, _ := protocol.Decode(data)
			res, ok := f.(*protocol.Res)
			if !ok {
				t.Errorf("response is %T, want *protocol.Res", f)
				return nil
			}
			return res
		}

		if res := send("id-rr", "bad_params"); res != nil {
			if res.Ok || res.Error == nil || res.Error.Code != protocol.ErrCodeBadRequest {
				t.Errorf("request/response bad-params res = %+v, want BAD_REQUEST", res)
			}
		}
		if res := send("id-stream", "bad_stream_params"); res != nil {
			if res.Ok || res.Error == nil || res.Error.Code != protocol.ErrCodeBadRequest {
				t.Errorf("streaming bad-params res = %+v, want BAD_REQUEST", res)
			}
		}

		close(done)
		<-ctx.Done()
	}))
	defer srv.Close()

	r := NewRunner(Config{
		Endpoint:         "ws" + strings.TrimPrefix(srv.URL, "http"),
		Token:            "dcagent_x",
		Region:           "region-a",
		Zone:             "zone-1",
		Version:          "test",
		Logger:           zerolog.Nop(),
		PingInterval:     time.Hour,
		Dispatcher:       disp,
		StreamDispatcher: stream,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go r.runSession(ctx)

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("timed out waiting for the bad-request exchanges to complete")
	}
}

// TestRunSessionRejectsBadAck verifies the agent treats a non-hello_ack
// first frame as a handshake failure (and reports established=false so the
// backoff schedule keeps escalating).
func TestRunSessionRejectsBadAck(t *testing.T) {
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
		// Reply with a pong instead of hello_ack.
		b, _ := protocol.Encode(&protocol.Pong{Type: protocol.TypePong, TS: "2026-06-12T00:00:00Z"})
		_ = c.Write(ctx, websocket.MessageText, b)
	}))
	defer srv.Close()

	r := NewRunner(Config{
		Endpoint: "ws" + strings.TrimPrefix(srv.URL, "http"),
		Token:    "dcagent_x",
		Region:   "region-a",
		Zone:     "zone-1",
		Version:  "test",
		Logger:   zerolog.Nop(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	established, err := r.runSession(ctx)
	if established {
		t.Error("runSession established=true for a failed handshake, want false")
	}
	if err == nil {
		t.Error("runSession err=nil for a failed handshake, want error")
	}
}
