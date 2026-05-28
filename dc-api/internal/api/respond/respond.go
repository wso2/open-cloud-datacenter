// Package respond holds the response helpers shared across every HTTP
// layer in dc-api — handlers, middleware, and the BFF session controller.
//
// Why a dedicated package: middleware short-circuits with auth /
// project-context errors before the handler runs. Pre-respond, those
// middleware paths called net/http.Error, which writes
//
//	Content-Type: text/plain; charset=utf-8
//
// while every other response body is JSON. The OpenAPI spec declares all
// error responses as application/json, so schemathesis flagged every
// middleware-short-circuited path as "Undocumented Content-Type" (26
// findings on a single run). The handlers package had its own writeJSON
// / writeError pair, but middleware can't import handlers without an
// import cycle — hence this minimal third package both can use.
package respond

import (
	"encoding/json"
	"net/http"
)

// JSON encodes v as JSON and writes it with the standard headers + the
// given status code.
//
// Cache-Control: no-store on every response: dc-api's bodies reflect
// dynamic state (resource phase, soft-delete toggles, quota counters)
// and the browser-default cacheability of 200/301/404/410 has bitten us
// before — a stale 410 surviving a KV secret restore. Cheaper to
// blanket no-store than to whitelist per-route.
func JSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Error writes the canonical {"error": "..."} body with the given status.
// Drop-in replacement for net/http.Error that keeps the Content-Type
// declared by openapi.yaml.
func Error(w http.ResponseWriter, status int, message string) {
	JSON(w, status, map[string]string{"error": message})
}
