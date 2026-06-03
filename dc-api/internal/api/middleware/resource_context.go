// Package middleware — resource_context.go
//
// ResourceScope injects an individual resource's UUID into the request context
// so the RBAC scope chain includes {resource, uuid} (narrowest scope). It does
// NOT gate access on its own: ProjectContext has already admitted the project,
// and each action's requireAction does the authorization with the resource now
// in the chain. That is what lets a role granted on a single resource authorize
// actions on that one resource — and only that one, since the engine matches by
// scope_uuid, so a grant on VM A never authorizes anything on VM B.
package middleware

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// ResourceScope returns middleware that reads a resource UUID from the named URL
// parameter (resources are identified by UUID, so the value is parsed as one)
// and stashes it as ContextKeyResourceUUID. A value that is not a UUID is
// ignored — the downstream handler will reject it — so the scope chain is never
// polluted with a bogus resource entry.
func ResourceScope(paramName string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if id, err := uuid.Parse(chi.URLParam(r, paramName)); err == nil {
				r = r.WithContext(context.WithValue(r.Context(), ContextKeyResourceUUID, id))
			}
			next.ServeHTTP(w, r)
		})
	}
}
