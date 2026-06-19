// Package handlers — agentchannel.go
//
// The agent command channel (protocol v1): a connected dc-agent's single
// WebSocket is turned into a multiplexed request/response RPC channel so dc-api
// can drive the agent to do work in its zone without holding that zone's cluster
// credentials. See docs/multi-region-protocol-v1.md.
//
//	Session  — one connected agent. The read loop (Serve) is the ONLY reader;
//	           Call writes a "req" and waits for the correlated "res". All writes
//	           go through writeFrame under a mutex, because coder/websocket
//	           writes are not concurrency-safe.
//	Registry — zone → live Session. The WS handler registers on connect and
//	           removes on disconnect; HTTP handlers resolve a zone's session to
//	           Call it.
//
// v0 (hello/ping/pong) is unchanged; this only adds the req/res/progress frames
// and the routing for them.
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// v1 frame type discriminators. Must match dc-agent's protocol package.
const (
	wsTypeReq      = "req"
	wsTypeRes      = "res"
	wsTypeProgress = "progress"
)

// Agent error codes an agent may return in a res, and the op names dc-api
// issues. These mirror dc-agent's protocol/executor packages — the JSON strings
// are the shared contract (the two are separate Go modules).
const (
	errCodeOpUnsupported = "OP_UNSUPPORTED"
	errCodeBadRequest    = "BAD_REQUEST"
	errCodeExecError     = "EXEC_ERROR"

	opGetInventory = "get_inventory"
)

// wsReq / wsRes / wsProgress are the server-side mirror of dc-agent's v1 frames
// (the two codebases share the JSON wire contract, not a Go package).
type wsReq struct {
	Type   string          `json:"type"`
	ID     string          `json:"id"`
	Op     string          `json:"op"`
	Params json.RawMessage `json:"params,omitempty"`
}

type wsRes struct {
	Type   string          `json:"type"`
	ID     string          `json:"id"`
	Ok     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *wsFrameError   `json:"error,omitempty"`
}

type wsFrameError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ErrAgentUnavailable is returned when no live agent session exists for a zone,
// or the session dies with a request in flight. It is transient — the caller
// may retry once an agent (re)connects.
var ErrAgentUnavailable = errors.New("no live agent session for the zone")

// AgentError is returned by Session.Call when the agent replies with a failure
// (ok=false). Code is one of the protocol error codes (e.g. OP_UNSUPPORTED,
// BAD_REQUEST, EXEC_ERROR); callers map it to an HTTP status.
type AgentError struct {
	Code    string
	Message string
}

func (e *AgentError) Error() string { return e.Code + ": " + e.Message }

// ── Session ─────────────────────────────────────────────────────────────────

// Session is one connected agent's multiplexed RPC channel.
type Session struct {
	conn   *websocket.Conn
	region string
	zone   string
	log    zerolog.Logger

	// writeMu serializes all outbound writes (coder/websocket writes are not
	// safe for concurrent use): a Call's req and the read loop's pong must not
	// interleave on the wire.
	writeMu sync.Mutex

	// mu guards pending and closed.
	mu      sync.Mutex
	pending map[string]chan *wsRes
	closed  bool
}

func newSession(conn *websocket.Conn, region, zone string, log zerolog.Logger) *Session {
	return &Session{
		conn:    conn,
		region:  region,
		zone:    zone,
		log:     log,
		pending: make(map[string]chan *wsRes),
	}
}

// Zone returns the session's (region, zone).
func (s *Session) Zone() (region, zone string) { return s.region, s.zone }

// Call issues op to the agent and waits for the terminal response. It returns
// the raw result on success, an *AgentError when the agent replies ok=false,
// ErrAgentUnavailable if the session is/goes dead, or ctx.Err() on deadline.
// Safe for concurrent use; responses may return out of order.
func (s *Session) Call(ctx context.Context, op string, params json.RawMessage) (json.RawMessage, error) {
	id := uuid.NewString()
	ch := make(chan *wsRes, 1)

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrAgentUnavailable
	}
	s.pending[id] = ch
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}()

	if err := s.writeFrame(ctx, wsReq{Type: wsTypeReq, ID: id, Op: op, Params: params}); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-ch:
		if res == nil { // session closed under us
			return nil, ErrAgentUnavailable
		}
		if !res.Ok {
			code, msg := errCodeExecError, ""
			if res.Error != nil {
				code, msg = res.Error.Code, res.Error.Message
			}
			return nil, &AgentError{Code: code, Message: msg}
		}
		return res.Result, nil
	}
}

// Serve runs the steady-state read loop until the agent disconnects or goes
// silent past serverReadDeadline. It is the only reader of the socket: pings are
// answered with pongs, res frames are routed to the waiting Call, progress is
// advisory (ignored in M-A), and unknown types are tolerated. onActivity (may be
// nil) fires on every inbound frame so the caller can refresh liveness.
//
// It returns a short, human-readable reason for the disconnect (clean close,
// idle timeout, server shutdown, or transport error) so the caller can attribute
// the teardown in its disconnect log.
func (s *Session) Serve(ctx context.Context, onActivity func()) string {
	defer s.close()
	for {
		readCtx, cancel := context.WithTimeout(ctx, serverReadDeadline)
		_, data, err := s.conn.Read(readCtx)
		cancel()
		if err != nil {
			return disconnectReason(ctx, err)
		}
		if onActivity != nil {
			onActivity()
		}

		var env struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &env); err != nil {
			s.log.Warn().Err(err).Msg("dropping undecodable agent frame")
			continue
		}
		switch env.Type {
		case wsTypePing:
			pong := wsPong{Type: wsTypePong, TS: time.Now().UTC().Format(time.RFC3339)}
			if err := s.writeFrame(ctx, pong); err != nil {
				s.log.Warn().Err(err).Msg("send pong failed")
				return "transport error"
			}
		case wsTypeRes:
			var res wsRes
			if err := json.Unmarshal(data, &res); err != nil {
				s.log.Warn().Err(err).Msg("dropping undecodable res frame")
				continue
			}
			s.deliver(&res)
		case wsTypeProgress:
			// Advisory; no streaming consumer in M-A.
			s.log.Debug().Msg("ignoring progress frame (no consumer in M-A)")
		default:
			// Forward compatibility: tolerate unknown / future frame types.
			s.log.Debug().Str("frame_type", env.Type).Msg("ignoring unhandled agent frame")
		}
	}
}

// disconnectReason classifies why the read loop ended, for the disconnect log.
// It distinguishes a server-initiated shutdown (outer ctx cancelled), the agent
// closing cleanly (a WebSocket close frame), the agent going silent past
// serverReadDeadline (the per-read deadline fired while the outer ctx is live),
// and any other transport-level failure.
func disconnectReason(ctx context.Context, err error) string {
	switch {
	case ctx.Err() != nil:
		// The session context was cancelled — dc-api is shutting the channel down.
		return "server shutdown"
	case errors.Is(err, context.DeadlineExceeded):
		// Only the per-read deadline could have fired (outer ctx is live): the
		// agent stopped sending frames/pings.
		return "idle timeout"
	default:
		switch websocket.CloseStatus(err) {
		case websocket.StatusNormalClosure, websocket.StatusGoingAway, websocket.StatusNoStatusRcvd:
			return "clean close"
		default:
			return "transport error"
		}
	}
}

// deliver routes a response to its waiting Call. A response whose id has no
// waiter (a late reply after the caller's deadline) is dropped.
func (s *Session) deliver(res *wsRes) {
	s.mu.Lock()
	ch, ok := s.pending[res.ID]
	s.mu.Unlock()
	if ok {
		ch <- res // buffered(1); never blocks
	}
}

// close marks the session dead and fails every in-flight Call so callers return
// promptly instead of waiting out their own deadline. Idempotent.
func (s *Session) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	for id, ch := range s.pending {
		ch <- nil // signal "closed" to Call
		delete(s.pending, id)
	}
}

// writeFrame marshals a frame and writes it as a text message under the write
// mutex with a bounded deadline.
func (s *Session) writeFrame(ctx context.Context, frame any) error {
	b, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, writeDeadline)
	defer cancel()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.conn.Write(writeCtx, websocket.MessageText, b)
}

// ── Registry ────────────────────────────────────────────────────────────────

// Registry maps a zone to its live agent Session.
type Registry struct {
	mu       sync.RWMutex
	sessions map[string]*Session // key: region "/" zone
}

// NewRegistry constructs an empty Registry.
func NewRegistry() *Registry {
	return &Registry{sessions: make(map[string]*Session)}
}

func zoneKey(region, zone string) string { return region + "/" + zone }

// register installs s for its zone. If a stale session for the same zone exists
// (a superseded or dead socket), it is replaced and closed — a new connection
// wins.
func (r *Registry) register(s *Session) {
	key := zoneKey(s.region, s.zone)
	r.mu.Lock()
	old := r.sessions[key]
	r.sessions[key] = s
	r.mu.Unlock()
	if old != nil && old != s {
		// A new connection superseded a still-registered session for this zone
		// (a redeployed agent, or a stale socket the old reader hasn't noticed
		// yet). Surface it: a flapping agent shows up as repeated replacements.
		s.log.Warn().Str("region", s.region).Str("zone", s.zone).
			Msg("replaced existing agent session for zone")
		old.close()
	}
}

// unregister removes s only if it is still the current session for its zone (a
// newer connection may already have replaced it).
func (r *Registry) unregister(s *Session) {
	key := zoneKey(s.region, s.zone)
	r.mu.Lock()
	if r.sessions[key] == s {
		delete(r.sessions, key)
	}
	r.mu.Unlock()
}

// Session returns the live session for a zone, or false if no agent is connected
// there.
func (r *Registry) Session(region, zone string) (*Session, bool) {
	r.mu.RLock()
	s, ok := r.sessions[zoneKey(region, zone)]
	r.mu.RUnlock()
	return s, ok
}
