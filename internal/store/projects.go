package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	projectKeyPattern  = regexp.MustCompile(`^[A-Z][A-Z0-9_-]{0,15}$`)
	projectSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	colorPattern       = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
)

type ProjectInput struct {
	Key         *string
	Slug        *string
	Name        *string
	Description *string
	Color       *string
	Favorite    *bool
	Archived    *bool
}

type ColumnInput struct {
	Name          *string
	SemanticState *string
	Position      *int
}

const projectSelect = `SELECT id, key, slug, name, description, color, favorite, archived_at, created_at, updated_at FROM projects`

func validateProjectInput(input ProjectInput, creating bool) (ProjectInput, error) {
	if creating && input.Name == nil {
		return ProjectInput{}, invalid("name is required", nil)
	}
	if creating && input.Key == nil {
		return ProjectInput{}, invalid("key is required", nil)
	}
	if input.Key != nil {
		value := strings.ToUpper(strings.TrimSpace(*input.Key))
		if !projectKeyPattern.MatchString(value) {
			return ProjectInput{}, invalid("key must be 1-16 uppercase letters, numbers, _ or - and start with a letter", nil)
		}
		input.Key = &value
	}
	if input.Slug != nil {
		value := strings.ToLower(strings.TrimSpace(*input.Slug))
		if !projectSlugPattern.MatchString(value) {
			return ProjectInput{}, invalid("slug must contain lowercase letters, numbers, and hyphens", nil)
		}
		input.Slug = &value
	}
	if input.Name != nil {
		value := strings.TrimSpace(*input.Name)
		if value == "" || len(value) > 200 {
			return ProjectInput{}, invalid("name must be between 1 and 200 characters", nil)
		}
		input.Name = &value
	}
	if input.Description != nil {
		value := strings.TrimSpace(*input.Description)
		if len(value) > 20000 {
			return ProjectInput{}, invalid("description is too long", nil)
		}
		input.Description = &value
	}
	if input.Color != nil {
		value := strings.TrimSpace(*input.Color)
		if !colorPattern.MatchString(value) {
			return ProjectInput{}, invalid("color must be a six-digit hexadecimal value", nil)
		}
		input.Color = &value
	}
	return input, nil
}

func slugify(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func (s *Store) CreateProject(ctx context.Context, input ProjectInput, actorID string) (Project, error) {
	validated, err := validateProjectInput(input, true)
	if err != nil {
		return Project{}, err
	}
	key := strings.ToUpper(strings.TrimSpace(*validated.Key))
	if !projectKeyPattern.MatchString(key) {
		return Project{}, invalid("key is required and must match the documented format", nil)
	}
	slug := ""
	if validated.Slug != nil {
		slug = *validated.Slug
	}
	if slug == "" {
		slug = slugify(*validated.Name)
	}
	if slug == "" {
		slug = strings.ToLower(key)
	}
	if !projectSlugPattern.MatchString(slug) {
		return Project{}, invalid("slug must match the documented format", nil)
	}
	description, color := "", "#64748b"
	if validated.Description != nil {
		description = *validated.Description
	}
	if validated.Color != nil && *validated.Color != "" {
		color = *validated.Color
	}
	favorite := false
	if validated.Favorite != nil {
		favorite = *validated.Favorite
	}
	id, created := newID(), now()
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO projects(id, key, slug, name, description, color, favorite, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, key, slug, *validated.Name, description, color, boolInt(favorite), created, created); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				return &Error{Kind: ErrAlreadyExists, Message: "project key or slug already exists"}
			}
			return err
		}
		defaults := []struct{ name, state string }{
			{"Backlog", "backlog"}, {"Ready", "ready"}, {"In progress", "active"}, {"Blocked", "blocked"}, {"Done", "completed"},
		}
		for position, column := range defaults {
			if _, err := tx.ExecContext(ctx, `INSERT INTO columns(id, project_id, name, semantic_state, position, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, newID(), id, column.name, column.state, position, created, created); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO project_counters(project_id, next_number) VALUES (?, 1)`, id); err != nil {
			return err
		}
		_, err := insertEvent(ctx, tx, "project.created", actorID, id, "", map[string]any{"key": key, "name": *validated.Name})
		return err
	})
	if err != nil {
		return Project{}, err
	}
	return s.GetProject(ctx, id)
}

func deriveKey(name string) string {
	parts := strings.Fields(strings.ToUpper(name))
	var b strings.Builder
	for _, part := range parts {
		for _, r := range part {
			if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
			}
			if b.Len() >= 8 {
				break
			}
		}
		if b.Len() >= 8 {
			break
		}
	}
	if b.Len() == 0 {
		return "PROJECT"
	}
	return b.String()
}

func (s *Store) GetProject(ctx context.Context, reference string) (Project, error) {
	reference = strings.TrimSpace(reference)
	row := s.DB.QueryRowContext(ctx, projectSelect+` WHERE id = ? OR slug = ? OR key = ? LIMIT 1`, reference, strings.ToLower(reference), strings.ToUpper(reference))
	p, err := projectFromRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, notFound("project not found")
	}
	if err != nil {
		return Project{}, err
	}
	if err := s.populateProjectCounts(ctx, &p); err != nil {
		return Project{}, err
	}
	return p, nil
}

func (s *Store) ListProjects(ctx context.Context, limit, offset int) ([]Project, error) {
	return s.ListProjectsFiltered(ctx, limit, offset, ProjectFilter{})
}

func (s *Store) ListProjectsFiltered(ctx context.Context, limit, offset int, filter ProjectFilter) ([]Project, error) {
	return s.listProjectsFiltered(ctx, limit, offset, filter, false)
}

// ListProjectsFilteredWithExtra is used by cursor endpoints that need one
// sentinel row to determine whether a page has more results. The public page
// size remains capped at 200; this internal variant permits at most 201 rows.
func (s *Store) ListProjectsFilteredWithExtra(ctx context.Context, limit, offset int, filter ProjectFilter) ([]Project, error) {
	return s.listProjectsFiltered(ctx, limit, offset, filter, true)
}

func (s *Store) listProjectsFiltered(ctx context.Context, limit, offset int, filter ProjectFilter, allowExtra bool) ([]Project, error) {
	if limit <= 0 {
		limit = 50
	}
	maxLimit := 200
	if allowExtra {
		maxLimit++
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	query := projectSelect + ` WHERE 1=1`
	args := make([]any, 0, 4)
	if !filter.IncludeArchived {
		query += ` AND archived_at IS NULL`
	}
	if filter.FavoriteOnly {
		query += ` AND favorite = 1`
	}
	if filter.ProjectIDs != nil && len(filter.ProjectIDs) == 0 {
		query += ` AND 1=0`
	}
	if len(filter.ProjectIDs) > 0 {
		placeholders := make([]string, len(filter.ProjectIDs))
		for i, projectID := range filter.ProjectIDs {
			placeholders[i] = "?"
			args = append(args, projectID)
		}
		query += ` AND id IN (` + strings.Join(placeholders, ",") + `)`
	}
	query += ` ORDER BY favorite DESC, lower(name), id LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Project, 0)
	for rows.Next() {
		p, err := projectFromRow(rows)
		if err != nil {
			return nil, err
		}
		if err := s.populateProjectCounts(ctx, &p); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

func (s *Store) populateProjectCounts(ctx context.Context, project *Project) error {
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM tasks WHERE project_id = ? AND deleted_at IS NULL`, project.ID).Scan(&project.TaskCount); err != nil {
		return err
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM tasks t JOIN columns c ON c.id=t.column_id WHERE t.project_id = ? AND t.deleted_at IS NULL AND c.semantic_state='completed'`, project.ID).Scan(&project.CompletedCount); err != nil {
		return err
	}
	project.CompletedTaskCount = project.CompletedCount
	project.OpenTaskCount = project.TaskCount - project.CompletedCount
	if project.OpenTaskCount < 0 {
		project.OpenTaskCount = 0
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM columns WHERE project_id = ?`, project.ID).Scan(&project.ColumnCount); err != nil {
		return err
	}
	return s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM tasks WHERE project_id=? AND deleted_at IS NULL AND due_at IS NOT NULL AND due_at < ? AND completed_at IS NULL`, project.ID, now()).Scan(&project.OverdueTaskCount)
}

func (s *Store) UpdateProject(ctx context.Context, id string, input ProjectInput, actorID string) (Project, error) {
	validated, err := validateProjectInput(input, false)
	if err != nil {
		return Project{}, err
	}
	current, err := s.GetProject(ctx, id)
	if err != nil {
		return Project{}, err
	}
	key, slug, name := current.Key, current.Slug, current.Name
	description, color := current.Description, current.Color
	favorite, archived := current.Favorite, current.ArchivedAt != nil
	if validated.Key != nil {
		key = *validated.Key
	}
	if validated.Slug != nil {
		slug = *validated.Slug
	}
	if validated.Name != nil {
		name = *validated.Name
	}
	if validated.Description != nil {
		description = *validated.Description
	}
	if validated.Color != nil {
		color = *validated.Color
	}
	if validated.Favorite != nil {
		favorite = *validated.Favorite
	}
	if validated.Archived != nil {
		archived = *validated.Archived
	}
	if !projectKeyPattern.MatchString(key) || !projectSlugPattern.MatchString(slug) {
		return Project{}, invalid("project key or slug is invalid", nil)
	}
	updated := now()
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		var archivedValue any
		if archived {
			archivedValue = updated
		}
		result, err := tx.ExecContext(ctx, `UPDATE projects SET key=?, slug=?, name=?, description=?, color=?, favorite=?, archived_at=?, updated_at=? WHERE id=?`, key, slug, name, description, color, boolInt(favorite), archivedValue, updated, current.ID)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				return &Error{Kind: ErrAlreadyExists, Message: "project key or slug already exists"}
			}
			return err
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			return ErrNotFound
		}
		returnEvent := map[string]any{"key": key, "slug": slug, "name": name}
		_, err = insertEvent(ctx, tx, "project.updated", actorID, current.ID, "", returnEvent)
		return err
	})
	if err != nil {
		return Project{}, err
	}
	return s.GetProject(ctx, current.ID)
}

func (s *Store) ListColumns(ctx context.Context, projectID string) ([]Column, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, project_id, name, semantic_state, position, created_at, updated_at FROM columns WHERE project_id = ? ORDER BY position, id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Column, 0)
	for rows.Next() {
		column, err := columnFromRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, column)
	}
	return result, rows.Err()
}

// ListColumnsPage returns one cursor page and whether another row follows it.
// The query fetches one sentinel row so callers can determine the next cursor
// without issuing a count query. The public page size is capped at 200.
func (s *Store) ListColumnsPage(ctx context.Context, projectID string, limit, offset int) ([]Column, bool, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id, project_id, name, semantic_state, position, created_at, updated_at FROM columns WHERE project_id = ? ORDER BY position, id LIMIT ? OFFSET ?`, projectID, limit+1, offset)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	result := make([]Column, 0, limit)
	for rows.Next() {
		column, err := columnFromRow(rows)
		if err != nil {
			return nil, false, err
		}
		result = append(result, column)
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

func (s *Store) GetColumn(ctx context.Context, id string) (Column, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT id, project_id, name, semantic_state, position, created_at, updated_at FROM columns WHERE id = ?`, id)
	column, err := columnFromRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Column{}, notFound("column not found")
	}
	return column, err
}

func validateColumn(input ColumnInput, creating bool) (ColumnInput, error) {
	if creating && input.Name == nil {
		return ColumnInput{}, invalid("name is required", nil)
	}
	if creating && input.SemanticState == nil {
		return ColumnInput{}, invalid("semantic_state is required", nil)
	}
	if input.Name != nil {
		value := strings.TrimSpace(*input.Name)
		if value == "" || len(value) > 100 {
			return ColumnInput{}, invalid("column name must be between 1 and 100 characters", nil)
		}
		input.Name = &value
	}
	if input.SemanticState != nil {
		value := *input.SemanticState
		if !validState(value) {
			return ColumnInput{}, invalid("semantic_state is invalid", nil)
		}
		input.SemanticState = &value
	}
	if input.Position != nil && *input.Position < 0 {
		return ColumnInput{}, invalid("position must be non-negative", nil)
	}
	return input, nil
}

func validState(value string) bool {
	switch value {
	case "backlog", "ready", "active", "blocked", "completed":
		return true
	}
	return false
}

func (s *Store) CreateColumn(ctx context.Context, projectID string, input ColumnInput, actorID string) (Column, error) {
	validated, err := validateColumn(input, true)
	if err != nil {
		return Column{}, err
	}
	position := 0
	if validated.Position != nil {
		position = *validated.Position
	} else {
		_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(position)+1, 0) FROM columns WHERE project_id = ?`, projectID).Scan(&position)
	}
	state := "backlog"
	if validated.SemanticState != nil {
		state = *validated.SemanticState
	}
	id, created := newID(), now()
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO columns(id, project_id, name, semantic_state, position, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, id, projectID, *validated.Name, state, position, created, created); err != nil {
			return err
		}
		_, err := insertEvent(ctx, tx, "column.created", actorID, projectID, "", map[string]any{"column_id": id})
		return err
	})
	if err != nil {
		return Column{}, err
	}
	return s.GetColumn(ctx, id)
}

func (s *Store) UpdateColumn(ctx context.Context, id string, input ColumnInput, actorID string) (Column, error) {
	return s.UpdateColumnWithClaimOverride(ctx, id, input, actorID, false)
}

// UpdateColumnWithClaimOverride updates a column and synchronizes the derived
// completion fields on all of its live tasks when the semantic state changes.
// A human administrator may explicitly override active claims; all other
// callers are rejected before any column or task row is changed.
func (s *Store) UpdateColumnWithClaimOverride(ctx context.Context, id string, input ColumnInput, actorID string, allowClaimOverride bool) (Column, error) {
	validated, err := validateColumn(input, false)
	if err != nil {
		return Column{}, err
	}
	updated := now()
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `SELECT id, project_id, name, semantic_state, position, created_at, updated_at FROM columns WHERE id=?`, id)
		current, err := columnFromRow(row)
		if errors.Is(err, sql.ErrNoRows) {
			return notFound("column not found")
		}
		if err != nil {
			return err
		}
		name, state, position := current.Name, current.SemanticState, current.Position
		if validated.Name != nil {
			name = *validated.Name
		}
		if validated.SemanticState != nil {
			state = *validated.SemanticState
		}
		if validated.Position != nil {
			position = *validated.Position
		}
		stateChanged := state != current.SemanticState
		query := `UPDATE columns SET name=?, semantic_state=?, position=?, updated_at=? WHERE id=? AND semantic_state=?`
		args := []any{name, state, position, updated, id, current.SemanticState}
		if stateChanged && !allowClaimOverride {
			query += ` AND NOT EXISTS (SELECT 1 FROM tasks WHERE column_id=? AND deleted_at IS NULL AND claimed_by IS NOT NULL AND claim_expires_at IS NOT NULL AND julianday(claim_expires_at) > julianday(?))`
			args = append(args, id, updated)
		}
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return mapDependencyLifecycleError(ctx, tx, err, dependencyLifecycleTarget{ColumnID: id, NextSemanticState: state})
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			if stateChanged && !allowClaimOverride {
				claimed, details, claimErr := activeClaimInColumnTx(ctx, tx, id, updated)
				if claimErr != nil {
					return claimErr
				}
				if claimed {
					return &Error{Kind: ErrClaimUnavailable, Message: "column contains actively claimed tasks", Details: details}
				}
			}
			return conflict("column has changed", map[string]any{"column_id": id})
		}
		var dependencyChanges []dependencyStateChange
		if stateChanged {
			dependencyChanges, err = dependencyColumnStateChanges(ctx, tx, current, state, updated)
			if err != nil {
				return err
			}
			var taskQuery string
			var taskArgs []any
			switch {
			case state == "completed":
				taskQuery = `UPDATE tasks SET completed_at=?, updated_at=?, version=version+1 WHERE column_id=? AND deleted_at IS NULL`
				taskArgs = []any{updated, updated, id}
			case current.SemanticState == "completed":
				taskQuery = `UPDATE tasks SET completed_at=NULL, updated_at=?, version=version+1 WHERE column_id=? AND deleted_at IS NULL`
				taskArgs = []any{updated, id}
			default:
				taskQuery = `UPDATE tasks SET updated_at=?, version=version+1 WHERE column_id=? AND deleted_at IS NULL`
				taskArgs = []any{updated, id}
			}
			if _, err := tx.ExecContext(ctx, taskQuery, taskArgs...); err != nil {
				return mapDependencyLifecycleError(ctx, tx, err, dependencyLifecycleTarget{ColumnID: id, NextSemanticState: state})
			}
		}
		if _, err = insertEvent(ctx, tx, "column.updated", actorID, current.ProjectID, "", map[string]any{"column_id": id}); err != nil {
			return err
		}
		return emitDependencyStateChanges(ctx, tx, actorID, dependencyChanges)
	})
	if err != nil {
		return Column{}, err
	}
	return s.GetColumn(ctx, id)
}

func activeClaimInColumnTx(ctx context.Context, tx *sql.Tx, columnID, timestamp string) (bool, map[string]any, error) {
	var taskID, claimedBy, expiresAt string
	err := tx.QueryRowContext(ctx, `SELECT id, claimed_by, claim_expires_at FROM tasks WHERE column_id=? AND deleted_at IS NULL AND claimed_by IS NOT NULL AND claim_expires_at IS NOT NULL AND julianday(claim_expires_at) > julianday(?) ORDER BY id LIMIT 1`, columnID, timestamp).Scan(&taskID, &claimedBy, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	return true, map[string]any{"task_id": taskID, "claimed_by": claimedBy, "claim_expires_at": expiresAt}, nil
}

func (s *Store) StateColumn(ctx context.Context, projectID, state string) (Column, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT id, project_id, name, semantic_state, position, created_at, updated_at FROM columns WHERE project_id = ? AND semantic_state = ? ORDER BY position LIMIT 1`, projectID, state)
	column, err := columnFromRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Column{}, notFound(fmt.Sprintf("%s column not found", state))
	}
	return column, err
}
