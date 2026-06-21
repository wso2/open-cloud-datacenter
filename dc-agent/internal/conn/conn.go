// Package conn owns the agent's connection lifecycle to the control plane:
// dial out over WSS, handshake (hello / hello_ack), keepalive pings, and
// reconnect-forever with exponential backoff + jitter.
//
// The agent is the dialing side by design — datacenters only ever open an
// outbound HTTPS(443) connection to the control plane; nothing dials in.
package conn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"

	"github.com/wso2/dc-agent/internal/protocol"
)

const (
	// dialTimeout bounds the TCP+TLS+HTTP upgrade of a single dial attempt.
	dialTimeout = 15 * time.Second

	// helloAckTimeout is how long the agent waits for the server's
	// hello_ack after sending hello.
	helloAckTimeout = 10 * time.Second

	// writeTimeout bounds any single frame write.
	writeTimeout = 10 * time.Second

	// defaultPingInterval is the agent→server keepalive cadence. The server
	// enforces a ~120s read deadline, so 30s gives four chances per window.
	defaultPingInterval = 30 * time.Second

	// defaultIdleLimit mirrors the server's read deadline: if the agent
	// hears nothing (no pong, no frame) for this long, it assumes the
	// connection is dead and reconnects.
	defaultIdleLimit = 120 * time.Second

	// backoffBase and backoffCap bound the reconnect backoff (1s → 60s).
	backoffBase = 1 * time.Second
	backoffCap  = 60 * time.Second
)

// Config carries everything Runner needs to maintain the channel.
type Config struct {
	// Endpoint is the control-plane WebSocket URL,
	// e.g. wss://controlplane.example.com/v1/agent/ws.
	Endpoint string
	// Token is the agent credential, sent as "Authorization: Bearer <token>".
	Token string
	// Region and Zone identify where this agent runs; sent in the hello frame.
	Region string
	Zone   string
	// Version is the agent build version; sent in the hello frame.
	Version string
	// Logger is the structured logger for connection events.
	Logger zerolog.Logger

	// PingInterval overrides the keepalive cadence (default 30s).
	// Exposed for tests; production uses the default.
	PingInterval time.Duration
	// IdleLimit overrides how long the server may stay silent before the
	// agent reconnects (default 120s, mirroring the server read deadline).
	// Exposed for tests; production uses the default.
	IdleLimit time.Duration

	// Dispatcher maps the request/response operation verbs this agent serves
	// (protocol v1) to their handlers. Its keys are advertised in the hello
	// frame. Empty (the zero value) keeps the agent presence-only — exactly v0
	// behaviour.
	Dispatcher Dispatcher

	// StreamDispatcher maps the streaming operation verbs (those that emit
	// progress frames before their terminal res, e.g. watch_status) to their
	// handlers. Its keys are advertised in the hello frame alongside Dispatcher's.
	// A streaming and a non-streaming op must not share a name.
	StreamDispatcher StreamDispatcher
}

// Handler runs one operation request: it receives the request's params and
// returns the result payload, or an error (surfaced to the control plane as an
// EXEC_ERROR response — or BAD_REQUEST if the error reports itself as a client
// fault, see BadRequest).
type Handler func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)

// Dispatcher maps op names to their handlers.
type Dispatcher map[string]Handler

// Emitter sends one advisory progress frame for an in-flight op, correlated by
// the request's id. It is wired to writeFrame, so it is serialized against the
// ping loop and every other op by the single-writer mutex — progress frames
// never interleave on the wire. Safe for concurrent use; best-effort (a write
// error is dropped, like any progress frame).
type Emitter func(stage string, data json.RawMessage)

// StreamHandler runs one streaming operation request: it may call emit zero or
// more times to send progress frames before returning its terminal result (or
// an error). Registered in a StreamDispatcher.
type StreamHandler func(ctx context.Context, params json.RawMessage, emit Emitter) (json.RawMessage, error)

// StreamDispatcher maps op names to their streaming handlers.
type StreamDispatcher map[string]StreamHandler

// ops returns the dispatcher's op names, sorted for a stable hello frame.
func (d Dispatcher) ops() []string {
	if len(d) == 0 {
		return nil
	}
	ops := make([]string, 0, len(d))
	for op := range d {
		ops = append(ops, op)
	}
	sort.Strings(ops)
	return ops
}

// advertisedOps returns the union of the request/response and streaming op
// names, sorted and de-duplicated, for the hello frame. A name appearing in both
// maps is listed once.
func (c Config) advertisedOps() []string {
	if len(c.Dispatcher) == 0 && len(c.StreamDispatcher) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(c.Dispatcher)+len(c.StreamDispatcher))
	for op := range c.Dispatcher {
		set[op] = struct{}{}
	}
	for op := range c.StreamDispatcher {
		set[op] = struct{}{}
	}
	ops := make([]string, 0, len(set))
	for op := range set {
		ops = append(ops, op)
	}
	sort.Strings(ops)
	return ops
}

// badRequester is implemented by handler (or executor) errors that are a
// client/params fault — reported as BAD_REQUEST rather than EXEC_ERROR. The
// executor package returns errors with this method (no import edge between the
// two packages); conn also wraps params-unmarshal failures via BadRequest.
type badRequester interface{ BadRequest() bool }

// badRequestError marks a conn-side error (e.g. a params-unmarshal failure) as a
// client fault. It satisfies badRequester.
type badRequestError struct{ err error }

func (e badRequestError) Error() string    { return e.err.Error() }
func (e badRequestError) Unwrap() error    { return e.err }
func (e badRequestError) BadRequest() bool { return true }

// BadRequest wraps err so dispatch reports it as BAD_REQUEST instead of
// EXEC_ERROR. Handlers use it for malformed params.
func BadRequest(err error) error { return badRequestError{err: err} }

// isBadRequest reports whether err (or anything it wraps) marks itself a
// client/params fault via the badRequester interface.
func isBadRequest(err error) bool {
	var br badRequester
	return errors.As(err, &br) && br.BadRequest()
}

// Runner maintains the agent↔control-plane channel for the life of the
// process. v0 (hello/ping) keeps the channel healthy; v1 operation verbs are
// served by the configured Dispatcher.
type Runner struct {
	cfg Config
	// writeMu serializes all outbound writes on the current connection: the
	// ping loop and the (concurrent) op-response goroutines must not interleave
	// on the wire, because coder/websocket writes are not concurrency-safe.
	writeMu sync.Mutex
}

// NewRunner builds a Runner from cfg. Validation of cfg happens in main
// (fail-fast at startup); Runner trusts its inputs.
func NewRunner(cfg Config) *Runner {
	if cfg.PingInterval <= 0 {
		cfg.PingInterval = defaultPingInterval
	}
	if cfg.IdleLimit <= 0 {
		cfg.IdleLimit = defaultIdleLimit
	}
	// Bind region/zone once so EVERY connection-loop line is attributable —
	// not just the "connected to control plane" line. The reconnect Warn and
	// shutdown lines inherit these fields from here.
	cfg.Logger = cfg.Logger.With().
		Str("region", cfg.Region).
		Str("zone", cfg.Zone).
		Logger()
	return &Runner{cfg: cfg}
}

// Run dials the control plane and keeps the session alive, reconnecting
// forever on any error. It returns only when ctx is cancelled.
func (r *Runner) Run(ctx context.Context) {
	attempt := 0
	for {
		established, err := r.runSession(ctx)
		if ctx.Err() != nil {
			r.cfg.Logger.Info().Msg("shutting down connection loop")
			return
		}
		if established {
			// The previous session completed a handshake — start the
			// backoff schedule over rather than escalating forever.
			attempt = 0
		}

		delay := Backoff(attempt)
		r.cfg.Logger.Warn().
			Err(err).
			Int("attempt", attempt).
			Dur("retry_in", delay).
			Msg("connection lost; reconnecting")
		attempt++

		select {
		case <-ctx.Done():
			r.cfg.Logger.Info().Msg("shutting down connection loop")
			return
		case <-time.After(delay):
		}
	}
}

// Backoff returns the wait before reconnect attempt n: exponential from
// backoffBase, capped at backoffCap, with equal jitter — the result is
// uniformly distributed in [d/2, d] where d = min(cap, base·2ⁿ). Jitter
// prevents a fleet of agents from reconnecting in lockstep after a
// control-plane restart.
func Backoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	// Shift overflows past attempt 6 (1s<<6 = 64s > cap), so clamp first.
	if attempt > 6 {
		attempt = 6
	}
	d := backoffBase << uint(attempt)
	if d > backoffCap {
		d = backoffCap
	}
	half := d / 2
	return half + rand.N(half+1)
}

// runSession performs one full connection lifecycle: dial, hello handshake,
// then ping/read until the connection breaks or ctx is cancelled.
// established reports whether the handshake completed (used to reset backoff).
func (r *Runner) runSession(ctx context.Context) (established bool, err error) {
	c, err := r.dial(ctx)
	if err != nil {
		return false, err
	}
	// StatusInternalError is sent only if we leave without a clean close.
	defer c.Close(websocket.StatusInternalError, "session ended")

	ack, err := r.handshake(ctx, c)
	if err != nil {
		return false, err
	}
	// Per-session logger: region/zone are already bound on r.cfg.Logger; add
	// agent_id so every line for this session (connected, ping/pong, op
	// dispatch) carries it. Scoped to the session — a reconnect gets a new id.
	sessionLog := r.cfg.Logger.With().Str("agent_id", ack.AgentID).Logger()
	sessionLog.Info().Msg("connected to control plane")

	return true, r.pingLoop(ctx, c, sessionLog)
}

// dial opens the WebSocket with the bearer token. The token is the only
// credential that ever travels to the control plane — the region's
// infrastructure credentials stay local to the datacenter.
func (r *Runner) dial(ctx context.Context) (*websocket.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	header := http.Header{}
	header.Set("Authorization", "Bearer "+r.cfg.Token)
	// A stable, identifiable User-Agent (dc-agent/<version>) so the connect
	// traffic is recognizable in edge/CDN logs and rules rather than appearing
	// as a generic Go WebSocket client — mirrors dcctl's User-Agent.
	header.Set("User-Agent", "dc-agent/"+r.cfg.Version)

	c, resp, err := websocket.Dial(dialCtx, r.cfg.Endpoint, &websocket.DialOptions{
		HTTPHeader: header,
	})
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("dial %s: %w (http status %d)", r.cfg.Endpoint, err, resp.StatusCode)
		}
		return nil, fmt.Errorf("dial %s: %w", r.cfg.Endpoint, err)
	}
	return c, nil
}

// handshake sends hello and waits (bounded) for hello_ack. The hello advertises
// the agent's supported ops so the server can return OP_UNSUPPORTED cleanly
// rather than timing out on a verb this agent lacks.
func (r *Runner) handshake(ctx context.Context, c *websocket.Conn) (*protocol.HelloAck, error) {
	hello := protocol.NewHello(r.cfg.Region, r.cfg.Zone, r.cfg.Version)
	hello.Ops = r.cfg.advertisedOps()
	if err := r.writeFrame(ctx, c, hello); err != nil {
		return nil, fmt.Errorf("send hello: %w", err)
	}

	ackCtx, cancel := context.WithTimeout(ctx, helloAckTimeout)
	defer cancel()
	_, data, err := c.Read(ackCtx)
	if err != nil {
		return nil, fmt.Errorf("await hello_ack: %w", err)
	}
	frame, err := protocol.Decode(data)
	if err != nil {
		return nil, fmt.Errorf("await hello_ack: %w", err)
	}
	ack, ok := frame.(*protocol.HelloAck)
	if !ok {
		return nil, fmt.Errorf("await hello_ack: unexpected frame %T", frame)
	}
	return ack, nil
}

// pingLoop is the steady state: a 30s ping ticker plus a read loop. Pongs
// refresh the idle clock; unknown frame types are logged and ignored
// (forward compatibility — a newer control plane may speak verbs this agent
// doesn't know yet). Returns when the connection breaks, the server goes
// silent past serverIdleLimit, or ctx is cancelled.
func (r *Runner) pingLoop(ctx context.Context, c *websocket.Conn, log zerolog.Logger) error {
	// sessionCtx bounds the op-dispatch goroutines to this connection: when the
	// session ends they are cancelled, so a slow op can't write to the next
	// session's socket.
	sessionCtx, cancelSession := context.WithCancel(ctx)
	defer cancelSession()

	type readResult struct {
		data []byte
		err  error
	}
	reads := make(chan readResult)
	go func() {
		for {
			_, data, err := c.Read(ctx)
			select {
			case reads <- readResult{data: data, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	ticker := time.NewTicker(r.cfg.PingInterval)
	defer ticker.Stop()
	lastRead := time.Now()

	for {
		select {
		case <-ctx.Done():
			c.Close(websocket.StatusNormalClosure, "agent shutting down")
			return ctx.Err()

		case res := <-reads:
			if res.err != nil {
				return fmt.Errorf("read: %w", res.err)
			}
			lastRead = time.Now()
			r.handleFrame(sessionCtx, c, res.data, log)

		case <-ticker.C:
			if idle := time.Since(lastRead); idle > r.cfg.IdleLimit {
				return fmt.Errorf("server silent for %s (limit %s)", idle.Truncate(time.Second), r.cfg.IdleLimit)
			}
			if err := r.writeFrame(ctx, c, protocol.NewPing(time.Now())); err != nil {
				return fmt.Errorf("send ping: %w", err)
			}
			log.Debug().Msg("ping sent")
		}
	}
}

// handleFrame processes one inbound steady-state frame: pongs refresh liveness,
// req frames are dispatched (each in its own goroutine), and unknown/unexpected
// types are logged and ignored (forward compatibility). log is the per-session
// logger (carries agent_id) — every line here fires inside an established
// session.
func (r *Runner) handleFrame(ctx context.Context, c *websocket.Conn, data []byte, log zerolog.Logger) {
	frame, err := protocol.Decode(data)
	if err != nil {
		log.Warn().Err(err).Msg("dropping undecodable frame")
		return
	}
	switch f := frame.(type) {
	case *protocol.Pong:
		log.Debug().Str("ts", f.TS).Msg("pong received")
	case *protocol.Req:
		// Run in its own goroutine so a slow op never stalls the ping loop or
		// the reader; bounded by the session ctx.
		go r.dispatch(ctx, c, f, log)
	case *protocol.Unknown:
		log.Warn().Str("frame_type", f.Type).Msg("ignoring unknown frame type (newer protocol?)")
	default:
		log.Warn().Str("frame_type", fmt.Sprintf("%T", f)).Msg("ignoring unexpected frame")
	}
}

// dispatch runs one request's handler and writes its terminal response. A
// request/response op is looked up in Dispatcher first; a streaming op (which may
// emit progress frames before its res) in StreamDispatcher; an op in neither
// returns OP_UNSUPPORTED. A handler error returns BAD_REQUEST if it reports
// itself a client fault (see badRequester/BadRequest), else EXEC_ERROR. log is
// the per-session logger (carries agent_id).
func (r *Runner) dispatch(ctx context.Context, c *websocket.Conn, req *protocol.Req, log zerolog.Logger) {
	if handler, ok := r.cfg.Dispatcher[req.Op]; ok {
		result, err := handler(ctx, req.Params)
		r.finishDispatch(ctx, c, req, result, err, log)
		return
	}
	if handler, ok := r.cfg.StreamDispatcher[req.Op]; ok {
		// The emitter is bound to this req's id and the current connection, and
		// goes through the mutex-guarded writeFrame so progress frames never
		// interleave with pongs or other ops' frames.
		emit := func(stage string, data json.RawMessage) {
			_ = r.writeFrame(ctx, c, protocol.NewProgressData(req.ID, stage, data))
		}
		result, err := handler(ctx, req.Params, emit)
		r.finishDispatch(ctx, c, req, result, err, log)
		return
	}
	r.writeRes(ctx, c, protocol.NewErrRes(req.ID, protocol.ErrCodeOpUnsupported, "unsupported op: "+req.Op), log)
}

// finishDispatch writes the single terminal res for a (streaming or
// request/response) op: a client-fault error → BAD_REQUEST, any other error →
// EXEC_ERROR, success → the result. Exactly one res ends every request.
func (r *Runner) finishDispatch(ctx context.Context, c *websocket.Conn, req *protocol.Req, result json.RawMessage, err error, log zerolog.Logger) {
	if err != nil {
		if isBadRequest(err) {
			log.Warn().Err(err).Str("op", req.Op).Msg("op rejected (bad request)")
			r.writeRes(ctx, c, protocol.NewErrRes(req.ID, protocol.ErrCodeBadRequest, err.Error()), log)
			return
		}
		log.Warn().Err(err).Str("op", req.Op).Msg("op handler failed")
		r.writeRes(ctx, c, protocol.NewErrRes(req.ID, protocol.ErrCodeExecError, err.Error()), log)
		return
	}
	log.Debug().Str("op", req.Op).Str("id", req.ID).Msg("op handled")
	r.writeRes(ctx, c, protocol.NewRes(req.ID, result), log)
}

// writeRes writes a response frame, logging (not returning) any error — the
// caller is a fire-and-forget dispatch goroutine. log is the per-session logger
// (carries agent_id).
func (r *Runner) writeRes(ctx context.Context, c *websocket.Conn, res *protocol.Res, log zerolog.Logger) {
	if err := r.writeFrame(ctx, c, res); err != nil {
		log.Warn().Err(err).Str("id", res.ID).Msg("send response failed")
	}
}

// writeFrame encodes and writes one frame with a bounded deadline.
func (r *Runner) writeFrame(ctx context.Context, c *websocket.Conn, frame any) error {
	b, err := protocol.Encode(frame)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	if err := c.Write(writeCtx, websocket.MessageText, b); err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}
		return fmt.Errorf("write %T: %w", frame, err)
	}
	return nil
}
