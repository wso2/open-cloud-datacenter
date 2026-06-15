package conn

import (
	"context"
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
