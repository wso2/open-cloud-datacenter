// Package audit carries the cross-cutting pieces of the audit framework that
// must be visible to several layers without import cycles: the action
// vocabulary and the actor attribution that rides the request context.
//
// ── THE AUDIT FRAMEWORK ──────────────────────────────────────────────────────
//
// Lifecycle auditing is automatic at the repository layer: every family's
// Create* method records CREATE, every Update*Status method records
// STATUS_CHANGE (capturing the previous status via RETURNING and skipping
// no-op transitions), and every Delete* row removal records a terminal
// DELETE event with to_status DELETED — before the row goes away, so the
// identity snapshot resolves. Handlers and the reconciler write NO audit
// events themselves.
//
// A new resource family is therefore audited automatically by following the
// repository template (Create/UpdateStatus/Delete wired through the db
// package's record helpers). The remaining per-family declarations are the
// resource_type value in openapi.yaml's ActivityEvent enum and, when the
// family has a detail page, cloud-ui's ACTIVITY_RESOURCE_ROUTES entry.
//
// Actor attribution: the auth middlewares stamp the authenticated principal
// onto the request context with WithActor; background workers stamp their
// own identity (e.g. "reconciler"). ActorFromContext falls back to "system"
// so an event is never attributed to an empty string.
package audit

import "context"

// Actions recorded on audit events. The vocabulary is deliberately small —
// states carry the detail; actions classify the row.
const (
	ActionCreate       = "CREATE"
	ActionStatusChange = "STATUS_CHANGE"
	ActionDelete       = "DELETE"
)

// SystemActor attributes events that no principal initiated directly.
const SystemActor = "system"

type ctxKey struct{}

// WithActor stamps the acting principal (OIDC sub, service-account ID, or a
// worker name like "reconciler") onto the context for the repository layer's
// automatic audit recording.
func WithActor(ctx context.Context, actor string) context.Context {
	if actor == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, actor)
}

// ActorFromContext returns the stamped actor, or SystemActor when none is.
func ActorFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKey{}).(string); ok && v != "" {
		return v
	}
	return SystemActor
}
