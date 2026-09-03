package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/KanterLabs/helm/internal/auth"
	"github.com/KanterLabs/helm/internal/store"
)

const (
	maxTaskMoveHTTPIdentifierLength = 200
	maxTaskMoveHTTPSourceLength     = 200
	maxTaskMoveHTTPReasonLength     = 10000
)

// taskMove handles the idempotent raw board-move operation. Route integration
// is intentionally kept in server.go's dispatcher so this handler can be
// reviewed and tested independently: POST /api/v1/tasks/{id}/move.
func (s *Server) taskMove(w http.ResponseWriter, r *http.Request, identity auth.Identity, reference string) {
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
	if strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
		s.writeError(w, http.StatusBadRequest, "idempotency_required", "Idempotency-Key is required for task moves", nil)
		return
	}
	// Replays are deliberately checked before decoding the body or resolving
	// the task. This lets a retry return the original response after a task was
	// changed or deleted, while scope/version/key policy still applies.
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
	input, err := decodeTaskMoveInput(r)
	if err != nil {
		writeTaskInputError(w, err)
		return
	}

	s.mutation(w, r, identity, func() (int, []byte, string, error) {
		moved, moveErr := s.Store.MoveTask(r.Context(), current.ID, input, version, identity.Actor.ID)
		if moveErr != nil {
			return 0, nil, "", moveErr
		}
		body, marshalErr := marshalTaskForIdentity(identity, moved)
		if marshalErr != nil {
			return 0, nil, "", marshalErr
		}
		return http.StatusOK, body, taskETag(moved), nil
	})
}

// decodeTaskMoveInput accepts the canonical destination_column_id and
// expected_source_column_id names plus the column aliases already used by the
// task API. Multiple aliases are rejected when they disagree, preventing a
// request from having two different movement meanings.
func decodeTaskMoveInput(r *http.Request) (store.TaskMoveInput, error) {
	var payload map[string]json.RawMessage
	fields, err := decodeJSONObject(r, &payload)
	if err != nil {
		return store.TaskMoveInput{}, err
	}
	for name := range fields {
		switch name {
		case "destination_column_id", "destination_column", "to_column_id", "to_column", "column_id", "column",
			"expected_source_column_id", "source_column_id", "from_column_id", "expected_column_id", "source_column", "from_column",
			"source", "reason":
		default:
			return store.TaskMoveInput{}, taskInputError("unknown task move field: " + name)
		}
	}

	destination, err := parseTaskMoveIdentifier(fields, "destination column", []string{"destination_column_id", "destination_column", "to_column_id", "to_column", "column_id", "column"}, true)
	if err != nil {
		return store.TaskMoveInput{}, err
	}
	expectedSource, err := parseTaskMoveIdentifier(fields, "expected source column", []string{"expected_source_column_id", "source_column_id", "from_column_id", "expected_column_id", "source_column", "from_column"}, true)
	if err != nil {
		return store.TaskMoveInput{}, err
	}
	source, err := parseTaskMoveMoveText(fields, "source", maxTaskMoveHTTPSourceLength, true)
	if err != nil {
		return store.TaskMoveInput{}, err
	}
	reason, err := parseTaskMoveMoveText(fields, "reason", maxTaskMoveHTTPReasonLength, false)
	if err != nil {
		return store.TaskMoveInput{}, err
	}
	return store.TaskMoveInput{
		DestinationColumnID:    destination,
		ExpectedSourceColumnID: expectedSource,
		Source:                 source,
		Reason:                 reason,
	}, nil
}

func parseTaskMoveIdentifier(fields map[string]json.RawMessage, label string, names []string, required bool) (string, error) {
	value, found, err := parseTaskMoveAliases(fields, label, names)
	if err != nil {
		return "", err
	}
	if !found {
		if required {
			return "", taskInputError(label + " is required")
		}
		return "", nil
	}
	if value == "" {
		return "", taskInputError(label + " must not be empty")
	}
	if utf8.RuneCountInString(value) > maxTaskMoveHTTPIdentifierLength {
		return "", taskInputError(label + " is too long")
	}
	return value, nil
}

func parseTaskMoveAliases(fields map[string]json.RawMessage, label string, names []string) (string, bool, error) {
	value := ""
	found := false
	for _, name := range names {
		raw, present := fields[name]
		if !present {
			continue
		}
		parsed, err := parseTaskString(raw, name, false)
		if err != nil {
			return "", false, err
		}
		parsedValue := strings.TrimSpace(*parsed)
		if found && parsedValue != value {
			return "", false, taskInputError(label + " aliases must agree")
		}
		value, found = parsedValue, true
	}
	return value, found, nil
}

func parseTaskMoveMoveText(fields map[string]json.RawMessage, name string, maxLength int, required bool) (string, error) {
	raw, present := fields[name]
	if !present {
		if required {
			return "", taskInputError(name + " is required")
		}
		return "", nil
	}
	value, err := parseTaskString(raw, name, false)
	if err != nil {
		return "", err
	}
	result := strings.TrimSpace(*value)
	if required && result == "" {
		return "", taskInputError(name + " must not be empty")
	}
	if !required && result == "" {
		return "", taskInputError(name + " must not be empty when supplied")
	}
	if utf8.RuneCountInString(result) > maxLength {
		return "", taskInputError(name + " is too long")
	}
	return result, nil
}
