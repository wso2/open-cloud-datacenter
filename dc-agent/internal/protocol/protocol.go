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
// Future operation verbs — Apply, Delete, GetStatus, WatchStatus — extend the
// Type space without changing this envelope. To stay forward compatible,
// receivers MUST tolerate unknown frame types (Decode returns *Unknown rather
// than an error) and log-and-ignore them.
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
)

// Hello is the first frame the agent sends after the WebSocket connects.
// It identifies the agent's region/zone and software version.
type Hello struct {
	Type    string `json:"type"`
	Region  string `json:"region"`
	Zone    string `json:"zone"`
	Version string `json:"version"`
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
	default:
		return &Unknown{Type: envelope.Type, Raw: append([]byte(nil), data...)}, nil
	}

	if err := json.Unmarshal(data, frame); err != nil {
		return nil, fmt.Errorf("protocol: decode %q frame: %w", envelope.Type, err)
	}
	return frame, nil
}
