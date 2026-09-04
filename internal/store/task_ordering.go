package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	maxTaskOrderingPlacementLength  = 20
	maxTaskOrderingIdentifierLength = maxTaskMoveIdentifierLength
	maxTaskOrderingRevision         = int64(9223372036854775807)
	maxTaskOrderingPosition         = 1e12
	// A generous gap keeps ordinary drag/drop operations from needing a
	// rebalance while remaining well below the existing position cap.
	taskOrderingRebalanceSpacing = 1024.0
)

// TaskReorderInput is an explicit alias for the guarded task movement shape.
// It is exposed separately so application callers can describe intent without
// depending on the legacy append-only operation name.
type TaskReorderInput = TaskMoveInput

// TaskReorderRequest is a descriptive alias for HTTP/application callers.
type TaskReorderRequest = TaskReorderInput

type orderedTask struct {
	ID       string
	Number   int
	Position float64
}

func preciseTaskMove(input TaskMoveInput) bool {
	return strings.TrimSpace(input.BeforeTaskID) != "" || strings.TrimSpace(input.AfterTaskID) != "" || strings.TrimSpace(input.Placement) != "" || input.ExpectedOrderingVersion != 0 || input.ExpectedSourceOrderingVersion != 0 || input.ExpectedDestinationOrderingVersion != 0
}

func normalizeTaskOrderingInput(input TaskMoveInput) (TaskMoveInput, error) {
	input.DestinationColumnID = strings.TrimSpace(input.DestinationColumnID)
	input.ExpectedSourceColumnID = strings.TrimSpace(input.ExpectedSourceColumnID)
	input.Source = strings.TrimSpace(input.Source)
	input.Reason = strings.TrimSpace(input.Reason)
	input.BeforeTaskID = strings.TrimSpace(input.BeforeTaskID)
	input.AfterTaskID = strings.TrimSpace(input.AfterTaskID)
	input.Placement = strings.ToLower(strings.TrimSpace(input.Placement))
	if input.DestinationColumnID == "" {
		return TaskMoveInput{}, invalid("destination_column_id is required", nil)
	}
	if len(input.DestinationColumnID) > maxTaskOrderingIdentifierLength || !utf8.ValidString(input.DestinationColumnID) {
		return TaskMoveInput{}, invalid("destination_column_id is invalid", nil)
	}
	if input.ExpectedSourceColumnID == "" {
		return TaskMoveInput{}, invalid("expected_source_column_id is required", nil)
	}
	if len(input.ExpectedSourceColumnID) > maxTaskOrderingIdentifierLength || !utf8.ValidString(input.ExpectedSourceColumnID) {
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
	for label, value := range map[string]string{"before_task_id": input.BeforeTaskID, "after_task_id": input.AfterTaskID} {
		if value != "" && (len(value) > maxTaskOrderingIdentifierLength || !utf8.ValidString(value)) {
			return TaskMoveInput{}, invalid(label+" is invalid", nil)
		}
	}
	if len(input.Placement) > maxTaskOrderingPlacementLength || !utf8.ValidString(input.Placement) {
		return TaskMoveInput{}, invalid("placement is invalid", nil)
	}
	switch input.Placement {
	case "", "first", "last", "before", "after", "between":
	default:
		return TaskMoveInput{}, invalid("placement must be first, before, between, after, or last", nil)
	}
	if input.BeforeTaskID != "" && input.BeforeTaskID == input.AfterTaskID {
		return TaskMoveInput{}, invalid("before_task_id and after_task_id must differ", nil)
	}
	if input.Placement == "first" || input.Placement == "last" {
		if input.BeforeTaskID != "" || input.AfterTaskID != "" {
			return TaskMoveInput{}, invalid("first and last placement cannot include anchors", nil)
		}
	} else if input.Placement == "before" && (input.BeforeTaskID == "" || input.AfterTaskID != "") {
		return TaskMoveInput{}, invalid("before placement requires only before_task_id", nil)
	} else if input.Placement == "after" && (input.AfterTaskID == "" || input.BeforeTaskID != "") {
		return TaskMoveInput{}, invalid("after placement requires only after_task_id", nil)
	} else if input.Placement == "between" && (input.BeforeTaskID == "" || input.AfterTaskID == "") {
		return TaskMoveInput{}, invalid("between placement requires before_task_id and after_task_id", nil)
	}
	if input.ExpectedOrderingVersion < 0 || input.ExpectedSourceOrderingVersion < 0 || input.ExpectedDestinationOrderingVersion < 0 {
		return TaskMoveInput{}, invalid("ordering version must be non-negative", nil)
	}
	if input.ExpectedOrderingVersion > maxTaskOrderingRevision || input.ExpectedSourceOrderingVersion > maxTaskOrderingRevision || input.ExpectedDestinationOrderingVersion > maxTaskOrderingRevision {
		return TaskMoveInput{}, invalid("ordering version is invalid", nil)
	}
	if input.ExpectedOrderingVersion > 0 {
		if input.ExpectedSourceOrderingVersion == 0 {
			input.ExpectedSourceOrderingVersion = input.ExpectedOrderingVersion
		}
		if input.ExpectedDestinationOrderingVersion == 0 {
			input.ExpectedDestinationOrderingVersion = input.ExpectedOrderingVersion
		}
	}
	return input, nil
}

// ReorderTask places an unfinished task at a precise location. The task ETag
// guards the moved row; optional column revisions guard visible neighbors and
// make filtered-board operations fail safely when another reorder happened in
// the meantime. The operation is one SQLite transaction and emits one
// task.moved activity event with the placement metadata.
func (s *Store) ReorderTask(ctx context.Context, id string, input TaskReorderInput, expected int64, actorID string) (Task, error) {
	return s.reorderTask(ctx, id, input, expected, actorID, false)
}

func (s *Store) reorderTask(ctx context.Context, inputID string, input TaskMoveInput, expected int64, actorID string, strictDestination bool) (Task, error) {
	validated, err := normalizeTaskOrderingInput(input)
	if err != nil {
		return Task{}, err
	}
	if expected <= 0 {
		return Task{}, ErrPrecondition
	}
	id := strings.TrimSpace(inputID)
	if id == "" {
		return Task{}, notFound("task not found")
	}
	actorID = strings.TrimSpace(actorID)
	if actorID == "" {
		return Task{}, invalid("actor is required", nil)
	}

	var current Task
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		// SQLite serializes writers, but acquire the writer lock before reading
		// anchors. Updating revisions in lexical order also keeps the operation
		// deterministic for database drivers that expose multiple connections.
		columnIDs := []string{validated.ExpectedSourceColumnID, validated.DestinationColumnID}
		sort.Strings(columnIDs)
		for index, columnID := range columnIDs {
			if index > 0 && columnID == columnIDs[index-1] {
				continue
			}
			result, lockErr := tx.ExecContext(ctx, `UPDATE columns SET ordering_version=ordering_version WHERE id=?`, columnID)
			if lockErr != nil {
				return lockErr
			}
			changed, _ := result.RowsAffected()
			if changed == 0 {
				return notFound("column not found")
			}
		}

		var readErr error
		current, readErr = taskFromRow(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks t WHERE t.id=? AND t.deleted_at IS NULL`, id))
		if errors.Is(readErr, sql.ErrNoRows) {
			return notFound("task not found")
		}
		if readErr != nil {
			return readErr
		}
		if current.Version != expected || current.ColumnID != validated.ExpectedSourceColumnID {
			return conflict("task has changed", map[string]any{
				"current": current, "expected_version": expected,
				"expected_source_column_id": validated.ExpectedSourceColumnID,
				"destination_column_id":     validated.DestinationColumnID,
			})
		}
		if current.CompletedAt != nil {
			return invalid("finished tasks must use a lifecycle action", map[string]any{"current": current})
		}

		var source, destination Column
		if source, readErr = columnByIDTx(ctx, tx, validated.ExpectedSourceColumnID); readErr != nil {
			return readErr
		}
		if destination, readErr = columnByIDTx(ctx, tx, validated.DestinationColumnID); readErr != nil {
			return readErr
		}
		if source.ProjectID != current.ProjectID || destination.ProjectID != current.ProjectID {
			return invalid("task columns must belong to the same project", map[string]any{"current": current})
		}
		if source.ArchivedAt != nil || destination.ArchivedAt != nil {
			return invalid("task columns must not be archived", map[string]any{"current": current})
		}
		if !taskOrderingStateAllowed(source.SemanticState) {
			return invalid("task source column is not reorderable", map[string]any{"current": current})
		}
		if strictDestination {
			if !taskMoveDestinationStateAllowed(destination.SemanticState) {
				return invalid("destination column must have backlog or ready semantic state", nil)
			}
		} else if !taskOrderingStateAllowed(destination.SemanticState) {
			return invalid("destination column is not reorderable", nil)
		}
		if err := verifyTaskOrderingRevision(validated, source, destination); err != nil {
			return err
		}
		if err := activeTaskClaimConflict(ctx, tx, current, actorID); err != nil {
			return err
		}
		if source.OrderingVersion >= maxTaskOrderingRevision || destination.OrderingVersion >= maxTaskOrderingRevision {
			return invalid("task ordering revision is exhausted", nil)
		}

		items, listErr := listOrderedTasksTx(ctx, tx, current.ProjectID, destination.ID, id)
		if listErr != nil {
			return listErr
		}
		before, after, boundsErr := orderingBounds(items, validated)
		if boundsErr != nil {
			return conflict(boundsErr.Error(), map[string]any{
				"current": current, "before_task_id": validated.BeforeTaskID,
				"after_task_id":    validated.AfterTaskID,
				"ordering_version": destination.OrderingVersion,
			})
		}
		position, rebalanced, positionErr := chooseOrderingPosition(ctx, tx, items, before, after)
		if positionErr != nil {
			return positionErr
		}

		// The guarded update remains the final authority for version, source,
		// lifecycle, and claim state even though the writer lock was acquired
		// before the reads above.
		updatedAt := now()
		result, updateErr := tx.ExecContext(ctx, `UPDATE tasks
			SET column_id=?, position=?,
				claimed_by=CASE WHEN claimed_by IS NOT NULL AND claim_expires_at IS NOT NULL AND julianday(claim_expires_at) IS NOT NULL AND julianday(claim_expires_at)>julianday('now') THEN claimed_by ELSE NULL END,
				claim_expires_at=CASE WHEN claimed_by IS NOT NULL AND claim_expires_at IS NOT NULL AND julianday(claim_expires_at) IS NOT NULL AND julianday(claim_expires_at)>julianday('now') THEN claim_expires_at ELSE NULL END,
				version=version+1, updated_at=?
			WHERE id=? AND version=? AND column_id=? AND deleted_at IS NULL AND completed_at IS NULL AND version < 9223372036854775807
				AND (claimed_by IS NULL OR claim_expires_at IS NULL OR julianday(claim_expires_at) IS NULL OR julianday(claim_expires_at)<=julianday('now'))`,
			validated.DestinationColumnID, position, updatedAt, id, expected, validated.ExpectedSourceColumnID)
		if updateErr != nil {
			return mapDependencyLifecycleError(ctx, tx, updateErr, dependencyLifecycleTarget{TaskID: id})
		}
		changed, _ := result.RowsAffected()
		if changed == 0 {
			return conflict("task reorder could not be applied", map[string]any{"current": current})
		}

		resultingVersion := expected + 1
		if err := tx.QueryRowContext(ctx, `SELECT version FROM tasks WHERE id=?`, id).Scan(&resultingVersion); err != nil {
			return err
		}
		var newPosition float64
		if err := tx.QueryRowContext(ctx, `SELECT position FROM tasks WHERE id=?`, id).Scan(&newPosition); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT ordering_version FROM columns WHERE id=?`, source.ID).Scan(&source.OrderingVersion); err != nil {
			return err
		}
		if source.ID == destination.ID {
			destination.OrderingVersion = source.OrderingVersion
		} else if err := tx.QueryRowContext(ctx, `SELECT ordering_version FROM columns WHERE id=?`, destination.ID).Scan(&destination.OrderingVersion); err != nil {
			return err
		}
		payload := map[string]any{
			"from":           map[string]any{"id": source.ID, "name": source.Name, "semantic_state": source.SemanticState},
			"to":             map[string]any{"id": destination.ID, "name": destination.Name, "semantic_state": destination.SemanticState},
			"from_column_id": source.ID, "to_column_id": destination.ID,
			"from_column": source.Name, "to_column": destination.Name,
			"from_semantic_state": source.SemanticState, "to_semantic_state": destination.SemanticState,
			"from_column_state": source.SemanticState, "to_column_state": destination.SemanticState,
			"old_position": current.Position, "new_position": newPosition,
			"version": resultingVersion, "resulting_version": resultingVersion,
			"actor": actorID, "actor_id": actorID, "source": validated.Source, "reason": validated.Reason,
			"before_task_id": validated.BeforeTaskID, "after_task_id": validated.AfterTaskID,
			"placement": orderingPlacement(validated), "rebalanced": rebalanced,
			"ordering_version":        destination.OrderingVersion,
			"source_ordering_version": source.OrderingVersion,
		}
		_, eventErr := insertEvent(ctx, tx, "task.moved", actorID, current.ProjectID, id, payload)
		return eventErr
	})
	if err != nil {
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrClaimUnavailable) {
			if latest, latestErr := s.GetTask(ctx, id); latestErr == nil {
				attachTaskMoveCurrent(err, latest)
			}
		}
		return Task{}, err
	}
	return s.GetTask(ctx, id)
}

func taskOrderingStateAllowed(state string) bool {
	return state == "backlog" || state == "ready" || state == "active" || state == "blocked"
}

func columnByIDTx(ctx context.Context, tx *sql.Tx, id string) (Column, error) {
	var column Column
	var archived sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT id, project_id, name, semantic_state, position, archived_at, ordering_version, created_at, updated_at, version FROM columns WHERE id=?`, id).Scan(
		&column.ID, &column.ProjectID, &column.Name, &column.SemanticState, &column.Position, &archived, &column.OrderingVersion, &column.CreatedAt, &column.UpdatedAt, &column.Version,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Column{}, notFound("column not found")
	}
	column.ArchivedAt = nullableString(archived)
	return column, err
}

func verifyTaskOrderingRevision(input TaskMoveInput, source, destination Column) error {
	if input.ExpectedSourceOrderingVersion > 0 && source.OrderingVersion != input.ExpectedSourceOrderingVersion {
		return conflict("source column order has changed", map[string]any{"expected_ordering_version": input.ExpectedSourceOrderingVersion, "current_ordering_version": source.OrderingVersion, "column_id": source.ID})
	}
	if input.ExpectedDestinationOrderingVersion > 0 && destination.OrderingVersion != input.ExpectedDestinationOrderingVersion {
		return conflict("destination column order has changed", map[string]any{"expected_ordering_version": input.ExpectedDestinationOrderingVersion, "current_ordering_version": destination.OrderingVersion, "column_id": destination.ID})
	}
	return nil
}

func activeTaskClaimConflict(ctx context.Context, tx *sql.Tx, current Task, actorID string) error {
	if current.ClaimedBy == nil || current.ClaimExpiresAt == nil {
		return nil
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE id=? AND deleted_at IS NULL AND claim_expires_at IS NOT NULL AND julianday(claim_expires_at)>julianday('now'))`, current.ID).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return &Error{Kind: ErrClaimUnavailable, Message: "task has an active claim; release it before reordering", Details: map[string]any{
			"claimed_by": *current.ClaimedBy, "claim_expires_at": *current.ClaimExpiresAt, "owned_by_caller": *current.ClaimedBy == actorID,
		}}
	}
	return nil
}

func listOrderedTasksTx(ctx context.Context, tx *sql.Tx, projectID, columnID, excludingID string) ([]orderedTask, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, number, position FROM tasks WHERE project_id=? AND column_id=? AND id<>? AND deleted_at IS NULL AND completed_at IS NULL ORDER BY position, number, id`, projectID, columnID, excludingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]orderedTask, 0)
	for rows.Next() {
		var item orderedTask
		if err := rows.Scan(&item.ID, &item.Number, &item.Position); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func orderingBounds(items []orderedTask, input TaskMoveInput) (*orderedTask, *orderedTask, error) {
	if len(items) == 0 {
		if input.BeforeTaskID != "" || input.AfterTaskID != "" {
			return nil, nil, errors.New("ordering anchor is not in the destination column")
		}
		return nil, nil, nil
	}
	byID := make(map[string]int, len(items))
	for index, item := range items {
		byID[item.ID] = index
	}
	beforeIndex, beforeFound := byID[input.BeforeTaskID]
	afterIndex, afterFound := byID[input.AfterTaskID]
	if input.BeforeTaskID != "" && !beforeFound || input.AfterTaskID != "" && !afterFound {
		return nil, nil, errors.New("ordering anchor is not in the destination column")
	}
	placement := input.Placement
	if placement == "" {
		switch {
		case beforeFound && afterFound:
			placement = "between"
		case beforeFound:
			placement = "before"
		case afterFound:
			placement = "after"
		default:
			placement = "last"
		}
	}
	switch placement {
	case "first":
		return nil, &items[0], nil
	case "last":
		return &items[len(items)-1], nil, nil
	case "before":
		var lower *orderedTask
		if beforeIndex > 0 {
			lower = &items[beforeIndex-1]
		}
		return lower, &items[beforeIndex], nil
	case "after":
		var upper *orderedTask
		if afterIndex+1 < len(items) {
			upper = &items[afterIndex+1]
		}
		return &items[afterIndex], upper, nil
	case "between":
		if afterIndex >= beforeIndex {
			return nil, nil, errors.New("ordering anchors are reversed")
		}
		return &items[afterIndex], &items[beforeIndex], nil
	default:
		return nil, nil, errors.New("invalid ordering placement")
	}
}

func orderingPlacement(input TaskMoveInput) string {
	if input.Placement != "" {
		return input.Placement
	}
	if input.BeforeTaskID != "" && input.AfterTaskID != "" {
		return "between"
	}
	if input.BeforeTaskID != "" {
		return "before"
	}
	if input.AfterTaskID != "" {
		return "after"
	}
	return "last"
}

func chooseOrderingPosition(ctx context.Context, tx *sql.Tx, items []orderedTask, before, after *orderedTask) (float64, bool, error) {
	position, ok := availableOrderingPosition(items, before, after)
	if ok {
		return position, false, nil
	}
	if err := rebalanceTaskOrdering(ctx, tx, items); err != nil {
		return 0, false, err
	}
	// Re-read the bounds from the rebalanced slice. The pointers still refer
	// to the same backing array, whose positions were updated in place.
	position, ok = availableOrderingPosition(items, before, after)
	if !ok {
		return 0, true, invalid("task ordering could not allocate a stable position", nil)
	}
	return position, true, nil
}

// availableOrderingPosition prefers the midpoint but also looks for an
// unoccupied representable gap. A hidden card may already own the midpoint
// between two visible anchors; choosing the first open sub-gap preserves that
// card's numeric position and relative order without an unnecessary rebalance.
func availableOrderingPosition(items []orderedTask, before, after *orderedTask) (float64, bool) {
	if candidate, ok := midpointOrderingPosition(before, after); ok && !orderingPositionUsed(items, candidate) {
		return candidate, true
	}
	lower := 0.0
	if before != nil {
		lower = before.Position
	}
	upper := maxTaskOrderingPosition
	if after != nil {
		upper = after.Position
	}
	if upper <= lower {
		return 0, false
	}
	for _, item := range items {
		if item.Position <= lower || item.Position >= upper {
			continue
		}
		if candidate, ok := openOrderingGap(lower, item.Position); ok && !orderingPositionUsed(items, candidate) {
			return candidate, true
		}
		lower = item.Position
	}
	return openOrderingGap(lower, upper)
}

func openOrderingGap(lower, upper float64) (float64, bool) {
	candidate := lower + (upper-lower)/2
	if math.IsNaN(candidate) || math.IsInf(candidate, 0) || candidate <= lower || candidate >= upper || candidate < 0 || candidate > maxTaskOrderingPosition {
		return 0, false
	}
	return candidate, true
}

func orderingPositionUsed(items []orderedTask, candidate float64) bool {
	for _, item := range items {
		if item.Position == candidate {
			return true
		}
	}
	return false
}

func midpointOrderingPosition(before, after *orderedTask) (float64, bool) {
	var candidate float64
	switch {
	case before == nil && after == nil:
		candidate = 0
	case before == nil:
		candidate = after.Position - 1
	case after == nil:
		candidate = before.Position + 1
	default:
		candidate = before.Position + (after.Position-before.Position)/2
	}
	if math.IsNaN(candidate) || math.IsInf(candidate, 0) || candidate < 0 || candidate > maxTaskOrderingPosition {
		return 0, false
	}
	if before != nil && !(candidate > before.Position) {
		return 0, false
	}
	if after != nil && !(candidate < after.Position) {
		return 0, false
	}
	return candidate, true
}

func rebalanceTaskOrdering(ctx context.Context, tx *sql.Tx, items []orderedTask) error {
	if len(items) == 0 {
		return nil
	}
	spacing := taskOrderingRebalanceSpacing
	if float64(len(items))*spacing > maxTaskOrderingPosition {
		spacing = maxTaskOrderingPosition / float64(len(items)+1)
	}
	if spacing <= 0 || math.IsInf(spacing, 0) || math.IsNaN(spacing) {
		return invalid("task ordering has too many cards to rebalance", nil)
	}
	for index := range items {
		position := float64(index+1) * spacing
		if position > maxTaskOrderingPosition {
			return invalid("task ordering has too many cards to rebalance", nil)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET position=? WHERE id=? AND deleted_at IS NULL`, position, items[index].ID); err != nil {
			return err
		}
		items[index].Position = position
	}
	return nil
}
