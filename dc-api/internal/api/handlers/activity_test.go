// Package handlers — activity_test.go
//
// Unit tests for ActivityHandler.List's request validation: the limit/offset
// 400 paths and the missing-project-context guard, all of which return before
// the repository is touched (so a nil *db.Repository is safe — same reasoning
// as role_assignments_test.go). The happy path (join shape, ordering,
// pagination, cross-project isolation) needs a real database and is covered in
// test/integration/activity_test.go (runs cluster-free with DCAPI_TEST_NOP=1).
package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/wso2/dc-api/internal/api/middleware"
)

// activityRequest builds a GET request to target with the project UUID the
// ProjectContext middleware would inject. A nil projectUUID leaves the context
// bare (simulating a route mounted outside the project group).
func activityRequest(target string, projectUUID *uuid.UUID) *http.Request {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	if projectUUID != nil {
		ctx := context.WithValue(r.Context(), middleware.ContextKeyProjectUUID, *projectUUID)
		r = r.WithContext(ctx)
	}
	return r
}

func TestActivityList_NoProjectContext_Returns500(t *testing.T) {
	t.Parallel()
	h := NewActivityHandler(nil, zerolog.Nop())
	w := httptest.NewRecorder()

	h.List(w, activityRequest("/v1/tenants/acme/projects/default/activity", nil))

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Equal(t, "no project UUID in context", errorField(t, w.Body.Bytes()))
}

func TestActivityList_InvalidPaging_Returns400(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		query string
	}{
		{"limit zero", "?limit=0"},
		{"limit above max", "?limit=101"},
		{"limit negative", "?limit=-1"},
		{"limit non-integer", "?limit=abc"},
		{"offset negative", "?offset=-1"},
		{"offset non-integer", "?offset=xyz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := NewActivityHandler(nil, zerolog.Nop())
			w := httptest.NewRecorder()
			pu := uuid.New()

			// A nil repo proves validation rejects the request BEFORE any DB
			// access — reaching the repository would panic.
			h.List(w, activityRequest("/v1/tenants/acme/projects/default/activity"+tc.query, &pu))

			assert.Equal(t, http.StatusBadRequest, w.Code, "body=%s", w.Body.String())
			errorField(t, w.Body.Bytes()) // must be the spec's Error envelope
		})
	}
}

func TestParseActivityPaging_Defaults(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/activity", nil)

	limit, offset, ok := parseActivityPaging(w, r)

	assert.True(t, ok)
	assert.Equal(t, 20, limit, "spec default limit is 20")
	assert.Equal(t, 0, offset, "spec default offset is 0")
}

func TestParseActivityPaging_Boundaries(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/activity?limit=100&offset=37", nil)

	limit, offset, ok := parseActivityPaging(w, r)

	assert.True(t, ok, "limit=100 is the inclusive maximum and must be accepted")
	assert.Equal(t, 100, limit)
	assert.Equal(t, 37, offset)
}
