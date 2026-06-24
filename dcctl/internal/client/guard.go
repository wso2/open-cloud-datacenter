// Package client — edge bot-protection guard.
//
// When dcctl points at the deployed API behind an edge proxy (Cloudflare) and
// the source network isn't allowed through bot protection, the proxy returns an
// HTML interstitial ("Just a moment...", referencing challenges.cloudflare.com)
// instead of the JSON the API would have sent. The generated client only
// decodes bodies whose Content-Type contains "json", so a challenge page leaves
// every typed field nil and surfaces as an opaque "HTTP 403"/raw-HTML dump.
//
// checkAPIResponse turns that situation into one clear, actionable error, and
// guardTransport wires it in front of every request the shared http.Client
// makes — covering both the typed client and the hand-written doJSON helpers.
package client

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// challengeMarkers are substrings that only appear in an edge bot-protection
// interstitial, never in a legitimate DC-API JSON response.
var challengeMarkers = []string{
	"challenges.cloudflare.com",
	"just a moment",
	"cf-ray",
	"cf-chl",
	"__cf_chl",
	"_cf_chl_opt",
}

// checkAPIResponse reports an actionable error when the response is clearly NOT
// something dc-api produced — i.e. it looks like an edge bot-protection
// challenge or another HTML interstitial. It returns nil for any plausible
// dc-api response (a success, an async 202/204 accept, or a real API JSON
// error), so normal handling downstream is unchanged.
//
//   - A body that actually looks like a challenge (HTML, or a Cloudflare marker)
//     is flagged whatever the status code.
//   - Otherwise a 2xx is a successful dc-api response and is left alone, even if
//     the body is empty or non-JSON: some endpoints answer 202/204 that way for
//     asynchronous operations (e.g. a routed delete). A 2xx is never a challenge
//     — the request reached dc-api.
//   - For a non-2xx, dc-api answers with JSON; a non-JSON body means something
//     other than dc-api replied, so we surface the guidance.
func checkAPIResponse(resp *http.Response, body []byte) error {
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))

	// A genuine edge challenge (HTML interstitial / Cloudflare marker) is flagged
	// regardless of status code.
	if isChallengeBody(contentType, body) {
		return challengeError(resp.StatusCode)
	}

	// A 2xx means the request reached dc-api and succeeded. Async endpoints answer
	// 202/204 with an empty or non-JSON body; that is not a challenge.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	// Non-2xx: dc-api's error responses are JSON. A JSON-shaped body flows through
	// to normal error handling; anything else is an interstitial we should flag.
	if strings.Contains(contentType, "json") && looksLikeJSON(body) {
		return nil
	}
	return challengeError(resp.StatusCode)
}

// challengeError is the single actionable message for a response that did not
// come from dc-api (an edge bot-protection challenge or other interstitial).
func challengeError(status int) error {
	return fmt.Errorf(
		"dc-api returned a non-JSON response (HTTP %d) — this looks like an "+
			"edge bot-protection challenge (Cloudflare). dcctl cannot solve a "+
			"browser challenge. Check that your `dcapi_url` is correct and that "+
			"your source network is allowed through the API's edge protection",
		status,
	)
}

// looksLikeJSON reports whether body begins with a JSON object or array (after
// leading whitespace). An empty body is treated as JSON-compatible — some
// endpoints answer 202/204 with no payload, which is not a challenge.
func looksLikeJSON(body []byte) bool {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return true
	}
	switch trimmed[0] {
	case '{', '[':
		return true
	default:
		return false
	}
}

// isChallengeBody reports whether the response is an HTML bot-protection
// interstitial, by content type and/or a Cloudflare challenge marker.
func isChallengeBody(contentType string, body []byte) bool {
	lower := strings.ToLower(string(body))
	for _, m := range challengeMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return strings.Contains(contentType, "text/html")
}

// guardTransport wraps an http.RoundTripper and fails fast with the
// checkAPIResponse message when the response is an edge challenge / non-JSON
// page, rather than letting the generated client decode it into nil fields.
type guardTransport struct {
	next http.RoundTripper
}

func (g *guardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := g.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		// Restore an empty body and let the caller surface the read error.
		resp.Body = io.NopCloser(bytes.NewReader(nil))
		return resp, nil
	}
	// Refill the body so downstream parsers (typed client, doJSON) can read it.
	resp.Body = io.NopCloser(bytes.NewReader(body))

	if guardErr := checkAPIResponse(resp, body); guardErr != nil {
		return nil, guardErr
	}
	return resp, nil
}
