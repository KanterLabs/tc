package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/KanterLabs/helm/internal/auth"
	"github.com/KanterLabs/helm/internal/store"
)

func (s *Server) tasks(w http.ResponseWriter, r *http.Request, identity auth.Identity, reference string) {
	if r.Method == http.MethodPost {
		if !requireScope(w, identity, "tasks:write") {
			return
		}
		if s.idempotencyReplay(w, r, identity) {
			return
		}
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
	switch r.Method {
	case http.MethodGet:
		if !requireScope(w, identity, "tasks:read") {
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
		columnFilter, err := parseOptionalIdentifier(r, "column")
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
		assignee, err := parseOptionalIdentifier(r, "assignee")
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
			return
		}
		kind, err := parseOptionalEnum(r, "kind", taskKinds)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
			return
		}
		severity, err := parseOptionalEnum(r, "severity", bugSeverityFilters)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
			return
		}
		reporter, err := parseOptionalIdentifier(r, "reporter")
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
			return
		}
		resolution, err := parseOptionalEnum(r, "resolution", bugResolutionFilters)
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
		dependency, err := parseOptionalEnum(r, "dependency", dependencyFilters)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
			return
		}
		filter := store.TaskFilter{State: state, Column: columnFilter, Priority: priority, Label: label, Assignee: assignee, Kind: kind, Severity: severity, Reporter: reporter, Resolution: resolution, Dependency: dependency, AgentState: agentState, ActionNeeded: actionNeeded, Query: query, Cursor: offset, Limit: limit, UpdatedAfter: updatedAfter}
		tasks, more, err := s.Store.ListTasksWithExtra(r.Context(), project.ID, filter)
		if err != nil {
			s.writeInternal(w, err)
			return
		}
		next := ""
		if more && len(tasks) > 0 {
			// Task numbers do not match the board ordering; the store interprets
			// this opaque cursor as the next row offset.
			next = encodeCursor(filter.Cursor + len(tasks))
		}
		s.writeCollection(w, tasks, next)
	case http.MethodPost:
		input, err := decodeTaskInput(r, true)
		if err != nil {
			writeTaskInputError(w, err)
			return
		}
		s.mutation(w, r, identity, func() (int, []byte, string, error) {
			task, err := s.Store.CreateTask(r.Context(), project.ID, input, identity.Actor.ID)
			if err != nil {
				return 0, nil, "", err
			}
			w.Header().Set("Location", "/api/v1/tasks/"+task.ID)
			body, err := marshalTaskForIdentity(identity, task)
			if err != nil {
				return 0, nil, "", err
			}
			return http.StatusCreated, body, taskETag(task), nil
		})
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
	}
}

func (s *Server) task(w http.ResponseWriter, r *http.Request, identity auth.Identity, id string) {
	var version int64
	if r.Method == http.MethodPatch || r.Method == http.MethodDelete {
		if !requireScope(w, identity, "tasks:write") {
			return
		}
		var err error
		version, err = parseVersion(r)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		if s.idempotencyReplay(w, r, identity) {
			return
		}
	}
	task, err := s.Store.ResolveTaskReference(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	id = task.ID
	if !identity.CanProject(task.ProjectID) {
		s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to this project", nil)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !requireScope(w, identity, "tasks:read") {
			return
		}
		s.writeTask(w, http.StatusOK, task)
	case http.MethodPatch:
		input, err := decodeTaskInput(r, false)
		if err != nil {
			writeTaskInputError(w, err)
			return
		}
		s.mutation(w, r, identity, func() (int, []byte, string, error) {
			updated, err := s.Store.UpdateTaskWithClaimOverride(r.Context(), id, input, version, identity.Actor.ID, !identity.IsToken && identity.Actor.Admin)
			if err != nil {
				return 0, nil, "", err
			}
			body, err := marshalTaskForIdentity(identity, updated)
			if err != nil {
				return 0, nil, "", err
			}
			return http.StatusOK, body, taskETag(updated), nil
		})
	case http.MethodDelete:
		s.mutation(w, r, identity, func() (int, []byte, string, error) {
			if err := s.Store.DeleteTaskWithClaimOverride(r.Context(), id, version, identity.Actor.ID, !identity.IsToken && identity.Actor.Admin); err != nil {
				return 0, nil, "", err
			}
			return http.StatusNoContent, nil, "", nil
		})
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
	}
}

func taskETag(task store.Task) string { return `"v` + strconv.FormatInt(task.Version, 10) + `"` }

// marshalTaskForIdentity keeps successful task mutations least-privilege. A
// bearer credential that can mutate or claim work but cannot read tasks gets
// only the immutable identity and new version; the ETag still carries the
// complete optimistic-concurrency validator. Humans and bearer credentials
// with tasks:read retain the normal full task response.
func marshalTaskForIdentity(identity auth.Identity, task store.Task) ([]byte, error) {
	if identity.IsToken && !identity.HasScope("tasks:read") {
		return json.Marshal(map[string]any{"id": task.ID, "version": task.Version})
	}
	return json.Marshal(task)
}

func (s *Server) writeTask(w http.ResponseWriter, status int, task store.Task) {
	body, err := json.Marshal(task)
	if err != nil {
		s.writeInternal(w, err)
		return
	}
	s.writeRaw(w, status, body, taskETag(task))
}

func decodeTaskInput(r *http.Request, creating bool) (store.TaskInput, error) {
	var payload map[string]json.RawMessage
	var typed map[string]json.RawMessage
	fields, err := decodeJSONObject(r, &typed)
	if err != nil {
		return store.TaskInput{}, err
	}
	payload = fields
	for name := range payload {
		switch name {
		case "title", "description", "priority", "kind", "bug", "column_id", "column", "position", "assignee", "assignee_id", "due_at", "labels", "label_ids":
		default:
			return store.TaskInput{}, taskInputError("unknown task field: " + name)
		}
	}
	if creating {
		if err := requireJSONFields(payload, "title"); err != nil {
			return store.TaskInput{}, err
		}
	}

	// A PATCH with no recognized fields would otherwise increment the task
	// version while making no change. Unknown-only payloads are also rejected so
	// clients cannot accidentally believe an ignored field was applied.
	if !creating {
		recognized := false
		for _, name := range []string{"title", "description", "priority", "kind", "bug", "column_id", "column", "position", "assignee", "assignee_id", "due_at", "labels", "label_ids"} {
			if _, ok := payload[name]; ok {
				recognized = true
				break
			}
		}
		if !recognized {
			return store.TaskInput{}, taskInputError("patch must include at least one task field")
		}
	}

	var input store.TaskInput
	if raw, ok := preferredTaskField(payload, "title"); ok {
		value, err := parseTaskString(raw, "title", false)
		if err != nil {
			return store.TaskInput{}, err
		}
		if value == nil || strings.TrimSpace(*value) == "" {
			return store.TaskInput{}, taskInputError("title must not be empty")
		}
		input.Title = value
	}
	if raw, ok := preferredTaskField(payload, "description"); ok {
		if isJSONNull(raw) {
			if creating {
				return store.TaskInput{}, taskInputError("description cannot be null")
			}
			empty := ""
			input.Description = &empty
		} else {
			value, err := parseTaskString(raw, "description", false)
			if err != nil {
				return store.TaskInput{}, err
			}
			input.Description = value
		}
	}
	if raw, ok := preferredTaskField(payload, "priority"); ok {
		value, err := parseTaskString(raw, "priority", false)
		if err != nil {
			return store.TaskInput{}, err
		}
		if value == nil || strings.TrimSpace(*value) == "" {
			return store.TaskInput{}, taskInputError("priority must not be empty")
		}
		input.Priority = value
	}
	if raw, ok := preferredTaskField(payload, "kind"); ok {
		value, err := parseTaskString(raw, "kind", false)
		if err != nil {
			return store.TaskInput{}, err
		}
		if value == nil || strings.TrimSpace(*value) == "" {
			return store.TaskInput{}, taskInputError("kind must not be empty")
		}
		input.Kind = value
	}
	if raw, ok := preferredTaskField(payload, "bug"); ok {
		input.BugSet = true
		if isJSONNull(raw) {
			input.Bug = nil
		} else {
			bug, err := decodeBugInput(raw)
			if err != nil {
				return store.TaskInput{}, err
			}
			input.Bug = &bug
		}
	}
	if raw, ok := preferredTaskField(payload, "column_id"); ok {
		value, err := parseTaskString(raw, "column_id", false)
		if err != nil {
			return store.TaskInput{}, err
		}
		if value == nil || strings.TrimSpace(*value) == "" {
			return store.TaskInput{}, taskInputError("column_id must not be empty")
		}
		input.ColumnID = value
	} else if raw, ok := preferredTaskField(payload, "column"); ok {
		value, err := parseTaskString(raw, "column", false)
		if err != nil {
			return store.TaskInput{}, err
		}
		if value == nil || strings.TrimSpace(*value) == "" {
			return store.TaskInput{}, taskInputError("column must not be empty")
		}
		input.ColumnID = value
	}
	if raw, ok := preferredTaskField(payload, "position"); ok {
		if isJSONNull(raw) {
			return store.TaskInput{}, taskInputError("position cannot be null")
		}
		var value float64
		if err := json.Unmarshal(raw, &value); err != nil {
			return store.TaskInput{}, err
		}
		input.Position = &value
	}
	if raw, ok := preferredTaskField(payload, "assignee"); ok {
		value, err := parseTaskString(raw, "assignee", true)
		if err != nil {
			return store.TaskInput{}, err
		}
		if value != nil && strings.TrimSpace(*value) == "" {
			return store.TaskInput{}, taskInputError("assignee must not be empty")
		}
		input.Assignee, input.AssigneeSet = value, true
	} else if raw, ok := preferredTaskField(payload, "assignee_id"); ok {
		value, err := parseTaskString(raw, "assignee_id", true)
		if err != nil {
			return store.TaskInput{}, err
		}
		if value != nil && strings.TrimSpace(*value) == "" {
			return store.TaskInput{}, taskInputError("assignee_id must not be empty")
		}
		input.Assignee, input.AssigneeSet = value, true
	}
	if raw, ok := preferredTaskField(payload, "due_at"); ok {
		value, err := parseTaskString(raw, "due_at", true)
		if err != nil {
			return store.TaskInput{}, err
		}
		if value != nil {
			if strings.TrimSpace(*value) == "" {
				return store.TaskInput{}, taskInputError("due_at must be RFC3339 or null")
			}
			if _, err := time.Parse(time.RFC3339, *value); err != nil {
				return store.TaskInput{}, taskInputError("due_at must be RFC3339 or null")
			}
		}
		input.DueAt, input.DueAtSet = value, true
	}
	if raw, ok := preferredTaskField(payload, "labels"); ok {
		if err := validateIdentifierArray(raw, "labels", true); err != nil {
			return store.TaskInput{}, err
		}
		if isJSONNull(raw) {
			input.Labels, input.LabelsSet = []string{}, true
		} else {
			if err := json.Unmarshal(raw, &input.Labels); err != nil {
				return store.TaskInput{}, err
			}
			input.LabelsSet = true
		}
	} else if raw, ok := preferredTaskField(payload, "label_ids"); ok {
		if err := validateIdentifierArray(raw, "label_ids", true); err != nil {
			return store.TaskInput{}, err
		}
		if isJSONNull(raw) {
			input.Labels, input.LabelsSet = []string{}, true
		} else {
			if err := json.Unmarshal(raw, &input.Labels); err != nil {
				return store.TaskInput{}, err
			}
			input.LabelsSet = true
		}
	}
	return input, nil
}

func decodeBugInput(raw json.RawMessage) (store.BugInput, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil || payload == nil {
		return store.BugInput{}, taskInputError("bug must be an object")
	}
	for name := range payload {
		switch name {
		case "severity", "actual_behavior", "expected_behavior", "reproduction_steps", "environment", "affected_version":
		default:
			return store.BugInput{}, taskInputError("unknown bug field: " + name)
		}
	}
	var input store.BugInput
	if value, ok := payload["severity"]; ok {
		input.SeveritySet = true
		if !isJSONNull(value) {
			parsed, err := parseTaskString(value, "severity", false)
			if err != nil {
				return store.BugInput{}, err
			}
			input.Severity = parsed
		}
	}
	if value, ok := payload["actual_behavior"]; ok {
		if isJSONNull(value) {
			return store.BugInput{}, taskInputError("actual_behavior cannot be null")
		}
		parsed, err := parseTaskString(value, "actual_behavior", false)
		if err != nil {
			return store.BugInput{}, err
		}
		input.ActualBehavior, input.ActualBehaviorSet = parsed, true
	}
	for name, target := range map[string]struct {
		value **string
		set   *bool
	}{
		"expected_behavior":  {&input.ExpectedBehavior, &input.ExpectedBehaviorSet},
		"reproduction_steps": {&input.ReproductionSteps, &input.ReproductionStepsSet},
		"environment":        {&input.Environment, &input.EnvironmentSet},
		"affected_version":   {&input.AffectedVersion, &input.AffectedVersionSet},
	} {
		value, ok := payload[name]
		if !ok {
			continue
		}
		*target.set = true
		if isJSONNull(value) {
			continue
		}
		parsed, err := parseTaskString(value, name, false)
		if err != nil {
			return store.BugInput{}, err
		}
		*target.value = parsed
	}
	return input, nil
}

func preferredTaskField(payload map[string]json.RawMessage, names ...string) (json.RawMessage, bool) {
	for _, name := range names {
		if value, ok := payload[name]; ok {
			return value, true
		}
	}
	return nil, false
}

func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

func parseTaskString(raw json.RawMessage, field string, nullable bool) (*string, error) {
	if isJSONNull(raw) {
		if nullable {
			return nil, nil
		}
		return nil, taskInputError(field + " cannot be null")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return &value, nil
}

func taskInputError(message string) error {
	return &store.Error{Kind: store.ErrInvalid, Message: message}
}

func writeTaskInputError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrInvalid) {
		sDummyWriteError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	sDummyWriteError(w, http.StatusBadRequest, "invalid_json", "request body is invalid", nil)
}

func (s *Server) comments(w http.ResponseWriter, r *http.Request, identity auth.Identity, taskID string) {
	if r.Method == http.MethodPost {
		if !requireScope(w, identity, "tasks:write") {
			return
		}
		if s.idempotencyReplay(w, r, identity) {
			return
		}
	}
	task, err := s.Store.ResolveTaskReference(r.Context(), taskID)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	taskID = task.ID
	if !identity.CanProject(task.ProjectID) {
		s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to this project", nil)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !requireScope(w, identity, "tasks:read") {
			return
		}
		limit, offset, paginationErr := parsePagination(r, 50)
		if paginationErr != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_request", paginationErr.Error(), nil)
			return
		}
		comments, more, err := s.Store.ListCommentsPage(r.Context(), taskID, limit, offset)
		if err != nil {
			s.writeInternal(w, err)
			return
		}
		next := ""
		if more {
			next = encodeCursor(offset + len(comments))
		}
		s.writeCollection(w, comments, next)
	case http.MethodPost:
		var payload struct {
			Body *string `json:"body"`
			Text *string `json:"text"`
		}
		fields, err := decodeJSONObject(r, &payload)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_json", "request body is invalid", nil)
			return
		}
		if !jsonFieldPresent(fields, "body") && !jsonFieldPresent(fields, "text") {
			s.writeStoreError(w, taskInputError("comment body is required"))
			return
		}
		body := ""
		if payload.Body != nil {
			body = strings.TrimSpace(*payload.Body)
		}
		if body == "" && payload.Text != nil {
			body = strings.TrimSpace(*payload.Text)
		}
		if body == "" {
			s.writeStoreError(w, taskInputError("comment body is required"))
			return
		}
		if len(body) > 20000 {
			s.writeStoreError(w, taskInputError("comment is too long"))
			return
		}
		s.mutation(w, r, identity, func() (int, []byte, string, error) {
			comment, err := s.Store.CreateComment(r.Context(), taskID, identity.Actor.ID, body)
			if err != nil {
				return 0, nil, "", err
			}
			body, _ := json.Marshal(comment)
			return http.StatusCreated, body, "", nil
		})
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
	}
}

func (s *Server) taskAction(w http.ResponseWriter, r *http.Request, identity auth.Identity, id, action string) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return
	}
	if !requireScope(w, identity, "tasks:claim") {
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
	current, err := s.Store.ResolveTaskReference(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	id = current.ID
	if !identity.CanProject(current.ProjectID) {
		s.writeError(w, http.StatusForbidden, "forbidden", "token is not scoped to this project", nil)
		return
	}
	if current.ClaimedBy != nil && *current.ClaimedBy != identity.Actor.ID && !identity.Actor.Admin && action != "claim" && activeTaskClaim(current) {
		s.writeError(w, http.StatusForbidden, "forbidden", "only the claim owner may perform this action", nil)
		return
	}
	var payload struct {
		LeaseSeconds    *int64  `json:"lease_seconds"`
		DurationSeconds *int64  `json:"duration_seconds"`
		Comment         *string `json:"comment"`
		Reason          *string `json:"reason"`
	}
	if r.Body != nil && len(bodyBytes(r)) > 0 {
		fields, err := decodeJSONObject(r, &payload)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_json", "request body is invalid", nil)
			return
		}
		if err := validateTaskActionFields(fields, action); err != nil {
			writeTaskInputError(w, err)
			return
		}
	}
	duration := time.Duration(0)
	if action == "claim" || action == "renew" {
		seconds, supplied := payload.LeaseSeconds, payload.LeaseSeconds != nil
		if !supplied {
			seconds, supplied = payload.DurationSeconds, payload.DurationSeconds != nil
		}
		if supplied {
			const maxLeaseSeconds = int64(store.MaxTaskClaimDuration / time.Second)
			const minLeaseSeconds = int64(store.MinTaskClaimDuration / time.Second)
			if *seconds < minLeaseSeconds || *seconds > maxLeaseSeconds {
				writeTaskInputError(w, taskInputError("lease duration must be between 30 and 604800 seconds"))
				return
			}
			duration = time.Duration(*seconds) * time.Second
		}
	}
	s.mutation(w, r, identity, func() (int, []byte, string, error) {
		var task store.Task
		var err error
		switch action {
		case "claim":
			task, err = s.Store.ClaimTask(r.Context(), id, identity.Actor.ID, duration, version)
		case "renew":
			if identity.IsToken {
				task, err = s.Store.RenewTaskWithClaim(r.Context(), id, identity.Actor.ID, duration, version)
			} else {
				task, err = s.Store.RenewTask(r.Context(), id, identity.Actor.ID, duration, version)
			}
		case "release":
			if identity.IsToken {
				task, err = s.Store.ReleaseTaskWithClaim(r.Context(), id, identity.Actor.ID, version)
			} else {
				task, err = s.Store.ReleaseTask(r.Context(), id, identity.Actor.ID, version)
			}
		case "complete":
			comment := ""
			if payload.Comment != nil {
				comment = *payload.Comment
			}
			if identity.IsToken {
				task, err = s.Store.CompleteTaskWithCommentWithClaim(r.Context(), id, identity.Actor.ID, version, comment)
			} else {
				task, err = s.Store.CompleteTaskWithComment(r.Context(), id, identity.Actor.ID, version, comment)
			}
		case "block":
			reason := ""
			if payload.Reason != nil {
				reason = *payload.Reason
			}
			if identity.IsToken {
				task, err = s.Store.BlockTaskWithReasonWithClaim(r.Context(), id, identity.Actor.ID, version, reason)
			} else {
				task, err = s.Store.BlockTaskWithReason(r.Context(), id, identity.Actor.ID, version, reason)
			}
		}
		if err != nil {
			return 0, nil, "", err
		}
		body, err := marshalTaskForIdentity(identity, task)
		if err != nil {
			return 0, nil, "", err
		}
		return http.StatusOK, body, taskETag(task), nil
	})
}

func validateTaskActionFields(payload map[string]json.RawMessage, action string) error {
	for name := range payload {
		allowed := false
		switch action {
		case "claim", "renew":
			allowed = name == "lease_seconds" || name == "duration_seconds"
		case "release":
			allowed = false
		case "complete":
			allowed = name == "comment"
		case "block":
			allowed = name == "reason"
		}
		if !allowed {
			return taskInputError("request body contains fields unsupported by this action")
		}
		if isJSONNull(payload[name]) {
			return taskInputError(name + " cannot be null")
		}
	}
	return nil
}

func activeTaskClaim(task store.Task) bool {
	if task.ClaimedBy == nil || task.ClaimExpiresAt == nil {
		return false
	}
	expires, err := time.Parse(time.RFC3339Nano, *task.ClaimExpiresAt)
	return err == nil && expires.After(time.Now().UTC())
}
