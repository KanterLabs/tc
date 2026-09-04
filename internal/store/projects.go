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
	Key                       *string
	Slug                      *string
	Name                      *string
	Description               *string
	Color                     *string
	Favorite                  *bool
	Archived                  *bool
	ChecklistCompletionPolicy *string
}

type ColumnInput struct {
	Name          *string
	SemanticState *string
	Position      *int
	Archived      *bool
}

const projectSelect = `SELECT id, key, slug, name, description, color, favorite, archived_at, checklist_completion_policy, created_at, updated_at, version FROM projects`
const columnSelect = `SELECT id, project_id, name, semantic_state, position, archived_at, ordering_version, created_at, updated_at, version FROM columns`

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
	if input.ChecklistCompletionPolicy != nil {
		value := strings.ToLower(strings.TrimSpace(*input.ChecklistCompletionPolicy))
		if value != "warn" && value != "require" {
			return ProjectInput{}, invalid("checklist_completion_policy must be warn or require", nil)
		}
		input.ChecklistCompletionPolicy = &value
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
	checklistPolicy := "warn"
	if validated.ChecklistCompletionPolicy != nil {
		checklistPolicy = *validated.ChecklistCompletionPolicy
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO projects(id, key, slug, name, description, color, favorite, checklist_completion_policy, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, key, slug, *validated.Name, description, color, boolInt(favorite), checklistPolicy, created, created); err != nil {
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
		_, err := insertEvent(ctx, tx, "project.created", actorID, id, "", map[string]any{"key": key, "name": *validated.Name, "checklist_completion_policy": checklistPolicy})
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
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM columns WHERE project_id = ? AND archived_at IS NULL`, project.ID).Scan(&project.ColumnCount); err != nil {
		return err
	}
	return s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM tasks WHERE project_id=? AND deleted_at IS NULL AND due_at IS NOT NULL AND due_at < ? AND completed_at IS NULL`, project.ID, now()).Scan(&project.OverdueTaskCount)
}

func (s *Store) UpdateProject(ctx context.Context, id string, input ProjectInput, actorID string) (Project, error) {
	return s.UpdateProjectWithVersion(ctx, id, input, actorID, nil)
}

// UpdateProjectWithVersion updates project metadata and, when expected is
// supplied, guards the mutation with the project's optimistic version. The
// legacy UpdateProject entry point intentionally remains unguarded for older
// internal callers; HTTP clients can opt into the stronger contract by
// sending If-Match.
func (s *Store) UpdateProjectWithVersion(ctx context.Context, id string, input ProjectInput, actorID string, expected *int64) (Project, error) {
	validated, err := validateProjectInput(input, false)
	if err != nil {
		return Project{}, err
	}
	if expected != nil && *expected <= 0 {
		return Project{}, ErrPrecondition
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
		checklistPolicy := current.ChecklistCompletionPolicy
		if checklistPolicy == "" {
			checklistPolicy = "warn"
		}
		if validated.ChecklistCompletionPolicy != nil {
			checklistPolicy = *validated.ChecklistCompletionPolicy
		}
		query := `UPDATE projects SET key=?, slug=?, name=?, description=?, color=?, favorite=?, archived_at=?, checklist_completion_policy=?, version=version+1, updated_at=? WHERE id=?`
		args := []any{key, slug, name, description, color, boolInt(favorite), archivedValue, checklistPolicy, updated, current.ID}
		if expected != nil {
			query += ` AND version=?`
			args = append(args, *expected)
		}
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				return &Error{Kind: ErrAlreadyExists, Message: "project key or slug already exists"}
			}
			return err
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			if expected != nil {
				var currentVersion int64
				if versionErr := tx.QueryRowContext(ctx, `SELECT version FROM projects WHERE id=?`, current.ID).Scan(&currentVersion); errors.Is(versionErr, sql.ErrNoRows) {
					return ErrNotFound
				} else if versionErr != nil {
					return versionErr
				}
				return conflict("project has changed", map[string]any{"project_id": current.ID, "expected_version": *expected, "current_version": currentVersion})
			}
			return ErrNotFound
		}
		returnEvent := map[string]any{"key": key, "slug": slug, "name": name, "archived": archived, "checklist_completion_policy": checklistPolicy, "version": current.Version + 1}
		_, err = insertEvent(ctx, tx, "project.updated", actorID, current.ID, "", returnEvent)
		return err
	})
	if err != nil {
		return Project{}, err
	}
	return s.GetProject(ctx, current.ID)
}

func (s *Store) ListColumns(ctx context.Context, projectID string) ([]Column, error) {
	return s.listColumns(ctx, projectID, false)
}

// ListColumnsIncludingArchived returns active columns followed by archived
// columns in stable board order. It is used by administration surfaces; the
// ordinary board-facing ListColumns call intentionally hides archived rows.
func (s *Store) ListColumnsIncludingArchived(ctx context.Context, projectID string) ([]Column, error) {
	return s.listColumns(ctx, projectID, true)
}

func (s *Store) listColumns(ctx context.Context, projectID string, includeArchived bool) ([]Column, error) {
	query := columnSelect + ` WHERE project_id = ?`
	if !includeArchived {
		query += ` AND archived_at IS NULL`
	}
	query += ` ORDER BY archived_at IS NOT NULL, position, id`
	rows, err := s.DB.QueryContext(ctx, query, projectID)
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
	return s.ListColumnsPageFiltered(ctx, projectID, limit, offset, false)
}

// ListColumnsPageFiltered is the cursor-paginated administration variant. A
// false includeArchived value retains the existing board API behavior.
func (s *Store) ListColumnsPageFiltered(ctx context.Context, projectID string, limit, offset int, includeArchived bool) ([]Column, bool, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	query := columnSelect + ` WHERE project_id = ?`
	if !includeArchived {
		query += ` AND archived_at IS NULL`
	}
	query += ` ORDER BY archived_at IS NOT NULL, position, id LIMIT ? OFFSET ?`
	rows, err := s.DB.QueryContext(ctx, query, projectID, limit+1, offset)
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
	row := s.DB.QueryRowContext(ctx, columnSelect+` WHERE id = ?`, id)
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
	state := "backlog"
	if validated.SemanticState != nil {
		state = *validated.SemanticState
	}
	id, created := newID(), now()
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		var projectExists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM projects WHERE id=?`, projectID).Scan(&projectExists); err != nil {
			return err
		}
		if projectExists == 0 {
			return notFound("project not found")
		}
		active, err := liveColumnIDsTx(ctx, tx, projectID)
		if err != nil {
			return err
		}
		position := len(active)
		if validated.Position != nil {
			position = *validated.Position
			if position > len(active) {
				position = len(active)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO columns(id, project_id, name, semantic_state, position, created_at, updated_at) VALUES (?, ?, ?, ?, -1, ?, ?)`, id, projectID, *validated.Name, state, created, created); err != nil {
			return err
		}
		active = append(active, "")
		copy(active[position+1:], active[position:])
		active[position] = id
		if err := assignLiveColumnPositionsTx(ctx, tx, projectID, active, created, id); err != nil {
			return err
		}
		_, err = insertEvent(ctx, tx, "column.created", actorID, projectID, "", map[string]any{"column_id": id, "semantic_state": state, "position": position, "version": 1})
		return err
	})
	if err != nil {
		return Column{}, err
	}
	return s.GetColumn(ctx, id)
}

func (s *Store) UpdateColumn(ctx context.Context, id string, input ColumnInput, actorID string) (Column, error) {
	return s.UpdateColumnWithVersion(ctx, id, input, actorID, false, nil)
}

// UpdateColumnWithClaimOverride updates a column and synchronizes the derived
// completion fields on all of its live tasks when the semantic state changes.
// A human administrator may explicitly override active claims; all other
// callers are rejected before any column or task row is changed.
func (s *Store) UpdateColumnWithClaimOverride(ctx context.Context, id string, input ColumnInput, actorID string, allowClaimOverride bool) (Column, error) {
	return s.UpdateColumnWithVersion(ctx, id, input, actorID, allowClaimOverride, nil)
}

// UpdateColumnWithVersion updates a column's label, semantic mapping, board
// order, or archive state in one transaction. Active task rows are rehomed to
// another column with the same semantic state before an archived column is
// hidden. The five semantic states must always retain at least one active
// column, and expected guards make stale administration edits recoverable.
func (s *Store) UpdateColumnWithVersion(ctx context.Context, id string, input ColumnInput, actorID string, allowClaimOverride bool, expected *int64) (Column, error) {
	validated, err := validateColumn(input, false)
	if err != nil {
		return Column{}, err
	}
	if expected != nil && *expected <= 0 {
		return Column{}, ErrPrecondition
	}
	updated := now()
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, columnSelect+` WHERE id=?`, id)
		current, err := columnFromRow(row)
		if errors.Is(err, sql.ErrNoRows) {
			return notFound("column not found")
		}
		if err != nil {
			return err
		}
		name, state, position := current.Name, current.SemanticState, current.Position
		archived := current.ArchivedAt != nil
		if validated.Name != nil {
			name = *validated.Name
		}
		if validated.SemanticState != nil {
			state = *validated.SemanticState
		}
		if validated.Position != nil {
			position = *validated.Position
		}
		if validated.Archived != nil {
			archived = *validated.Archived
		}
		stateChanged := state != current.SemanticState
		willBeLive := !archived
		wasLive := current.ArchivedAt == nil
		// The unguarded legacy entry point historically allowed semantic
		// remapping (including the final column for a state). Keep that contract
		// for existing automation, while versioned administration edits enforce
		// the invariant so the new UI cannot remove a state's only mapping.
		if wasLive && (!willBeLive || (stateChanged && expected != nil)) {
			var stateCount int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM columns WHERE project_id=? AND semantic_state=? AND archived_at IS NULL`, current.ProjectID, current.SemanticState).Scan(&stateCount); err != nil {
				return err
			}
			if stateCount <= 1 {
				return &Error{Kind: ErrConflict, Message: fmt.Sprintf("cannot remove the only active column mapped to %s", current.SemanticState), Details: map[string]any{"column_id": id, "semantic_state": current.SemanticState}}
			}
		}
		if stateChanged && wasLive && !allowClaimOverride {
			claimed, details, claimErr := activeClaimInColumnTx(ctx, tx, id, updated)
			if claimErr != nil {
				return claimErr
			}
			if claimed {
				return &Error{Kind: ErrClaimUnavailable, Message: "column contains actively claimed tasks", Details: details}
			}
		}
		// A temporary negative position lets the partial unique index accept a
		// reorder/restore before all active rows are assigned their final slots.
		query := `UPDATE columns SET name=?, semantic_state=?, position=-1, archived_at=?, version=version+1, updated_at=? WHERE id=?`
		args := []any{name, state, nullableTimeValue(archived, updated), updated, id}
		if expected != nil {
			query += ` AND version=?`
			args = append(args, *expected)
		}
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return mapDependencyLifecycleError(ctx, tx, err, dependencyLifecycleTarget{ColumnID: id, NextSemanticState: state})
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			if expected != nil {
				var currentVersion int64
				if versionErr := tx.QueryRowContext(ctx, `SELECT version FROM columns WHERE id=?`, id).Scan(&currentVersion); errors.Is(versionErr, sql.ErrNoRows) {
					return ErrNotFound
				} else if versionErr != nil {
					return versionErr
				}
				return conflict("column has changed", map[string]any{"column_id": id, "expected_version": *expected, "current_version": currentVersion})
			}
			return ErrNotFound
		}
		var dependencyChanges []dependencyStateChange
		checklistStatus := checklistCompletionStatus{}
		if stateChanged && wasLive && willBeLive {
			if err := validateColumnBugLifecycleTransitionTx(ctx, tx, id, state); err != nil {
				return err
			}
		}
		if stateChanged && wasLive && willBeLive && state == "completed" {
			// The guarded column update has acquired the writer transaction, so
			// require-policy rejection rolls back the column itself before any
			// task, dependency, archive, or activity row is changed.
			checklistStatus, err = checklistCompletionStatusForColumnTx(ctx, tx, current.ProjectID, id)
			if err != nil {
				return err
			}
			if err := rejectIncompleteChecklist(checklistStatus); err != nil {
				return err
			}
		}
		// Changing the mapping of a live column updates the derived completion
		// state of its tasks. An archived column is drained below, so applying
		// the intermediate mapping to those tasks would briefly (and
		// incorrectly) mark them complete before moving them to the fallback
		// column.
		if stateChanged && wasLive && willBeLive {
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
		var taskCount int
		if !willBeLive && wasLive {
			destination, destinationErr := liveColumnForStateTx(ctx, tx, current.ProjectID, current.SemanticState, id)
			if destinationErr != nil {
				return destinationErr
			}
			taskIDs, taskErr := taskIDsInColumnTx(ctx, tx, id)
			if taskErr != nil {
				return taskErr
			}
			taskCount = len(taskIDs)
			var nextDestinationPosition float64
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position)+1, 0) FROM tasks WHERE column_id=? AND deleted_at IS NULL`, destination.ID).Scan(&nextDestinationPosition); err != nil {
				return err
			}
			for index, taskID := range taskIDs {
				if _, err := tx.ExecContext(ctx, `UPDATE tasks SET column_id=?, position=?, version=version+1, updated_at=? WHERE id=? AND deleted_at IS NULL`, destination.ID, nextDestinationPosition+float64(index), updated, taskID); err != nil {
					return err
				}
			}
		}
		if willBeLive {
			active, activeErr := liveColumnIDsTx(ctx, tx, current.ProjectID)
			if activeErr != nil {
				return activeErr
			}
			ordered := moveColumnID(active, id, position)
			if err := assignLiveColumnPositionsTx(ctx, tx, current.ProjectID, ordered, updated, id); err != nil {
				return err
			}
		} else {
			// Removing a live column leaves a gap unless the remaining board rows
			// are compacted before the transaction commits. Keep active positions
			// contiguous so a subsequent restore can use an intentional position
			// and board clients never render misleading ordinal labels.
			active, activeErr := liveColumnIDsTx(ctx, tx, current.ProjectID)
			if activeErr != nil {
				return activeErr
			}
			if err := assignLiveColumnPositionsTx(ctx, tx, current.ProjectID, active, updated); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE columns SET position=?, updated_at=? WHERE id=?`, position, updated, id); err != nil {
				return err
			}
		}
		eventPayload := map[string]any{"column_id": id, "name": name, "semantic_state": state, "archived": archived, "position": position, "task_count": taskCount, "version": current.Version + 1}
		if stateChanged && wasLive && willBeLive && state == "completed" {
			addChecklistCompletionEventFields(eventPayload, checklistStatus)
		}
		if _, err = insertEvent(ctx, tx, "column.updated", actorID, current.ProjectID, "", eventPayload); err != nil {
			return err
		}
		return emitDependencyStateChanges(ctx, tx, actorID, dependencyChanges)
	})
	if err != nil {
		return Column{}, err
	}
	return s.GetColumn(ctx, id)
}

func nullableTimeValue(enabled bool, timestamp string) any {
	if enabled {
		return timestamp
	}
	return nil
}

func liveColumnIDsTx(ctx context.Context, tx *sql.Tx, projectID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM columns WHERE project_id=? AND archived_at IS NULL ORDER BY position, id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func assignLiveColumnPositionsTx(ctx context.Context, tx *sql.Tx, projectID string, ids []string, timestamp string, unchangedVersionID ...string) error {
	// SQLite checks the partial unique index as each row is updated. Move every
	// live position into a disjoint negative range first; a simple sign flip
	// would collide with the row currently occupying the destination slot.
	if _, err := tx.ExecContext(ctx, `UPDATE columns SET position=-1000000-position WHERE project_id=? AND archived_at IS NULL`, projectID); err != nil {
		return err
	}
	keepVersionID := ""
	if len(unchangedVersionID) > 0 {
		keepVersionID = unchangedVersionID[0]
	}
	for position, id := range ids {
		// A guarded metadata edit must also detect a concurrent reorder that
		// shifted this row. Bump versions only when the position actually
		// changed, while leaving the row whose mutation already incremented its
		// version at exactly one increment for this transaction.
		if _, err := tx.ExecContext(ctx, `UPDATE columns SET position=?, version=CASE WHEN id=? OR position=? THEN version ELSE version+1 END, updated_at=? WHERE id=? AND project_id=? AND archived_at IS NULL`, position, keepVersionID, -1000000-position, timestamp, id, projectID); err != nil {
			return err
		}
	}
	return nil
}

func moveColumnID(ids []string, target string, position int) []string {
	ordered := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != target {
			ordered = append(ordered, id)
		}
	}
	if position < 0 {
		position = 0
	}
	if position > len(ordered) {
		position = len(ordered)
	}
	ordered = append(ordered, "")
	copy(ordered[position+1:], ordered[position:])
	ordered[position] = target
	return ordered
}

func liveColumnForStateTx(ctx context.Context, tx *sql.Tx, projectID, state, excludeID string) (Column, error) {
	row := tx.QueryRowContext(ctx, columnSelect+` WHERE project_id=? AND semantic_state=? AND archived_at IS NULL AND id<>? ORDER BY position, id LIMIT 1`, projectID, state, excludeID)
	column, err := columnFromRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Column{}, &Error{Kind: ErrConflict, Message: fmt.Sprintf("cannot archive the only active column mapped to %s", state), Details: map[string]any{"semantic_state": state}}
	}
	return column, err
}

func taskIDsInColumnTx(ctx context.Context, tx *sql.Tx, columnID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM tasks WHERE column_id=? AND deleted_at IS NULL ORDER BY position, id`, columnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
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

// authoritativeColumnStateTx reads a live project column after the caller has
// acquired SQLite's writer transaction. Task mutations use this to close the
// gap between their read-only preflight and guarded write when an administrator
// concurrently archives or reclassifies a column.
func authoritativeColumnStateTx(ctx context.Context, tx *sql.Tx, columnID, projectID string) (string, error) {
	var actualProjectID, state string
	var archivedAt sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT project_id, semantic_state, archived_at FROM columns WHERE id=?`, columnID).Scan(&actualProjectID, &state, &archivedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", notFound("column not found")
	}
	if err != nil {
		return "", err
	}
	if actualProjectID != projectID {
		return "", invalid("column belongs to another project", nil)
	}
	if archivedAt.Valid {
		return "", conflict("column changed during the task update", map[string]any{"column_id": columnID, "archived": true})
	}
	return state, nil
}

// validateColumnBugLifecycleTransitionTx keeps the invariant between a bug's
// resolution and its column mapping when an administrator changes a live
// column's semantic state. Returning an error rolls back the guarded column
// update and every derived task change in the same transaction.
func validateColumnBugLifecycleTransitionTx(ctx context.Context, tx *sql.Tx, columnID, state string) error {
	var taskID string
	var err error
	if state == "completed" {
		err = tx.QueryRowContext(ctx, `SELECT t.id FROM tasks t LEFT JOIN bug_details b ON b.task_id=t.id WHERE t.column_id=? AND t.deleted_at IS NULL AND t.kind='bug' AND b.resolution IS NULL ORDER BY t.id LIMIT 1`, columnID).Scan(&taskID)
		if err == nil {
			return conflict("column contains an unresolved bug", map[string]any{"column_id": columnID, "task_id": taskID, "semantic_state": state})
		}
	} else {
		err = tx.QueryRowContext(ctx, `SELECT t.id FROM tasks t JOIN bug_details b ON b.task_id=t.id WHERE t.column_id=? AND t.deleted_at IS NULL AND t.kind='bug' AND b.resolution IS NOT NULL ORDER BY t.id LIMIT 1`, columnID).Scan(&taskID)
		if err == nil {
			return conflict("column contains a resolved bug", map[string]any{"column_id": columnID, "task_id": taskID, "semantic_state": state})
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

func (s *Store) StateColumn(ctx context.Context, projectID, state string) (Column, error) {
	row := s.DB.QueryRowContext(ctx, columnSelect+` WHERE project_id = ? AND semantic_state = ? AND archived_at IS NULL ORDER BY position LIMIT 1`, projectID, state)
	column, err := columnFromRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Column{}, notFound(fmt.Sprintf("%s column not found", state))
	}
	return column, err
}
