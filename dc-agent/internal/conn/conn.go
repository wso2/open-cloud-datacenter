// Package conn owns the agent's connection lifecycle to the control plane:
// dial out over WSS, handshake (hello / hello_ack), keepalive pings, and
// reconnect-forever with exponential backoff + jitter.
//
// The agent is the dialing side by design — datacenters only ever open an
// outbound HTTPS(443) connection to the control plane; nothing dials in.
package conn

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
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
}

// Runner maintains the agent↔control-plane channel for the life of the
// process. It is comms-only today: protocol v0 (hello/ping) keeps the channel
// healthy; operation verbs arrive in a later protocol version.
type Runner struct {
	cfg Config
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
	r.cfg.Logger.Info().
		Str("agent_id", ack.AgentID).
		Str("region", r.cfg.Region).
		Str("zone", r.cfg.Zone).
		Msg("connected to control plane")

	return true, r.pingLoop(ctx, c)
}

// dial opens the WebSocket with the bearer token. The token is the only
// credential that ever travels to the control plane — the region's
// infrastructure credentials stay local to the datacenter.
func (r *Runner) dial(ctx context.Context) (*websocket.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	header := http.Header{}
	header.Set("Authorization", "Bearer "+r.cfg.Token)

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

// handshake sends hello and waits (bounded) for hello_ack.
func (r *Runner) handshake(ctx context.Context, c *websocket.Conn) (*protocol.HelloAck, error) {
	if err := r.writeFrame(ctx, c, protocol.NewHello(r.cfg.Region, r.cfg.Zone, r.cfg.Version)); err != nil {
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
func (r *Runner) pingLoop(ctx context.Context, c *websocket.Conn) error {
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
			r.handleFrame(res.data)

		case <-ticker.C:
			if idle := time.Since(lastRead); idle > r.cfg.IdleLimit {
				return fmt.Errorf("server silent for %s (limit %s)", idle.Truncate(time.Second), r.cfg.IdleLimit)
			}
			if err := r.writeFrame(ctx, c, protocol.NewPing(time.Now())); err != nil {
				return fmt.Errorf("send ping: %w", err)
			}
			r.cfg.Logger.Debug().Msg("ping sent")
		}
	}
}

// handleFrame processes one inbound steady-state frame. v0 expects only
// pongs; everything else is logged and ignored.
func (r *Runner) handleFrame(data []byte) {
	frame, err := protocol.Decode(data)
	if err != nil {
		r.cfg.Logger.Warn().Err(err).Msg("dropping undecodable frame")
		return
	}
	switch f := frame.(type) {
	case *protocol.Pong:
		r.cfg.Logger.Debug().Str("ts", f.TS).Msg("pong received")
	case *protocol.Unknown:
		r.cfg.Logger.Warn().Str("frame_type", f.Type).Msg("ignoring unknown frame type (newer protocol?)")
	default:
		r.cfg.Logger.Warn().Str("frame_type", fmt.Sprintf("%T", f)).Msg("ignoring unexpected frame")
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
	if err := c.Write(writeCtx, websocket.MessageText, b); err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}
		return fmt.Errorf("write %T: %w", frame, err)
	}
	return nil
}
