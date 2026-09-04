package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	maxTaskMoveIdentifierLength = 200
	maxTaskMoveSourceLength     = 200
	maxTaskMoveReasonLength     = 10000
)

// TaskMoveInput contains the caller's intent for a raw board move. Position
// is deliberately absent: the store assigns the destination position while
// the guarded UPDATE owns the SQLite writer lock.
//
// ExpectedSourceColumnID is required even when the destination is supplied by
// an opaque column ID. It prevents a stale drag/reconciliation result from
// silently moving a task after another writer has changed its column.
type TaskMoveInput struct {
	DestinationColumnID    string
	ExpectedSourceColumnID string
	Source                 string
	Reason                 string
	// BeforeTaskID and AfterTaskID are optional visible-card anchors. When
	// either is supplied, the move is a precise reorder rather than the
	// legacy append-only move. Supplying both places the task between the two
	// anchors, even when filtered cards occupy the gap.
	BeforeTaskID string
	AfterTaskID  string
	// Placement can be first, last, before, or after. A blank placement is
	// inferred from the supplied anchors; with no anchors it retains the
	// append-only move contract.
	Placement string
	// Column revisions guard neighbors that do not share the task's ETag.
	// ExpectedOrderingVersion is a convenient value for same-column callers;
	// the source/destination-specific fields take precedence when supplied.
	ExpectedOrderingVersion            int64
	ExpectedSourceOrderingVersion      int64
	ExpectedDestinationOrderingVersion int64
}

// TaskMoveRequest is retained as a descriptive alias for HTTP/application
// callers. Both names intentionally describe the same validated shape.
type TaskMoveRequest = TaskMoveInput

func validateTaskMoveInput(input TaskMoveInput) (TaskMoveInput, error) {
	input.DestinationColumnID = strings.TrimSpace(input.DestinationColumnID)
	input.ExpectedSourceColumnID = strings.TrimSpace(input.ExpectedSourceColumnID)
	input.Source = strings.TrimSpace(input.Source)
	input.Reason = strings.TrimSpace(input.Reason)

	if input.DestinationColumnID == "" {
		return TaskMoveInput{}, invalid("destination_column_id is required", nil)
	}
	if len(input.DestinationColumnID) > maxTaskMoveIdentifierLength || !utf8.ValidString(input.DestinationColumnID) {
		return TaskMoveInput{}, invalid("destination_column_id is invalid", nil)
	}
	if input.ExpectedSourceColumnID == "" {
		return TaskMoveInput{}, invalid("expected_source_column_id is required", nil)
	}
	if len(input.ExpectedSourceColumnID) > maxTaskMoveIdentifierLength || !utf8.ValidString(input.ExpectedSourceColumnID) {
		return TaskMoveInput{}, invalid("expected_source_column_id is invalid", nil)
	}
	if input.Source == "" {
		return TaskMoveInput{}, invalid("source is required", nil)
	}
	if len(input.Source) > maxTaskMoveSourceLength || !utf8.ValidString(input.Source) {
		return TaskMoveInput{}, invalid("source is too long", nil)
	}
	if len(input.Reason) > maxTaskMoveReasonLength || !utf8.ValidString(input.Reason) {
		return TaskMoveInput{}, invalid("reason is too long", nil)
	}
	return input, nil
}

// MoveTask applies one guarded, raw move. Only backlog and ready are valid
// destinations. Active is entered through the claim/resume workflow, while
// blocked and completed are lifecycle actions rather than column moves.
//
// Any live claim is rejected in the UPDATE predicate, after SQLite has
// acquired the writer lock. Callers must explicitly release even their own
// claim before a raw move; this prevents claimed work from existing outside
// the active semantic state. Expired or malformed claims are cleared as part
// of this move; this is the only automatic claim cleanup performed here.
func (s *Store) MoveTask(ctx context.Context, id string, input TaskMoveInput, expected int64, actorID string) (Task, error) {
	if preciseTaskMove(input) {
		// Keep the historical /move contract append-only unless an explicit
		// anchor or placement was supplied. This lets audit reconciliation and
		// older callers continue to use the same guarded endpoint unchanged.
		return s.reorderTask(ctx, id, input, expected, actorID, true)
	}
	validated, err := validateTaskMoveInput(input)
	if err != nil {
		return Task{}, err
	}
	if expected <= 0 {
		return Task{}, ErrPrecondition
	}
	if strings.TrimSpace(id) == "" {
		return Task{}, notFound("task not found")
	}
	actorID = strings.TrimSpace(actorID)
	if actorID == "" {
		return Task{}, invalid("actor is required", nil)
	}

	// These reads provide early, useful validation and the safe display values
	// used by the activity payload. The SQL below repeats project/state checks
	// so a race after these reads cannot authorize a move.
	current, err := s.GetTask(ctx, id)
	if err != nil {
		return Task{}, err
	}
	destination, err := s.GetColumn(ctx, validated.DestinationColumnID)
	if err != nil {
		return Task{}, err
	}
	if destination.ProjectID != current.ProjectID {
		return Task{}, invalid("destination column belongs to another project", nil)
	}
	if destination.ArchivedAt != nil {
		return Task{}, invalid("destination column is archived", nil)
	}
	if !taskMoveDestinationStateAllowed(destination.SemanticState) {
		return Task{}, invalid("destination column must have backlog or ready semantic state", nil)
	}

	err = s.withTx(ctx, func(tx *sql.Tx) error {
		// The clock, destination position, claim check, version check, and
		// source-column check all execute in one statement. In particular, do
		// not calculate an expiry decision or destination position in Go before
		// this UPDATE: a writer may have waited for SQLite's lock meanwhile.
		result, updateErr := tx.ExecContext(ctx, `UPDATE tasks
			SET column_id=?,
				position=COALESCE((SELECT MAX(other.position)+1 FROM tasks other WHERE other.column_id=? AND other.deleted_at IS NULL AND other.id<>?), 0),
				claimed_by=CASE WHEN claimed_by IS NOT NULL AND claim_expires_at IS NOT NULL AND julianday(claim_expires_at) IS NOT NULL AND julianday(claim_expires_at)>julianday('now') THEN claimed_by ELSE NULL END,
				claim_expires_at=CASE WHEN claimed_by IS NOT NULL AND claim_expires_at IS NOT NULL AND julianday(claim_expires_at) IS NOT NULL AND julianday(claim_expires_at)>julianday('now') THEN claim_expires_at ELSE NULL END,
				version=version+1,
				updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE id=?
				AND version=?
				AND column_id=?
				AND deleted_at IS NULL
				AND completed_at IS NULL
				AND version < 9223372036854775807
				AND EXISTS (SELECT 1 FROM columns source_column WHERE source_column.id=tasks.column_id AND source_column.project_id=tasks.project_id AND source_column.archived_at IS NULL AND source_column.semantic_state IN ('backlog','ready','active','blocked'))
				AND EXISTS (SELECT 1 FROM columns destination_column WHERE destination_column.id=? AND destination_column.project_id=tasks.project_id AND destination_column.archived_at IS NULL AND destination_column.semantic_state IN ('backlog','ready'))
				AND (claimed_by IS NULL OR claim_expires_at IS NULL OR julianday(claim_expires_at) IS NULL OR julianday(claim_expires_at)<=julianday('now'))`,
			validated.DestinationColumnID,
			validated.DestinationColumnID,
			id,
			id,
			expected,
			validated.ExpectedSourceColumnID,
			validated.DestinationColumnID,
		)
		if updateErr != nil {
			return updateErr
		}
		changed, _ := result.RowsAffected()
		if changed == 0 {
			return s.taskMoveMutationFailure(ctx, tx, id, actorID, expected, validated.ExpectedSourceColumnID, validated.DestinationColumnID)
		}

		var newPosition float64
		var resultingVersion int64
		if err := tx.QueryRowContext(ctx, `SELECT position, version FROM tasks WHERE id=?`, id).Scan(&newPosition, &resultingVersion); err != nil {
			return err
		}
		// Column names and semantic states can change without changing the task
		// version. Read the activity labels only after the guarded UPDATE has
		// acquired SQLite's writer lock so the event cannot describe an older
		// column classification than the move it records.
		var eventSource, eventDestination Column
		if err := tx.QueryRowContext(ctx, `SELECT id, project_id, name, semantic_state, position, ordering_version, created_at, updated_at FROM columns WHERE id=?`, validated.ExpectedSourceColumnID).Scan(
			&eventSource.ID, &eventSource.ProjectID, &eventSource.Name, &eventSource.SemanticState, &eventSource.Position, &eventSource.OrderingVersion, &eventSource.CreatedAt, &eventSource.UpdatedAt,
		); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT id, project_id, name, semantic_state, position, ordering_version, created_at, updated_at FROM columns WHERE id=?`, validated.DestinationColumnID).Scan(
			&eventDestination.ID, &eventDestination.ProjectID, &eventDestination.Name, &eventDestination.SemanticState, &eventDestination.Position, &eventDestination.OrderingVersion, &eventDestination.CreatedAt, &eventDestination.UpdatedAt,
		); err != nil {
			return err
		}

		payload := map[string]any{
			// Nested from/to records are the canonical activity shape. The flat
			// fields preserve the concise shape used by existing event clients.
			"from": map[string]any{
				"id":             eventSource.ID,
				"name":           eventSource.Name,
				"semantic_state": eventSource.SemanticState,
			},
			"to": map[string]any{
				"id":             eventDestination.ID,
				"name":           eventDestination.Name,
				"semantic_state": eventDestination.SemanticState,
			},
			"from_column_id":          eventSource.ID,
			"to_column_id":            eventDestination.ID,
			"from_column":             eventSource.Name,
			"to_column":               eventDestination.Name,
			"from_semantic_state":     eventSource.SemanticState,
			"to_semantic_state":       eventDestination.SemanticState,
			"from_column_state":       eventSource.SemanticState,
			"to_column_state":         eventDestination.SemanticState,
			"old_position":            current.Position,
			"new_position":            newPosition,
			"version":                 resultingVersion,
			"resulting_version":       resultingVersion,
			"actor":                   actorID,
			"actor_id":                actorID,
			"source":                  validated.Source,
			"reason":                  validated.Reason,
			"placement":               "last",
			"rebalanced":              false,
			"ordering_version":        eventDestination.OrderingVersion,
			"source_ordering_version": eventSource.OrderingVersion,
		}
		_, err := insertEvent(ctx, tx, "task.moved", actorID, current.ProjectID, id, payload)
		return err
	})
	if err != nil {
		// GetTask after rollback makes stale/version/source conflicts carry an
		// authoritative enriched snapshot. It is intentionally best effort for
		// low-level failures, while preserving not-found and original errors.
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrClaimUnavailable) {
			if latest, latestErr := s.GetTask(ctx, id); latestErr == nil {
				attachTaskMoveCurrent(err, latest)
			}
		}
		return Task{}, err
	}
	return s.GetTask(ctx, id)
}

// ApplyTaskMove is a descriptive alias for callers that model moves as an
// explicit apply operation. It retains the same guarded semantics as MoveTask.
func (s *Store) ApplyTaskMove(ctx context.Context, id string, input TaskMoveInput, expected int64, actorID string) (Task, error) {
	return s.MoveTask(ctx, id, input, expected, actorID)
}

func taskMoveDestinationStateAllowed(state string) bool {
	return state == "backlog" || state == "ready"
}

// taskMoveMutationFailure classifies a zero-row guarded UPDATE using the row
// visible after the writer statement. The claim query uses SQLite's statement
// clock rather than a timestamp captured before a possible lock wait.
func (s *Store) taskMoveMutationFailure(ctx context.Context, tx *sql.Tx, id, actorID string, expected int64, expectedSourceColumnID, destinationColumnID string) error {
	var claimedBy, claimExpiresAt sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT claimed_by, claim_expires_at FROM tasks WHERE id=? AND deleted_at IS NULL`, id).Scan(&claimedBy, &claimExpiresAt); errors.Is(err, sql.ErrNoRows) {
		return notFound("task not found")
	} else if err != nil {
		return err
	}
	if claimedBy.Valid && claimExpiresAt.Valid {
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE id=? AND claim_expires_at IS NOT NULL AND julianday(claim_expires_at)>julianday('now') AND deleted_at IS NULL)`, id).Scan(&active); err != nil {
			return err
		}
		if active != 0 {
			return &Error{Kind: ErrClaimUnavailable, Message: "task has an active claim; release it before moving", Details: map[string]any{
				"claimed_by":       claimedBy.String,
				"claim_expires_at": claimExpiresAt.String,
				"owned_by_caller":  claimedBy.String == actorID,
			}}
		}
	}

	current, err := taskFromRow(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks t WHERE t.id=? AND t.deleted_at IS NULL`, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return notFound("task not found")
		}
		return err
	}
	if current.Version != expected || current.ColumnID != expectedSourceColumnID {
		return conflict("task has changed", map[string]any{
			"current":                   current,
			"expected_version":          expected,
			"expected_source_column_id": expectedSourceColumnID,
			"destination_column_id":     destinationColumnID,
		})
	}
	var sourceState string
	if err := tx.QueryRowContext(ctx, `SELECT semantic_state FROM columns WHERE id=? AND project_id=?`, current.ColumnID, current.ProjectID).Scan(&sourceState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return invalid("task source column is invalid", map[string]any{"current": current})
		}
		return err
	}
	if sourceState == "completed" || (sourceState != "backlog" && sourceState != "ready" && sourceState != "active" && sourceState != "blocked") {
		return invalid("finished tasks must use a lifecycle action", map[string]any{"current": current})
	}
	if current.CompletedAt != nil {
		return invalid("finished tasks must use a lifecycle action", map[string]any{"current": current})
	}
	return conflict("task move could not be applied", map[string]any{
		"current":                   current,
		"expected_version":          expected,
		"expected_source_column_id": expectedSourceColumnID,
		"destination_column_id":     destinationColumnID,
	})
}

func attachTaskMoveCurrent(err error, current Task) {
	var typed *Error
	if !errors.As(err, &typed) {
		return
	}
	details, ok := typed.Details.(map[string]any)
	if !ok || details == nil {
		details = make(map[string]any)
	}
	details["current"] = current
	typed.Details = details
}
