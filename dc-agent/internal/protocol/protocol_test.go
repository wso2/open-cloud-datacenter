package protocol

import (
	"encoding/json"
	"reflect"
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
				if !reflect.DeepEqual(h, want) {
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

// TestV1FrameRoundTrip verifies the v1 request/response frames survive an
// Encode → Decode round-trip with their fields (including raw JSON bodies)
// intact and correctly typed.
func TestV1FrameRoundTrip(t *testing.T) {
	t.Run("req", func(t *testing.T) {
		want := NewReq("id-1", "get_inventory", json.RawMessage(`{"include":["nodes"]}`))
		got := roundTrip(t, want)
		req, ok := got.(*Req)
		if !ok {
			t.Fatalf("Decode returned %T, want *Req", got)
		}
		if req.Type != TypeReq || req.ID != want.ID || req.Op != want.Op || string(req.Params) != string(want.Params) {
			t.Errorf("req round-trip mismatch: got %+v", req)
		}
	})
	t.Run("res ok", func(t *testing.T) {
		want := NewRes("id-1", json.RawMessage(`{"vm_count":12}`))
		got := roundTrip(t, want)
		res, ok := got.(*Res)
		if !ok {
			t.Fatalf("Decode returned %T, want *Res", got)
		}
		if !res.Ok || string(res.Result) != string(want.Result) || res.Error != nil {
			t.Errorf("res ok round-trip mismatch: got %+v", res)
		}
	})
	t.Run("res error", func(t *testing.T) {
		want := NewErrRes("id-1", ErrCodeExecError, "boom")
		got := roundTrip(t, want)
		res, ok := got.(*Res)
		if !ok {
			t.Fatalf("Decode returned %T, want *Res", got)
		}
		if res.Ok || res.Error == nil || res.Error.Code != ErrCodeExecError || res.Error.Message != "boom" {
			t.Errorf("res error round-trip mismatch: got %+v", res)
		}
	})
	t.Run("progress", func(t *testing.T) {
		want := NewProgress("id-1", "applying", "step 2/3")
		got := roundTrip(t, want)
		p, ok := got.(*Progress)
		if !ok {
			t.Fatalf("Decode returned %T, want *Progress", got)
		}
		if *p != *want {
			t.Errorf("progress round-trip mismatch: got %+v, want %+v", p, want)
		}
	})
}

// TestReqResWireFormat pins the v1 frame JSON — the wire format is the contract
// with dc-api, so a field/tag change must fail here.
func TestReqResWireFormat(t *testing.T) {
	// req with nil params: params must be omitted.
	b, err := Encode(NewReq("id-1", "get_inventory", nil))
	if err != nil {
		t.Fatalf("Encode req: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("Unmarshal req: %v", err)
	}
	if m["type"] != "req" || m["id"] != "id-1" || m["op"] != "get_inventory" {
		t.Errorf("req wire format wrong: %s", b)
	}
	if _, has := m["params"]; has {
		t.Errorf("nil params must be omitted: %s", b)
	}

	// error res: ok=false, error present, result omitted.
	b, err = Encode(NewErrRes("id-1", ErrCodeBadRequest, "nope"))
	if err != nil {
		t.Fatalf("Encode res: %v", err)
	}
	m = nil
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("Unmarshal res: %v", err)
	}
	if m["ok"] != false {
		t.Errorf("error res must have ok=false: %s", b)
	}
	if _, has := m["result"]; has {
		t.Errorf("error res must omit result: %s", b)
	}
	errObj, ok := m["error"].(map[string]any)
	if !ok || errObj["code"] != ErrCodeBadRequest {
		t.Errorf("error res error.code wrong: %s", b)
	}
}

// TestHelloOpsField verifies the v1 op advertisement round-trips when set.
// (A v0 hello with no ops keeps the exact v0 wire shape — see
// TestHelloWireFormat, which asserts exactly four fields.)
func TestHelloOpsField(t *testing.T) {
	h := NewHello("region-a", "zone-1", "0.2.0")
	h.Ops = []string{"get_inventory"}
	got := roundTrip(t, h)
	hh, ok := got.(*Hello)
	if !ok {
		t.Fatalf("Decode returned %T, want *Hello", got)
	}
	if len(hh.Ops) != 1 || hh.Ops[0] != "get_inventory" {
		t.Errorf("ops round-trip mismatch: got %+v", hh)
	}
}

// roundTrip encodes a frame and decodes it back, failing the test on any error.
func roundTrip(t *testing.T, frame any) any {
	t.Helper()
	b, err := Encode(frame)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := Decode(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return got
}
