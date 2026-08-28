package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"

	"roadmap/internal/auth"
	"roadmap/internal/store"
)

func (s *Server) roadmap(w http.ResponseWriter, r *http.Request, identity auth.Identity, reference string, projectRoute bool) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return
	}
	if !requireScope(w, identity, "projects:read") {
		return
	}
	projectID := ""
	if !projectRoute {
		reference, err := parseOptionalIdentifier(r, "project")
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
			return
		}
		if reference != "" {
			project, err := s.Store.GetProject(r.Context(), reference)
			if err != nil {
				s.writeStoreError(w, err)
				return
			}
			if !identity.CanProject(project.ID) {
				s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to this project", nil)
				return
			}
			projectID = project.ID
		}
	}
	if projectRoute {
		project, err := s.Store.GetProject(r.Context(), reference)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		if !identity.CanProject(project.ID) {
			s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to this project", nil)
			return
		}
		projectID = project.ID
	} else if projectID == "" && identity.IsToken && identity.Token.ProjectsScoped {
		// A global aggregate would otherwise reveal counts from projects outside
		// the token's allow-list. Agents can request each scoped project route.
		s.writeError(w, http.StatusForbidden, "forbidden", "a project is required for a scoped token", nil)
		return
	}
	roadmap, err := s.Store.Roadmap(r.Context(), projectID)
	if err != nil {
		s.writeInternal(w, err)
		return
	}
	roadmap = redactRoadmapForIdentity(identity, roadmap)
	s.writeJSON(w, http.StatusOK, roadmap)
}

// redactRoadmapForIdentity keeps the projects:read route gate independent
// from the task and event collections embedded in a roadmap summary. A
// bearer token may therefore retain useful project totals without receiving
// task or activity records for scopes it was not granted. Empty slices are
// intentional: clients can distinguish an authorized, redacted collection
// from an omitted or null field.
func redactRoadmapForIdentity(identity auth.Identity, roadmap store.Roadmap) store.Roadmap {
	if !identity.IsToken {
		return roadmap
	}
	if !identity.HasScope("tasks:read") {
		roadmap.Upcoming = []store.Task{}
		roadmap.UpcomingTasks = []store.Task{}
	}
	if !identity.HasScope("events:read") {
		roadmap.RecentActivity = []store.Event{}
	} else {
		roadmap.RecentActivity = redactEventsForIdentity(identity, roadmap.RecentActivity)
	}
	return roadmap
}

func (s *Server) myWork(w http.ResponseWriter, r *http.Request, identity auth.Identity) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return
	}
	if !requireScope(w, identity, "tasks:read") {
		return
	}
	var projectIDs []string
	if identity.IsToken && identity.Token.ProjectsScoped {
		projectIDs = make([]string, 0, len(identity.Token.Projects))
		for projectID := range identity.Token.Projects {
			projectIDs = append(projectIDs, projectID)
		}
	}
	if reference, err := parseOptionalIdentifier(r, "project"); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	} else if reference != "" {
		project, err := s.Store.GetProject(r.Context(), reference)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		if !identity.CanProject(project.ID) {
			s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to this project", nil)
			return
		}
		projectIDs = []string{project.ID}
	} else if identity.IsToken && identity.Token.ProjectsScoped {
		// A global my-work aggregate would reveal task metadata across every
		// project allowed by the token. Require callers to select one project,
		// matching the scoped /roadmap contract.
		s.writeError(w, http.StatusForbidden, "forbidden", "a project is required for a scoped token", nil)
		return
	}
	view, err := parseOptionalEnum(r, "view", map[string]struct{}{"assigned": {}, "live": {}})
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	limit, offset, paginationErr := parsePagination(r, 50)
	if paginationErr != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", paginationErr.Error(), nil)
		return
	}
	state, err := parseOptionalEnum(r, "state", semanticStates)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	priority, err := parseOptionalEnum(r, "priority", taskPriorities)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	label, err := parseOptionalIdentifier(r, "label")
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	agentState, err := parseOptionalEnum(r, "agent_state", agentWorkStates)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	actionNeeded, err := parseOptionalStrictBool(r, "action_needed")
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	query, err := parseOptionalSearch(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	updatedAfter, err := parseOptionalTimestamp(r, "updated_after")
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	filter := store.TaskFilter{State: state, Priority: priority, Label: label, AgentState: agentState, ActionNeeded: actionNeeded, LiveWork: view == "live", Query: query, Cursor: offset, Limit: limit, UpdatedAfter: updatedAfter}
	tasks, more, err := s.Store.ListMyWorkFilteredWithExtra(r.Context(), identity.Actor.ID, projectIDs, filter)
	if err != nil {
		s.writeInternal(w, err)
		return
	}
	next := ""
	if more && len(tasks) > 0 {
		// Task/my-work cursors are opaque offsets matching the store's stable
		// ordering; task numbers are not a seek key for that ordering.
		next = encodeCursor(filter.Cursor + len(tasks))
	}
	s.writeCollection(w, tasks, next)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request, identity auth.Identity) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return
	}
	if !requireScope(w, identity, "events:read") {
		return
	}
	after, err := parseAfterCursor(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	projectID := ""
	if reference, err := parseOptionalIdentifier(r, "project"); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	} else if reference != "" {
		project, err := s.Store.GetProject(r.Context(), reference)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		if !identity.CanProject(project.ID) {
			s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to this project", nil)
			return
		}
		projectID = project.ID
	}
	limit, paginationErr := parseLimit(r, 50)
	if paginationErr != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", paginationErr.Error(), nil)
		return
	}
	eventFilter := store.EventFilter{After: after, ProjectID: projectID, Limit: limit}
	if identity.IsToken && identity.Token.ProjectsScoped && projectID == "" {
		eventFilter.ProjectIDs = scopedProjectIDs(identity)
	}
	events, more, err := s.Store.ListEvents(r.Context(), eventFilter)
	if err != nil {
		s.writeInternal(w, err)
		return
	}
	next := ""
	if more && len(events) > 0 {
		next = strconv.FormatInt(events[len(events)-1].Cursor, 10)
	}
	events = redactEventsForIdentity(identity, events)
	s.writeCollection(w, events, next)
}

// redactEventsForIdentity prevents an events:read-only bearer token from
// using task.progressed events as a side channel for task narrative. The
// progress event intentionally retains machine-readable state and checkpoint
// counts so polling clients can drive coordination without tasks:read.
func redactEventsForIdentity(identity auth.Identity, events []store.Event) []store.Event {
	if !identity.IsToken || identity.HasScope("tasks:read") {
		return events
	}
	result := make([]store.Event, len(events))
	copy(result, events)
	for i := range result {
		if result[i].Type != "task.progressed" || len(result[i].Payload) == 0 {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(result[i].Payload, &payload); err != nil || payload == nil {
			continue
		}
		if !redactAgentWorkNarrative(payload) {
			continue
		}
		if encoded, err := json.Marshal(payload); err == nil {
			result[i].Payload = encoded
		}
	}
	return result
}

func redactAgentWorkNarrative(payload map[string]any) bool {
	changed := false
	for _, field := range []string{"summary", "phase", "next_action", "checkpoint_refs"} {
		if _, ok := payload[field]; ok {
			delete(payload, field)
			changed = true
		}
	}
	if nested, ok := payload["agent_work"].(map[string]any); ok {
		changed = redactAgentWorkNarrative(nested) || changed
	}
	return changed
}
