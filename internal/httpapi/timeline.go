package httpapi

import (
	"net/http"
	"strings"

	"roadmap/internal/auth"
	"roadmap/internal/store"
)

var taskTimelineKinds = map[string]struct{}{
	"agent_progress": {},
	"comment":        {},
	"task_change":    {},
}

// taskTimeline serves the durable, task-scoped activity stream. Authorization
// happens before task lookup so a bearer token without tasks:read cannot use a
// timeline request to probe task existence or actor metadata.
func (s *Server) taskTimeline(w http.ResponseWriter, r *http.Request, identity auth.Identity, reference string) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return
	}
	if !requireScope(w, identity, "tasks:read") {
		return
	}
	task, err := s.Store.ResolveTaskReference(r.Context(), reference)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if !identity.CanProject(task.ProjectID) {
		s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to this project", nil)
		return
	}
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	kind, err := parseOptionalEnum(r, "kind", taskTimelineKinds)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	before, present, err := queryValue(r, "before")
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	if present {
		before = strings.TrimSpace(before)
		if before == "" {
			s.writeError(w, http.StatusBadRequest, "invalid_request", "before must not be empty", nil)
			return
		}
		if len(before) > 2048 {
			s.writeError(w, http.StatusBadRequest, "invalid_request", "before is too long", nil)
			return
		}
	}
	items, more, err := s.Store.ListTaskTimeline(r.Context(), task.ID, store.TaskTimelineFilter{Before: before, Kind: kind, Limit: limit})
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	next := ""
	if more && len(items) > 0 {
		next = items[len(items)-1].Cursor
	}
	s.writeCollection(w, items, next)
}

// projectTimeline serves the board-scoped activity stream. It resolves the
// project before reading any task activity, but only after the tasks:read gate
// so callers without the required scope cannot probe project existence.
func (s *Server) projectTimeline(w http.ResponseWriter, r *http.Request, identity auth.Identity, reference string) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return
	}
	if !requireScope(w, identity, "tasks:read") {
		return
	}
	project, err := s.Store.GetProject(r.Context(), reference)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if !identity.CanProject(project.ID) {
		s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to this project", nil)
		return
	}
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	kind, err := parseOptionalEnum(r, "kind", taskTimelineKinds)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	before, present, err := queryValue(r, "before")
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	if present {
		before = strings.TrimSpace(before)
		if before == "" {
			s.writeError(w, http.StatusBadRequest, "invalid_request", "before must not be empty", nil)
			return
		}
		if len(before) > 2048 {
			s.writeError(w, http.StatusBadRequest, "invalid_request", "before is too long", nil)
			return
		}
	}
	items, more, err := s.Store.ListProjectTimeline(r.Context(), project.ID, store.TaskTimelineFilter{Before: before, Kind: kind, Limit: limit})
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	next := ""
	if more && len(items) > 0 {
		next = items[len(items)-1].Cursor
	}
	s.writeCollection(w, items, next)
}
