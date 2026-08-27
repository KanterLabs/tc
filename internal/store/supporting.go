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
	rows, err := s.DB.QueryContext(ctx, `SELECT id, task_id, actor_id, body, created_at, updated_at FROM comments WHERE task_id=? ORDER BY created_at, id LIMIT ? OFFSET ?`, taskID, limit+1, offset)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	result := make([]Comment, 0)
	for rows.Next() {
		var comment Comment
		if err := rows.Scan(&comment.ID, &comment.TaskID, &comment.ActorID, &comment.Body, &comment.CreatedAt, &comment.UpdatedAt); err != nil {
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
	var comment Comment
	err = s.DB.QueryRowContext(ctx, `SELECT id, task_id, actor_id, body, created_at, updated_at FROM comments WHERE id=?`, id).Scan(&comment.ID, &comment.TaskID, &comment.ActorID, &comment.Body, &comment.CreatedAt, &comment.UpdatedAt)
	return comment, err
}
