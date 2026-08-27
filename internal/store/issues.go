package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// getBugDetails loads the 1:1 detail row. A missing row is returned as nil so
// a partially upgraded/manual fixture remains readable; normal create and
// update paths always enforce the row's presence for kind=bug.
func (s *Store) getBugDetails(ctx context.Context, taskID string) (*Bug, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT reporter_id, severity, actual_behavior, expected_behavior, reproduction_steps, environment, affected_version, resolution, resolved_by, resolved_at, duplicate_of FROM bug_details WHERE task_id=?`, taskID)
	bug, err := bugFromRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &bug, nil
}

func bugFromRow(scanner interface{ Scan(...any) error }) (Bug, error) {
	var bug Bug
	var severity, resolution, resolvedBy, resolvedAt, duplicateOf sql.NullString
	if err := scanner.Scan(&bug.ReporterID, &severity, &bug.ActualBehavior, &bug.ExpectedBehavior, &bug.ReproductionSteps, &bug.Environment, &bug.AffectedVersion, &resolution, &resolvedBy, &resolvedAt, &duplicateOf); err != nil {
		return Bug{}, err
	}
	bug.Severity = nullableString(severity)
	bug.Resolution = nullableString(resolution)
	bug.ResolvedBy = nullableString(resolvedBy)
	bug.ResolvedAt = nullableString(resolvedAt)
	bug.DuplicateOf = nullableString(duplicateOf)
	return bug, nil
}

func stringOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func insertBugDetailsTx(ctx context.Context, tx *sql.Tx, taskID, reporterID string, input BugInput) error {
	if err := validateBugCreateInput(input); err != nil {
		return err
	}
	if strings.TrimSpace(reporterID) == "" {
		return invalid("a reporter actor is required for bugs", nil)
	}
	var severity any
	if input.Severity != nil {
		severity = *input.Severity
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO bug_details(task_id, reporter_id, severity, actual_behavior, expected_behavior, reproduction_steps, environment, affected_version) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, taskID, reporterID, severity, strings.TrimSpace(*input.ActualBehavior), stringOrEmpty(input.ExpectedBehavior), stringOrEmpty(input.ReproductionSteps), stringOrEmpty(input.Environment), stringOrEmpty(input.AffectedVersion))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "foreign key") {
			return invalid("reporter actor not found", nil)
		}
		return err
	}
	return nil
}

func updateBugDetailsTx(ctx context.Context, tx *sql.Tx, taskID string, input BugInput) error {
	row := tx.QueryRowContext(ctx, `SELECT reporter_id, severity, actual_behavior, expected_behavior, reproduction_steps, environment, affected_version, resolution, resolved_by, resolved_at, duplicate_of FROM bug_details WHERE task_id=?`, taskID)
	current, err := bugFromRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return invalid("bug details not found", nil)
	}
	if err != nil {
		return err
	}
	merged, err := mergeBugInput(current, input)
	if err != nil {
		return err
	}
	var severity any
	if merged.Severity != nil {
		severity = *merged.Severity
	}
	_, err = tx.ExecContext(ctx, `UPDATE bug_details SET severity=?, actual_behavior=?, expected_behavior=?, reproduction_steps=?, environment=?, affected_version=? WHERE task_id=?`, severity, merged.ActualBehavior, merged.ExpectedBehavior, merged.ReproductionSteps, merged.Environment, merged.AffectedVersion, taskID)
	return err
}

func mergeBugInput(current Bug, input BugInput) (Bug, error) {
	merged := current
	if input.SeveritySet || input.Severity != nil {
		merged.Severity = input.Severity
	}
	if input.ActualBehaviorSet || input.ActualBehavior != nil {
		if input.ActualBehavior == nil || strings.TrimSpace(*input.ActualBehavior) == "" {
			return Bug{}, invalid("actual_behavior must not be empty", nil)
		}
		merged.ActualBehavior = strings.TrimSpace(*input.ActualBehavior)
	}
	if input.ExpectedBehaviorSet || input.ExpectedBehavior != nil {
		merged.ExpectedBehavior = stringOrEmpty(input.ExpectedBehavior)
	}
	if input.ReproductionStepsSet || input.ReproductionSteps != nil {
		merged.ReproductionSteps = stringOrEmpty(input.ReproductionSteps)
	}
	if input.EnvironmentSet || input.Environment != nil {
		merged.Environment = stringOrEmpty(input.Environment)
	}
	if input.AffectedVersionSet || input.AffectedVersion != nil {
		merged.AffectedVersion = stringOrEmpty(input.AffectedVersion)
	}
	return merged, nil
}

// ListIssues returns all live bugs visible to the caller. ListIssuesWithExtra
// follows the same sentinel-row contract as ListTasksWithExtra and supports a
// ProjectIDs allow-list for global discovery endpoints.
func (s *Store) ListIssues(ctx context.Context, filter TaskFilter) ([]Task, bool, error) {
	return s.listIssues(ctx, filter, false)
}

func (s *Store) ListIssuesWithExtra(ctx context.Context, filter TaskFilter) ([]Task, bool, error) {
	return s.listIssues(ctx, filter, true)
}

// ListBugsWithExtra is a compatibility alias for clients that use the domain
// term rather than the API's global "issues" collection name.
func (s *Store) ListBugsWithExtra(ctx context.Context, filter TaskFilter) ([]Task, bool, error) {
	return s.ListIssuesWithExtra(ctx, filter)
}

func (s *Store) listIssues(ctx context.Context, filter TaskFilter, allowExtra bool) ([]Task, bool, error) {
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
	query := `SELECT ` + taskColumns + ` FROM tasks t JOIN columns c ON c.id=t.column_id WHERE t.deleted_at IS NULL AND t.kind='bug'`
	args := make([]any, 0, 12)
	if filter.ProjectIDs != nil && len(filter.ProjectIDs) == 0 {
		query += ` AND 1=0`
	}
	if len(filter.ProjectIDs) > 0 {
		placeholders := make([]string, len(filter.ProjectIDs))
		for i, projectID := range filter.ProjectIDs {
			placeholders[i] = "?"
			args = append(args, projectID)
		}
		query += ` AND t.project_id IN (` + strings.Join(placeholders, ",") + `)`
	}
	query, args = appendIssueFilters(query, args, filter)
	query += ` ORDER BY t.project_id, c.position, t.position, t.number, t.id LIMIT ? OFFSET ?`
	args = append(args, filter.Limit+1, filter.Cursor)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	result := make([]Task, 0, filter.Limit)
	for rows.Next() {
		task, err := taskFromRow(rows)
		if err != nil {
			return nil, false, err
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
	for i := range result {
		if err := s.enrichTask(ctx, &result[i]); err != nil {
			return nil, false, err
		}
	}
	return result, hasMore, nil
}

func appendIssueFilters(query string, args []any, filter TaskFilter) (string, []any) {
	if filter.State != "" {
		query += ` AND c.semantic_state=?`
		args = append(args, filter.State)
	}
	if filter.Column != "" {
		query += ` AND (t.column_id=? OR lower(c.name)=lower(?))`
		args = append(args, filter.Column, filter.Column)
	}
	if filter.Priority != "" {
		query += ` AND t.priority=?`
		args = append(args, filter.Priority)
	}
	if filter.Assignee != "" {
		if strings.EqualFold(filter.Assignee, "none") {
			query += ` AND t.assignee_id IS NULL`
		} else {
			query += ` AND t.assignee_id=?`
			args = append(args, filter.Assignee)
		}
	}
	if filter.Kind != "" {
		query += ` AND lower(t.kind)=lower(?)`
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
	if filter.Label != "" {
		query += ` AND EXISTS (SELECT 1 FROM task_labels tl JOIN labels l ON l.id=tl.label_id WHERE tl.task_id=t.id AND (l.id=? OR lower(l.name)=lower(?)))`
		args = append(args, filter.Label, filter.Label)
	}
	if filter.Query != "" {
		query += ` AND (lower(t.title) LIKE ? OR lower(t.description) LIKE ? OR EXISTS (SELECT 1 FROM bug_details bd WHERE bd.task_id=t.id AND (lower(bd.actual_behavior) LIKE ? OR lower(bd.expected_behavior) LIKE ? OR lower(bd.reproduction_steps) LIKE ? OR lower(bd.environment) LIKE ? OR lower(bd.affected_version) LIKE ?)))`
		value := "%" + strings.ToLower(filter.Query) + "%"
		args = append(args, value, value, value, value, value, value, value)
	}
	if filter.UpdatedAfter != nil {
		query += ` AND t.updated_at>?`
		args = append(args, filter.UpdatedAfter.UTC().Format(time.RFC3339Nano))
	}
	return query, args
}

func (s *Store) TriageBug(ctx context.Context, id string, input TriageBugInput, expected int64, actorID string) (Task, error) {
	return s.TriageBugWithClaimOverride(ctx, id, input, expected, actorID, false)
}

func (s *Store) TriageBugWithClaimOverride(ctx context.Context, id string, input TriageBugInput, expected int64, actorID string, allowClaimOverride bool) (Task, error) {
	if expected <= 0 {
		return Task{}, ErrPrecondition
	}
	current, err := s.GetTask(ctx, id)
	if err != nil {
		return Task{}, err
	}
	if current.Kind != bugKind || current.Bug == nil {
		return Task{}, invalid("task is not a bug", nil)
	}
	if current.Bug.Resolution != nil {
		return Task{}, invalid("resolved bugs must be reopened before triage", nil)
	}
	provided := input.SeveritySet || input.Severity != nil
	if !provided {
		return Task{}, invalid("severity is required", nil)
	}
	var severity any
	if input.Severity != nil {
		value := strings.ToLower(strings.TrimSpace(*input.Severity))
		if !validBugSeverity(value) {
			return Task{}, invalid("severity must be s1, s2, s3, or s4", nil)
		}
		severity = value
	}
	priority := current.Priority
	if input.Priority != nil {
		priority = strings.ToLower(strings.TrimSpace(*input.Priority))
		if !validPriority(priority) {
			return Task{}, invalid("priority must be low, normal, high, or urgent", nil)
		}
	}
	assignee := ""
	if current.Assignee != nil {
		assignee = *current.Assignee
	}
	if input.AssigneeSet || input.Assignee != nil {
		assignee = ""
		if input.Assignee != nil {
			assignee = strings.TrimSpace(*input.Assignee)
		}
		if assignee != "" {
			if _, err := s.GetActor(ctx, assignee); err != nil {
				return Task{}, invalid("assignee actor not found", nil)
			}
		}
	}
	columnID := current.ColumnID
	if input.ColumnID != nil {
		columnID = strings.TrimSpace(*input.ColumnID)
	}
	column, err := s.GetColumn(ctx, columnID)
	if err != nil {
		return Task{}, err
	}
	if column.ProjectID != current.ProjectID {
		return Task{}, invalid("column belongs to another project", nil)
	}
	if column.SemanticState == "completed" && current.Bug.Resolution == nil {
		return Task{}, invalid("bugs must be resolved before entering a completed column", nil)
	}
	timestamp := now()
	completedAt := any(nil)
	if column.SemanticState == "completed" {
		if current.CompletedAt != nil {
			completedAt = *current.CompletedAt
		} else {
			completedAt = timestamp
		}
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		query := `UPDATE tasks SET priority=?, assignee_id=NULLIF(?, ''), column_id=?, completed_at=?, version=version+1, updated_at=? WHERE id=? AND kind='bug' AND version=? AND deleted_at IS NULL`
		args := []any{priority, assignee, columnID, completedAt, timestamp, id, expected}
		if !allowClaimOverride {
			query += ` AND (claimed_by IS NULL OR claim_expires_at IS NULL OR julianday(claim_expires_at)<=julianday(?) OR claimed_by=?)`
			args = append(args, timestamp, actorID)
		}
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			return bugMutationFailure(ctx, tx, id, actorID, expected, timestamp, current, allowClaimOverride)
		}
		result, err = tx.ExecContext(ctx, `UPDATE bug_details SET severity=? WHERE task_id=?`, severity, id)
		if err != nil {
			return err
		}
		if changed, _ := result.RowsAffected(); changed == 0 {
			return invalid("bug details not found", nil)
		}
		_, err = insertEvent(ctx, tx, "bug.triaged", actorID, current.ProjectID, id, map[string]any{"version": expected + 1})
		return err
	})
	if err != nil {
		return Task{}, err
	}
	return s.GetTask(ctx, id)
}

func (s *Store) ResolveBug(ctx context.Context, id string, input ResolveBugInput, expected int64, actorID string) (Task, error) {
	return s.ResolveBugWithClaimOverride(ctx, id, input, expected, actorID, false)
}

func (s *Store) ResolveBugWithClaimOverride(ctx context.Context, id string, input ResolveBugInput, expected int64, actorID string, allowClaimOverride bool) (Task, error) {
	if expected <= 0 {
		return Task{}, ErrPrecondition
	}
	resolution := strings.ToLower(strings.TrimSpace(input.Resolution))
	if !validBugResolution(resolution) {
		return Task{}, invalid("resolution is invalid", nil)
	}
	if strings.TrimSpace(actorID) == "" {
		return Task{}, invalid("a resolver actor is required", nil)
	}
	if _, err := s.GetActor(ctx, actorID); err != nil {
		return Task{}, invalid("resolver actor not found", nil)
	}
	current, err := s.GetTask(ctx, id)
	if err != nil {
		return Task{}, err
	}
	if current.Kind != bugKind || current.Bug == nil {
		return Task{}, invalid("task is not a bug", nil)
	}
	var duplicateOf string
	if input.DuplicateOf != nil {
		duplicateOf = strings.TrimSpace(*input.DuplicateOf)
	}
	if resolution == "duplicate" {
		if duplicateOf == "" {
			return Task{}, invalid("duplicate_of is required for duplicate resolution", nil)
		}
	} else if duplicateOf != "" {
		return Task{}, invalid("duplicate_of is only valid for duplicate resolution", nil)
	}
	note := strings.TrimSpace(input.Note)
	if len(note) > 10000 {
		return Task{}, invalid("resolution note is too long", nil)
	}
	completedColumn, err := s.StateColumn(ctx, current.ProjectID, "completed")
	if err != nil {
		return Task{}, err
	}
	timestamp := now()
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		duplicateTargetID := ""
		if resolution == "duplicate" {
			target, targetErr := s.resolveDuplicateTargetTx(ctx, tx, current.ProjectID, id, duplicateOf)
			if targetErr != nil {
				return targetErr
			}
			duplicateTargetID = target
		}
		query := `UPDATE tasks SET column_id=?, completed_at=CASE WHEN (SELECT semantic_state FROM columns WHERE id=tasks.column_id)='completed' AND tasks.completed_at IS NOT NULL THEN tasks.completed_at ELSE ? END, claimed_by=NULL, claim_expires_at=NULL, version=version+1, updated_at=? WHERE id=? AND kind='bug' AND version=? AND deleted_at IS NULL`
		args := []any{completedColumn.ID, timestamp, timestamp, id, expected}
		if !allowClaimOverride {
			query += ` AND (claimed_by IS NULL OR claim_expires_at IS NULL OR julianday(claim_expires_at)<=julianday(?) OR claimed_by=?)`
			args = append(args, timestamp, actorID)
		}
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			return bugMutationFailure(ctx, tx, id, actorID, expected, timestamp, current, allowClaimOverride)
		}
		var duplicateValue any
		if duplicateTargetID != "" {
			duplicateValue = duplicateTargetID
		}
		if _, err := tx.ExecContext(ctx, `UPDATE bug_details SET resolution=?, resolved_by=?, resolved_at=?, duplicate_of=? WHERE task_id=?`, resolution, actorID, timestamp, duplicateValue, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM task_links WHERE source_task_id=? AND link_type='duplicate'`, id); err != nil {
			return err
		}
		if duplicateTargetID != "" {
			if _, err := tx.ExecContext(ctx, `INSERT INTO task_links(source_task_id,target_task_id,link_type,created_at) VALUES (?,?,?,?)`, id, duplicateTargetID, "duplicate", timestamp); err != nil {
				return err
			}
		}
		if note != "" {
			commentID := newID()
			if _, err := tx.ExecContext(ctx, `INSERT INTO comments(id, task_id, actor_id, body, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, commentID, id, actorID, note, timestamp, timestamp); err != nil {
				return err
			}
			if _, err := insertEvent(ctx, tx, "comment.created", actorID, current.ProjectID, id, map[string]any{"comment_id": commentID}); err != nil {
				return err
			}
		}
		eventType := "bug.resolved"
		if resolution == "duplicate" {
			eventType = "bug.duplicated"
		}
		_, err = insertEvent(ctx, tx, eventType, actorID, current.ProjectID, id, map[string]any{"version": expected + 1, "resolution": resolution})
		return err
	})
	if err != nil {
		return Task{}, err
	}
	return s.GetTask(ctx, id)
}

func (s *Store) ReopenBug(ctx context.Context, id, reason string, expected int64, actorID string) (Task, error) {
	return s.ReopenBugWithClaimOverride(ctx, id, reason, expected, actorID, false)
}

func (s *Store) ReopenBugWithClaimOverride(ctx context.Context, id, reason string, expected int64, actorID string, allowClaimOverride bool) (Task, error) {
	if expected <= 0 {
		return Task{}, ErrPrecondition
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return Task{}, invalid("reopen reason is required", nil)
	}
	if len(reason) > 10000 {
		return Task{}, invalid("reopen reason is too long", nil)
	}
	if strings.TrimSpace(actorID) == "" {
		return Task{}, invalid("a reopening actor is required", nil)
	}
	if _, err := s.GetActor(ctx, actorID); err != nil {
		return Task{}, invalid("reopening actor not found", nil)
	}
	current, err := s.GetTask(ctx, id)
	if err != nil {
		return Task{}, err
	}
	if current.Kind != bugKind || current.Bug == nil {
		return Task{}, invalid("task is not a bug", nil)
	}
	if current.Bug.Resolution == nil {
		return Task{}, invalid("bug is not resolved", nil)
	}
	backlogColumn, err := s.StateColumn(ctx, current.ProjectID, "backlog")
	if err != nil {
		return Task{}, err
	}
	timestamp := now()
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		query := `UPDATE tasks SET column_id=?, completed_at=NULL, claimed_by=NULL, claim_expires_at=NULL, version=version+1, updated_at=? WHERE id=? AND kind='bug' AND version=? AND deleted_at IS NULL`
		args := []any{backlogColumn.ID, timestamp, id, expected}
		if !allowClaimOverride {
			query += ` AND (claimed_by IS NULL OR claim_expires_at IS NULL OR julianday(claim_expires_at)<=julianday(?) OR claimed_by=?)`
			args = append(args, timestamp, actorID)
		}
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			return bugMutationFailure(ctx, tx, id, actorID, expected, timestamp, current, allowClaimOverride)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE bug_details SET resolution=NULL, resolved_by=NULL, resolved_at=NULL, duplicate_of=NULL WHERE task_id=?`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM task_links WHERE source_task_id=? AND link_type='duplicate'`, id); err != nil {
			return err
		}
		commentID := newID()
		if _, err := tx.ExecContext(ctx, `INSERT INTO comments(id, task_id, actor_id, body, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, commentID, id, actorID, reason, timestamp, timestamp); err != nil {
			return err
		}
		if _, err := insertEvent(ctx, tx, "comment.created", actorID, current.ProjectID, id, map[string]any{"comment_id": commentID}); err != nil {
			return err
		}
		_, err = insertEvent(ctx, tx, "bug.reopened", actorID, current.ProjectID, id, map[string]any{"version": expected + 1})
		return err
	})
	if err != nil {
		return Task{}, err
	}
	return s.GetTask(ctx, id)
}

func (s *Store) resolveDuplicateTargetTx(ctx context.Context, tx *sql.Tx, projectID, sourceID, reference string) (string, error) {
	var targetID, targetProject, targetKind string
	err := tx.QueryRowContext(ctx, `SELECT t.id, t.project_id, t.kind FROM tasks t JOIN projects p ON p.id=t.project_id WHERE t.deleted_at IS NULL AND (t.id=? OR lower(p.key || '-' || CAST(t.number AS TEXT))=lower(?)) LIMIT 1`, reference, reference).Scan(&targetID, &targetProject, &targetKind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", notFound("duplicate target not found")
	}
	if err != nil {
		return "", err
	}
	if targetID == sourceID {
		return "", invalid("a bug cannot duplicate itself", nil)
	}
	if targetProject != projectID {
		return "", invalid("duplicate target must be in the same project", nil)
	}
	if targetKind != bugKind {
		return "", invalid("duplicate target must be a bug", nil)
	}
	var cycle int
	if err := tx.QueryRowContext(ctx, `WITH RECURSIVE duplicate_chain(task_id) AS (
		SELECT target_task_id FROM task_links WHERE source_task_id=? AND link_type='duplicate'
		UNION
		SELECT l.target_task_id FROM task_links l JOIN duplicate_chain c ON l.source_task_id=c.task_id WHERE l.link_type='duplicate'
	) SELECT EXISTS(SELECT 1 FROM duplicate_chain WHERE task_id=?)`, targetID, sourceID).Scan(&cycle); err != nil {
		return "", err
	}
	if cycle != 0 {
		return "", invalid("duplicate link would create a cycle", nil)
	}
	return targetID, nil
}

func bugMutationFailure(ctx context.Context, tx *sql.Tx, id, actorID string, expected int64, timestamp string, current Task, allowClaimOverride bool) error {
	if !allowClaimOverride {
		claimed, details, err := activeClaimByOtherTx(ctx, tx, id, actorID, timestamp)
		if err != nil {
			return err
		}
		if claimed {
			return &Error{Kind: ErrClaimUnavailable, Message: "task is currently claimed by another actor", Details: details}
		}
	}
	return conflict("task has changed", map[string]any{"current": current})
}
