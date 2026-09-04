package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	// Checklist limits are intentionally small enough for board/detail reads
	// while still allowing a useful acceptance list. They are enforced both in
	// the store and by the migration trigger for direct/retained writers.
	MaxTaskChecklistItems     = 100
	MaxTaskChecklistItemText  = 1000
	MaxTaskChecklistTextBytes = 100000
	MaxTaskChecklistPosition  = MaxTaskChecklistItems
)

func normalizeChecklistItemInput(input ChecklistItemInput, creating bool) (ChecklistItemInput, error) {
	if input.Completed != nil {
		input.CompletedSet = true
	}
	if input.Position != nil {
		input.PositionSet = true
	}
	if input.CompletedSet && input.Completed == nil {
		return ChecklistItemInput{}, invalid("checklist item completed value is required", nil)
	}
	if input.PositionSet && input.Position == nil {
		return ChecklistItemInput{}, invalid("checklist item position is required", nil)
	}
	if creating && input.Text == nil {
		return ChecklistItemInput{}, invalid("checklist item text is required", nil)
	}
	if input.Text != nil {
		value := strings.TrimSpace(*input.Text)
		length := utf8.RuneCountInString(value)
		if length == 0 || length > MaxTaskChecklistItemText {
			return ChecklistItemInput{}, invalid("checklist item text must be between 1 and 1000 characters", nil)
		}
		input.Text = &value
	}
	if input.PositionSet {
		if *input.Position < 0 || *input.Position > MaxTaskChecklistPosition {
			return ChecklistItemInput{}, invalid("checklist item position is invalid", nil)
		}
	}
	if !creating && input.Text == nil && !input.CompletedSet && !input.PositionSet {
		return ChecklistItemInput{}, invalid("checklist item patch must include at least one field", nil)
	}
	return input, nil
}

func normalizeChecklistReorder(input ChecklistReorderInput) (ChecklistReorderInput, error) {
	if len(input.ItemIDs) == 0 {
		return ChecklistReorderInput{}, invalid("checklist order must contain at least one item", nil)
	}
	if len(input.ItemIDs) > MaxTaskChecklistItems {
		return ChecklistReorderInput{}, ErrChecklistLimitExceeded
	}
	seen := make(map[string]struct{}, len(input.ItemIDs))
	for index, itemID := range input.ItemIDs {
		itemID = strings.TrimSpace(itemID)
		if itemID == "" || utf8.RuneCountInString(itemID) > 200 {
			return ChecklistReorderInput{}, invalid("checklist item IDs must be non-empty identifiers", nil)
		}
		if _, exists := seen[itemID]; exists {
			return ChecklistReorderInput{}, invalid("checklist order must not contain duplicate items", nil)
		}
		seen[itemID] = struct{}{}
		input.ItemIDs[index] = itemID
	}
	return input, nil
}

func normalizeChecklistItemID(itemID string) (string, error) {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" || utf8.RuneCountInString(itemID) > 200 {
		return "", invalid("checklist item ID must be a non-empty identifier", nil)
	}
	return itemID, nil
}

func checklistItemFromRow(scanner interface{ Scan(...any) error }) (TaskChecklistItem, error) {
	var item TaskChecklistItem
	var completedAt, completedBy sql.NullString
	var completedValue int
	if err := scanner.Scan(&item.ID, &item.TaskID, &item.Text, &item.Position, &completedValue, &completedAt, &completedBy, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return TaskChecklistItem{}, err
	}
	item.Completed = completedValue != 0
	item.CompletedAt = nullableString(completedAt)
	item.CompletedBy = nullableString(completedBy)
	return item, nil
}

const checklistItemColumns = `id, task_id, text, position, completed, completed_at, completed_by, created_at, updated_at`

func (s *Store) listTaskChecklistItems(ctx context.Context, taskID string) ([]TaskChecklistItem, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+checklistItemColumns+` FROM task_checklist_items WHERE task_id=? ORDER BY position, id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]TaskChecklistItem, 0)
	for rows.Next() {
		item, err := checklistItemFromRow(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) populateTaskChecklist(ctx context.Context, task *Task) error {
	items, err := s.listTaskChecklistItems(ctx, task.ID)
	if err != nil {
		return err
	}
	task.Checklist = items
	if task.Checklist == nil {
		task.Checklist = []TaskChecklistItem{}
	}
	var total, completed int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1), COALESCE(SUM(completed), 0) FROM task_checklist_items WHERE task_id=?`, task.ID).Scan(&total, &completed); err != nil {
		return err
	}
	policy := "warn"
	if err := s.DB.QueryRowContext(ctx, `SELECT checklist_completion_policy FROM projects WHERE id=?`, task.ProjectID).Scan(&policy); err != nil {
		return err
	}
	if policy != "require" {
		policy = "warn"
	}
	open := total - completed
	percent := float64(0)
	if total > 0 {
		percent = float64(completed) * 100 / float64(total)
	}
	task.ChecklistSummary = TaskChecklistSummary{
		Total:            total,
		Completed:        completed,
		Open:             open,
		Percent:          percent,
		CompletionPolicy: policy,
		Warning:          task.CompletedAt != nil && open > 0 && policy == "warn",
	}
	return nil
}

func (s *Store) GetTaskChecklist(ctx context.Context, reference string) (ChecklistCollection, error) {
	task, err := s.ResolveTaskReference(ctx, reference)
	if err != nil {
		return ChecklistCollection{}, err
	}
	return ChecklistCollection{TaskID: task.ID, Version: task.Version, Items: task.Checklist, Summary: task.ChecklistSummary}, nil
}

func (s *Store) AddTaskChecklistItem(ctx context.Context, taskID string, input ChecklistItemInput, expected int64, actorID string) (Task, error) {
	return s.AddTaskChecklistItemWithClaimOverride(ctx, taskID, input, expected, actorID, false)
}

func (s *Store) AddTaskChecklistItemWithClaimOverride(ctx context.Context, taskID string, input ChecklistItemInput, expected int64, actorID string, allowClaimOverride bool) (Task, error) {
	validated, err := normalizeChecklistItemInput(input, true)
	if err != nil {
		return Task{}, err
	}
	if expected <= 0 {
		return Task{}, ErrPrecondition
	}
	current, err := s.ResolveTaskReference(ctx, taskID)
	if err != nil {
		return Task{}, err
	}
	created, itemID := now(), newID()
	position := len(current.Checklist)
	if validated.PositionSet && validated.Position != nil {
		position = *validated.Position
		if position > len(current.Checklist) {
			return Task{}, invalid("checklist item position is invalid", nil)
		}
	}
	completed := false
	if validated.CompletedSet && validated.Completed != nil {
		completed = *validated.Completed
	}
	var completedAt, completedBy any
	if completed {
		completedAt, completedBy = created, actorID
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if err := guardChecklistTaskTx(ctx, tx, current, expected, actorID, allowClaimOverride); err != nil {
			return err
		}
		if err := validateChecklistTextTotalTx(ctx, tx, current.ID, *validated.Text, ""); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE task_checklist_items SET position=position+1 WHERE task_id=? AND position>=?`, current.ID, position); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_checklist_items(id, task_id, text, position, completed, completed_at, completed_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?)`, itemID, current.ID, *validated.Text, position, boolInt(completed), completedAt, completedBy, created, created); err != nil {
			return mapChecklistStorageError(err)
		}
		_, err := insertEvent(ctx, tx, "task.checklist_item_added", actorID, current.ProjectID, current.ID, map[string]any{
			"item_id": itemID, "text": *validated.Text, "position": position, "completed": completed, "version": expected + 1,
		})
		return err
	})
	if err != nil {
		return Task{}, err
	}
	return s.GetTask(ctx, current.ID)
}

func (s *Store) UpdateTaskChecklistItem(ctx context.Context, taskID, itemID string, input ChecklistItemInput, expected int64, actorID string) (Task, error) {
	return s.UpdateTaskChecklistItemWithClaimOverride(ctx, taskID, itemID, input, expected, actorID, false)
}

func (s *Store) UpdateTaskChecklistItemWithClaimOverride(ctx context.Context, taskID, itemID string, input ChecklistItemInput, expected int64, actorID string, allowClaimOverride bool) (Task, error) {
	validated, err := normalizeChecklistItemInput(input, false)
	if err != nil {
		return Task{}, err
	}
	if expected <= 0 {
		return Task{}, ErrPrecondition
	}
	current, err := s.ResolveTaskReference(ctx, taskID)
	if err != nil {
		return Task{}, err
	}
	itemID, err = normalizeChecklistItemID(itemID)
	if err != nil {
		return Task{}, err
	}
	updated := now()
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if err := guardChecklistTaskTx(ctx, tx, current, expected, actorID, allowClaimOverride); err != nil {
			return err
		}
		var text string
		var position, completedValue int
		var completedAt, completedBy sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT text, position, completed, completed_at, completed_by FROM task_checklist_items WHERE id=? AND task_id=?`, itemID, current.ID).Scan(&text, &position, &completedValue, &completedAt, &completedBy); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return notFound("checklist item not found")
			}
			return err
		}
		if validated.Text != nil && *validated.Text != text {
			if err := validateChecklistTextTotalTx(ctx, tx, current.ID, *validated.Text, text); err != nil {
				return err
			}
			text = *validated.Text
		}
		completed := completedValue != 0
		if validated.CompletedSet && validated.Completed != nil {
			completed = *validated.Completed
		}
		var nextCompletedAt, nextCompletedBy any
		nextCompletedAt, nextCompletedBy = nullableValue(completedAt), nullableValue(completedBy)
		if validated.CompletedSet {
			if completed {
				nextCompletedAt, nextCompletedBy = updated, actorID
			} else {
				nextCompletedAt, nextCompletedBy = nil, nil
			}
		}
		targetPosition := position
		if validated.PositionSet && validated.Position != nil {
			if *validated.Position >= len(current.Checklist) {
				return invalid("checklist item position is invalid", nil)
			}
			targetPosition = *validated.Position
			if targetPosition < position {
				if _, err := tx.ExecContext(ctx, `UPDATE task_checklist_items SET position=position+1, updated_at=? WHERE task_id=? AND position>=? AND position<?`, updated, current.ID, targetPosition, position); err != nil {
					return err
				}
			} else if targetPosition > position {
				if _, err := tx.ExecContext(ctx, `UPDATE task_checklist_items SET position=position-1, updated_at=? WHERE task_id=? AND position>? AND position<=?`, updated, current.ID, position, targetPosition); err != nil {
					return err
				}
			}
			position = targetPosition
		}
		if _, err := tx.ExecContext(ctx, `UPDATE task_checklist_items SET text=?, position=?, completed=?, completed_at=?, completed_by=NULLIF(?, ''), updated_at=? WHERE id=? AND task_id=?`, text, position, boolInt(completed), nextCompletedAt, stringValue(nextCompletedBy), updated, itemID, current.ID); err != nil {
			return err
		}
		_, err := insertEvent(ctx, tx, "task.checklist_item_updated", actorID, current.ProjectID, current.ID, map[string]any{
			"item_id": itemID, "text": text, "position": position, "completed": completed, "version": expected + 1,
		})
		return err
	})
	if err != nil {
		return Task{}, err
	}
	return s.GetTask(ctx, current.ID)
}

func (s *Store) DeleteTaskChecklistItem(ctx context.Context, taskID, itemID string, expected int64, actorID string) (Task, error) {
	return s.DeleteTaskChecklistItemWithClaimOverride(ctx, taskID, itemID, expected, actorID, false)
}

func (s *Store) DeleteTaskChecklistItemWithClaimOverride(ctx context.Context, taskID, itemID string, expected int64, actorID string, allowClaimOverride bool) (Task, error) {
	if expected <= 0 {
		return Task{}, ErrPrecondition
	}
	current, err := s.ResolveTaskReference(ctx, taskID)
	if err != nil {
		return Task{}, err
	}
	itemID, err = normalizeChecklistItemID(itemID)
	if err != nil {
		return Task{}, err
	}
	updated := now()
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if err := guardChecklistTaskTx(ctx, tx, current, expected, actorID, allowClaimOverride); err != nil {
			return err
		}
		var position int
		if err := tx.QueryRowContext(ctx, `SELECT position FROM task_checklist_items WHERE id=? AND task_id=?`, itemID, current.ID).Scan(&position); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return notFound("checklist item not found")
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM task_checklist_items WHERE id=? AND task_id=?`, itemID, current.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE task_checklist_items SET position=position-1, updated_at=? WHERE task_id=? AND position>?`, updated, current.ID, position); err != nil {
			return err
		}
		_, err := insertEvent(ctx, tx, "task.checklist_item_removed", actorID, current.ProjectID, current.ID, map[string]any{
			"item_id": itemID, "position": position, "version": expected + 1,
		})
		return err
	})
	if err != nil {
		return Task{}, err
	}
	return s.GetTask(ctx, current.ID)
}

func (s *Store) ReorderTaskChecklist(ctx context.Context, taskID string, input ChecklistReorderInput, expected int64, actorID string) (Task, error) {
	return s.ReorderTaskChecklistWithClaimOverride(ctx, taskID, input, expected, actorID, false)
}

func (s *Store) ReorderTaskChecklistWithClaimOverride(ctx context.Context, taskID string, input ChecklistReorderInput, expected int64, actorID string, allowClaimOverride bool) (Task, error) {
	validated, err := normalizeChecklistReorder(input)
	if err != nil {
		return Task{}, err
	}
	if expected <= 0 {
		return Task{}, ErrPrecondition
	}
	current, err := s.ResolveTaskReference(ctx, taskID)
	if err != nil {
		return Task{}, err
	}
	if len(validated.ItemIDs) != len(current.Checklist) {
		return Task{}, invalid("checklist order must include every existing item", nil)
	}
	updated := now()
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if err := guardChecklistTaskTx(ctx, tx, current, expected, actorID, allowClaimOverride); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT id FROM task_checklist_items WHERE task_id=?`, current.ID)
		if err != nil {
			return err
		}
		existing := make(map[string]struct{}, len(current.Checklist))
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			existing[id] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for position, id := range validated.ItemIDs {
			if _, ok := existing[id]; !ok {
				return notFound("checklist item not found")
			}
			if _, err := tx.ExecContext(ctx, `UPDATE task_checklist_items SET position=?, updated_at=? WHERE id=? AND task_id=?`, position, updated, id, current.ID); err != nil {
				return err
			}
		}
		_, err = insertEvent(ctx, tx, "task.checklist_reordered", actorID, current.ProjectID, current.ID, map[string]any{
			"item_ids": validated.ItemIDs, "version": expected + 1,
		})
		return err
	})
	if err != nil {
		return Task{}, err
	}
	return s.GetTask(ctx, current.ID)
}

// Short aliases keep the store pleasant for callers that do not need the
// Task-prefixed operation names while preserving one canonical implementation.
func (s *Store) AddChecklistItem(ctx context.Context, taskID string, input ChecklistItemInput, expected int64, actorID string) (Task, error) {
	return s.AddTaskChecklistItem(ctx, taskID, input, expected, actorID)
}

func (s *Store) UpdateChecklistItem(ctx context.Context, taskID, itemID string, input ChecklistItemInput, expected int64, actorID string) (Task, error) {
	return s.UpdateTaskChecklistItem(ctx, taskID, itemID, input, expected, actorID)
}

func (s *Store) DeleteChecklistItem(ctx context.Context, taskID, itemID string, expected int64, actorID string) (Task, error) {
	return s.DeleteTaskChecklistItem(ctx, taskID, itemID, expected, actorID)
}

func (s *Store) ReorderChecklist(ctx context.Context, taskID string, input ChecklistReorderInput, expected int64, actorID string) (Task, error) {
	return s.ReorderTaskChecklist(ctx, taskID, input, expected, actorID)
}

func guardChecklistTaskTx(ctx context.Context, tx *sql.Tx, current Task, expected int64, actorID string, allowClaimOverride bool) error {
	updated := now()
	query := `UPDATE tasks SET version=version+1, updated_at=? WHERE id=? AND version=? AND deleted_at IS NULL`
	args := []any{updated, current.ID, expected}
	if !allowClaimOverride {
		query += ` AND (claimed_by IS NULL OR claim_expires_at IS NULL OR julianday(claim_expires_at) <= julianday(?) OR claimed_by=?)`
		args = append(args, updated, actorID)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return mapChecklistStorageError(err)
	}
	count, _ := result.RowsAffected()
	if count != 0 {
		return nil
	}
	if !allowClaimOverride {
		claimed, details, claimErr := activeClaimByOtherTx(ctx, tx, current.ID, actorID, updated)
		if claimErr != nil {
			return claimErr
		}
		if claimed {
			return &Error{Kind: ErrClaimUnavailable, Message: "task is currently claimed by another actor", Details: details}
		}
	}
	return conflict("task has changed", map[string]any{"current": current})
}

func validateChecklistTextTotalTx(ctx context.Context, tx *sql.Tx, taskID, nextText, previousText string) error {
	var total int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(CAST(text AS BLOB))), 0) FROM task_checklist_items WHERE task_id=?`, taskID).Scan(&total); err != nil {
		return err
	}
	total -= int64(len(previousText))
	total += int64(len(nextText))
	if total > MaxTaskChecklistTextBytes {
		return ErrChecklistLimitExceeded
	}
	return nil
}

func nullableValue(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if parsed, ok := value.(string); ok {
		return parsed
	}
	return fmt.Sprint(value)
}

func mapChecklistStorageError(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "checklist_limit_exceeded") {
		return ErrChecklistLimitExceeded
	}
	return err
}
