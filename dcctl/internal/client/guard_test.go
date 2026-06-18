package client

import (
	"net/http"
	"strings"
	"testing"
)

// cloudflareChallengeHTML is a trimmed but representative Cloudflare bot-protection
// interstitial — the kind of page returned instead of the JSON API response when
// the source network isn't allowed through edge protection.
const cloudflareChallengeHTML = `<!DOCTYPE html>
<html lang="en-US">
<head><title>Just a moment...</title></head>
<body>
<div id="challenge-running"></div>
<script src="https://challenges.cloudflare.com/turnstile/v0/api.js"></script>
</body>
</html>`

func newResp(status int, contentType string) *http.Response {
	h := http.Header{}
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	return &http.Response{StatusCode: status, Header: h}
}

func TestCheckAPIResponse_CloudflareChallenge(t *testing.T) {
	resp := newResp(http.StatusForbidden, "text/html; charset=UTF-8")

	err := checkAPIResponse(resp, []byte(cloudflareChallengeHTML))
	if err == nil {
		t.Fatal("expected an error for a Cloudflare challenge page, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"non-JSON response", "HTTP 403", "edge bot-protection", "Cloudflare", "dcapi_url"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q\ngot: %s", want, msg)
		}
	}
}

func TestCheckAPIResponse_ValidJSON(t *testing.T) {
	resp := newResp(http.StatusOK, "application/json")

	if err := checkAPIResponse(resp, []byte(`{"id":"abc","name":"web-01"}`)); err != nil {
		t.Fatalf("expected no error for a valid JSON body, got: %v", err)
	}
}

func TestCheckAPIResponse_ValidJSONArray(t *testing.T) {
	resp := newResp(http.StatusOK, "application/json")

	if err := checkAPIResponse(resp, []byte(`[{"id":"abc"}]`)); err != nil {
		t.Fatalf("expected no error for a valid JSON array body, got: %v", err)
	}
}

func TestCheckAPIResponse_RealJSONAPIError(t *testing.T) {
	// A genuine API error with a JSON body and JSON content type must flow
	// through unchanged — the guard only catches non-JSON / challenge pages.
	resp := newResp(http.StatusForbidden, "application/json")

	if err := checkAPIResponse(resp, []byte(`{"error":"forbidden"}`)); err != nil {
		t.Fatalf("expected no error for a JSON API error response, got: %v", err)
	}
}

func TestCheckAPIResponse_EmptyBody(t *testing.T) {
	// 202/204-style empty bodies are not challenges.
	resp := newResp(http.StatusAccepted, "application/json")

	if err := checkAPIResponse(resp, nil); err != nil {
		t.Fatalf("expected no error for an empty body, got: %v", err)
	}
}

func TestCheckAPIResponse_HTMLWithoutMarker(t *testing.T) {
	// Any HTML interstitial (even without a Cloudflare marker) is not the JSON
	// the CLI expects and should still produce the actionable error.
	resp := newResp(http.StatusBadGateway, "text/html")

	if err := checkAPIResponse(resp, []byte("<html><body>502 Bad Gateway</body></html>")); err == nil {
		t.Fatal("expected an error for an HTML body, got nil")
	}
}

func TestCheckAPIResponse_JSONContentTypeButHTMLBody(t *testing.T) {
	// Defensive: a spoofed JSON content type carrying a challenge marker in the
	// body must still be caught.
	resp := newResp(http.StatusOK, "application/json")

	if err := checkAPIResponse(resp, []byte(cloudflareChallengeHTML)); err == nil {
		t.Fatal("expected an error when a challenge marker is present despite a JSON content type, got nil")
	}
}
