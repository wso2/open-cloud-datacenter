// Package handlers — activity.go
//
// ActivityHandler serves the read-only project activity feed:
//
//	GET /v1/tenants/{tenant_id}/projects/{project_id}/activity
//
// One page of audit events for every resource in the project, newest first.
// Pure DB read — no provider calls. The route is gated with
// resourcemanager/activity/read in router.go (Reader's `*/read` covers it, so
// any project member can read the feed); project scoping comes from the
// ProjectContext middleware's project_uuid, never from the URL slug.
package handlers

import (
	"net/http"
	"strconv"

	"github.com/rs/zerolog"
	"github.com/wso2/dc-api/internal/api/middleware"
	"github.com/wso2/dc-api/internal/db"
	"github.com/wso2/dc-api/internal/models"
)

const (
	activityDefaultLimit = 20
	activityMaxLimit     = 100
)

// ActivityHandler serves the project-scoped activity feed.
type ActivityHandler struct {
	repo *db.Repository
	log  zerolog.Logger
}

// NewActivityHandler creates an ActivityHandler.
func NewActivityHandler(repo *db.Repository, log zerolog.Logger) *ActivityHandler {
	return &ActivityHandler{repo: repo, log: log}
}

// List handles GET .../projects/{project_id}/activity.
func (h *ActivityHandler) List(w http.ResponseWriter, r *http.Request) {
	projectUUID, ok := middleware.ProjectUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "no project UUID in context")
		return
	}

	limit, offset, ok := parseActivityPaging(w, r)
	if !ok {
		return
	}

	entries, total, err := h.repo.ListProjectActivity(r.Context(), projectUUID, limit, offset)
	if err != nil {
		h.log.Error().Err(err).Str("project_uuid", projectUUID.String()).Msg("list project activity failed")
		writeError(w, http.StatusInternalServerError, "failed to list project activity")
		return
	}

	if entries == nil {
		entries = []models.ActivityEntry{} // zero events must serialize as [], not null
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items": entries,
		"total": total,
	})
}

// parseActivityPaging reads limit/offset with the spec's defaults and bounds
// (limit default 20, 1..100; offset >= 0, default 0). Writes a 400 and returns
// ok=false on invalid input. Mirrors parseDirectoryPaging — separate because
// the bounds differ and the two specs evolve independently.
func parseActivityPaging(w http.ResponseWriter, r *http.Request) (limit, offset int, ok bool) {
	limit = activityDefaultLimit
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > activityMaxLimit {
			writeError(w, http.StatusBadRequest, "limit must be an integer between 1 and 100")
			return 0, 0, false
		}
		limit = n
	}
	if s := r.URL.Query().Get("offset"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "offset must be a non-negative integer")
			return 0, 0, false
		}
		offset = n
	}
	return limit, offset, true
}
