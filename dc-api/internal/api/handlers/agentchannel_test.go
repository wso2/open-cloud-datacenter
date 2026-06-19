package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"
)

// testChannel is an in-process agent channel: an httptest server runs the
// server-side Session.Serve loop, and `client` is the agent end the test drives.
type testChannel struct {
	sess   *Session
	client *websocket.Conn
	srv    *httptest.Server
}

func newTestChannel(t *testing.T) *testChannel {
	t.Helper()
	sessCh := make(chan *Session, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		sess := newSession(c, "lk", "zone-1", zerolog.Nop())
		sessCh <- sess
		sess.Serve(context.Background(), nil) // Serve's defer closes the session
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	client, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		srv.Close()
		t.Fatalf("dial: %v", err)
	}
	select {
	case sess := <-sessCh:
		return &testChannel{sess: sess, client: client, srv: srv}
	case <-ctx.Done():
		client.CloseNow()
		srv.Close()
		t.Fatal("server never produced a session")
		return nil
	}
}

func (tc *testChannel) close() {
	tc.client.CloseNow()
	tc.srv.Close()
}

// runAgent plays the agent: it reads req frames and replies with respond(req)
// (skip with ""), each in its own goroutine so responses can return out of
// order, with writes serialized as a real agent would. Best-effort — a
// misbehaving agent surfaces as a Call timeout in the test goroutine.
func (tc *testChannel) runAgent(respond func(req wsReq) string) {
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
				reply := respond(req)
				if reply == "" {
					return
				}
				wctx, wcancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer wcancel()
				writeMu.Lock()
				defer writeMu.Unlock()
				_ = tc.client.Write(wctx, websocket.MessageText, []byte(reply))
			}(req)
		}
	}()
}

func resOK(id, result string) string {
	return `{"type":"res","id":"` + id + `","ok":true,"result":` + result + `}`
}

func TestSessionCall_Success(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()
	tc.runAgent(func(req wsReq) string {
		if req.Op != "get_inventory" {
			return `{"type":"res","id":"` + req.ID + `","ok":false,"error":{"code":"OP_UNSUPPORTED","message":"no"}}`
		}
		return resOK(req.ID, `{"vm_count":7}`)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := tc.sess.Call(ctx, "get_inventory", nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if string(result) != `{"vm_count":7}` {
		t.Errorf("result = %s, want {\"vm_count\":7}", result)
	}
}

func TestSessionCall_PassesParams(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()
	gotParams := make(chan string, 1)
	tc.runAgent(func(req wsReq) string {
		gotParams <- string(req.Params)
		return resOK(req.ID, `{}`)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := tc.sess.Call(ctx, "get_inventory", json.RawMessage(`{"include":["nodes"]}`)); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if p := <-gotParams; p != `{"include":["nodes"]}` {
		t.Errorf("agent saw params %q", p)
	}
}

func TestSessionCall_AgentError(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()
	tc.runAgent(func(req wsReq) string {
		return `{"type":"res","id":"` + req.ID + `","ok":false,"error":{"code":"EXEC_ERROR","message":"boom"}}`
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := tc.sess.Call(ctx, "get_inventory", nil)
	var ae *AgentError
	if !errors.As(err, &ae) {
		t.Fatalf("want *AgentError, got %v", err)
	}
	if ae.Code != "EXEC_ERROR" || ae.Message != "boom" {
		t.Errorf("AgentError = %+v", ae)
	}
}

func TestSessionCall_Timeout(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()
	tc.runAgent(func(req wsReq) string { return "" }) // reads but never replies

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := tc.sess.Call(ctx, "get_inventory", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded, got %v", err)
	}
}

// TestSessionCall_OutOfOrder proves correlation: two concurrent calls whose
// responses return in reverse order each receive their own result.
func TestSessionCall_OutOfOrder(t *testing.T) {
	tc := newTestChannel(t)
	defer tc.close()

	var mu sync.Mutex
	n := 0
	tc.runAgent(func(req wsReq) string {
		mu.Lock()
		n++
		first := n == 1
		mu.Unlock()
		if first {
			time.Sleep(150 * time.Millisecond) // delay the first so the second returns first
		}
		return resOK(req.ID, `{"op":"`+req.Op+`"}`)
	})

	call := func(op string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		r, err := tc.sess.Call(ctx, op, nil)
		if err != nil {
			return err
		}
		if want := `{"op":"` + op + `"}`; string(r) != want {
			return fmt.Errorf("op %s got %s, want %s", op, r, want)
		}
		return nil
	}

	errc := make(chan error, 2)
	go func() { errc <- call("first") }()
	time.Sleep(20 * time.Millisecond) // ensure "first" is in flight first
	go func() { errc <- call("second") }()
	for i := 0; i < 2; i++ {
		if err := <-errc; err != nil {
			t.Errorf("%v", err)
		}
	}
}

// TestSessionCall_SessionClosed verifies an in-flight Call returns promptly with
// ErrAgentUnavailable when the channel dies, instead of hanging to its deadline.
func TestSessionCall_SessionClosed(t *testing.T) {
	tc := newTestChannel(t)
	tc.runAgent(func(req wsReq) string { return "" }) // never replies

	errc := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, err := tc.sess.Call(ctx, "get_inventory", nil)
		errc <- err
	}()

	time.Sleep(50 * time.Millisecond) // let the Call register and send its req
	tc.client.CloseNow()              // kill the channel → server Serve returns → close() fails pending

	select {
	case err := <-errc:
		if !errors.Is(err, ErrAgentUnavailable) {
			t.Fatalf("want ErrAgentUnavailable, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Call did not return after the session closed")
	}
	tc.srv.Close()
}

// TestDisconnectReason verifies the read-loop teardown is categorized correctly
// so the "agent disconnected" log attributes the cause: a cancelled session ctx
// is a server shutdown (regardless of the read error), a per-read deadline with a
// live ctx is an idle timeout, a WebSocket close frame is a clean close, and
// anything else is a transport error.
func TestDisconnectReason(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name string
		ctx  context.Context
		err  error
		want string
	}{
		{"server shutdown beats read error", cancelled, errors.New("read: connection reset"), "server shutdown"},
		{"server shutdown beats deadline", cancelled, context.DeadlineExceeded, "server shutdown"},
		{"idle timeout", context.Background(), context.DeadlineExceeded, "idle timeout"},
		{"clean normal closure", context.Background(), websocket.CloseError{Code: websocket.StatusNormalClosure}, "clean close"},
		{"clean going away", context.Background(), websocket.CloseError{Code: websocket.StatusGoingAway}, "clean close"},
		{"clean no status received", context.Background(), websocket.CloseError{Code: websocket.StatusNoStatusRcvd}, "clean close"},
		{"abnormal close is transport error", context.Background(), websocket.CloseError{Code: websocket.StatusAbnormalClosure}, "transport error"},
		{"plain read error is transport error", context.Background(), errors.New("read: connection reset"), "transport error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := disconnectReason(tt.ctx, tt.err); got != tt.want {
				t.Errorf("disconnectReason() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Session("lk", "zone-1"); ok {
		t.Fatal("empty registry returned a session")
	}

	s1 := newSession(nil, "lk", "zone-1", zerolog.Nop())
	r.register(s1)
	if got, ok := r.Session("lk", "zone-1"); !ok || got != s1 {
		t.Fatal("register/Session round-trip failed")
	}

	// A new connection for the same zone replaces and closes the old session.
	s2 := newSession(nil, "lk", "zone-1", zerolog.Nop())
	r.register(s2)
	if got, _ := r.Session("lk", "zone-1"); got != s2 {
		t.Fatal("re-register did not replace the session")
	}
	if !s1.closed {
		t.Error("replaced session was not closed")
	}

	// Unregistering the stale s1 must NOT remove the current s2.
	r.unregister(s1)
	if _, ok := r.Session("lk", "zone-1"); !ok {
		t.Error("unregister of a stale session wrongly removed the current one")
	}
	r.unregister(s2)
	if _, ok := r.Session("lk", "zone-1"); ok {
		t.Error("unregister of the current session did not remove it")
	}
}
