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

	// M-B mutating/status verbs (protocol v1). These exact strings are the wire
	// contract with dc-agent; the two are separate Go modules.
	opApply       = "apply"
	opDelete      = "delete"
	opGetStatus   = "get_status"
	opWatchStatus = "watch_status"
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

// wsProgress is an advisory, mid-flight frame an agent emits for a streaming op
// (watch_status), correlated to the in-flight req by ID. Data carries an
// op-specific structured payload (a status snapshot for watch_status); it is an
// additive, omitempty field — an older receiver ignores the unknown key.
type wsProgress struct {
	Type   string          `json:"type"`
	ID     string          `json:"id"`
	Stage  string          `json:"stage"`
	Detail string          `json:"detail,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// ── M-B op param/result shapes ───────────────────────────────────────────────
//
// These mirror dc-agent's executor structs by JSON tag only (no shared Go
// package): the field names below ARE the wire contract. dc-api builds the
// reference from objects it already holds, so it addresses by GVK
// (api_version + kind), not GVR — the agent owns the GVK→GVR mapping.

// ResourceRef identifies one Kubernetes object by GVK + namespace/name. It is
// embedded inline (not nested) in delete/get_status/watch_status params.
type ResourceRef struct {
	APIVersion string `json:"api_version"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name"`
}

// applyParams is the request body for the apply op: a full manifest plus the SSA
// field manager and force-conflicts flag.
type applyParams struct {
	Manifest     json.RawMessage `json:"manifest"`
	FieldManager string          `json:"field_manager,omitempty"`
	Force        bool            `json:"force,omitempty"`
}

// ApplyResult is the apply op's result: the applied object's identity and its
// post-apply version.
type ApplyResult struct {
	APIVersion      string `json:"api_version"`
	Kind            string `json:"kind"`
	Namespace       string `json:"namespace,omitempty"`
	Name            string `json:"name"`
	UID             string `json:"uid"`
	ResourceVersion string `json:"resource_version"`
}

// deleteParams is the request body for the delete op: a ResourceRef plus an
// optional propagation policy.
type deleteParams struct {
	ResourceRef
	PropagationPolicy string `json:"propagation_policy,omitempty"`
}

// DeleteResult is the delete op's result. Existed is false when the object was
// already absent (a 404 is a successful, idempotent delete — not an error).
type DeleteResult struct {
	Existed bool `json:"existed"`
}

// StatusSnapshot is the result of get_status AND the payload of each
// watch_status progress frame — one shape for both the single read and the
// stream. Found is false when the object is absent (not an error).
type StatusSnapshot struct {
	Found           bool            `json:"found"`
	ResourceVersion string          `json:"resource_version,omitempty"`
	Generation      int64           `json:"generation,omitempty"`
	Status          json.RawMessage `json:"status,omitempty"`
}

// watchStatusParams is the request body for the watch_status op: a ResourceRef
// plus the stream's snapshot cap.
type watchStatusParams struct {
	ResourceRef
	MaxSnapshots int `json:"max_snapshots,omitempty"`
}

// WatchResult is the terminal summary of a watch_status stream.
type WatchResult struct {
	SnapshotsSent int    `json:"snapshots_sent"`
	Reason        string `json:"reason"`
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

	// mu guards pending, streams, and closed.
	mu      sync.Mutex
	pending map[string]chan *wsRes
	// streams routes progress frames to an in-flight WatchStatus, keyed by req
	// id. It is separate from pending (which is single-shot, terminal-only):
	// progress is multi-shot, the terminal res still flows through pending.
	//
	// Each value is a per-watch buffered channel. deliverProgress (on the Serve
	// read goroutine) does a NON-BLOCKING send onto it; WatchStatus (on the
	// caller's goroutine) drains it and runs onSnapshot. Routing progress through
	// a channel — rather than calling a sink inline on the Serve goroutine — means
	// a slow onSnapshot can never head-of-line-block the single reader, and
	// onSnapshot can never fire after WatchStatus has returned.
	streams map[string]chan *wsProgress
	closed  bool
}

// watchProgressBuffer is the per-watch progress channel's buffer. A watch that
// can't keep up drops the oldest-beyond-buffer snapshots (deliverProgress's
// non-blocking send) rather than stalling the Serve reader — snapshots are
// advisory and the terminal res is authoritative.
const watchProgressBuffer = 16

func newSession(conn *websocket.Conn, region, zone string, log zerolog.Logger) *Session {
	return &Session{
		conn:    conn,
		region:  region,
		zone:    zone,
		log:     log,
		pending: make(map[string]chan *wsRes),
		streams: make(map[string]chan *wsProgress),
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

// ── M-B op drivers ───────────────────────────────────────────────────────────
//
// Apply / Delete / GetStatus are thin wrappers over Call (marshal params →
// Call → unmarshal result), mirroring how inventory.go drives get_inventory.
// Errors from Call (*AgentError, ErrAgentUnavailable, ctx.Err()) pass through
// unchanged for the caller to map to an HTTP status.

// Apply server-side-applies manifest in the agent's zone cluster. fieldManager
// defaults to "dc-api" on the agent when empty; force toggles SSA
// force-conflicts. It returns the applied object's identity and version.
func (s *Session) Apply(ctx context.Context, manifest json.RawMessage, fieldManager string, force bool) (ApplyResult, error) {
	var res ApplyResult
	params, err := json.Marshal(applyParams{Manifest: manifest, FieldManager: fieldManager, Force: force})
	if err != nil {
		return res, err
	}
	raw, err := s.Call(ctx, opApply, params)
	if err != nil {
		return res, err
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return ApplyResult{}, err
	}
	return res, nil
}

// Delete removes the referenced object. A missing object is a successful,
// idempotent delete (DeleteResult.Existed == false), not an error.
// propagationPolicy, when non-empty, is one of Foreground/Background/Orphan.
func (s *Session) Delete(ctx context.Context, ref ResourceRef, propagationPolicy string) (DeleteResult, error) {
	var res DeleteResult
	params, err := json.Marshal(deleteParams{ResourceRef: ref, PropagationPolicy: propagationPolicy})
	if err != nil {
		return res, err
	}
	raw, err := s.Call(ctx, opDelete, params)
	if err != nil {
		return res, err
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return DeleteResult{}, err
	}
	return res, nil
}

// GetStatus reads the referenced object's status once. A missing object yields
// StatusSnapshot{Found: false}, not an error — a reconciler can poll for "gone
// yet?" without treating a 404 as a failure.
func (s *Session) GetStatus(ctx context.Context, ref ResourceRef) (StatusSnapshot, error) {
	var res StatusSnapshot
	params, err := json.Marshal(ref)
	if err != nil {
		return res, err
	}
	raw, err := s.Call(ctx, opGetStatus, params)
	if err != nil {
		return res, err
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return StatusSnapshot{}, err
	}
	return res, nil
}

// WatchStatus drives the watch_status streaming op: it sends one req and invokes
// onSnapshot for each progress frame the agent emits (correlated by id), then
// returns the terminal summary. It blocks until the terminal res, ctx is done,
// or the session dies (ErrAgentUnavailable).
//
// Unlike Call, it registers BOTH a terminal waiter (in pending) and a progress
// channel (in streams) under one id, because Call's one-shot waiter has no
// progress hook and deletes itself on return.
//
// onSnapshot is invoked ONLY from this function's own select loop, on the
// caller's goroutine — never on the Serve read goroutine. deliverProgress just
// does a non-blocking send onto the per-watch buffered channel drained here, so
// (a) a slow onSnapshot cannot head-of-line-block the single Serve reader (it
// would only fill this watch's buffer, then drop), and (b) onSnapshot can never
// fire after WatchStatus has returned — once the select exits, the deferred
// cleanup removes the channel and nothing else reads it.
func (s *Session) WatchStatus(
	ctx context.Context,
	ref ResourceRef,
	maxSnapshots int,
	onSnapshot func(stage string, snap StatusSnapshot),
) (WatchResult, error) {
	var zero WatchResult

	params, err := json.Marshal(watchStatusParams{ResourceRef: ref, MaxSnapshots: maxSnapshots})
	if err != nil {
		return zero, err
	}

	id := uuid.NewString()
	ch := make(chan *wsRes, 1)
	progressCh := make(chan *wsProgress, watchProgressBuffer)

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return zero, ErrAgentUnavailable
	}
	s.pending[id] = ch
	s.streams[id] = progressCh
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.pending, id)
		delete(s.streams, id)
		s.mu.Unlock()
	}()

	if err := s.writeFrame(ctx, wsReq{Type: wsTypeReq, ID: id, Op: opWatchStatus, Params: params}); err != nil {
		return zero, err
	}

	// deliver a single progress frame to onSnapshot, on this (the caller's)
	// goroutine. An undecodable snapshot is dropped, not fatal — the stream
	// continues.
	deliver := func(p *wsProgress) {
		if onSnapshot == nil {
			return
		}
		var snap StatusSnapshot
		if len(p.Data) > 0 {
			if err := json.Unmarshal(p.Data, &snap); err != nil {
				s.log.Warn().Err(err).Str("id", p.ID).Msg("dropping undecodable watch_status snapshot")
				return
			}
		}
		onSnapshot(p.Stage, snap)
	}

	for {
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case p := <-progressCh:
			deliver(p)
		case res := <-ch:
			if res == nil { // session closed under us
				return zero, ErrAgentUnavailable
			}
			if !res.Ok {
				code, msg := errCodeExecError, ""
				if res.Error != nil {
					code, msg = res.Error.Code, res.Error.Message
				}
				return zero, &AgentError{Code: code, Message: msg}
			}
			var out WatchResult
			if err := json.Unmarshal(res.Result, &out); err != nil {
				return zero, err
			}
			return out, nil
		}
	}
}

// Serve runs the steady-state read loop until the agent disconnects or goes
// silent past serverReadDeadline. It is the only reader of the socket: pings are
// answered with pongs, res frames are routed to the waiting Call, progress
// frames are routed to an in-flight WatchStatus stream (or dropped if none),
// and unknown types are tolerated. onActivity (may be nil) fires on every
// inbound frame so the caller can refresh liveness.
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
			var p wsProgress
			if err := json.Unmarshal(data, &p); err != nil {
				s.log.Warn().Err(err).Msg("dropping undecodable progress frame")
				continue
			}
			s.deliverProgress(&p)
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

// deliverProgress routes a progress frame to its in-flight WatchStatus stream's
// buffered channel. A frame whose id has no registered stream (a stray frame, or
// one arriving after the watch ended or the session closed) is dropped. The send
// is NON-BLOCKING by design: this runs on the single Serve read goroutine, so it
// must never block on a slow consumer (which would head-of-line-block every other
// Call's res frame). If the watch's buffer is full the snapshot is dropped —
// snapshots are advisory; the terminal res, routed through pending, is
// authoritative. onSnapshot is NOT run here; WatchStatus drains the channel on
// the caller's goroutine.
func (s *Session) deliverProgress(p *wsProgress) {
	s.mu.Lock()
	ch, ok := s.streams[p.ID]
	s.mu.Unlock()
	if !ok {
		s.log.Debug().Str("id", p.ID).Msg("progress for unknown/closed stream; dropping")
		return
	}
	select {
	case ch <- p:
	default:
		s.log.Warn().Str("id", p.ID).Msg("watch buffer full; dropping snapshot")
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
		ch <- nil // signal "closed" to Call (incl. a WatchStatus terminal waiter)
		delete(s.pending, id)
	}
	// Drop every progress channel so no late progress is routed after close. The
	// matching pending-terminal waiter above already unblocked any WatchStatus
	// select with nil → ErrAgentUnavailable, so it returns without draining what
	// remains. We delete (rather than close) the channels: deliverProgress holds
	// s.mu while sending, so once a channel is unreachable here no further send
	// can target it, and an un-closed buffered channel is simply garbage-collected
	// — this avoids any send-on-closed-channel hazard.
	for id := range s.streams {
		delete(s.streams, id)
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
