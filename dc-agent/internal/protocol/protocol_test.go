package protocol

import (
	"encoding/json"
	"testing"
	"time"
)

// TestEncodeDecodeRoundTrip verifies that every v0 frame survives an
// Encode → Decode round-trip with its type discriminator and fields intact.
func TestEncodeDecodeRoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		frame any
	}{
		{"hello", NewHello("region-a", "zone-1", "0.1.0")},
		{"hello_ack", &HelloAck{Type: TypeHelloAck, AgentID: "5f1c8e1a-0000-4000-8000-000000000000"}},
		{"ping", NewPing(time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC))},
		{"pong", &Pong{Type: TypePong, TS: "2026-06-12T10:00:01Z"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := Encode(tc.frame)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			got, err := Decode(b)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}

			switch want := tc.frame.(type) {
			case *Hello:
				h, ok := got.(*Hello)
				if !ok {
					t.Fatalf("Decode returned %T, want *Hello", got)
				}
				if *h != *want {
					t.Errorf("round-trip mismatch: got %+v, want %+v", h, want)
				}
			case *HelloAck:
				h, ok := got.(*HelloAck)
				if !ok {
					t.Fatalf("Decode returned %T, want *HelloAck", got)
				}
				if *h != *want {
					t.Errorf("round-trip mismatch: got %+v, want %+v", h, want)
				}
			case *Ping:
				p, ok := got.(*Ping)
				if !ok {
					t.Fatalf("Decode returned %T, want *Ping", got)
				}
				if *p != *want {
					t.Errorf("round-trip mismatch: got %+v, want %+v", p, want)
				}
			case *Pong:
				p, ok := got.(*Pong)
				if !ok {
					t.Fatalf("Decode returned %T, want *Pong", got)
				}
				if *p != *want {
					t.Errorf("round-trip mismatch: got %+v, want %+v", p, want)
				}
			}
		})
	}
}

// TestHelloWireFormat pins the exact JSON the server expects — the wire
// format is the compatibility contract with dc-api, so a field rename or
// tag change must fail this test.
func TestHelloWireFormat(t *testing.T) {
	b, err := Encode(NewHello("region-a", "zone-1", "0.1.0"))
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	want := map[string]string{
		"type":    "hello",
		"region":  "region-a",
		"zone":    "zone-1",
		"version": "0.1.0",
	}
	if len(m) != len(want) {
		t.Fatalf("hello frame has unexpected fields: %s", b)
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("field %q = %q, want %q", k, m[k], v)
		}
	}
}

// TestDecodeUnknownType verifies forward compatibility: a frame type this
// agent version does not know must decode to *Unknown, never an error.
func TestDecodeUnknownType(t *testing.T) {
	raw := []byte(`{"type":"apply","manifest":{"kind":"ConfigMap"}}`)
	got, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode of unknown type must not error, got: %v", err)
	}
	u, ok := got.(*Unknown)
	if !ok {
		t.Fatalf("Decode returned %T, want *Unknown", got)
	}
	if u.Type != "apply" {
		t.Errorf("Unknown.Type = %q, want %q", u.Type, "apply")
	}
	if string(u.Raw) != string(raw) {
		t.Errorf("Unknown.Raw = %s, want original payload preserved", u.Raw)
	}
}

// TestDecodeInvalid verifies malformed input is an error (not a panic and
// not silently tolerated).
func TestDecodeInvalid(t *testing.T) {
	if _, err := Decode([]byte(`not json`)); err == nil {
		t.Error("Decode of invalid JSON must return an error")
	}
	if _, err := Decode([]byte(`{"type":"ping","ts":42}`)); err == nil {
		t.Error("Decode of type-mismatched known frame must return an error")
	}
}
