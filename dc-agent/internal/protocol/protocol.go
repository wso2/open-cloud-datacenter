// Package protocol defines the dc-agent ↔ control-plane wire frames.
//
// This package mirrors the server-side frame definitions in dc-api. The two
// codebases do not share a Go package — the JSON wire format itself is the
// compatibility contract. Frames are JSON text messages over a WebSocket,
// discriminated by the "type" field; this framing is protocol v0.
//
// Protocol v0 covers connection lifecycle only:
//
//	agent  → server   {"type":"hello","region":"…","zone":"…","version":"…"}
//	server → agent    {"type":"hello_ack","agent_id":"<uuid>"}
//	agent  → server   {"type":"ping","ts":"<RFC3339>"}   (every 30 seconds)
//	server → agent    {"type":"pong","ts":"<RFC3339>"}
//
// Protocol v1 adds a request/response layer over the same socket (see
// docs/multi-region-protocol-v1.md): the server sends a "req" frame carrying a
// correlation "id" and an "op" name, the agent runs it and replies with a "res"
// frame echoing the id (with zero or more advisory "progress" frames first).
// Adding an operation is a new op string, not a new frame type. To stay forward
// compatible, receivers MUST tolerate unknown frame types (Decode returns
// *Unknown rather than an error) and log-and-ignore them.
package protocol

import (
	"encoding/json"
	"fmt"
	"time"
)

// Frame type discriminators (the "type" JSON field).
const (
	TypeHello    = "hello"
	TypeHelloAck = "hello_ack"
	TypePing     = "ping"
	TypePong     = "pong"

	// v1 request/response layer.
	TypeReq      = "req"
	TypeRes      = "res"
	TypeProgress = "progress"
)

// Error codes an agent may return in a Res (see docs/multi-region-protocol-v1.md
// §7). The server synthesizes AGENT_UNAVAILABLE and TIMEOUT itself — those are
// never sent over the wire by the agent.
const (
	ErrCodeOpUnsupported = "OP_UNSUPPORTED"
	ErrCodeBadRequest    = "BAD_REQUEST"
	ErrCodeExecError     = "EXEC_ERROR"
)

// Hello is the first frame the agent sends after the WebSocket connects. It
// identifies the agent's region/zone and software version. Ops (v1) advertises
// the request operations this agent can serve; it is omitted by v0 agents, which
// the server reads as "no v1 ops".
type Hello struct {
	Type    string   `json:"type"`
	Region  string   `json:"region"`
	Zone    string   `json:"zone"`
	Version string   `json:"version"`
	Ops     []string `json:"ops,omitempty"`
}

// HelloAck is the server's response to Hello. AgentID is the server-assigned
// identity (UUID) for this agent session.
type HelloAck struct {
	Type    string `json:"type"`
	AgentID string `json:"agent_id"`
}

// Ping is the agent→server keepalive, sent every 30 seconds. The server
// enforces a ~120s read deadline, so missing a few pings tears the
// connection down and the agent reconnects.
type Ping struct {
	Type string `json:"type"`
	TS   string `json:"ts"`
}

// Pong is the server's reply to Ping.
type Pong struct {
	Type string `json:"type"`
	TS   string `json:"ts"`
}

// Req is a control-plane → agent operation request. ID correlates the eventual
// response; Op names the operation; Params is the op-specific argument object
// (nil for argument-less ops).
type Req struct {
	Type   string          `json:"type"`
	ID     string          `json:"id"`
	Op     string          `json:"op"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Res is the terminal response to a Req, correlated by ID — exactly one ends a
// request. On success Ok is true and Result carries the op-specific payload; on
// failure Ok is false and Error describes why.
type Res struct {
	Type   string          `json:"type"`
	ID     string          `json:"id"`
	Ok     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *FrameError     `json:"error,omitempty"`
}

// FrameError is a structured operation error. Code is a stable machine-readable
// token (see the Err* constants); Message is human-readable detail.
type FrameError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Progress is an optional, advisory in-flight update emitted zero or more times
// before the terminal Res, correlated by ID. Receivers may ignore it.
//
// Data (added in M-B) carries a structured payload — e.g. a status snapshot for
// watch_status. It is additive and omitempty: v0/M-A receivers decode Progress
// into a struct without the field and encoding/json silently drops the unknown
// key, so the wire stays backward compatible.
type Progress struct {
	Type   string          `json:"type"`
	ID     string          `json:"id"`
	Stage  string          `json:"stage"`
	Detail string          `json:"detail,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// Unknown is returned by Decode for frame types this agent version does not
// recognise. Callers log and ignore it — never error — so that newer servers
// can introduce frame types without breaking older agents.
type Unknown struct {
	Type string
	Raw  json.RawMessage
}

// NewHello builds a Hello frame with the type discriminator set.
func NewHello(region, zone, version string) *Hello {
	return &Hello{Type: TypeHello, Region: region, Zone: zone, Version: version}
}

// NewPing builds a Ping frame with the given timestamp in RFC3339.
func NewPing(ts time.Time) *Ping {
	return &Ping{Type: TypePing, TS: ts.UTC().Format(time.RFC3339)}
}

// NewReq builds a request frame. params may be nil for argument-less ops.
func NewReq(id, op string, params json.RawMessage) *Req {
	return &Req{Type: TypeReq, ID: id, Op: op, Params: params}
}

// NewRes builds a successful terminal response carrying result.
func NewRes(id string, result json.RawMessage) *Res {
	return &Res{Type: TypeRes, ID: id, Ok: true, Result: result}
}

// NewErrRes builds a failure terminal response with a structured error.
func NewErrRes(id, code, message string) *Res {
	return &Res{Type: TypeRes, ID: id, Ok: false, Error: &FrameError{Code: code, Message: message}}
}

// NewProgress builds an advisory progress frame with a human-readable detail
// string. Unchanged from M-A for existing callers.
func NewProgress(id, stage, detail string) *Progress {
	return &Progress{Type: TypeProgress, ID: id, Stage: stage, Detail: detail}
}

// NewProgressData builds an advisory progress frame carrying a structured Data
// payload (M-B) — e.g. a watch_status status snapshot. Detail is left empty.
func NewProgressData(id, stage string, data json.RawMessage) *Progress {
	return &Progress{Type: TypeProgress, ID: id, Stage: stage, Data: data}
}

// Encode marshals a frame to its JSON wire representation.
func Encode(frame any) ([]byte, error) {
	b, err := json.Marshal(frame)
	if err != nil {
		return nil, fmt.Errorf("protocol: encode: %w", err)
	}
	return b, nil
}

// Decode parses a JSON wire frame into its typed struct based on the "type"
// discriminator. Unknown types are NOT an error: they decode to *Unknown so
// callers can log and ignore them (forward compatibility).
func Decode(data []byte) (any, error) {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("protocol: decode envelope: %w", err)
	}

	var frame any
	switch envelope.Type {
	case TypeHello:
		frame = &Hello{}
	case TypeHelloAck:
		frame = &HelloAck{}
	case TypePing:
		frame = &Ping{}
	case TypePong:
		frame = &Pong{}
	case TypeReq:
		frame = &Req{}
	case TypeRes:
		frame = &Res{}
	case TypeProgress:
		frame = &Progress{}
	default:
		return &Unknown{Type: envelope.Type, Raw: append([]byte(nil), data...)}, nil
	}

	if err := json.Unmarshal(data, frame); err != nil {
		return nil, fmt.Errorf("protocol: decode %q frame: %w", envelope.Type, err)
	}
	return frame, nil
}
