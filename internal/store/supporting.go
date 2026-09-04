package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

type LabelInput struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

func (s *Store) ListLabels(ctx context.Context, projectID string) ([]Label, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, project_id, name, color, created_at, updated_at FROM labels WHERE project_id=? ORDER BY lower(name), id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Label, 0)
	for rows.Next() {
		label, err := labelFromRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, label)
	}
	return result, rows.Err()
}

// ListLabelsPage returns one cursor page and whether another row follows it.
// It fetches one sentinel row while keeping the externally visible page size
// capped at 200.
func (s *Store) ListLabelsPage(ctx context.Context, projectID string, limit, offset int) ([]Label, bool, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id, project_id, name, color, created_at, updated_at FROM labels WHERE project_id=? ORDER BY lower(name), id LIMIT ? OFFSET ?`, projectID, limit+1, offset)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	result := make([]Label, 0, limit)
	for rows.Next() {
		label, err := labelFromRow(rows)
		if err != nil {
			return nil, false, err
		}
		result = append(result, label)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(result) > limit
	if hasMore {
		result = result[:limit]
	}
	return result, hasMore, nil
}

func (s *Store) CreateLabel(ctx context.Context, projectID string, input LabelInput, actorID string) (Label, error) {
	name, color := strings.TrimSpace(input.Name), strings.TrimSpace(input.Color)
	if name == "" || len(name) > 100 {
		return Label{}, invalid("label name must be between 1 and 100 characters", nil)
	}
	if color == "" {
		color = "#94a3b8"
	}
	if !colorPattern.MatchString(color) {
		return Label{}, invalid("label color must be a six-digit hexadecimal value", nil)
	}
	id, created := newID(), now()
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO labels(id, project_id, name, color, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, id, projectID, name, color, created, created); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				return &Error{Kind: ErrAlreadyExists, Message: "label already exists"}
			}
			return err
		}
		_, err := insertEvent(ctx, tx, "label.created", actorID, projectID, "", map[string]any{"label_id": id, "name": name})
		return err
	})
	if err != nil {
		return Label{}, err
	}
	row := s.DB.QueryRowContext(ctx, `SELECT id, project_id, name, color, created_at, updated_at FROM labels WHERE id=?`, id)
	return labelFromRow(row)
}

func (s *Store) DeleteLabel(ctx context.Context, id, actorID string) error {
	var projectID string
	if err := s.DB.QueryRowContext(ctx, `SELECT project_id FROM labels WHERE id=?`, id).Scan(&projectID); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `DELETE FROM labels WHERE id=?`, id)
		if err != nil {
			return err
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			return ErrNotFound
		}
		_, err = insertEvent(ctx, tx, "label.deleted", actorID, projectID, "", map[string]any{"label_id": id})
		return err
	})
}

func (s *Store) ListComments(ctx context.Context, taskID string, limit, offset int) ([]Comment, error) {
	comments, _, err := s.ListCommentsPage(ctx, taskID, limit, offset)
	return comments, err
}

func (s *Store) ListCommentsPage(ctx context.Context, taskID string, limit, offset int) ([]Comment, bool, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id, task_id, actor_id, body, version, created_at, updated_at, deleted_at FROM comments WHERE task_id=? AND deleted_at IS NULL ORDER BY created_at, id LIMIT ? OFFSET ?`, taskID, limit+1, offset)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	result := make([]Comment, 0)
	for rows.Next() {
		comment, err := scanComment(rows)
		if err != nil {
			return nil, false, err
		}
		result = append(result, comment)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(result) > limit
	if hasMore {
		result = result[:limit]
	}
	return result, hasMore, nil
}

func (s *Store) CreateComment(ctx context.Context, taskID, actorID, body string) (Comment, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return Comment{}, invalid("comment body is required", nil)
	}
	if len(body) > 20000 {
		return Comment{}, invalid("comment is too long", nil)
	}
	var projectID string
	if err := s.DB.QueryRowContext(ctx, `SELECT project_id FROM tasks WHERE id=? AND deleted_at IS NULL`, taskID).Scan(&projectID); errors.Is(err, sql.ErrNoRows) {
		return Comment{}, notFound("task not found")
	} else if err != nil {
		return Comment{}, err
	}
	id, created := newID(), now()
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO comments(id, task_id, actor_id, body, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, id, taskID, actorID, body, created, created); err != nil {
			return err
		}
		_, err := insertEvent(ctx, tx, "comment.created", actorID, projectID, taskID, map[string]any{"comment_id": id})
		return err
	})
	if err != nil {
		return Comment{}, err
	}
	return s.GetComment(ctx, id)
}

// scanComment keeps comment reads compatible with the additive lifecycle
// columns. DeletedAt is populated for internal callers that need to inspect a
// tombstone; public reads use GetComment/ListCommentsPage and hide tombstones.
func scanComment(scanner interface{ Scan(...any) error }) (Comment, error) {
	var comment Comment
	var deletedAt sql.NullString
	if err := scanner.Scan(&comment.ID, &comment.TaskID, &comment.ActorID, &comment.Body, &comment.Version, &comment.CreatedAt, &comment.UpdatedAt, &deletedAt); err != nil {
		return Comment{}, err
	}
	comment.DeletedAt = nullableString(deletedAt)
	return comment, nil
}

// GetComment returns one active comment. A deleted comment intentionally looks
// like a missing resource to callers; the immutable comment.deleted event is
// the retained audit representation.
func (s *Store) GetComment(ctx context.Context, commentID string) (Comment, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT id, task_id, actor_id, body, version, created_at, updated_at, deleted_at FROM comments WHERE id=? AND deleted_at IS NULL`, commentID)
	comment, err := scanComment(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Comment{}, notFound("comment not found")
	}
	return comment, err
}

// UpdateComment edits only the current comment row while appending an
// immutable event describing the edit in the same transaction. The author may
// edit their own comment; an administrator may explicitly override that
// ownership check. expectedVersion is guarded in SQL so concurrent edits are
// never silently lost.
func (s *Store) UpdateComment(ctx context.Context, taskID, commentID, actorID, body string, expectedVersion int64, allowAdmin bool) (Comment, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return Comment{}, invalid("comment body is required", nil)
	}
	if len(body) > 20000 {
		return Comment{}, invalid("comment is too long", nil)
	}
	if expectedVersion <= 0 {
		return Comment{}, ErrPrecondition
	}
	var updatedVersion int64
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var currentActor string
		var currentVersion int64
		var projectID string
		if err := tx.QueryRowContext(ctx, `SELECT c.actor_id, c.version, t.project_id FROM comments c JOIN tasks t ON t.id=c.task_id WHERE c.id=? AND c.task_id=? AND c.deleted_at IS NULL AND t.deleted_at IS NULL`, commentID, taskID).Scan(&currentActor, &currentVersion, &projectID); errors.Is(err, sql.ErrNoRows) {
			return notFound("comment not found")
		} else if err != nil {
			return err
		} else if currentActor != actorID && !allowAdmin {
			return forbidden("only the comment author may edit this comment")
		} else if currentVersion != expectedVersion {
			return conflict("comment has changed", nil)
		}
		timestamp := now()
		result, err := tx.ExecContext(ctx, `UPDATE comments SET body=?, version=version+1, updated_at=? WHERE id=? AND task_id=? AND version=? AND deleted_at IS NULL`, body, timestamp, commentID, taskID, expectedVersion)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return conflict("comment has changed", nil)
		}
		updatedVersion = expectedVersion + 1
		_, err = insertEvent(ctx, tx, "comment.updated", actorID, projectID, taskID, map[string]any{
			"comment_id":       commentID,
			"version":          updatedVersion,
			"previous_version": expectedVersion,
		})
		return err
	})
	if err != nil {
		return Comment{}, err
	}
	return s.GetComment(ctx, commentID)
}

// DeleteComment tombstones a comment rather than removing it. Its original
// body remains in the database for retention/integrity, while ordinary reads
// hide it and the immutable comment.deleted event explains the change.
func (s *Store) DeleteComment(ctx context.Context, taskID, commentID, actorID string, expectedVersion int64, allowAdmin bool) error {
	if expectedVersion <= 0 {
		return ErrPrecondition
	}
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var currentActor string
		var currentVersion int64
		var projectID string
		if err := tx.QueryRowContext(ctx, `SELECT c.actor_id, c.version, t.project_id FROM comments c JOIN tasks t ON t.id=c.task_id WHERE c.id=? AND c.task_id=? AND c.deleted_at IS NULL AND t.deleted_at IS NULL`, commentID, taskID).Scan(&currentActor, &currentVersion, &projectID); errors.Is(err, sql.ErrNoRows) {
			return notFound("comment not found")
		} else if err != nil {
			return err
		} else if currentActor != actorID && !allowAdmin {
			return forbidden("only the comment author may delete this comment")
		} else if currentVersion != expectedVersion {
			return conflict("comment has changed", nil)
		}
		timestamp := now()
		result, err := tx.ExecContext(ctx, `UPDATE comments SET version=version+1, updated_at=?, deleted_at=? WHERE id=? AND task_id=? AND version=? AND deleted_at IS NULL`, timestamp, timestamp, commentID, taskID, expectedVersion)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return conflict("comment has changed", nil)
		}
		_, err = insertEvent(ctx, tx, "comment.deleted", actorID, projectID, taskID, map[string]any{
			"comment_id":       commentID,
			"version":          expectedVersion + 1,
			"previous_version": expectedVersion,
		})
		return err
	})
	return err
}
