package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Hierarchy limits are deliberately conservative. They keep relation reads
// and recursive validation bounded while still allowing substantial task
// decomposition in a project.
const (
	MaxTaskHierarchyDepth    = 20
	MaxTaskHierarchyChildren = 200
)

var (
	ErrHierarchySelfReference  = errors.New("hierarchy_self_reference")
	ErrHierarchyCrossProject   = errors.New("hierarchy_cross_project")
	ErrHierarchyAlreadyExists  = errors.New("hierarchy_already_exists")
	ErrHierarchyLimitExceeded  = errors.New("hierarchy_limit_exceeded")
	ErrHierarchyDepthExceeded  = errors.New("hierarchy_depth_exceeded")
	ErrHierarchyFanoutExceeded = errors.New("hierarchy_fanout_exceeded")
	ErrHierarchyCycle          = errors.New("hierarchy_cycle")
	ErrHierarchyNotFound       = errors.New("hierarchy_not_found")
	ErrHierarchyInUse          = errors.New("hierarchy_in_use")
)

type hierarchyTask struct {
	ID           string
	ProjectID    string
	ProjectKey   string
	Number       int
	Key          string
	Title        string
	Kind         string
	ColumnID     string
	State        string
	Version      int64
	ParentTaskID sql.NullString
	CompletedAt  sql.NullString
}

func hierarchyInvalid(kind error, message string, details any, fallback error) error {
	return &Error{Kind: errors.Join(kind, fallback), Message: message, Details: details}
}

func hierarchyNotFound(message string, details any) error {
	return &Error{Kind: errors.Join(ErrHierarchyNotFound, ErrNotFound), Message: message, Details: details}
}

func resolveHierarchyTask(ctx context.Context, q dependencySQL, reference string) (hierarchyTask, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return hierarchyTask{}, hierarchyNotFound("task not found", nil)
	}
	var task hierarchyTask
	err := q.QueryRowContext(ctx, `SELECT t.id, t.project_id, p.key, t.number, t.title, t.kind,
		t.column_id, c.semantic_state, t.version, t.parent_task_id, t.completed_at
		FROM tasks t
		JOIN projects p ON p.id=t.project_id
		JOIN columns c ON c.id=t.column_id
		WHERE t.deleted_at IS NULL
		  AND (t.id=? OR lower(p.key || '-' || CAST(t.number AS TEXT))=lower(?))
		LIMIT 1`, reference, reference).Scan(
		&task.ID, &task.ProjectID, &task.ProjectKey, &task.Number, &task.Title,
		&task.Kind, &task.ColumnID, &task.State, &task.Version, &task.ParentTaskID,
		&task.CompletedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return hierarchyTask{}, hierarchyNotFound("task not found", map[string]any{"reference": reference})
	}
	if err != nil {
		return hierarchyTask{}, err
	}
	task.Key = fmt.Sprintf("%s-%d", task.ProjectKey, task.Number)
	return task, nil
}

func hierarchyReferenceFromTask(task hierarchyTask) TaskHierarchyReference {
	return TaskHierarchyReference{
		ID:            task.ID,
		Number:        task.Number,
		Key:           task.Key,
		ProjectID:     task.ProjectID,
		Title:         task.Title,
		Kind:          task.Kind,
		ColumnID:      task.ColumnID,
		SemanticState: task.State,
		State:         task.State,
		Version:       task.Version,
		ParentID:      nullableString(task.ParentTaskID),
		CompletedAt:   nullableString(task.CompletedAt),
	}
}

func (s *Store) hierarchyReference(ctx context.Context, task hierarchyTask) (TaskHierarchyReference, error) {
	return s.hierarchyReferenceAt(ctx, task, nowTime())
}

func (s *Store) hierarchyReferenceAt(ctx context.Context, task hierarchyTask, at time.Time) (TaskHierarchyReference, error) {
	reference := hierarchyReferenceFromTask(task)
	work, err := s.agentWorkAt(ctx, task.ID, at)
	if err != nil {
		return TaskHierarchyReference{}, err
	}
	reference.AgentWork = work
	return reference, nil
}

func nowTime() (at time.Time) { return time.Now().UTC() }

// hierarchySummary computes all rollup fields from live server rows. The
// summary intentionally stays direct-child scoped; callers can ask for each
// ancestor separately without silently double-counting nested work.
func (s *Store) hierarchySummary(ctx context.Context, taskID, projectID string) (HierarchySummary, error) {
	return s.hierarchySummaryAt(ctx, taskID, projectID, nowTime())
}

func (s *Store) hierarchySummaryAt(ctx context.Context, taskID, projectID string, at time.Time) (HierarchySummary, error) {
	summary := HierarchySummary{StateCounts: map[string]int{
		"backlog":   0,
		"ready":     0,
		"active":    0,
		"blocked":   0,
		"completed": 0,
	}}
	var completed, blocked int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1),
		COALESCE(SUM(CASE WHEN c.semantic_state='completed' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN c.semantic_state='blocked' OR EXISTS (
			SELECT 1
			FROM task_dependencies dependency
			JOIN tasks prerequisite ON prerequisite.id=dependency.prerequisite_task_id
			JOIN columns prerequisite_column ON prerequisite_column.id=prerequisite.column_id
			WHERE dependency.task_id=child.id
			  AND prerequisite.project_id=child.project_id
			  AND prerequisite.deleted_at IS NULL
			  AND (prerequisite.completed_at IS NULL OR prerequisite_column.semantic_state <> 'completed')
		) THEN 1 ELSE 0 END), 0)
		FROM tasks child
		JOIN columns c ON c.id=child.column_id
		WHERE child.parent_task_id=?
		  AND child.project_id=?
		  AND child.deleted_at IS NULL`, taskID, projectID).Scan(&summary.ChildCount, &completed, &blocked); err != nil {
		return HierarchySummary{}, err
	}
	summary.CompletedChildCount = completed
	summary.BlockedChildCount = blocked
	if summary.ChildCount > 0 {
		summary.CompletionPercent = float64(completed) * 100 / float64(summary.ChildCount)
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT c.semantic_state, COUNT(1)
		FROM tasks child
		JOIN columns c ON c.id=child.column_id
		WHERE child.parent_task_id=?
		  AND child.project_id=?
		  AND child.deleted_at IS NULL
		GROUP BY c.semantic_state`, taskID, projectID)
	if err != nil {
		return HierarchySummary{}, err
	}
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			rows.Close()
			return HierarchySummary{}, err
		}
		summary.StateCounts[state] = count
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return HierarchySummary{}, err
	}
	rows.Close()
	cutoff := agentWorkStaleCutoff(at)
	if err := s.DB.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN child.completed_at IS NULL AND aw.task_id IS NOT NULL THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN child.completed_at IS NULL AND aw.task_id IS NOT NULL
			AND (aw.state IN ('waiting', 'handoff') OR julianday(aw.updated_at) <= julianday(?)) THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN child.completed_at IS NULL AND aw.task_id IS NOT NULL
			AND julianday(aw.updated_at) <= julianday(?) THEN 1 ELSE 0 END), 0)
		FROM tasks child
		LEFT JOIN task_agent_work aw ON aw.task_id=child.id
		WHERE child.parent_task_id=?
		  AND child.project_id=?
		  AND child.deleted_at IS NULL`, cutoff, cutoff, taskID, projectID).Scan(
		&summary.LiveAgentWorkCount, &summary.ActionNeededCount, &summary.StaleAgentWorkCount,
	); err != nil {
		return HierarchySummary{}, err
	}
	return summary, nil
}

func (s *Store) populateTaskHierarchy(ctx context.Context, task *Task) error {
	return s.populateTaskHierarchyAt(ctx, task, nowTime())
}

func (s *Store) populateTaskHierarchyAt(ctx context.Context, task *Task, at time.Time) error {
	if task.ParentTaskID != nil {
		parent, err := resolveHierarchyTask(ctx, s.DB, *task.ParentTaskID)
		if err == nil && parent.ProjectID == task.ProjectID {
			reference, referenceErr := s.hierarchyReferenceAt(ctx, parent, at)
			if referenceErr != nil {
				return referenceErr
			}
			task.Parent = &reference
			task.ParentID = task.ParentTaskID
		} else {
			// Historical rows written by a retained binary may contain an edge to
			// a deleted task. Keep it out of live API reads while preserving the
			// persisted identifier for inspection and later cleanup.
			task.Parent = nil
		}
	}
	return s.hierarchySummaryIntoAt(ctx, task, at)
}

func (s *Store) hierarchySummaryInto(ctx context.Context, task *Task) error {
	return s.hierarchySummaryIntoAt(ctx, task, nowTime())
}

func (s *Store) hierarchySummaryIntoAt(ctx context.Context, task *Task, at time.Time) error {
	projectID := task.ProjectID
	summary, err := s.hierarchySummaryAt(ctx, task.ID, projectID, at)
	if err != nil {
		return err
	}
	task.HierarchySummary = summary
	return nil
}

func (s *Store) listHierarchyChildren(ctx context.Context, task hierarchyTask) ([]TaskHierarchyReference, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT child.id, child.project_id, p.key, child.number,
		child.title, child.kind, child.column_id, c.semantic_state, child.version,
		child.parent_task_id, child.completed_at
		FROM tasks child
		JOIN projects p ON p.id=child.project_id
		JOIN columns c ON c.id=child.column_id
		WHERE child.parent_task_id=?
		  AND child.project_id=?
		  AND child.deleted_at IS NULL
		ORDER BY c.position, child.position, child.number, child.id
		LIMIT ?`, task.ID, task.ProjectID, MaxTaskHierarchyChildren)
	if err != nil {
		return nil, err
	}
	result := make([]TaskHierarchyReference, 0)
	for rows.Next() {
		var child hierarchyTask
		if err := rows.Scan(&child.ID, &child.ProjectID, &child.ProjectKey, &child.Number, &child.Title, &child.Kind, &child.ColumnID, &child.State, &child.Version, &child.ParentTaskID, &child.CompletedAt); err != nil {
			rows.Close()
			return nil, err
		}
		child.Key = fmt.Sprintf("%s-%d", child.ProjectKey, child.Number)
		reference, err := s.hierarchyReference(ctx, child)
		if err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, reference)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	return result, nil
}

func (s *Store) listHierarchyAncestors(ctx context.Context, task hierarchyTask) ([]TaskHierarchyReference, error) {
	rows, err := s.DB.QueryContext(ctx, `WITH RECURSIVE ancestors(id, depth) AS (
		SELECT parent_task_id, 1
		FROM tasks
		WHERE id=? AND parent_task_id IS NOT NULL
		UNION ALL
		SELECT parent.parent_task_id, ancestors.depth + 1
		FROM tasks parent
		JOIN ancestors ON parent.id=ancestors.id
		WHERE parent.parent_task_id IS NOT NULL
		  AND parent.deleted_at IS NULL
		  AND ancestors.depth < ?
	)
	SELECT parent.id, parent.project_id, p.key, parent.number, parent.title,
		parent.kind, parent.column_id, c.semantic_state, parent.version,
		parent.parent_task_id, parent.completed_at, ancestors.depth
	FROM ancestors
	JOIN tasks parent ON parent.id=ancestors.id
	JOIN projects p ON p.id=parent.project_id
	JOIN columns c ON c.id=parent.column_id
	WHERE parent.deleted_at IS NULL
	  AND parent.project_id=?
	ORDER BY ancestors.depth
	LIMIT ?`, task.ID, MaxTaskHierarchyDepth, task.ProjectID, MaxTaskHierarchyDepth)
	if err != nil {
		return nil, err
	}
	result := make([]TaskHierarchyReference, 0)
	for rows.Next() {
		var ancestor hierarchyTask
		var depth int
		if err := rows.Scan(&ancestor.ID, &ancestor.ProjectID, &ancestor.ProjectKey, &ancestor.Number, &ancestor.Title, &ancestor.Kind, &ancestor.ColumnID, &ancestor.State, &ancestor.Version, &ancestor.ParentTaskID, &ancestor.CompletedAt, &depth); err != nil {
			rows.Close()
			return nil, err
		}
		ancestor.Key = fmt.Sprintf("%s-%d", ancestor.ProjectKey, ancestor.Number)
		reference, err := s.hierarchyReference(ctx, ancestor)
		if err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, reference)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	return result, nil
}

func (s *Store) listHierarchyDescendants(ctx context.Context, task hierarchyTask) ([]TaskHierarchyReference, error) {
	rows, err := s.DB.QueryContext(ctx, `WITH RECURSIVE descendants(id, depth) AS (
		SELECT id, 0 FROM tasks WHERE id=?
		UNION ALL
		SELECT child.id, descendants.depth + 1
		FROM tasks child
		JOIN descendants ON child.parent_task_id=descendants.id
		WHERE child.deleted_at IS NULL
		  AND child.project_id=?
		  AND descendants.depth < ?
	)
	SELECT child.id, child.project_id, p.key, child.number, child.title,
		child.kind, child.column_id, c.semantic_state, child.version,
		child.parent_task_id, child.completed_at, descendants.depth
	FROM descendants
	JOIN tasks child ON child.id=descendants.id
	JOIN projects p ON p.id=child.project_id
	JOIN columns c ON c.id=child.column_id
	WHERE descendants.depth > 0
	  AND child.deleted_at IS NULL
	ORDER BY descendants.depth, c.position, child.position, child.number, child.id
	LIMIT ?`, task.ID, task.ProjectID, MaxTaskHierarchyDepth, MaxTaskHierarchyChildren)
	if err != nil {
		return nil, err
	}
	result := make([]TaskHierarchyReference, 0)
	for rows.Next() {
		var descendant hierarchyTask
		var depth int
		if err := rows.Scan(&descendant.ID, &descendant.ProjectID, &descendant.ProjectKey, &descendant.Number, &descendant.Title, &descendant.Kind, &descendant.ColumnID, &descendant.State, &descendant.Version, &descendant.ParentTaskID, &descendant.CompletedAt, &depth); err != nil {
			rows.Close()
			return nil, err
		}
		descendant.Key = fmt.Sprintf("%s-%d", descendant.ProjectKey, descendant.Number)
		reference, err := s.hierarchyReference(ctx, descendant)
		if err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, reference)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	return result, nil
}

// GetTaskHierarchy returns a bounded parent/child/ancestor view. Every
// collection is initialized to an empty array for stable JSON contracts.
func (s *Store) GetTaskHierarchy(ctx context.Context, reference string) (TaskHierarchy, error) {
	task, err := resolveHierarchyTask(ctx, s.DB, reference)
	if err != nil {
		return TaskHierarchy{}, err
	}
	result := TaskHierarchy{
		Children:    make([]TaskHierarchyReference, 0),
		Ancestors:   make([]TaskHierarchyReference, 0),
		Descendants: make([]TaskHierarchyReference, 0),
	}
	if task.ParentTaskID.Valid {
		parent, parentErr := resolveHierarchyTask(ctx, s.DB, task.ParentTaskID.String)
		if parentErr == nil && parent.ProjectID == task.ProjectID {
			parentReference, referenceErr := s.hierarchyReference(ctx, parent)
			if referenceErr != nil {
				return TaskHierarchy{}, referenceErr
			}
			result.Parent = &parentReference
		}
	}
	result.Children, err = s.listHierarchyChildren(ctx, task)
	if err != nil {
		return TaskHierarchy{}, err
	}
	result.Ancestors, err = s.listHierarchyAncestors(ctx, task)
	if err != nil {
		return TaskHierarchy{}, err
	}
	result.Descendants, err = s.listHierarchyDescendants(ctx, task)
	if err != nil {
		return TaskHierarchy{}, err
	}
	result.Summary, err = s.hierarchySummary(ctx, task.ID, task.ProjectID)
	if err != nil {
		return TaskHierarchy{}, err
	}
	return result, nil
}

func (s *Store) ListTaskChildren(ctx context.Context, reference string) ([]TaskHierarchyReference, error) {
	task, err := resolveHierarchyTask(ctx, s.DB, reference)
	if err != nil {
		return nil, err
	}
	return s.listHierarchyChildren(ctx, task)
}

func (s *Store) ListTaskAncestors(ctx context.Context, reference string) ([]TaskHierarchyReference, error) {
	task, err := resolveHierarchyTask(ctx, s.DB, reference)
	if err != nil {
		return nil, err
	}
	return s.listHierarchyAncestors(ctx, task)
}

func (s *Store) ListTaskDescendants(ctx context.Context, reference string) ([]TaskHierarchyReference, error) {
	task, err := resolveHierarchyTask(ctx, s.DB, reference)
	if err != nil {
		return nil, err
	}
	return s.listHierarchyDescendants(ctx, task)
}

func (s *Store) checkHierarchyDepth(ctx context.Context, q dependencySQL, childID, parentID string) error {
	var cycle, parentDepth, subtreeDepth int
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(
		WITH RECURSIVE parent_chain(id, depth) AS (
			SELECT ?, 1
			UNION ALL
			SELECT parent.parent_task_id, parent_chain.depth + 1
			FROM tasks parent
			JOIN parent_chain ON parent.id=parent_chain.id
			WHERE parent.parent_task_id IS NOT NULL
			  AND parent_chain.depth < ?
		)
		SELECT 1 FROM parent_chain WHERE id=?
	)`, parentID, MaxTaskHierarchyDepth+2, childID).Scan(&cycle); err != nil {
		return err
	}
	if cycle != 0 {
		return hierarchyInvalid(ErrHierarchyCycle, "parent assignment would create a hierarchy cycle", map[string]any{
			"child_id":  childID,
			"parent_id": parentID,
		}, ErrConflict)
	}
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(depth), 0)
		FROM (
			WITH RECURSIVE parent_chain(id, depth) AS (
				SELECT ?, 1
				UNION ALL
				SELECT parent.parent_task_id, parent_chain.depth + 1
				FROM tasks parent
				JOIN parent_chain ON parent.id=parent_chain.id
				WHERE parent.parent_task_id IS NOT NULL
				  AND parent_chain.depth < ?
			)
			SELECT depth FROM parent_chain
		)`, parentID, MaxTaskHierarchyDepth+2).Scan(&parentDepth); err != nil {
		return err
	}
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(depth), 0)
		FROM (
			WITH RECURSIVE descendants(id, depth) AS (
				SELECT ?, 0
				UNION ALL
				SELECT child.id, descendants.depth + 1
				FROM tasks child
				JOIN descendants ON child.parent_task_id=descendants.id
				WHERE child.deleted_at IS NULL
				  AND descendants.depth < ?
			)
			SELECT depth FROM descendants
		)`, childID, MaxTaskHierarchyDepth+2).Scan(&subtreeDepth); err != nil {
		return err
	}
	if parentDepth > MaxTaskHierarchyDepth || parentDepth+subtreeDepth > MaxTaskHierarchyDepth {
		return hierarchyInvalid(ErrHierarchyDepthExceeded, "task hierarchy depth exceeds the configured limit", map[string]any{
			"limit":         MaxTaskHierarchyDepth,
			"parent_depth":  parentDepth,
			"subtree_depth": subtreeDepth,
		}, ErrInvalid)
	}
	return nil
}

func mapHierarchyMutationError(err error, child, parent hierarchyTask) error {
	if err == nil {
		return nil
	}
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "hierarchy_self_reference"):
		return hierarchyInvalid(ErrHierarchySelfReference, "a task cannot be its own parent", map[string]any{"child_id": child.ID}, ErrInvalid)
	case strings.Contains(lower, "hierarchy_cross_project_or_not_live"):
		return hierarchyInvalid(ErrHierarchyCrossProject, "parent and child must be live tasks in the same project", map[string]any{"child_project_id": child.ProjectID, "parent_project_id": parent.ProjectID}, ErrInvalid)
	case strings.Contains(lower, "hierarchy_fanout_exceeded"):
		return hierarchyInvalid(errors.Join(ErrHierarchyFanoutExceeded, ErrHierarchyLimitExceeded), "parent has reached its direct child limit", map[string]any{"limit": MaxTaskHierarchyChildren, "parent_id": parent.ID}, ErrInvalid)
	case strings.Contains(lower, "hierarchy_depth_exceeded"):
		return hierarchyInvalid(ErrHierarchyDepthExceeded, "task hierarchy depth exceeds the configured limit", map[string]any{"limit": MaxTaskHierarchyDepth}, ErrInvalid)
	case strings.Contains(lower, "hierarchy_parent_in_use"):
		return hierarchyInvalid(ErrHierarchyInUse, "task has live children and cannot be deleted", map[string]any{"parent_id": child.ID}, ErrConflict)
	default:
		return err
	}
}

func (s *Store) setTaskParent(ctx context.Context, childReference, parentReference string, expectedVersion int64, actorID string, allowClaimOverride bool) error {
	return s.withImmediateDependencyTx(ctx, func(q dependencySQL) error {
		child, err := resolveHierarchyTask(ctx, q, childReference)
		if err != nil {
			return err
		}
		if !allowClaimOverride {
			if err := dependencyClaimConflict(ctx, q, child.ID, actorID); err != nil {
				return err
			}
		}
		parent, err := resolveHierarchyTask(ctx, q, parentReference)
		if err != nil {
			return err
		}
		if child.ID == parent.ID {
			return hierarchyInvalid(ErrHierarchySelfReference, "a task cannot be its own parent", map[string]any{"child_id": child.ID}, ErrInvalid)
		}
		if child.ProjectID != parent.ProjectID {
			return hierarchyInvalid(ErrHierarchyCrossProject, "parent and child must belong to the same project", map[string]any{
				"child_project_id":  child.ProjectID,
				"parent_project_id": parent.ProjectID,
			}, ErrInvalid)
		}
		if child.Version != expectedVersion {
			return conflict("task has changed", map[string]any{
				"current_version":  child.Version,
				"expected_version": expectedVersion,
				"current":          map[string]any{"id": child.ID, "key": child.Key, "version": child.Version},
			})
		}
		if child.ParentTaskID.Valid && child.ParentTaskID.String == parent.ID {
			return hierarchyInvalid(ErrHierarchyAlreadyExists, "task already has this parent", map[string]any{"child_id": child.ID, "parent_id": parent.ID}, ErrAlreadyExists)
		}
		var children int
		if err := q.QueryRowContext(ctx, `SELECT COUNT(1) FROM tasks WHERE parent_task_id=? AND project_id=? AND deleted_at IS NULL AND id<>?`, parent.ID, child.ProjectID, child.ID).Scan(&children); err != nil {
			return err
		}
		if children >= MaxTaskHierarchyChildren {
			return hierarchyInvalid(errors.Join(ErrHierarchyFanoutExceeded, ErrHierarchyLimitExceeded), "parent has reached its direct child limit", map[string]any{
				"limit":     MaxTaskHierarchyChildren,
				"count":     children,
				"parent_id": parent.ID,
			}, ErrInvalid)
		}
		if err := s.checkHierarchyDepth(ctx, q, child.ID, parent.ID); err != nil {
			return err
		}
		at := now()
		result, err := q.ExecContext(ctx, `UPDATE tasks SET parent_task_id=?, version=version+1, updated_at=? WHERE id=? AND version=? AND deleted_at IS NULL`, parent.ID, at, child.ID, expectedVersion)
		if err != nil {
			return mapHierarchyMutationError(err, child, parent)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return conflict("task has changed", map[string]any{"current_version": child.Version, "expected_version": expectedVersion})
		}
		return insertHierarchyEvent(ctx, q, "task.parent_linked", actorID, child, &parent, child.Version+1)
	})
}

func (s *Store) clearTaskParent(ctx context.Context, childReference, expectedParentReference string, expectedVersion int64, actorID string, allowClaimOverride bool) error {
	return s.withImmediateDependencyTx(ctx, func(q dependencySQL) error {
		child, err := resolveHierarchyTask(ctx, q, childReference)
		if err != nil {
			return err
		}
		if !allowClaimOverride {
			if err := dependencyClaimConflict(ctx, q, child.ID, actorID); err != nil {
				return err
			}
		}
		if child.Version != expectedVersion {
			return conflict("task has changed", map[string]any{"current_version": child.Version, "expected_version": expectedVersion, "current": map[string]any{"id": child.ID, "key": child.Key, "version": child.Version}})
		}
		if !child.ParentTaskID.Valid {
			return hierarchyNotFound("task has no parent", map[string]any{"child_id": child.ID})
		}
		parent := hierarchyTask{ID: child.ParentTaskID.String, ProjectID: child.ProjectID}
		var parentProjectKey string
		if err := q.QueryRowContext(ctx, `SELECT p.key, parent.number
			FROM tasks parent
			JOIN projects p ON p.id=parent.project_id
			WHERE parent.id=?`, child.ParentTaskID.String).Scan(&parentProjectKey, &parent.Number); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		} else if parentProjectKey != "" {
			parent.ProjectKey = parentProjectKey
			parent.Key = fmt.Sprintf("%s-%d", parentProjectKey, parent.Number)
		}
		if expectedParentReference != "" {
			expected, expectedErr := resolveHierarchyTask(ctx, q, expectedParentReference)
			if expectedErr != nil {
				return expectedErr
			}
			if expected.ID != parent.ID {
				return hierarchyNotFound("task is not linked to that parent", map[string]any{"child_id": child.ID, "parent_id": expected.ID})
			}
			parent = expected
		}
		at := now()
		result, err := q.ExecContext(ctx, `UPDATE tasks SET parent_task_id=NULL, version=version+1, updated_at=? WHERE id=? AND version=? AND deleted_at IS NULL`, at, child.ID, expectedVersion)
		if err != nil {
			return mapHierarchyMutationError(err, child, parent)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return conflict("task has changed", map[string]any{"current_version": child.Version, "expected_version": expectedVersion})
		}
		return insertHierarchyEvent(ctx, q, "task.parent_unlinked", actorID, child, &parent, child.Version+1)
	})
}

func insertHierarchyEvent(ctx context.Context, q dependencySQL, eventType, actorID string, child hierarchyTask, parent *hierarchyTask, version int64) error {
	payload := map[string]any{
		"child_id":  child.ID,
		"child_key": child.Key,
		"version":   version,
	}
	if parent != nil {
		payload["parent_id"] = parent.ID
		payload["parent_key"] = parent.Key
		// A replacement invalidates both the previous and new parent's
		// server-derived rollups. The unlink form already uses parent_id for
		// the old parent; a relink needs this explicit second edge.
		if child.ParentTaskID.Valid && child.ParentTaskID.String != parent.ID {
			payload["previous_parent_id"] = child.ParentTaskID.String
		}
	}
	_, err := q.ExecContext(ctx, `INSERT INTO events(id, type, actor_id, project_id, task_id, payload, created_at) VALUES (?, ?, NULLIF(?, ''), ?, ?, ?, ?)`, newID(), eventType, actorID, child.ProjectID, child.ID, eventPayload(payload), now())
	return err
}

func (s *Store) SetTaskParent(ctx context.Context, childReference, parentReference string, expectedVersion int64, actorID string) (Task, error) {
	return s.SetTaskParentWithClaimOverride(ctx, childReference, parentReference, expectedVersion, actorID, false)
}

func (s *Store) SetTaskParentWithClaimOverride(ctx context.Context, childReference, parentReference string, expectedVersion int64, actorID string, allowClaimOverride bool) (Task, error) {
	if expectedVersion <= 0 {
		return Task{}, ErrPrecondition
	}
	if err := s.setTaskParent(ctx, childReference, parentReference, expectedVersion, actorID, allowClaimOverride); err != nil {
		return Task{}, err
	}
	return s.ResolveTaskReference(ctx, childReference)
}

func (s *Store) ClearTaskParent(ctx context.Context, childReference string, expectedVersion int64, actorID string) (Task, error) {
	return s.ClearTaskParentWithClaimOverride(ctx, childReference, expectedVersion, actorID, false)
}

func (s *Store) ClearTaskParentWithClaimOverride(ctx context.Context, childReference string, expectedVersion int64, actorID string, allowClaimOverride bool) (Task, error) {
	if expectedVersion <= 0 {
		return Task{}, ErrPrecondition
	}
	if err := s.clearTaskParent(ctx, childReference, "", expectedVersion, actorID, allowClaimOverride); err != nil {
		return Task{}, err
	}
	return s.ResolveTaskReference(ctx, childReference)
}

func (s *Store) RemoveTaskChild(ctx context.Context, parentReference, childReference string, expectedVersion int64, actorID string) (Task, error) {
	return s.RemoveTaskChildWithClaimOverride(ctx, parentReference, childReference, expectedVersion, actorID, false)
}

func (s *Store) RemoveTaskChildWithClaimOverride(ctx context.Context, parentReference, childReference string, expectedVersion int64, actorID string, allowClaimOverride bool) (Task, error) {
	if expectedVersion <= 0 {
		return Task{}, ErrPrecondition
	}
	if err := s.clearTaskParent(ctx, childReference, parentReference, expectedVersion, actorID, allowClaimOverride); err != nil {
		return Task{}, err
	}
	return s.ResolveTaskReference(ctx, childReference)
}

// Compatibility aliases make the relation naming explicit to callers that
// think in terms of linking/unlinking rather than setting a nullable parent.
func (s *Store) LinkTaskParent(ctx context.Context, childReference, parentReference string, expectedVersion int64, actorID string) (Task, error) {
	return s.SetTaskParent(ctx, childReference, parentReference, expectedVersion, actorID)
}

func (s *Store) UnlinkTaskParent(ctx context.Context, childReference string, expectedVersion int64, actorID string) (Task, error) {
	return s.ClearTaskParent(ctx, childReference, expectedVersion, actorID)
}
