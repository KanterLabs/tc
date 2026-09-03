package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// Task claims default to a 30-minute lease when no duration is supplied.
	// Explicit durations are bounded to keep leases useful without allowing a
	// caller to hold work indefinitely.
	DefaultTaskClaimDuration = 30 * time.Minute
	MinTaskClaimDuration     = 30 * time.Second
	MaxTaskClaimDuration     = 7 * 24 * time.Hour
)

type TaskInput struct {
	Title       *string
	Description *string
	Priority    *string
	Kind        *string
	Bug         *BugInput
	BugSet      bool
	ColumnID    *string
	Position    *float64
	Assignee    *string
	AssigneeSet bool
	DueAt       *string
	DueAtSet    bool
	Labels      []string
	LabelsSet   bool
}

const (
	defaultTaskKind = "task"
	bugKind         = "bug"
)

func validateTaskInput(input TaskInput, creating bool) (TaskInput, error) {
	if creating && input.Title == nil {
		return TaskInput{}, invalid("title is required", nil)
	}
	if input.Title != nil {
		value := strings.TrimSpace(*input.Title)
		if value == "" || len(value) > 500 {
			return TaskInput{}, invalid("title must be between 1 and 500 characters", nil)
		}
		input.Title = &value
	}
	if input.Description != nil && len(*input.Description) > 100000 {
		return TaskInput{}, invalid("description is too long", nil)
	}
	if input.Priority != nil {
		value := *input.Priority
		if !validPriority(value) {
			return TaskInput{}, invalid("priority must be low, normal, high, or urgent", nil)
		}
		input.Priority = &value
	}
	if input.Kind != nil {
		value := strings.ToLower(strings.TrimSpace(*input.Kind))
		if value != defaultTaskKind && value != bugKind {
			return TaskInput{}, invalid("kind must be task or bug", nil)
		}
		input.Kind = &value
	}
	if input.Bug != nil {
		normalized, err := validateBugInput(*input.Bug, false)
		if err != nil {
			return TaskInput{}, err
		}
		input.Bug = &normalized
		input.BugSet = true
	}
	if creating {
		kind := defaultTaskKind
		if input.Kind != nil {
			kind = *input.Kind
		}
		if kind == bugKind {
			if input.Bug == nil {
				return TaskInput{}, invalid("bug details are required when kind is bug", nil)
			}
			if err := validateBugCreateInput(*input.Bug); err != nil {
				return TaskInput{}, err
			}
		} else if input.Bug != nil || input.BugSet {
			return TaskInput{}, invalid("bug details require kind bug", nil)
		}
	}
	if input.Position != nil && (*input.Position < 0 || *input.Position > 1e12) {
		return TaskInput{}, invalid("position is invalid", nil)
	}
	if input.DueAt != nil {
		parsed, err := parseTime(*input.DueAt)
		if err != nil {
			return TaskInput{}, invalid(err.Error(), nil)
		}
		input.DueAt = parsed
	}
	if len(input.Labels) > 100 {
		return TaskInput{}, invalid("too many labels", nil)
	}
	return input, nil
}

// validateBugInput normalizes patch values and enforces bounded text sizes.
// A missing field remains nil; an explicit Set flag lets callers clear a
// nullable or optional field without confusing it with omission.
func validateBugInput(input BugInput, creating bool) (BugInput, error) {
	if input.Severity != nil {
		value := strings.ToLower(strings.TrimSpace(*input.Severity))
		if !validBugSeverity(value) {
			return BugInput{}, invalid("severity must be s1, s2, s3, or s4", nil)
		}
		input.Severity = &value
	}
	if input.ActualBehavior != nil {
		value := strings.TrimSpace(*input.ActualBehavior)
		if value == "" {
			return BugInput{}, invalid("actual_behavior must not be empty", nil)
		}
		if len(value) > 100000 {
			return BugInput{}, invalid("actual_behavior is too long", nil)
		}
		input.ActualBehavior = &value
	}
	for _, field := range []struct {
		name  string
		value **string
		set   bool
	}{
		{"expected_behavior", &input.ExpectedBehavior, input.ExpectedBehaviorSet},
		{"reproduction_steps", &input.ReproductionSteps, input.ReproductionStepsSet},
		{"environment", &input.Environment, input.EnvironmentSet},
		{"affected_version", &input.AffectedVersion, input.AffectedVersionSet},
	} {
		if *field.value == nil {
			if field.set && creating {
				// Explicit null is represented as an empty optional value on
				// create; this keeps the stored shape deterministic.
			}
			continue
		}
		normalized := strings.TrimSpace(**field.value)
		if len(normalized) > 100000 {
			return BugInput{}, invalid(field.name+" is too long", nil)
		}
		*field.value = &normalized
	}
	return input, nil
}

func validateBugCreateInput(input BugInput) error {
	if input.ActualBehavior == nil || strings.TrimSpace(*input.ActualBehavior) == "" {
		return invalid("actual_behavior is required for bugs", nil)
	}
	return nil
}

func validBugSeverity(value string) bool {
	switch value {
	case "s1", "s2", "s3", "s4":
		return true
	}
	return false
}

func validBugResolution(value string) bool {
	switch value {
	case "fixed", "duplicate", "not_planned", "cannot_reproduce", "works_as_designed":
		return true
	}
	return false
}

func validPriority(value string) bool {
	switch value {
	case "low", "normal", "high", "urgent":
		return true
	}
	return false
}

func (s *Store) CreateTask(ctx context.Context, projectID string, input TaskInput, actorID string) (Task, error) {
	validated, err := validateTaskInput(input, true)
	if err != nil {
		return Task{}, err
	}
	kind := defaultTaskKind
	if validated.Kind != nil {
		kind = *validated.Kind
	}
	if kind == bugKind {
		// ReporterID is intentionally the authenticated server actor rather
		// than client-controlled bug input. Validate it before opening the
		// mutation transaction so malformed bug creates fail cleanly.
		if strings.TrimSpace(actorID) == "" {
			return Task{}, invalid("a reporter actor is required for bugs", nil)
		}
		if _, err := s.GetActor(ctx, actorID); err != nil {
			return Task{}, invalid("reporter actor not found", nil)
		}
	}
	project, err := s.GetProject(ctx, projectID)
	if err != nil {
		return Task{}, err
	}
	columnID := ""
	if validated.ColumnID != nil {
		columnID = strings.TrimSpace(*validated.ColumnID)
	}
	var column Column
	if columnID == "" {
		column, err = s.StateColumn(ctx, project.ID, "backlog")
	} else {
		column, err = s.GetColumn(ctx, columnID)
		if err == nil && column.ProjectID != project.ID {
			err = invalid("column belongs to another project", nil)
		}
	}
	if err != nil {
		return Task{}, err
	}
	if kind == bugKind && column.SemanticState == "completed" {
		return Task{}, invalid("bugs must be resolved before entering a completed column", nil)
	}
	priority := "normal"
	if validated.Priority != nil {
		priority = *validated.Priority
	}
	description := ""
	if validated.Description != nil {
		description = *validated.Description
	}
	position := 0.0
	if validated.Position != nil {
		position = *validated.Position
	} else {
		_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(position)+1, 0) FROM tasks WHERE column_id = ? AND deleted_at IS NULL`, column.ID).Scan(&position)
	}
	assignee := ""
	if validated.AssigneeSet && validated.Assignee != nil {
		assignee = strings.TrimSpace(*validated.Assignee)
	}
	if assignee != "" {
		if _, err := s.GetActor(ctx, assignee); err != nil {
			return Task{}, invalid("assignee actor not found", nil)
		}
	}
	dueAt := ""
	if validated.DueAtSet && validated.DueAt != nil {
		dueAt = *validated.DueAt
	}
	id, created := newID(), now()
	completedAt := any(nil)
	if column.SemanticState == "completed" {
		completedAt = created
	}
	var number int
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO project_counters(project_id, next_number) VALUES (?, 2) ON CONFLICT(project_id) DO UPDATE SET next_number = project_counters.next_number + 1`, project.ID); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT next_number - 1 FROM project_counters WHERE project_id = ?`, project.ID).Scan(&number); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tasks(id, project_id, number, column_id, kind, title, description, priority, position, assignee_id, due_at, version, completed_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), 1, ?, ?, ?)`, id, project.ID, number, column.ID, kind, *validated.Title, description, priority, position, assignee, dueAt, completedAt, created, created); err != nil {
			return err
		}
		if validated.LabelsSet {
			if err := replaceTaskLabels(ctx, tx, id, project.ID, validated.Labels); err != nil {
				return err
			}
		}
		if kind == bugKind {
			if err := insertBugDetailsTx(ctx, tx, id, actorID, *validated.Bug); err != nil {
				return err
			}
		}
		eventType := "task.created"
		if kind == bugKind {
			eventType = "bug.created"
		}
		_, err := insertEvent(ctx, tx, eventType, actorID, project.ID, id, map[string]any{"number": number})
		return err
	})
	if err != nil {
		return Task{}, err
	}
	return s.GetTask(ctx, id)
}

func (s *Store) GetTask(ctx context.Context, id string) (Task, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks t WHERE t.id = ? AND t.deleted_at IS NULL`, id)
	task, err := taskFromRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, notFound("task not found")
	}
	if err != nil {
		return Task{}, err
	}
	if err := s.enrichTask(ctx, &task); err != nil {
		return Task{}, err
	}
	if err := s.populateDependencySummary(ctx, &task); err != nil {
		return Task{}, err
	}
	return task, nil
}

// ResolveTaskReference accepts either an opaque task ID or the project-local
// task key (for example, OPS-42), and always returns the fully enriched task.
// Project keys are compared case-insensitively while opaque IDs retain their
// exact-match semantics.
func (s *Store) ResolveTaskReference(ctx context.Context, reference string) (Task, error) {
	reference = strings.TrimSpace(reference)
	row := s.DB.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks t JOIN projects p ON p.id=t.project_id WHERE t.deleted_at IS NULL AND (t.id=? OR lower(p.key || '-' || CAST(t.number AS TEXT))=lower(?)) LIMIT 1`, reference, reference)
	task, err := taskFromRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, notFound("task not found")
	}
	if err != nil {
		return Task{}, err
	}
	if err := s.enrichTask(ctx, &task); err != nil {
		return Task{}, err
	}
	if err := s.populateDependencySummary(ctx, &task); err != nil {
		return Task{}, err
	}
	return task, nil
}

func (s *Store) enrichTask(ctx context.Context, task *Task) error {
	return s.enrichTaskAt(ctx, task, time.Now().UTC())
}

func (s *Store) enrichTaskAt(ctx context.Context, task *Task, at time.Time) error {
	var key string
	if err := s.DB.QueryRowContext(ctx, `SELECT key FROM projects WHERE id = ?`, task.ProjectID).Scan(&key); err != nil {
		return err
	}
	task.Key = fmt.Sprintf("%s-%d", key, task.Number)
	rows, err := s.DB.QueryContext(ctx, `SELECT l.id, l.project_id, l.name, l.color, l.created_at, l.updated_at FROM labels l JOIN task_labels tl ON tl.label_id=l.id WHERE tl.task_id=? ORDER BY lower(l.name), l.id`, task.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		label, err := labelFromRow(rows)
		if err != nil {
			rows.Close()
			return err
		}
		task.Labels = append(task.Labels, label)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if task.Labels == nil {
		task.Labels = []Label{}
	}
	if task.Kind == bugKind {
		bug, err := s.getBugDetails(ctx, task.ID)
		if err != nil {
			return err
		}
		task.Bug = bug
	}
	work, err := s.agentWorkAt(ctx, task.ID, at)
	if err != nil {
		return err
	}
	task.AgentWork = work
	return s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM comments WHERE task_id=?`, task.ID).Scan(&task.CommentCount)
}

func (s *Store) ListTasks(ctx context.Context, projectID string, filter TaskFilter) ([]Task, bool, error) {
	return s.listTasks(ctx, projectID, filter, false)
}

// ListTasksWithExtra lets a cursor endpoint fetch one sentinel row while the
// externally visible page size remains capped at 200.
func (s *Store) ListTasksWithExtra(ctx context.Context, projectID string, filter TaskFilter) ([]Task, bool, error) {
	return s.listTasks(ctx, projectID, filter, true)
}

func (s *Store) listTasks(ctx context.Context, projectID string, filter TaskFilter, allowExtra bool) ([]Task, bool, error) {
	readAt := time.Now().UTC()
	staleCutoff := agentWorkStaleCutoff(readAt)
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
	query := `SELECT ` + taskColumns + ` FROM tasks t JOIN columns c ON c.id=t.column_id WHERE t.project_id = ? AND t.deleted_at IS NULL`
	args := []any{projectID}
	if filter.State != "" {
		query += ` AND c.semantic_state = ?`
		args = append(args, filter.State)
	}
	if filter.Column != "" {
		query += ` AND (t.column_id = ? OR lower(c.name) = lower(?))`
		args = append(args, filter.Column, filter.Column)
	}
	if filter.Priority != "" {
		query += ` AND t.priority = ?`
		args = append(args, filter.Priority)
	}
	if filter.Assignee != "" {
		if strings.EqualFold(filter.Assignee, "none") {
			query += ` AND t.assignee_id IS NULL`
		} else {
			query += ` AND t.assignee_id = ?`
			args = append(args, filter.Assignee)
		}
	}
	if filter.Kind != "" {
		query += ` AND lower(t.kind) = lower(?)`
		args = append(args, filter.Kind)
	}
	if filter.Severity != "" {
		if strings.EqualFold(filter.Severity, "none") || strings.EqualFold(filter.Severity, "untriaged") {
			query += ` AND EXISTS (SELECT 1 FROM bug_details bd WHERE bd.task_id=t.id AND bd.severity IS NULL)`
		} else {
			query += ` AND EXISTS (SELECT 1 FROM bug_details bd WHERE bd.task_id=t.id AND bd.severity=?)`
			args = append(args, filter.Severity)
		}
	}
	if filter.Reporter != "" {
		query += ` AND EXISTS (SELECT 1 FROM bug_details bd WHERE bd.task_id=t.id AND bd.reporter_id=?)`
		args = append(args, filter.Reporter)
	}
	if filter.Resolution != "" {
		if strings.EqualFold(filter.Resolution, "none") || strings.EqualFold(filter.Resolution, "unresolved") {
			query += ` AND EXISTS (SELECT 1 FROM bug_details bd WHERE bd.task_id=t.id AND bd.resolution IS NULL)`
		} else {
			query += ` AND EXISTS (SELECT 1 FROM bug_details bd WHERE bd.task_id=t.id AND bd.resolution=?)`
			args = append(args, filter.Resolution)
		}
	}
	if filter.Dependency != "" {
		switch strings.ToLower(strings.TrimSpace(filter.Dependency)) {
		case "blocked":
			query += ` AND EXISTS (
				SELECT 1
				FROM task_dependencies td
				JOIN tasks prerequisite ON prerequisite.id=td.prerequisite_task_id
				JOIN columns prerequisite_column ON prerequisite_column.id=prerequisite.column_id
				WHERE td.task_id=t.id
				  AND prerequisite.project_id=t.project_id
				  AND prerequisite.deleted_at IS NULL
				  AND (prerequisite.completed_at IS NULL OR prerequisite_column.semantic_state <> 'completed'))`
		case "ready":
			query += ` AND EXISTS (
				SELECT 1 FROM task_dependencies td
				JOIN tasks prerequisite ON prerequisite.id=td.prerequisite_task_id
				WHERE td.task_id=t.id
				  AND prerequisite.project_id=t.project_id
				  AND prerequisite.deleted_at IS NULL)
			AND NOT EXISTS (
				SELECT 1
				FROM task_dependencies td
				JOIN tasks prerequisite ON prerequisite.id=td.prerequisite_task_id
				JOIN columns prerequisite_column ON prerequisite_column.id=prerequisite.column_id
				WHERE td.task_id=t.id
				  AND prerequisite.project_id=t.project_id
				  AND prerequisite.deleted_at IS NULL
				  AND (prerequisite.completed_at IS NULL OR prerequisite_column.semantic_state <> 'completed'))`
		default:
			return nil, false, invalid("dependency must be blocked or ready", map[string]any{"dependency": filter.Dependency})
		}
	}
	// Completion suppresses the liveness classification of a retained
	// snapshot. Keep completed tasks visible in ordinary board listings, but
	// never let them satisfy an agent-state/action-needed filter.
	if filter.AgentState != "" || filter.ActionNeeded {
		query += ` AND t.completed_at IS NULL`
	}
	if filter.AgentState != "" {
		switch filter.AgentState {
		case "missing":
			query += ` AND NOT EXISTS (SELECT 1 FROM task_agent_work aw WHERE aw.task_id=t.id)`
		case "stale":
			query += ` AND EXISTS (SELECT 1 FROM task_agent_work aw WHERE aw.task_id=t.id AND julianday(aw.updated_at) <= julianday(?))`
			args = append(args, staleCutoff)
		default:
			query += ` AND EXISTS (SELECT 1 FROM task_agent_work aw WHERE aw.task_id=t.id AND aw.state=?)`
			args = append(args, filter.AgentState)
		}
	}
	if filter.ActionNeeded {
		query += ` AND EXISTS (SELECT 1 FROM task_agent_work aw WHERE aw.task_id=t.id AND (aw.state IN ('waiting', 'handoff') OR julianday(aw.updated_at) <= julianday(?)))`
		args = append(args, staleCutoff)
	}
	if filter.Label != "" {
		query += ` AND EXISTS (SELECT 1 FROM task_labels tl JOIN labels l ON l.id=tl.label_id WHERE tl.task_id=t.id AND (l.id=? OR lower(l.name)=lower(?)))`
		args = append(args, filter.Label, filter.Label)
	}
	if filter.Query != "" {
		query += ` AND (lower(t.title) LIKE ? OR lower(t.description) LIKE ? OR EXISTS (SELECT 1 FROM bug_details bd WHERE bd.task_id=t.id AND (lower(bd.actual_behavior) LIKE ? OR lower(bd.expected_behavior) LIKE ? OR lower(bd.reproduction_steps) LIKE ? OR lower(bd.environment) LIKE ? OR lower(bd.affected_version) LIKE ?)))`
		q := "%" + strings.ToLower(filter.Query) + "%"
		args = append(args, q, q, q, q, q, q, q)
	}
	if filter.UpdatedAfter != nil {
		query += ` AND t.updated_at > ?`
		args = append(args, filter.UpdatedAfter.UTC().Format(time.RFC3339Nano))
	}
	// Task numbers are only unique within a project and do not describe the
	// board ordering below. Treat Cursor as an opaque row offset so a page
	// boundary cannot skip rows whose numbers sort lower in a later column.
	query += ` ORDER BY c.position, t.position, t.number, t.id LIMIT ? OFFSET ?`
	args = append(args, filter.Limit+1, filter.Cursor)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	result := make([]Task, 0, filter.Limit)
	for rows.Next() {
		task, err := taskFromRow(rows)
		if err != nil {
			rows.Close()
			return nil, false, err
		}
		result = append(result, task)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, false, err
	}
	rows.Close()
	hasMore := len(result) > filter.Limit
	if hasMore {
		result = result[:filter.Limit]
	}
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

func (s *Store) UpdateTask(ctx context.Context, id string, input TaskInput, expected int64, actorID string) (Task, error) {
	return s.UpdateTaskWithClaimOverride(ctx, id, input, expected, actorID, false)
}

// UpdateTaskWithClaimOverride applies a task patch while atomically honoring
// an active claim owned by another actor. The override is deliberately
// explicit so a bearer token cannot inherit a human administrator's power.
func (s *Store) UpdateTaskWithClaimOverride(ctx context.Context, id string, input TaskInput, expected int64, actorID string, allowClaimOverride bool) (Task, error) {
	validated, err := validateTaskInput(input, false)
	if err != nil {
		return Task{}, err
	}
	if expected <= 0 {
		return Task{}, ErrPrecondition
	}
	current, err := s.GetTask(ctx, id)
	if err != nil {
		return Task{}, err
	}
	if current.Kind == "" {
		// Databases opened before migration 008 are upgraded before serving
		// requests, but retain the safe default for direct store test fixtures.
		current.Kind = defaultTaskKind
	}
	kind := current.Kind
	if validated.Kind != nil {
		kind = *validated.Kind
	}
	bugMutation := validated.BugSet || validated.Bug != nil
	// Supplying bug details is an ergonomic shorthand for a task-to-bug
	// conversion for direct store callers. HTTP clients normally send kind as
	// well, and an explicit task kind still rejects a bug object below.
	if validated.Kind == nil && current.Kind == defaultTaskKind && bugMutation {
		kind = bugKind
	}
	if kind == bugKind && current.Kind == defaultTaskKind && !bugMutation {
		return Task{}, invalid("bug details are required when changing kind to bug", nil)
	}
	if kind == defaultTaskKind && current.Kind == bugKind {
		return Task{}, invalid("a bug cannot be changed back to a task", nil)
	}
	if kind == defaultTaskKind && bugMutation {
		return Task{}, invalid("bug details require kind bug", nil)
	}
	if kind == bugKind && bugMutation && validated.Bug == nil {
		return Task{}, invalid("bug details cannot be null for a bug", nil)
	}
	if current.Kind == defaultTaskKind && kind == bugKind {
		if err := validateBugCreateInput(*validated.Bug); err != nil {
			return Task{}, err
		}
		if strings.TrimSpace(actorID) == "" {
			return Task{}, invalid("a reporter actor is required for bugs", nil)
		}
		if _, err := s.GetActor(ctx, actorID); err != nil {
			return Task{}, invalid("reporter actor not found", nil)
		}
	}
	title, description, priority, columnID, position := current.Title, current.Description, current.Priority, current.ColumnID, current.Position
	assignee := ""
	if current.Assignee != nil {
		assignee = *current.Assignee
	}
	dueAt := ""
	if current.DueAt != nil {
		dueAt = *current.DueAt
	}
	if validated.Title != nil {
		title = *validated.Title
	}
	if validated.Description != nil {
		description = *validated.Description
	}
	if validated.Priority != nil {
		priority = *validated.Priority
	}
	if validated.ColumnID != nil {
		columnID = *validated.ColumnID
	}
	if validated.Position != nil {
		position = *validated.Position
	}
	if validated.AssigneeSet {
		assignee = ""
		if validated.Assignee != nil {
			assignee = strings.TrimSpace(*validated.Assignee)
		}
	}
	if validated.DueAtSet {
		dueAt = ""
		if validated.DueAt != nil {
			dueAt = *validated.DueAt
		}
	}
	column, err := s.GetColumn(ctx, columnID)
	if err != nil {
		return Task{}, err
	}
	if column.ProjectID != current.ProjectID {
		return Task{}, invalid("column belongs to another project", nil)
	}
	if kind == bugKind && column.SemanticState == "completed" && (current.Bug == nil || current.Bug.Resolution == nil) {
		return Task{}, invalid("bugs must be resolved before entering a completed column", nil)
	}
	if kind == bugKind && current.Bug != nil && current.Bug.Resolution != nil && column.SemanticState != "completed" {
		return Task{}, invalid("resolved bugs must be reopened before leaving a completed column", nil)
	}
	if assignee != "" {
		if _, err := s.GetActor(ctx, assignee); err != nil {
			return Task{}, invalid("assignee actor not found", nil)
		}
	}
	updated := now()
	completedAt := any(nil)
	if column.SemanticState == "completed" {
		if current.CompletedAt != nil {
			completedAt = *current.CompletedAt
		} else {
			completedAt = updated
		}
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		query := `UPDATE tasks SET kind=?, title=?, description=?, priority=?, column_id=?, position=?, assignee_id=NULLIF(?, ''), due_at=NULLIF(?, ''), version=version+1, completed_at=?, updated_at=? WHERE id=? AND version=? AND deleted_at IS NULL`
		args := []any{kind, title, description, priority, columnID, position, assignee, dueAt, completedAt, updated, id, expected}
		if !allowClaimOverride {
			query += ` AND (claimed_by IS NULL OR claim_expires_at IS NULL OR julianday(claim_expires_at) <= julianday(?) OR claimed_by=?)`
			args = append(args, updated, actorID)
		}
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			if !allowClaimOverride {
				claimed, details, claimErr := activeClaimByOtherTx(ctx, tx, id, actorID, updated)
				if claimErr != nil {
					return claimErr
				}
				if claimed {
					return &Error{Kind: ErrClaimUnavailable, Message: "task is currently claimed by another actor", Details: details}
				}
			}
			return conflict("task has changed", map[string]any{"current": current})
		}
		if validated.LabelsSet {
			if err := replaceTaskLabels(ctx, tx, id, current.ProjectID, validated.Labels); err != nil {
				return err
			}
		}
		if current.Kind == defaultTaskKind && kind == bugKind {
			if err := insertBugDetailsTx(ctx, tx, id, actorID, *validated.Bug); err != nil {
				return err
			}
		} else if kind == bugKind && bugMutation {
			if err := updateBugDetailsTx(ctx, tx, id, *validated.Bug); err != nil {
				return err
			}
		}
		taskMutation := validated.Title != nil || validated.Description != nil || validated.Priority != nil || validated.ColumnID != nil || validated.Position != nil || validated.AssigneeSet || validated.DueAtSet || validated.LabelsSet || kind != current.Kind
		if taskMutation {
			if _, err = insertEvent(ctx, tx, "task.updated", actorID, current.ProjectID, id, map[string]any{"version": expected + 1}); err != nil {
				return err
			}
		}
		if kind == bugKind && (bugMutation || current.Kind != kind) {
			_, err = insertEvent(ctx, tx, "bug.updated", actorID, current.ProjectID, id, map[string]any{"version": expected + 1})
			return err
		}
		return nil
	})
	if err != nil {
		return Task{}, err
	}
	return s.GetTask(ctx, id)
}

func (s *Store) DeleteTask(ctx context.Context, id string, expected int64, actorID string) error {
	return s.DeleteTaskWithClaimOverride(ctx, id, expected, actorID, false)
}

// DeleteTaskWithClaimOverride is the delete counterpart to
// UpdateTaskWithClaimOverride.
func (s *Store) DeleteTaskWithClaimOverride(ctx context.Context, id string, expected int64, actorID string, allowClaimOverride bool) error {
	if expected <= 0 {
		return ErrPrecondition
	}
	current, err := s.GetTask(ctx, id)
	if err != nil {
		return err
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		updated := now()
		query := `UPDATE tasks SET deleted_at=?, updated_at=?, version=version+1 WHERE id=? AND version=? AND deleted_at IS NULL`
		args := []any{updated, updated, id, expected}
		if !allowClaimOverride {
			query += ` AND (claimed_by IS NULL OR claim_expires_at IS NULL OR julianday(claim_expires_at) <= julianday(?) OR claimed_by=?)`
			args = append(args, updated, actorID)
		}
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			if !allowClaimOverride {
				claimed, details, claimErr := activeClaimByOtherTx(ctx, tx, id, actorID, updated)
				if claimErr != nil {
					return claimErr
				}
				if claimed {
					return &Error{Kind: ErrClaimUnavailable, Message: "task is currently claimed by another actor", Details: details}
				}
			}
			return conflict("task has changed", map[string]any{"current": current})
		}
		_, err = insertEvent(ctx, tx, "task.deleted", actorID, current.ProjectID, id, map[string]any{"number": current.Number})
		return err
	})
	return err
}

// activeClaimByOtherTx reads the current row after a guarded mutation did not
// match. The mutation's SQL predicate is the authorization boundary; this
// follow-up only gives callers a useful error distinction from a stale version.
func activeClaimByOtherTx(ctx context.Context, tx *sql.Tx, id, actorID, timestamp string) (bool, map[string]any, error) {
	var claimedBy, claimExpiry sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT claimed_by, claim_expires_at FROM tasks WHERE id=? AND deleted_at IS NULL`, id).Scan(&claimedBy, &claimExpiry)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	if !claimedBy.Valid || claimedBy.String == actorID || !claimExpiry.Valid {
		return false, nil, nil
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE id=? AND deleted_at IS NULL AND claimed_by IS NOT NULL AND claimed_by<>? AND claim_expires_at IS NOT NULL AND julianday(claim_expires_at) > julianday(?))`, id, actorID, timestamp).Scan(&active); err != nil {
		return false, nil, err
	}
	if active == 0 {
		return false, nil, nil
	}
	return true, map[string]any{"claimed_by": claimedBy.String, "claim_expires_at": claimExpiry.String}, nil
}

// taskClaimStateTx returns the current version and whether actorID owns an
// active lease. It is used only after a guarded UPDATE matched zero rows, so
// the authorization decision itself remains in the UPDATE predicate.
func taskClaimStateTx(ctx context.Context, tx *sql.Tx, id, actorID string) (int64, bool, error) {
	var version int64
	var claimedBy, claimExpiry sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT version, claimed_by, claim_expires_at FROM tasks WHERE id=? AND deleted_at IS NULL`, id).Scan(&version, &claimedBy, &claimExpiry)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	var activeValue int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE id=? AND deleted_at IS NULL AND claimed_by=? AND claim_expires_at IS NOT NULL AND julianday(claim_expires_at) > julianday('now'))`, id, actorID).Scan(&activeValue); err != nil {
		return 0, false, err
	}
	active := claimedBy.Valid && claimedBy.String == actorID && claimExpiry.Valid && activeValue != 0
	return version, active, nil
}

func replaceTaskLabels(ctx context.Context, tx *sql.Tx, taskID, projectID string, values []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM task_labels WHERE task_id=?`, taskID); err != nil {
		return err
	}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		var id string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM labels WHERE project_id=? AND (id=? OR lower(name)=lower(?))`, projectID, value, value).Scan(&id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return invalid("label not found: "+value, nil)
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO task_labels(task_id,label_id) VALUES (?,?)`, taskID, id); err != nil {
			return err
		}
	}
	return nil
}

// NormalizeTaskClaimDuration applies the lease contract shared by direct
// store callers and HTTP handlers. A zero duration means the caller omitted a
// duration and receives the documented default; every explicit duration must
// be within the inclusive minimum and maximum.
func NormalizeTaskClaimDuration(duration time.Duration) (time.Duration, error) {
	if duration == 0 {
		return DefaultTaskClaimDuration, nil
	}
	if duration < MinTaskClaimDuration || duration > MaxTaskClaimDuration {
		return 0, invalid("lease duration must be between 30 and 604800 seconds", nil)
	}
	return duration, nil
}

func (s *Store) ClaimTask(ctx context.Context, id, actorID string, duration time.Duration, expected int64) (Task, error) {
	var err error
	duration, err = NormalizeTaskClaimDuration(duration)
	if err != nil {
		return Task{}, err
	}
	current, err := s.GetTask(ctx, id)
	if err != nil {
		return Task{}, err
	}
	// Keep the duration as a SQLite date modifier. The clock itself is read by
	// the guarded UPDATE, after it acquires SQLite's writer lock; calculating an
	// expiry or updated_at value in Go here would let lock wait time age both
	// values before the claim is authorized.
	expiresModifier := fmt.Sprintf("+%.9f seconds", duration.Seconds())
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE tasks SET claimed_by=?, claim_expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now',?), version=version+1, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND version=? AND deleted_at IS NULL AND completed_at IS NULL AND (claimed_by=? OR claimed_by IS NULL OR claim_expires_at IS NULL OR julianday(claim_expires_at) <= julianday('now'))`, actorID, expiresModifier, id, expected, actorID)
		if err != nil {
			return err
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			if current.Version != expected {
				return conflict("task has changed", map[string]any{"current": current})
			}
			if current.CompletedAt != nil {
				return conflict("task is already finished", nil)
			}
			return &Error{Kind: ErrClaimUnavailable, Message: "task is currently claimed", Details: map[string]any{"current": current}}
		}
		var expires string
		if err := tx.QueryRowContext(ctx, `SELECT claim_expires_at FROM tasks WHERE id=?`, id).Scan(&expires); err != nil {
			return err
		}
		_, err = insertEvent(ctx, tx, "task.claimed", actorID, current.ProjectID, id, map[string]any{"expires_at": expires})
		return err
	})
	if err != nil {
		return Task{}, err
	}
	return s.GetTask(ctx, id)
}

func (s *Store) RenewTask(ctx context.Context, id, actorID string, duration time.Duration, expected int64) (Task, error) {
	return s.renewTask(ctx, id, actorID, duration, expected, false)
}

// RenewTaskWithClaim is the bearer-token variant. RenewTask already checks
// ownership and lease expiry in SQL; this variant additionally reports a
// missing/expired lease as forbidden rather than a generic version conflict.
func (s *Store) RenewTaskWithClaim(ctx context.Context, id, actorID string, duration time.Duration, expected int64) (Task, error) {
	return s.renewTask(ctx, id, actorID, duration, expected, true)
}

func (s *Store) renewTask(ctx context.Context, id, actorID string, duration time.Duration, expected int64, requireActiveClaim bool) (Task, error) {
	var err error
	duration, err = NormalizeTaskClaimDuration(duration)
	if err != nil {
		return Task{}, err
	}
	current, err := s.GetTask(ctx, id)
	if err != nil {
		return Task{}, err
	}
	// The expiry modifier is fixed input; SQLite supplies the clock when the
	// guarded UPDATE executes after writer-lock acquisition.
	expiresModifier := fmt.Sprintf("+%.9f seconds", duration.Seconds())
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE tasks SET claim_expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now',?), version=version+1, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND version=? AND claimed_by=? AND julianday(claim_expires_at) > julianday('now') AND deleted_at IS NULL AND completed_at IS NULL`, expiresModifier, id, expected, actorID)
		if err != nil {
			return err
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			if current.Version == expected && current.CompletedAt != nil {
				return conflict("task is already finished", nil)
			}
			if requireActiveClaim {
				version, active, claimErr := taskClaimStateTx(ctx, tx, id, actorID)
				if claimErr != nil {
					return claimErr
				}
				if version == expected && !active {
					return forbidden("an active claim owned by this actor is required")
				}
			}
			return conflict("claim cannot be renewed", map[string]any{"current": current})
		}
		var expires string
		if err := tx.QueryRowContext(ctx, `SELECT claim_expires_at FROM tasks WHERE id=?`, id).Scan(&expires); err != nil {
			return err
		}
		_, err = insertEvent(ctx, tx, "task.claim_renewed", actorID, current.ProjectID, id, map[string]any{"expires_at": expires})
		return err
	})
	if err != nil {
		return Task{}, err
	}
	return s.GetTask(ctx, id)
}

func (s *Store) ReleaseTask(ctx context.Context, id, actorID string, expected int64) (Task, error) {
	return s.releaseTask(ctx, id, actorID, expected, false)
}

// ReleaseTaskWithClaim requires the caller to own an unexpired lease. Human
// callers use ReleaseTask so the UI can clean up an expired lease as well.
func (s *Store) ReleaseTaskWithClaim(ctx context.Context, id, actorID string, expected int64) (Task, error) {
	return s.releaseTask(ctx, id, actorID, expected, true)
}

func (s *Store) releaseTask(ctx context.Context, id, actorID string, expected int64, requireActiveClaim bool) (Task, error) {
	current, err := s.GetTask(ctx, id)
	if err != nil {
		return Task{}, err
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		query := `UPDATE tasks SET claimed_by=NULL, claim_expires_at=NULL, version=version+1, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND version=? AND claimed_by=? AND deleted_at IS NULL`
		args := []any{id, expected, actorID}
		if requireActiveClaim {
			query += ` AND claim_expires_at IS NOT NULL AND julianday(claim_expires_at) > julianday('now')`
		}
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			if requireActiveClaim {
				version, active, claimErr := taskClaimStateTx(ctx, tx, id, actorID)
				if claimErr != nil {
					return claimErr
				}
				if version == expected && !active {
					return forbidden("an active claim owned by this actor is required")
				}
			}
			return conflict("claim cannot be released", map[string]any{"current": current})
		}
		_, err = insertEvent(ctx, tx, "task.released", actorID, current.ProjectID, id, nil)
		return err
	})
	if err != nil {
		return Task{}, err
	}
	return s.GetTask(ctx, id)
}

func (s *Store) CompleteTask(ctx context.Context, id, actorID string, expected int64) (Task, error) {
	return s.CompleteTaskWithComment(ctx, id, actorID, expected, "")
}

func (s *Store) CompleteTaskWithComment(ctx context.Context, id, actorID string, expected int64, comment string) (Task, error) {
	return s.transitionTask(ctx, id, actorID, expected, "completed", "task.completed", comment, false)
}

func (s *Store) CompleteTaskWithClaim(ctx context.Context, id, actorID string, expected int64) (Task, error) {
	return s.CompleteTaskWithCommentWithClaim(ctx, id, actorID, expected, "")
}

func (s *Store) CompleteTaskWithCommentWithClaim(ctx context.Context, id, actorID string, expected int64, comment string) (Task, error) {
	return s.transitionTask(ctx, id, actorID, expected, "completed", "task.completed", comment, true)
}

func (s *Store) BlockTask(ctx context.Context, id, actorID string, expected int64) (Task, error) {
	return s.BlockTaskWithReason(ctx, id, actorID, expected, "")
}

func (s *Store) BlockTaskWithReason(ctx context.Context, id, actorID string, expected int64, reason string) (Task, error) {
	return s.transitionTask(ctx, id, actorID, expected, "blocked", "task.blocked", reason, false)
}

func (s *Store) BlockTaskWithClaim(ctx context.Context, id, actorID string, expected int64) (Task, error) {
	return s.BlockTaskWithReasonWithClaim(ctx, id, actorID, expected, "")
}

func (s *Store) BlockTaskWithReasonWithClaim(ctx context.Context, id, actorID string, expected int64, reason string) (Task, error) {
	return s.transitionTask(ctx, id, actorID, expected, "blocked", "task.blocked", reason, true)
}

func (s *Store) transitionTask(ctx context.Context, id, actorID string, expected int64, state, eventType, note string, requireActiveClaim bool) (Task, error) {
	current, err := s.GetTask(ctx, id)
	if err != nil {
		return Task{}, err
	}
	if current.Kind == bugKind && state == "completed" {
		return Task{}, invalid("bugs must be completed with a resolution", nil)
	}
	if current.Kind == bugKind && current.Bug != nil && current.Bug.Resolution != nil && state != "completed" {
		return Task{}, invalid("resolved bugs must be reopened before leaving a completed column", nil)
	}
	column, err := s.StateColumn(ctx, current.ProjectID, state)
	if err != nil {
		return Task{}, err
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		// Compare the task's pre-update column state inside the guarded UPDATE.
		// Repeating complete while already completed must preserve its original
		// timestamp; only a transition into completed gets a new one.
		query := `UPDATE tasks SET column_id=?, completed_at=CASE WHEN ? <> 'completed' THEN NULL WHEN (SELECT semantic_state FROM columns WHERE id=tasks.column_id) = 'completed' THEN tasks.completed_at ELSE strftime('%Y-%m-%dT%H:%M:%fZ','now') END, claimed_by=NULL, claim_expires_at=NULL, version=version+1, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND version=? AND deleted_at IS NULL`
		args := []any{column.ID, state, id, expected}
		if requireActiveClaim {
			query += ` AND claimed_by=? AND claim_expires_at IS NOT NULL AND julianday(claim_expires_at) > julianday('now')`
			args = append(args, actorID)
		}
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			if requireActiveClaim {
				version, active, claimErr := taskClaimStateTx(ctx, tx, id, actorID)
				if claimErr != nil {
					return claimErr
				}
				if version == expected && !active {
					return forbidden("an active claim owned by this actor is required")
				}
			}
			return conflict("task has changed", map[string]any{"current": current})
		}
		if strings.TrimSpace(note) != "" {
			if len(note) > 10000 {
				return invalid("action note is too long", nil)
			}
			commentID := newID()
			if _, err := tx.ExecContext(ctx, `INSERT INTO comments(id, task_id, actor_id, body, created_at, updated_at) VALUES (?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ','now'), strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, commentID, id, actorID, strings.TrimSpace(note)); err != nil {
				return err
			}
			if _, err := insertEvent(ctx, tx, "comment.created", actorID, current.ProjectID, id, map[string]any{"comment_id": commentID}); err != nil {
				return err
			}
		}
		_, err = insertEvent(ctx, tx, eventType, actorID, current.ProjectID, id, map[string]any{"column_id": column.ID})
		return err
	})
	if err != nil {
		return Task{}, err
	}
	return s.GetTask(ctx, id)
}
