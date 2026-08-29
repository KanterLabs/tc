package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

func scanEvent(scanner interface{ Scan(...any) error }) (Event, error) {
	var event Event
	var actor, project, task, payload sql.NullString
	if err := scanner.Scan(&event.Cursor, &event.ID, &event.Type, &actor, &project, &task, &payload, &event.CreatedAt); err != nil {
		return Event{}, err
	}
	event.ActorID, event.ProjectID, event.TaskID = nullableString(actor), nullableString(project), nullableString(task)
	if payload.Valid {
		event.Payload = json.RawMessage(payload.String)
	} else {
		event.Payload = json.RawMessage(`{}`)
	}
	return event, nil
}

func (s *Store) ListEvents(ctx context.Context, filter EventFilter) ([]Event, bool, error) {
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	if filter.Limit > 200 {
		filter.Limit = 200
	}
	query := `SELECT cursor, id, type, actor_id, project_id, task_id, payload, created_at FROM events WHERE cursor > ?`
	args := []any{filter.After}
	if filter.ProjectID != "" {
		query += ` AND project_id=?`
		args = append(args, filter.ProjectID)
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
		query += ` AND project_id IN (` + strings.Join(placeholders, ",") + `)`
	}
	query += ` ORDER BY cursor LIMIT ?`
	args = append(args, filter.Limit+1)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	result := make([]Event, 0, filter.Limit)
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, false, err
		}
		result = append(result, event)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(result) > filter.Limit
	if hasMore {
		result = result[:filter.Limit]
	}
	return result, hasMore, nil
}

func (s *Store) ListRecentEvents(ctx context.Context, projectID string, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	query := `SELECT cursor, id, type, actor_id, project_id, task_id, payload, created_at FROM events`
	args := []any{}
	if projectID != "" {
		query += ` WHERE project_id=?`
		args = append(args, projectID)
	}
	query += ` ORDER BY cursor DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Event, 0, limit)
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, event)
	}
	return result, rows.Err()
}

func (s *Store) Roadmap(ctx context.Context, projectID string) (Roadmap, error) {
	var roadmap Roadmap
	roadmap.StateCounts = map[string]int{"backlog": 0, "ready": 0, "active": 0, "blocked": 0, "completed": 0}
	args := []any{}
	where := `t.deleted_at IS NULL`
	if projectID != "" {
		where += ` AND t.project_id=?`
		args = append(args, projectID)
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT c.semantic_state, COUNT(1) FROM tasks t JOIN columns c ON c.id=t.column_id WHERE `+where+` GROUP BY c.semantic_state`, args...)
	if err != nil {
		return roadmap, err
	}
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			rows.Close()
			return roadmap, err
		}
		roadmap.StateCounts[state] = count
		roadmap.TaskTotals += count
	}
	rows.Close()
	roadmap.Completed = roadmap.StateCounts["completed"]
	if roadmap.TaskTotals > 0 {
		roadmap.CompletionPercent = float64(roadmap.Completed) * 100 / float64(roadmap.TaskTotals)
	}
	roadmap.TotalTasks = roadmap.TaskTotals
	roadmap.CompletedTasks = roadmap.Completed
	roadmap.CompletionPercentage = roadmap.CompletionPercent
	nowTime := time.Now().UTC()
	nowValue := nowTime.Format(time.RFC3339Nano)
	// "Due soon" is the next seven calendar days throughout the UI and API.
	soonValue := nowTime.Add(7 * 24 * time.Hour).Format(time.RFC3339Nano)
	args = []any{nowValue}
	if projectID != "" {
		args = append(args, projectID)
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM tasks t WHERE t.deleted_at IS NULL AND t.due_at IS NOT NULL AND t.due_at < ? AND t.completed_at IS NULL`+func() string {
		if projectID != "" {
			return ` AND t.project_id=?`
		}
		return ""
	}(), args...).Scan(&roadmap.Overdue); err != nil {
		return roadmap, err
	}
	args = []any{nowValue, soonValue}
	if projectID != "" {
		args = append(args, projectID)
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM tasks t WHERE t.deleted_at IS NULL AND t.due_at IS NOT NULL AND t.due_at >= ? AND t.due_at <= ? AND t.completed_at IS NULL`+func() string {
		if projectID != "" {
			return ` AND t.project_id=?`
		}
		return ""
	}(), args...).Scan(&roadmap.DueSoon); err != nil {
		return roadmap, err
	}

	query := `SELECT ` + taskColumns + ` FROM tasks t WHERE t.deleted_at IS NULL AND t.due_at IS NOT NULL AND t.completed_at IS NULL`
	args = []any{}
	if projectID != "" {
		query += ` AND t.project_id=?`
		args = append(args, projectID)
	}
	query += ` ORDER BY t.due_at, t.id LIMIT 10`
	rows, err = s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return roadmap, err
	}
	for rows.Next() {
		task, err := taskFromRow(rows)
		if err != nil {
			rows.Close()
			return roadmap, err
		}
		roadmap.Upcoming = append(roadmap.Upcoming, task)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return roadmap, err
	}
	rows.Close()
	if roadmap.Upcoming == nil {
		roadmap.Upcoming = []Task{}
	}
	roadmap.UpcomingTasks = roadmap.Upcoming
	roadmap.OverdueCount = roadmap.Overdue
	roadmap.DueSoonCount = roadmap.DueSoon
	for i := range roadmap.Upcoming {
		if err := s.enrichTaskAt(ctx, &roadmap.Upcoming[i], nowTime); err != nil {
			return roadmap, err
		}
	}
	events, err := s.ListRecentEvents(ctx, projectID, 10)
	if err != nil {
		return roadmap, err
	}
	roadmap.RecentActivity = events
	if roadmap.RecentActivity == nil {
		roadmap.RecentActivity = []Event{}
	}
	if projectID != "" {
		project, err := s.GetProject(ctx, projectID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return roadmap, err
		} else if err == nil {
			roadmap.Project = &project
		}
	} else {
		projects, err := s.ListProjectsFiltered(ctx, 200, 0, ProjectFilter{})
		if err != nil {
			return roadmap, err
		}
		roadmap.Projects = make([]map[string]any, 0, len(projects))
		for _, project := range projects {
			roadmap.Projects = append(roadmap.Projects, map[string]any{
				"project":         project,
				"total_tasks":     project.TaskCount,
				"completed_tasks": project.CompletedCount,
				"completion_percentage": func() float64 {
					if project.TaskCount == 0 {
						return 0
					}
					return float64(project.CompletedCount) * 100 / float64(project.TaskCount)
				}(),
			})
		}
	}
	return roadmap, nil
}

func (s *Store) ListMyWork(ctx context.Context, actorID string, limit int) ([]Task, bool, error) {
	return s.ListMyWorkFiltered(ctx, actorID, nil, TaskFilter{Limit: limit})
}

func (s *Store) ListMyWorkScoped(ctx context.Context, actorID string, projectIDs []string, limit int) ([]Task, bool, error) {
	return s.ListMyWorkFiltered(ctx, actorID, projectIDs, TaskFilter{Limit: limit})
}

func (s *Store) ListMyWorkFiltered(ctx context.Context, actorID string, projectIDs []string, filter TaskFilter) ([]Task, bool, error) {
	return s.listMyWorkFiltered(ctx, actorID, projectIDs, filter, false)
}

// ListMyWorkFilteredWithExtra fetches one sentinel row for cursor endpoints;
// ordinary callers retain the public 200-row cap.
func (s *Store) ListMyWorkFilteredWithExtra(ctx context.Context, actorID string, projectIDs []string, filter TaskFilter) ([]Task, bool, error) {
	return s.listMyWorkFiltered(ctx, actorID, projectIDs, filter, true)
}

func (s *Store) listMyWorkFiltered(ctx context.Context, actorID string, projectIDs []string, filter TaskFilter, allowExtra bool) ([]Task, bool, error) {
	limit := filter.Limit
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
	readAt := time.Now().UTC()
	timestamp := readAt.Format(time.RFC3339Nano)
	query := `SELECT ` + taskColumns + ` FROM tasks t JOIN columns c ON c.id=t.column_id`
	args := []any{}
	if filter.LiveWork {
		// Live work is a workspace pulse view rather than an actor-assignment
		// view: every unfinished task with a pulse in an active project is
		// eligible, subject to the caller's project allow-list.
		query += ` JOIN task_agent_work aw ON aw.task_id=t.id JOIN projects p ON p.id=t.project_id WHERE t.deleted_at IS NULL AND t.completed_at IS NULL AND p.archived_at IS NULL`
	} else {
		query += ` WHERE t.deleted_at IS NULL AND (t.assignee_id=? OR (t.claimed_by=? AND t.claim_expires_at IS NOT NULL AND julianday(t.claim_expires_at) > julianday(?)))`
		args = append(args, actorID, actorID, timestamp)
	}
	staleCutoff := agentWorkStaleCutoff(readAt)
	if projectIDs != nil && len(projectIDs) == 0 {
		query += ` AND 1=0`
	}
	if len(projectIDs) > 0 {
		placeholders := make([]string, len(projectIDs))
		for i, projectID := range projectIDs {
			placeholders[i] = "?"
			args = append(args, projectID)
		}
		query += ` AND t.project_id IN (` + strings.Join(placeholders, ",") + ")"
	}
	if filter.State != "" {
		query += ` AND c.semantic_state=?`
		args = append(args, filter.State)
	}
	// Assigned work may still show a completed task when unfiltered, preserving
	// the legacy assignment view. Liveness filters only classify unfinished
	// tasks, so a completed task with a retained snapshot must be excluded.
	if !filter.LiveWork && (filter.AgentState != "" || filter.ActionNeeded) {
		query += ` AND t.completed_at IS NULL`
	}
	if filter.AgentState != "" {
		switch filter.AgentState {
		case "missing":
			if filter.LiveWork {
				// The live view's inner join already excludes missing pulses.
				query += ` AND 1=0`
			} else {
				query += ` AND NOT EXISTS (SELECT 1 FROM task_agent_work aw WHERE aw.task_id=t.id)`
			}
		case "stale":
			if filter.LiveWork {
				query += ` AND julianday(aw.updated_at) <= julianday(?)`
			} else {
				query += ` AND EXISTS (SELECT 1 FROM task_agent_work aw WHERE aw.task_id=t.id AND julianday(aw.updated_at) <= julianday(?))`
			}
			args = append(args, staleCutoff)
		default:
			if filter.LiveWork {
				query += ` AND aw.state=?`
			} else {
				query += ` AND EXISTS (SELECT 1 FROM task_agent_work aw WHERE aw.task_id=t.id AND aw.state=?)`
			}
			args = append(args, filter.AgentState)
		}
	}
	if filter.ActionNeeded {
		if filter.LiveWork {
			query += ` AND (aw.state IN ('waiting', 'handoff') OR julianday(aw.updated_at) <= julianday(?))`
		} else {
			query += ` AND EXISTS (SELECT 1 FROM task_agent_work aw WHERE aw.task_id=t.id AND (aw.state IN ('waiting', 'handoff') OR julianday(aw.updated_at) <= julianday(?)))`
		}
		args = append(args, staleCutoff)
	}
	if filter.Priority != "" {
		query += ` AND t.priority=?`
		args = append(args, filter.Priority)
	}
	if filter.Label != "" {
		query += ` AND EXISTS (SELECT 1 FROM task_labels tl JOIN labels l ON l.id=tl.label_id WHERE tl.task_id=t.id AND (l.id=? OR lower(l.name)=lower(?)))`
		args = append(args, filter.Label, filter.Label)
	}
	if filter.Query != "" {
		query += ` AND (lower(t.title) LIKE ? OR lower(t.description) LIKE ?)`
		value := "%" + strings.ToLower(filter.Query) + "%"
		args = append(args, value, value)
	}
	if filter.UpdatedAfter != nil {
		query += ` AND t.updated_at > ?`
		args = append(args, filter.UpdatedAfter.UTC().Format(time.RFC3339Nano))
	}
	// Cursor is an opaque offset. Live work prioritizes actionable pulses and
	// then the newest pulse; the legacy view retains its claimed/board order.
	if filter.LiveWork {
		query += ` ORDER BY CASE WHEN aw.state IN ('waiting', 'handoff') OR julianday(aw.updated_at) <= julianday(?) THEN 0 ELSE 1 END, aw.updated_at DESC, t.updated_at DESC, t.id LIMIT ? OFFSET ?`
		args = append(args, staleCutoff, limit+1, filter.Cursor)
	} else {
		query += ` ORDER BY CASE WHEN t.claimed_by=? AND t.claim_expires_at IS NOT NULL AND julianday(t.claim_expires_at) > julianday(?) THEN 0 ELSE 1 END, c.position, t.position, t.updated_at DESC, t.id LIMIT ? OFFSET ?`
		args = append(args, actorID, timestamp, limit+1, filter.Cursor)
	}
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	result := make([]Task, 0, limit)
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
	hasMore := len(result) > limit
	if hasMore {
		result = result[:limit]
	}
	for i := range result {
		if err := s.enrichTaskAt(ctx, &result[i], readAt); err != nil {
			return nil, false, err
		}
	}
	return result, hasMore, nil
}

func (s *Store) SeedDemo(ctx context.Context, actorID string) error {
	var count int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM projects`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	name, key := "Demo roadmap", "DEMO"
	_, err := s.CreateProject(ctx, ProjectInput{Key: &key, Name: &name}, actorID)
	if err != nil {
		return err
	}
	return nil
}
