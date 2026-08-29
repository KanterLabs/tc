package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"roadmap/internal/auth"
	"roadmap/internal/store"
)

// issues is the agent-friendly, cross-project discovery endpoint. It reuses
// the task authorization ceiling and cursor contract while returning only bug
// tasks, so callers do not have to enumerate every project first.
func (s *Server) issues(w http.ResponseWriter, r *http.Request, identity auth.Identity) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return
	}
	if !requireScope(w, identity, "tasks:read") {
		return
	}

	limit, offset, err := parsePagination(r, 50)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	filter := store.TaskFilter{Kind: "bug", Cursor: offset, Limit: limit}
	if identity.IsToken && identity.Token.ProjectsScoped {
		filter.ProjectIDs = scopedProjectIDs(identity)
	}
	if reference, parseErr := parseOptionalIdentifier(r, "project"); parseErr != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", parseErr.Error(), nil)
		return
	} else if reference != "" {
		project, storeErr := s.Store.GetProject(r.Context(), reference)
		if storeErr != nil {
			s.writeStoreError(w, storeErr)
			return
		}
		if !identity.CanProject(project.ID) {
			s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to this project", nil)
			return
		}
		filter.ProjectIDs = []string{project.ID}
	}
	if filter.State, err = parseOptionalEnum(r, "state", semanticStates); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	if filter.Column, err = parseOptionalIdentifier(r, "column"); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	if filter.Priority, err = parseOptionalEnum(r, "priority", taskPriorities); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	if filter.Severity, err = parseOptionalEnum(r, "severity", bugSeverityFilters); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	if filter.Label, err = parseOptionalIdentifier(r, "label"); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	if filter.Assignee, err = parseOptionalIdentifier(r, "assignee"); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	if filter.Reporter, err = parseOptionalIdentifier(r, "reporter"); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	if filter.Resolution, err = parseOptionalEnum(r, "resolution", bugResolutionFilters); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	if filter.AgentState, err = parseOptionalEnum(r, "agent_state", agentWorkStates); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	if filter.ActionNeeded, err = parseOptionalStrictBool(r, "action_needed"); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	if filter.Query, err = parseOptionalSearch(r); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	if filter.UpdatedAfter, err = parseOptionalTimestamp(r, "updated_after"); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}

	issues, more, err := s.Store.ListIssuesWithExtra(r.Context(), filter)
	if err != nil {
		s.writeInternal(w, err)
		return
	}
	next := ""
	if more && len(issues) > 0 {
		next = encodeCursor(filter.Cursor + len(issues))
	}
	s.writeCollection(w, issues, next)
}

func (s *Server) issueAction(w http.ResponseWriter, r *http.Request, identity auth.Identity, reference, action string) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return
	}
	if !requireScope(w, identity, "tasks:write") {
		return
	}
	version, err := parseVersion(r)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if s.idempotencyReplay(w, r, identity) {
		return
	}
	current, err := s.Store.ResolveTaskReference(r.Context(), reference)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if !identity.CanProject(current.ProjectID) {
		s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to this project", nil)
		return
	}

	fields, err := decodeIssueActionBody(r)
	if err != nil {
		writeTaskInputError(w, err)
		return
	}
	allowClaimOverride := !identity.IsToken && identity.Actor.Admin
	s.mutation(w, r, identity, func() (int, []byte, string, error) {
		var updated store.Task
		var mutationErr error
		switch action {
		case "triage":
			input, parseErr := parseTriageBugInput(fields)
			if parseErr != nil {
				return 0, nil, "", parseErr
			}
			updated, mutationErr = s.Store.TriageBugWithClaimOverride(r.Context(), current.ID, input, version, identity.Actor.ID, allowClaimOverride)
		case "resolve":
			input, parseErr := parseResolveBugInput(fields)
			if parseErr != nil {
				return 0, nil, "", parseErr
			}
			updated, mutationErr = s.Store.ResolveBugWithClaimOverride(r.Context(), current.ID, input, version, identity.Actor.ID, allowClaimOverride)
		case "reopen":
			reason, parseErr := parseReopenBugReason(fields)
			if parseErr != nil {
				return 0, nil, "", parseErr
			}
			updated, mutationErr = s.Store.ReopenBugWithClaimOverride(r.Context(), current.ID, reason, version, identity.Actor.ID, allowClaimOverride)
		default:
			mutationErr = taskInputError("unsupported issue action")
		}
		if mutationErr != nil {
			return 0, nil, "", mutationErr
		}
		body, marshalErr := marshalTaskForIdentity(identity, updated)
		if marshalErr != nil {
			return 0, nil, "", marshalErr
		}
		return http.StatusOK, body, taskETag(updated), nil
	})
}

func decodeIssueActionBody(r *http.Request) (map[string]json.RawMessage, error) {
	var payload map[string]json.RawMessage
	fields, err := decodeJSONObject(r, &payload)
	if err != nil {
		return nil, err
	}
	return fields, nil
}

func parseTriageBugInput(payload map[string]json.RawMessage) (store.TriageBugInput, error) {
	for name := range payload {
		switch name {
		case "severity", "priority", "assignee", "assignee_id", "column_id", "column":
		default:
			return store.TriageBugInput{}, taskInputError("request body contains fields unsupported by triage")
		}
	}
	if err := requireJSONFields(payload, "severity"); err != nil {
		return store.TriageBugInput{}, err
	}
	severity, err := parseTaskString(payload["severity"], "severity", false)
	if err != nil || severity == nil || strings.TrimSpace(*severity) == "" {
		if err != nil {
			return store.TriageBugInput{}, err
		}
		return store.TriageBugInput{}, taskInputError("severity must not be empty")
	}
	input := store.TriageBugInput{Severity: severity, SeveritySet: true}
	if raw, ok := payload["priority"]; ok {
		value, parseErr := parseTaskString(raw, "priority", false)
		if parseErr != nil {
			return store.TriageBugInput{}, parseErr
		}
		input.Priority = value
	}
	if raw, ok := preferredTaskField(payload, "assignee", "assignee_id"); ok {
		value, parseErr := parseTaskString(raw, "assignee", true)
		if parseErr != nil {
			return store.TriageBugInput{}, parseErr
		}
		if value != nil && strings.TrimSpace(*value) == "" {
			return store.TriageBugInput{}, taskInputError("assignee must not be empty")
		}
		input.Assignee, input.AssigneeSet = value, true
	}
	if raw, ok := preferredTaskField(payload, "column_id", "column"); ok {
		value, parseErr := parseTaskString(raw, "column_id", false)
		if parseErr != nil {
			return store.TriageBugInput{}, parseErr
		}
		if value == nil || strings.TrimSpace(*value) == "" {
			return store.TriageBugInput{}, taskInputError("column_id must not be empty")
		}
		input.ColumnID = value
	}
	return input, nil
}

func parseResolveBugInput(payload map[string]json.RawMessage) (store.ResolveBugInput, error) {
	for name := range payload {
		if name != "resolution" && name != "duplicate_of" && name != "note" {
			return store.ResolveBugInput{}, taskInputError("request body contains fields unsupported by resolve")
		}
	}
	if err := requireJSONFields(payload, "resolution"); err != nil {
		return store.ResolveBugInput{}, err
	}
	resolution, err := parseTaskString(payload["resolution"], "resolution", false)
	if err != nil || resolution == nil || strings.TrimSpace(*resolution) == "" {
		if err != nil {
			return store.ResolveBugInput{}, err
		}
		return store.ResolveBugInput{}, taskInputError("resolution must not be empty")
	}
	input := store.ResolveBugInput{Resolution: *resolution}
	if raw, ok := payload["duplicate_of"]; ok {
		value, parseErr := parseTaskString(raw, "duplicate_of", true)
		if parseErr != nil {
			return store.ResolveBugInput{}, parseErr
		}
		if value != nil && strings.TrimSpace(*value) == "" {
			return store.ResolveBugInput{}, taskInputError("duplicate_of must not be empty")
		}
		input.DuplicateOf, input.DuplicateOfSet = value, true
	}
	if raw, ok := payload["note"]; ok {
		value, parseErr := parseTaskString(raw, "note", false)
		if parseErr != nil {
			return store.ResolveBugInput{}, parseErr
		}
		input.Note = strings.TrimSpace(*value)
	}
	return input, nil
}

func parseReopenBugReason(payload map[string]json.RawMessage) (string, error) {
	for name := range payload {
		if name != "reason" {
			return "", taskInputError("request body contains fields unsupported by reopen")
		}
	}
	if err := requireJSONFields(payload, "reason"); err != nil {
		return "", err
	}
	reason, err := parseTaskString(payload["reason"], "reason", false)
	if err != nil {
		return "", err
	}
	if reason == nil || strings.TrimSpace(*reason) == "" {
		return "", taskInputError("reason must not be empty")
	}
	return strings.TrimSpace(*reason), nil
}
