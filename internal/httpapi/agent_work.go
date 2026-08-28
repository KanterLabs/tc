package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"roadmap/internal/auth"
	"roadmap/internal/store"
)

// agentWorkStates is the public state vocabulary for an agent's current
// progress snapshot. Keep this separate from semanticStates: board columns
// describe task lifecycle, while agent work describes coordination state.
var agentWorkStates = map[string]struct{}{
	"working":   {},
	"waiting":   {},
	"verifying": {},
	"handoff":   {},
	"stale":     {},
	"missing":   {},
}

var publishAgentWorkStates = map[string]struct{}{
	"working":   {},
	"waiting":   {},
	"verifying": {},
	"handoff":   {},
}

// agentWorkRequest uses pointers for scalar fields so required fields can be
// distinguished from omitted values after strict raw-field validation. The
// store receives a complete snapshot: omitted optional values are represented
// by their zero value and therefore clear the prior snapshot value.
type agentWorkRequest struct {
	OperationID         *string  `json:"operation_id"`
	State               *string  `json:"state"`
	Phase               *string  `json:"phase"`
	Summary             *string  `json:"summary"`
	NextAction          *string  `json:"next_action"`
	CheckpointRefs      []string `json:"checkpoint_refs"`
	CheckpointCompleted *int     `json:"checkpoint_completed"`
	CheckpointTotal     *int     `json:"checkpoint_total"`
}

func (s *Server) taskProgress(w http.ResponseWriter, r *http.Request, identity auth.Identity, reference string) {
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
	// Replay checks intentionally precede resource lookup and body validation,
	// matching other task mutations. Scope and If-Match remain admission gates
	// even for a replay so callers cannot use an idempotency key to bypass them.
	if s.idempotencyReplay(w, r, identity) {
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
	input, err := decodeAgentWorkInput(r)
	if err != nil {
		writeTaskInputError(w, err)
		return
	}
	s.mutation(w, r, identity, func() (int, []byte, string, error) {
		updated, err := s.Store.PublishAgentWork(r.Context(), task.ID, input, version, identity.Actor.ID)
		if err != nil {
			return 0, nil, "", err
		}
		body, err := marshalTaskForIdentity(identity, updated)
		if err != nil {
			return 0, nil, "", err
		}
		return http.StatusOK, body, taskETag(updated), nil
	})
}

func decodeAgentWorkInput(r *http.Request) (store.AgentWorkInput, error) {
	var payload agentWorkRequest
	var rawPayload map[string]json.RawMessage
	fields, err := decodeJSONObject(r, &rawPayload)
	if err != nil {
		return store.AgentWorkInput{}, err
	}
	body, err := requestBodyData(r)
	if err != nil {
		return store.AgentWorkInput{}, err
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return store.AgentWorkInput{}, err
	}
	for name := range fields {
		switch name {
		case "operation_id", "state", "phase", "summary", "next_action", "checkpoint_refs", "checkpoint_completed", "checkpoint_total":
		default:
			return store.AgentWorkInput{}, taskInputError("unknown agent progress field: " + name)
		}
	}
	if err := requireJSONFields(fields, "operation_id", "state", "summary"); err != nil {
		return store.AgentWorkInput{}, err
	}
	// Every exposed progress field is non-nullable. Optional fields clear by
	// omission in this complete-snapshot endpoint; explicit null is rejected so
	// clients cannot accidentally turn a typed value into an ambiguous zero.
	if err := rejectJSONNull(fields, "operation_id", "state", "phase", "summary", "next_action", "checkpoint_refs", "checkpoint_completed", "checkpoint_total"); err != nil {
		return store.AgentWorkInput{}, err
	}
	if payload.OperationID == nil || strings.TrimSpace(*payload.OperationID) == "" {
		return store.AgentWorkInput{}, taskInputError("operation_id must not be empty")
	}
	operationID := strings.TrimSpace(*payload.OperationID)
	if utf8.RuneCountInString(operationID) > 128 || !safeAgentWorkIdentifier(operationID) {
		return store.AgentWorkInput{}, taskInputError("operation_id must be between 1 and 128 safe identifier characters")
	}
	if payload.State == nil || strings.TrimSpace(*payload.State) == "" {
		return store.AgentWorkInput{}, taskInputError("state must not be empty")
	}
	if _, ok := publishAgentWorkStates[*payload.State]; !ok {
		return store.AgentWorkInput{}, taskInputError("state is invalid")
	}
	if payload.Summary == nil || strings.TrimSpace(*payload.Summary) == "" {
		return store.AgentWorkInput{}, taskInputError("summary must not be empty")
	}
	if utf8.RuneCountInString(*payload.Summary) > 1000 {
		return store.AgentWorkInput{}, taskInputError("summary is too long")
	}
	if payload.Phase != nil {
		if utf8.RuneCountInString(*payload.Phase) > 120 {
			return store.AgentWorkInput{}, taskInputError("phase is too long")
		}
	}
	if payload.NextAction != nil {
		if utf8.RuneCountInString(*payload.NextAction) > 1000 {
			return store.AgentWorkInput{}, taskInputError("next_action is too long")
		}
	}
	if raw, present := fields["checkpoint_refs"]; present {
		if err := validateIdentifierArray(raw, "checkpoint_refs", false); err != nil {
			return store.AgentWorkInput{}, err
		}
		var refs []string
		if err := json.Unmarshal(raw, &refs); err != nil {
			return store.AgentWorkInput{}, err
		}
		if len(refs) > 100 {
			return store.AgentWorkInput{}, taskInputError("checkpoint_refs must contain at most 100 items")
		}
		for _, ref := range refs {
			if utf8.RuneCountInString(strings.TrimSpace(ref)) > 128 {
				return store.AgentWorkInput{}, taskInputError("checkpoint_refs items must be at most 128 characters")
			}
		}
	}
	completedPresent := jsonFieldPresent(fields, "checkpoint_completed")
	totalPresent := jsonFieldPresent(fields, "checkpoint_total")
	if completedPresent != totalPresent {
		return store.AgentWorkInput{}, taskInputError("checkpoint_completed and checkpoint_total must be supplied together")
	}
	if completedPresent {
		if payload.CheckpointCompleted == nil || payload.CheckpointTotal == nil {
			return store.AgentWorkInput{}, taskInputError("checkpoint counts must be integers")
		}
		if *payload.CheckpointCompleted < 0 || *payload.CheckpointTotal < 1 || *payload.CheckpointTotal > 100 {
			return store.AgentWorkInput{}, taskInputError("checkpoint counts are out of range")
		}
		if *payload.CheckpointCompleted > *payload.CheckpointTotal {
			return store.AgentWorkInput{}, taskInputError("checkpoint_completed must not exceed checkpoint_total")
		}
		if len(payload.CheckpointRefs) > 0 && len(payload.CheckpointRefs) != *payload.CheckpointTotal {
			return store.AgentWorkInput{}, taskInputError("checkpoint_refs length must equal checkpoint_total")
		}
	} else if len(payload.CheckpointRefs) > 0 {
		return store.AgentWorkInput{}, taskInputError("checkpoint_total is required when checkpoint_refs is non-empty")
	}

	input := store.AgentWorkInput{
		OperationID: operationID,
		State:       *payload.State,
		Summary:     *payload.Summary,
	}
	if payload.Phase != nil {
		input.Phase = *payload.Phase
	}
	if payload.NextAction != nil {
		input.NextAction = *payload.NextAction
	}
	if payload.CheckpointRefs != nil {
		input.CheckpointRefs = payload.CheckpointRefs
	}
	if completedPresent {
		input.CheckpointCompleted = payload.CheckpointCompleted
		input.CheckpointTotal = payload.CheckpointTotal
	}
	return input, nil
}

func safeAgentWorkIdentifier(value string) bool {
	for index, char := range value {
		if index == 0 && !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9')) {
			return false
		}
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("-_.:/", char) {
			continue
		}
		return false
	}
	return value != ""
}
