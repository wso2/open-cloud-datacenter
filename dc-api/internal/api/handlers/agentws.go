// Package handlers — agentws.go
//
// AgentWSHandler serves GET /v1/agent/ws — the control-plane end of the
// dc-agent channel. Agents dial OUTBOUND over WSS/443 (datacenters never
// accept inbound connections), authenticate with a "dcagent_" bearer token,
// and then speak protocol v0 (hello / hello_ack / ping / pong) to keep the
// channel alive. Operation verbs (Apply/Delete/GetStatus/WatchStatus) extend
// the same JSON envelope in a later protocol version.
//
// This route is mounted OUTSIDE the OIDC-protected /v1 group (like /healthz):
// agents present a bearer token from agent_tokens, not an Asgardeo JWT.
//
// Protocol v0 wire contract (JSON text frames), mirrored from dc-agent's
// internal/protocol package — the two codebases share the wire format, not a
// Go package:
//
//	agent  → server   {"type":"hello","region":"…","zone":"…","version":"…"}
//	server → agent    {"type":"hello_ack","agent_id":"<uuid>"}
//	agent  → server   {"type":"ping","ts":"<RFC3339>"}   (every 30s)
//	server → agent    {"type":"pong","ts":"<RFC3339>"}
//
// Forward compatibility: unknown frame types are logged and ignored, never
// fatal, so a newer agent can introduce frames this server doesn't know.
package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/wso2/dc-api/internal/db"
)

// Frame type discriminators (the "type" JSON field). Must match dc-agent.
const (
	wsTypeHello    = "hello"
	wsTypeHelloAck = "hello_ack"
	wsTypePing     = "ping"
	wsTypePong     = "pong"
)

const (
	// agentBearerPrefix self-identifies an agent credential and lets us reject
	// non-agent bearers before hashing.
	agentBearerPrefix = "dcagent_"

	// serverReadDeadline bounds how long the server waits for the next frame.
	// The agent pings every 30s, so a healthy channel never approaches this;
	// crossing it means the agent is gone and we tear the channel down. Mirrors
	// the agent's own 120s idle limit.
	serverReadDeadline = 120 * time.Second

	// helloDeadline bounds the wait for the agent's first (hello) frame, which
	// it sends immediately after the WebSocket opens.
	helloDeadline = 15 * time.Second

	// writeDeadline bounds a single outbound frame write.
	writeDeadline = 10 * time.Second
)

// wire frames (server-side mirror of dc-agent's protocol structs).
type wsHello struct {
	Type    string `json:"type"`
	Region  string `json:"region"`
	Zone    string `json:"zone"`
	Version string `json:"version"`
}

type wsHelloAck struct {
	Type    string `json:"type"`
	AgentID string `json:"agent_id"`
}

type wsPong struct {
	Type string `json:"type"`
	TS   string `json:"ts"`
}

// AgentWSHandler upgrades agent connections and runs the protocol-v0 loop.
type AgentWSHandler struct {
	repo *db.Repository
	log  zerolog.Logger
}

// NewAgentWSHandler constructs the handler with injected dependencies.
func NewAgentWSHandler(repo *db.Repository, log zerolog.Logger) *AgentWSHandler {
	return &AgentWSHandler{repo: repo, log: log}
}

// ServeHTTP authenticates the bearer token, upgrades to WebSocket, and runs
// the keepalive loop until the agent disconnects or goes silent.
func (h *AgentWSHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// ── Authn: "Authorization: Bearer dcagent_<token>" ──────────────────────
	// Done BEFORE the upgrade so a bad credential gets a normal HTTP 401 (which
	// the agent's dial surfaces as "http status 401"), not a WebSocket close.
	rawToken, ok := bearerAgentToken(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing or malformed agent bearer token")
		return
	}
	sum := sha256.Sum256([]byte(rawToken))
	region, zone, found, err := h.repo.LookupAgentToken(r.Context(), hex.EncodeToString(sum[:]))
	if err != nil {
		h.log.Error().Err(err).Msg("agent token lookup failed")
		writeError(w, http.StatusInternalServerError, "token lookup failed")
		return
	}
	if !found {
		writeError(w, http.StatusUnauthorized, "invalid agent token")
		return
	}

	// ── Upgrade ─────────────────────────────────────────────────────────────
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		// Accept has already written the failure response.
		h.log.Warn().Err(err).Msg("agent websocket upgrade failed")
		return
	}
	defer c.CloseNow() //nolint:errcheck // safety net; clean path calls Close below

	// The channel outlives the request: chi's global 60s Timeout middleware
	// cancels r.Context(), which would kill a long-lived loop derived from it.
	// Use a fresh background context for the session instead.
	sessCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Record last_used_at on the credential (once, at connect — it's an auth
	// event, distinct from the per-frame liveness on the agents row).
	if err := h.repo.MarkAgentTokenUsed(sessCtx, hex.EncodeToString(sum[:])); err != nil {
		h.log.Warn().Err(err).Msg("mark agent token used failed (non-fatal)")
	}

	// ── Handshake: read hello, upsert agent, send hello_ack ─────────────────
	hello, err := h.readHello(sessCtx, c)
	if err != nil {
		h.log.Warn().Err(err).Str("region", region).Str("zone", zone).Msg("agent handshake failed")
		_ = c.Close(websocket.StatusProtocolError, "expected hello")
		return
	}
	// The token, not the hello frame, is authoritative for (region, zone): an
	// agent can't claim a zone it wasn't issued a credential for. We log a
	// mismatch but bind the agents row to the token's scope.
	if hello.Region != region || hello.Zone != zone {
		h.log.Warn().
			Str("token_region", region).Str("token_zone", zone).
			Str("hello_region", hello.Region).Str("hello_zone", hello.Zone).
			Msg("agent hello region/zone differs from token scope; using token scope")
	}

	agentID, err := h.repo.UpsertAgent(sessCtx, region, zone, hello.Version, r.RemoteAddr)
	if err != nil {
		h.log.Error().Err(err).Msg("upsert agent failed")
		_ = c.Close(websocket.StatusInternalError, "registration failed")
		return
	}

	if err := writeFrame(sessCtx, c, wsHelloAck{Type: wsTypeHelloAck, AgentID: agentID.String()}); err != nil {
		h.log.Warn().Err(err).Msg("send hello_ack failed")
		return
	}
	h.log.Info().
		Str("agent_id", agentID.String()).
		Str("region", region).Str("zone", zone).
		Str("version", hello.Version).Str("remote", r.RemoteAddr).
		Msg("agent connected")

	// ── Steady state: read frames, bump liveness, reply to pings ────────────
	h.serve(sessCtx, c, agentID)

	_ = c.Close(websocket.StatusNormalClosure, "")
	h.log.Info().Str("agent_id", agentID.String()).Msg("agent disconnected")
}

// readHello reads and decodes the first frame, which must be a hello.
func (h *AgentWSHandler) readHello(ctx context.Context, c *websocket.Conn) (*wsHello, error) {
	readCtx, cancel := context.WithTimeout(ctx, helloDeadline)
	defer cancel()
	_, data, err := c.Read(readCtx)
	if err != nil {
		return nil, err
	}
	var env struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	if env.Type != wsTypeHello {
		return nil, &protocolError{got: env.Type}
	}
	var hello wsHello
	if err := json.Unmarshal(data, &hello); err != nil {
		return nil, err
	}
	return &hello, nil
}

// serve is the steady-state loop: every inbound frame refreshes the agent's
// last_seen; pings are answered with a pong. The 120s read deadline tears the
// channel down if the agent goes silent (the agent reconnects on its side).
func (h *AgentWSHandler) serve(ctx context.Context, c *websocket.Conn, agentID uuid.UUID) {
	for {
		readCtx, cancel := context.WithTimeout(ctx, serverReadDeadline)
		_, data, err := c.Read(readCtx)
		cancel()
		if err != nil {
			// Normal close, deadline, or transport error — end the session.
			return
		}

		// Bump liveness on every frame so derived health stays fresh.
		if err := h.repo.TouchAgent(ctx, agentID); err != nil {
			h.log.Warn().Err(err).Msg("touch agent failed (non-fatal)")
		}

		var env struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &env); err != nil {
			h.log.Warn().Err(err).Msg("dropping undecodable agent frame")
			continue
		}
		switch env.Type {
		case wsTypePing:
			pong := wsPong{Type: wsTypePong, TS: time.Now().UTC().Format(time.RFC3339)}
			if err := writeFrame(ctx, c, pong); err != nil {
				h.log.Warn().Err(err).Msg("send pong failed")
				return
			}
		default:
			// Forward compatibility: tolerate unknown / future frame types.
			h.log.Debug().Str("frame_type", env.Type).Msg("ignoring non-ping agent frame")
		}
	}
}

// bearerAgentToken extracts a "dcagent_"-prefixed token from the Authorization
// header. ok is false for a missing header, a non-Bearer scheme, or a token
// without the agent prefix.
func bearerAgentToken(r *http.Request) (string, bool) {
	const scheme = "Bearer "
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, scheme) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(auth, scheme))
	if !strings.HasPrefix(token, agentBearerPrefix) {
		return "", false
	}
	return token, true
}

// writeFrame marshals a frame and writes it as a text message with a bounded
// deadline.
func writeFrame(ctx context.Context, c *websocket.Conn, frame any) error {
	b, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, writeDeadline)
	defer cancel()
	return c.Write(writeCtx, websocket.MessageText, b)
}

// protocolError reports an unexpected frame type during the handshake.
type protocolError struct{ got string }

func (e *protocolError) Error() string { return "unexpected frame type: " + e.got }
