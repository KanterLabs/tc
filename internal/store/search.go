package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxSavedViewName        = 200
	maxSavedViewDescription = 2000
	maxSavedViewJSONBytes   = 64 * 1024
)

var searchSortFields = map[string]string{
	"updated_at": "t.updated_at",
	"created_at": "t.created_at",
	"due_at":     "t.due_at",
	"title":      "lower(t.title)",
	"key":        "lower(p.key || '-' || CAST(t.number AS TEXT))",
	"priority":   "CASE t.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END",
	"state":      "c.position",
	"position":   "t.position",
}

// ListSearchTasks returns one bounded page of globally searchable tasks. The
// effective project ceiling is included in the SQL WHERE clause before LIMIT
// and OFFSET, so inaccessible matches cannot consume or reveal a page slot.
func (s *Store) ListSearchTasks(ctx context.Context, filter SearchFilter) ([]Task, bool, error) {
	return s.listSearchTasks(ctx, filter, false)
}

// ListSearchTasksWithExtra fetches one sentinel row to determine whether a
// cursor page has more results while keeping the public page cap at 200.
func (s *Store) ListSearchTasksWithExtra(ctx context.Context, filter SearchFilter) ([]Task, bool, error) {
	return s.listSearchTasks(ctx, filter, true)
}

// ListSearchProjects returns the small project metadata set used by command
// search. It intentionally skips the board-count enrichment performed by the
// normal project collection: command search only needs navigation labels and
// should remain bounded even when a workspace has many projects.
func (s *Store) ListSearchProjects(ctx context.Context, projectIDs []string, queryValue string) ([]Project, error) {
	query := projectSelect + ` WHERE archived_at IS NULL`
	args := make([]any, 0, len(projectIDs)+1)
	if projectIDs != nil && len(projectIDs) == 0 {
		query += ` AND 1=0`
	}
	if len(projectIDs) > 0 {
		query += ` AND id IN (` + placeholders(len(projectIDs)) + `)`
		for _, projectID := range projectIDs {
			args = append(args, projectID)
		}
	}
	if strings.TrimSpace(queryValue) != "" {
		value := containsSearchValue(queryValue)
		query += ` AND (lower(name) LIKE ? OR lower(key) LIKE ? OR lower(slug) LIKE ?)`
		args = append(args, value, value, value)
	}
	query += ` ORDER BY favorite DESC, lower(name), id LIMIT 200`
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Project, 0)
	for rows.Next() {
		project, scanErr := projectFromRow(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, project)
	}
	return result, rows.Err()
}

func (s *Store) listSearchTasks(ctx context.Context, filter SearchFilter, allowExtra bool) ([]Task, bool, error) {
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	maxLimit := 200
	if allowExtra {
		maxLimit++
	}
	if filter.Limit > maxLimit {
		filter.Limit = maxLimit
	}
	if filter.Cursor < 0 {
		return nil, false, invalid("cursor must be non-negative", nil)
	}
	query := `SELECT ` + taskColumns + ` FROM tasks t
	JOIN projects p ON p.id=t.project_id
	JOIN columns c ON c.id=t.column_id
	WHERE t.deleted_at IS NULL AND p.archived_at IS NULL`
	args := make([]any, 0, 20)
	if filter.ProjectIDs != nil && len(filter.ProjectIDs) == 0 {
		query += ` AND 1=0`
	}
	if len(filter.ProjectIDs) > 0 {
		query += ` AND t.project_id IN (` + placeholders(len(filter.ProjectIDs)) + `)`
		for _, projectID := range filter.ProjectIDs {
			args = append(args, projectID)
		}
	}
	if filter.Project != "" {
		query += ` AND (p.id=? OR lower(p.key)=lower(?) OR lower(p.slug)=lower(?))`
		args = append(args, filter.Project, filter.Project, filter.Project)
	}
	if filter.State != "" {
		query += ` AND c.semantic_state=?`
		args = append(args, filter.State)
	}
	if filter.Priority != "" {
		query += ` AND t.priority=?`
		args = append(args, filter.Priority)
	}
	if filter.Key != "" {
		query += ` AND lower(p.key || '-' || CAST(t.number AS TEXT)) LIKE ?`
		args = append(args, containsSearchValue(filter.Key))
	}
	if filter.Title != "" {
		query += ` AND lower(t.title) LIKE ?`
		args = append(args, containsSearchValue(filter.Title))
	}
	if filter.Description != "" {
		query += ` AND lower(t.description) LIKE ?`
		args = append(args, containsSearchValue(filter.Description))
	}
	if filter.Label != "" {
		query += ` AND EXISTS (SELECT 1 FROM task_labels tl JOIN labels l ON l.id=tl.label_id WHERE tl.task_id=t.id AND (l.id=? OR lower(l.name) LIKE ?))`
		args = append(args, filter.Label, containsSearchValue(filter.Label))
	}
	if filter.Assignee != "" {
		query += ` AND ((? = 'none' AND t.assignee_id IS NULL) OR EXISTS (SELECT 1 FROM actors a WHERE a.id=t.assignee_id AND (a.id=? OR lower(a.name) LIKE ?)))`
		args = append(args, strings.ToLower(strings.TrimSpace(filter.Assignee)), filter.Assignee, containsSearchValue(filter.Assignee))
	}
	if filter.ClaimOwner != "" {
		query += ` AND ((? = 'none' AND t.claimed_by IS NULL) OR EXISTS (SELECT 1 FROM actors a WHERE a.id=t.claimed_by AND (a.id=? OR lower(a.name) LIKE ?)))`
		args = append(args, strings.ToLower(strings.TrimSpace(filter.ClaimOwner)), filter.ClaimOwner, containsSearchValue(filter.ClaimOwner))
	}
	if filter.DueFrom != nil {
		query += ` AND t.due_at IS NOT NULL AND t.due_at >= ?`
		args = append(args, filter.DueFrom.UTC().Format(time.RFC3339Nano))
	}
	if filter.DueTo != nil {
		query += ` AND t.due_at IS NOT NULL AND t.due_at <= ?`
		args = append(args, filter.DueTo.UTC().Format(time.RFC3339Nano))
	}
	if filter.Query != "" {
		q := containsSearchValue(filter.Query)
		query += ` AND (
			lower(p.key || '-' || CAST(t.number AS TEXT)) LIKE ?
			OR lower(t.title) LIKE ?
			OR lower(t.description) LIKE ?
			OR lower(p.name) LIKE ?
			OR lower(p.key) LIKE ?
			OR EXISTS (SELECT 1 FROM labels l JOIN task_labels tl ON tl.label_id=l.id WHERE tl.task_id=t.id AND lower(l.name) LIKE ?)
			OR lower(c.name) LIKE ?
			OR lower(c.semantic_state) LIKE ?
			OR lower(t.priority) LIKE ?
			OR EXISTS (SELECT 1 FROM actors a WHERE (a.id=t.assignee_id OR a.id=t.claimed_by) AND lower(a.name) LIKE ?)
			OR EXISTS (SELECT 1 FROM bug_details bd WHERE bd.task_id=t.id AND (lower(bd.actual_behavior) LIKE ? OR lower(bd.expected_behavior) LIKE ? OR lower(bd.reproduction_steps) LIKE ? OR lower(bd.environment) LIKE ? OR lower(bd.affected_version) LIKE ?))
		)`
		for i := 0; i < 15; i++ {
			args = append(args, q)
		}
	}
	orderBy, err := searchOrder(filter.Sort)
	if err != nil {
		return nil, false, err
	}
	query += ` ORDER BY ` + orderBy + `, t.updated_at DESC, t.id DESC LIMIT ? OFFSET ?`
	args = append(args, filter.Limit+1, filter.Cursor)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	result := make([]Task, 0, filter.Limit)
	for rows.Next() {
		task, scanErr := taskFromRow(rows)
		if scanErr != nil {
			return nil, false, scanErr
		}
		result = append(result, task)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(result) > filter.Limit
	if hasMore {
		result = result[:filter.Limit]
	}
	readAt := time.Now().UTC()
	for i := range result {
		if err := s.enrichTaskAt(ctx, &result[i], readAt); err != nil {
			return nil, false, err
		}
	}
	if err := s.populateTaskDependencySummaries(ctx, result); err != nil {
		return nil, false, err
	}
	return result, hasMore, nil
}

func placeholders(count int) string {
	if count <= 0 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", count), ",")
}

func containsSearchValue(value string) string {
	return "%" + strings.ToLower(strings.TrimSpace(value)) + "%"
}

func searchOrder(sortTerms []SearchSort) (string, error) {
	terms := make([]string, 0, len(sortTerms))
	for _, term := range sortTerms {
		field := strings.ToLower(strings.TrimSpace(term.Field))
		expression, ok := searchSortFields[field]
		if !ok {
			return "", invalid("sort field is invalid", map[string]any{"field": term.Field})
		}
		direction := strings.ToLower(strings.TrimSpace(term.Direction))
		if direction == "" {
			direction = "asc"
		}
		if direction != "asc" && direction != "desc" {
			return "", invalid("sort direction must be asc or desc", map[string]any{"direction": term.Direction})
		}
		terms = append(terms, expression+" "+direction)
	}
	if len(terms) == 0 {
		return "t.updated_at DESC", nil
	}
	return strings.Join(terms, ", "), nil
}

func savedViewFromRow(scanner interface{ Scan(...any) error }) (SavedView, error) {
	var view SavedView
	var filters, sortJSON string
	var shared int
	if err := scanner.Scan(&view.ID, &view.ActorID, &view.Name, &view.Description, &filters, &sortJSON, &shared, &view.CreatedAt, &view.UpdatedAt); err != nil {
		return SavedView{}, err
	}
	if err := json.Unmarshal([]byte(filters), &view.Filters); err != nil {
		return SavedView{}, fmt.Errorf("decode saved view filters: %w", err)
	}
	if view.Filters == nil {
		view.Filters = map[string]any{}
	}
	if err := json.Unmarshal([]byte(sortJSON), &view.Sort); err != nil {
		return SavedView{}, fmt.Errorf("decode saved view sort: %w", err)
	}
	if view.Sort == nil {
		view.Sort = []SearchSort{}
	}
	view.Shared = shared != 0
	return view, nil
}

func normalizeSavedViewInput(input SavedViewInput, creating bool, current *SavedView) (SavedViewInput, error) {
	if creating && !input.FiltersSet {
		input.Filters = map[string]any{}
		input.FiltersSet = true
	}
	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if name == "" || utf8.RuneCountInString(name) > maxSavedViewName {
			return SavedViewInput{}, invalid("view name must be between 1 and 200 characters", nil)
		}
		input.Name = &name
	} else if creating {
		return SavedViewInput{}, invalid("view name is required", nil)
	}
	if input.Description != nil {
		description := strings.TrimSpace(*input.Description)
		if utf8.RuneCountInString(description) > maxSavedViewDescription {
			return SavedViewInput{}, invalid("view description is too long", nil)
		}
		input.Description = &description
	}
	if input.FiltersSet {
		if input.Filters == nil {
			input.Filters = map[string]any{}
		}
		encoded, err := json.Marshal(input.Filters)
		if err != nil || len(encoded) > maxSavedViewJSONBytes {
			return SavedViewInput{}, invalid("view filters are invalid or too large", nil)
		}
	}
	if input.SortSet {
		if len(input.Sort) > 8 {
			return SavedViewInput{}, invalid("view sort has too many fields", nil)
		}
		for _, term := range input.Sort {
			if _, ok := searchSortFields[strings.ToLower(strings.TrimSpace(term.Field))]; !ok {
				return SavedViewInput{}, invalid("sort field is invalid", map[string]any{"field": term.Field})
			}
			direction := strings.ToLower(strings.TrimSpace(term.Direction))
			if direction != "asc" && direction != "desc" {
				return SavedViewInput{}, invalid("sort direction must be asc or desc", map[string]any{"direction": term.Direction})
			}
		}
	}
	if current != nil {
		if input.Name == nil {
			name := current.Name
			input.Name = &name
		}
		if input.Description == nil {
			description := current.Description
			input.Description = &description
		}
		if !input.FiltersSet {
			input.Filters = current.Filters
			input.FiltersSet = true
		}
		if !input.SortSet {
			input.Sort = current.Sort
			input.SortSet = true
		}
		if input.Shared == nil {
			shared := current.Shared
			input.Shared = &shared
		}
	}
	return input, nil
}

func (s *Store) CreateSavedView(ctx context.Context, actorID string, input SavedViewInput) (SavedView, error) {
	validated, err := normalizeSavedViewInput(input, true, nil)
	if err != nil {
		return SavedView{}, err
	}
	filtersJSON, err := json.Marshal(validated.Filters)
	if err != nil {
		return SavedView{}, invalid("view filters are invalid", nil)
	}
	if validated.Sort == nil {
		validated.Sort = []SearchSort{}
	}
	sortJSON, err := json.Marshal(validated.Sort)
	if err != nil {
		return SavedView{}, invalid("view sort is invalid", nil)
	}
	description := ""
	if validated.Description != nil {
		description = *validated.Description
	}
	shared := false
	if validated.Shared != nil {
		shared = *validated.Shared
	}
	id, created := newID(), now()
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO saved_views(id, actor_id, name, description, filters, sort, shared, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, actorID, *validated.Name, description, string(filtersJSON), string(sortJSON), boolInt(shared), created, created); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				return &Error{Kind: ErrAlreadyExists, Message: "a view with this name already exists"}
			}
			return err
		}
		_, err := insertEvent(ctx, tx, "view.created", actorID, "", "", map[string]any{"view_id": id, "name": *validated.Name, "shared": shared})
		return err
	})
	if err != nil {
		return SavedView{}, err
	}
	return s.GetSavedView(ctx, id)
}

func (s *Store) GetSavedView(ctx context.Context, id string) (SavedView, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT id, actor_id, name, description, filters, sort, shared, created_at, updated_at FROM saved_views WHERE id=?`, id)
	view, err := savedViewFromRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return SavedView{}, notFound("view not found")
	}
	return view, err
}

// ListSavedViews returns views owned by actorID plus shared views. The result
// is deliberately small and server-filtered before pagination by the HTTP
// layer's project ceiling checks.
func (s *Store) ListSavedViews(ctx context.Context, actorID string, limit, offset int) ([]SavedView, bool, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id, actor_id, name, description, filters, sort, shared, created_at, updated_at FROM saved_views WHERE actor_id=? OR shared=1 ORDER BY CASE WHEN actor_id=? THEN 0 ELSE 1 END, lower(name), id LIMIT ? OFFSET ?`, actorID, actorID, limit+1, offset)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	result := make([]SavedView, 0, limit)
	for rows.Next() {
		view, scanErr := savedViewFromRow(rows)
		if scanErr != nil {
			return nil, false, scanErr
		}
		result = append(result, view)
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

func (s *Store) UpdateSavedView(ctx context.Context, id, actorID string, input SavedViewInput) (SavedView, error) {
	current, err := s.GetSavedView(ctx, id)
	if err != nil {
		return SavedView{}, err
	}
	if current.ActorID != actorID {
		return SavedView{}, forbidden("only the view owner can update it")
	}
	validated, err := normalizeSavedViewInput(input, false, &current)
	if err != nil {
		return SavedView{}, err
	}
	filtersJSON, err := json.Marshal(validated.Filters)
	if err != nil {
		return SavedView{}, invalid("view filters are invalid", nil)
	}
	if validated.Sort == nil {
		validated.Sort = []SearchSort{}
	}
	sortJSON, err := json.Marshal(validated.Sort)
	if err != nil {
		return SavedView{}, invalid("view sort is invalid", nil)
	}
	description := ""
	if validated.Description != nil {
		description = *validated.Description
	}
	shared := false
	if validated.Shared != nil {
		shared = *validated.Shared
	}
	updated := now()
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		result, updateErr := tx.ExecContext(ctx, `UPDATE saved_views SET name=?, description=?, filters=?, sort=?, shared=?, updated_at=? WHERE id=? AND actor_id=?`, *validated.Name, description, string(filtersJSON), string(sortJSON), boolInt(shared), updated, id, actorID)
		if updateErr != nil {
			if strings.Contains(strings.ToLower(updateErr.Error()), "unique") {
				return &Error{Kind: ErrAlreadyExists, Message: "a view with this name already exists"}
			}
			return updateErr
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			return notFound("view not found")
		}
		_, updateErr = insertEvent(ctx, tx, "view.updated", actorID, "", "", map[string]any{"view_id": id, "name": *validated.Name, "shared": shared})
		return updateErr
	})
	if err != nil {
		return SavedView{}, err
	}
	return s.GetSavedView(ctx, id)
}

func (s *Store) DeleteSavedView(ctx context.Context, id, actorID string) error {
	current, err := s.GetSavedView(ctx, id)
	if err != nil {
		return err
	}
	if current.ActorID != actorID {
		return forbidden("only the view owner can delete it")
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		result, deleteErr := tx.ExecContext(ctx, `DELETE FROM saved_views WHERE id=? AND actor_id=?`, id, actorID)
		if deleteErr != nil {
			return deleteErr
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			return notFound("view not found")
		}
		_, deleteErr = insertEvent(ctx, tx, "view.deleted", actorID, "", "", map[string]any{"view_id": id})
		return deleteErr
	})
}
