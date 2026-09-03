package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const maxDirectTaskDependencies = 200

// dependencySQL is implemented by *sql.DB, *sql.Tx, and *sql.Conn. Dependency
// mutations use a dedicated *sql.Conn so they can explicitly issue
// BEGIN IMMEDIATE before any graph validation read.
type dependencySQL interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type dependencyTask struct {
	ID            string
	ProjectID     string
	ProjectKey    string
	Number        int
	Key           string
	Title         string
	CompletedAt   sql.NullString
	SemanticState string
	Version       int64
}

// dependencyTaskFromTask adapts the store's enriched task snapshot for
// lifecycle comparisons. The caller must supply the semantic state belonging
// to the snapshot's source column; task responses intentionally keep that
// state on the column rather than duplicating it on Task.
func dependencyTaskFromTask(task Task, semanticState string) dependencyTask {
	var completedAt sql.NullString
	if task.CompletedAt != nil {
		completedAt = sql.NullString{String: *task.CompletedAt, Valid: true}
	}
	return dependencyTask{
		ID:            task.ID,
		ProjectID:     task.ProjectID,
		ProjectKey:    strings.TrimSuffix(task.Key, fmt.Sprintf("-%d", task.Number)),
		Number:        task.Number,
		Key:           task.Key,
		Title:         task.Title,
		CompletedAt:   completedAt,
		SemanticState: semanticState,
		Version:       task.Version,
	}
}

// withImmediateDependencyTx is the dependency-specific transaction entrypoint.
// database/sql's ordinary BeginTx starts a deferred transaction, which would
// let opposing cycle checks both read the same pre-write graph and then race
// at insertion. A dedicated connection and BEGIN IMMEDIATE establish the
// serialization point before the first validation read.
func (s *Store) withImmediateDependencyTx(ctx context.Context, fn func(dependencySQL) error) (retErr error) {
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		// Preserve the original function/commit error. Rollback is best effort;
		// the connection is closed immediately afterwards and returned to the
		// pool only once SQLite has had a chance to unwind the transaction.
		_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
	}()
	if err := fn(conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	return nil
}

func dependencyNotFound(message string, details any) error {
	return &Error{Kind: errors.Join(ErrDependencyNotFound, ErrNotFound), Message: message, Details: details}
}

func dependencyInvalid(kind error, message string, details any, fallback error) error {
	return &Error{Kind: errors.Join(kind, fallback), Message: message, Details: details}
}

func mapDependencyMutationError(ctx context.Context, q dependencySQL, err error, dependent, prerequisite dependencyTask) error {
	if err == nil {
		return nil
	}
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "unmet_dependencies"):
		return dependencyInvalid(ErrUnmetDependencies, "dependency prerequisites are not satisfied for this task state", map[string]any{
			"task":         dependencyTaskDetails(dependent),
			"prerequisite": dependencyTaskDetails(prerequisite),
		}, ErrInvalid)
	case strings.Contains(lower, "dependency_cycle"):
		details, detailsErr := dependencyCycleDetails(ctx, q, prerequisite, dependent)
		if detailsErr == nil {
			return dependencyInvalid(ErrDependencyCycle, "dependency would create a cycle", details, ErrConflict)
		}
		return dependencyInvalid(ErrDependencyCycle, "dependency would create a cycle", map[string]any{
			"task_id":              dependent.ID,
			"prerequisite_task_id": prerequisite.ID,
		}, ErrConflict)
	case strings.Contains(lower, "dependency_limit_exceeded_prerequisites"):
		return dependencyInvalid(ErrDependencyLimitExceeded, "task has reached its direct prerequisite limit", map[string]any{
			"direction": "prerequisites",
			"limit":     maxDirectTaskDependencies,
			"task_id":   dependent.ID,
		}, ErrInvalid)
	case strings.Contains(lower, "dependency_limit_exceeded_dependents"):
		return dependencyInvalid(ErrDependencyLimitExceeded, "task has reached its direct dependent limit", map[string]any{
			"direction": "dependents",
			"limit":     maxDirectTaskDependencies,
			"task_id":   prerequisite.ID,
		}, ErrInvalid)
	case strings.Contains(lower, "dependency_cross_project_or_not_live"):
		return dependencyInvalid(ErrDependencyCrossProject, "dependencies must stay within one live project", map[string]any{
			"task_id":              dependent.ID,
			"prerequisite_task_id": prerequisite.ID,
		}, ErrInvalid)
	case strings.Contains(lower, "unique constraint failed") && strings.Contains(lower, "task_dependencies"):
		return dependencyInvalid(ErrDependencyAlreadyExists, "dependency already exists", map[string]any{
			"task":         dependencyTaskDetails(dependent),
			"prerequisite": dependencyTaskDetails(prerequisite),
		}, ErrAlreadyExists)
	default:
		return err
	}
}

func resolveDependencyTask(ctx context.Context, q dependencySQL, reference string) (dependencyTask, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return dependencyTask{}, dependencyNotFound("task not found", nil)
	}
	var task dependencyTask
	if err := q.QueryRowContext(ctx, `SELECT t.id, t.project_id, p.key, t.number, t.title, t.completed_at, c.semantic_state, t.version
		FROM tasks t
		JOIN projects p ON p.id=t.project_id
		JOIN columns c ON c.id=t.column_id
		WHERE t.deleted_at IS NULL
		  AND (t.id=? OR lower(p.key || '-' || CAST(t.number AS TEXT))=lower(?))
		LIMIT 1`, reference, reference).Scan(&task.ID, &task.ProjectID, &task.ProjectKey, &task.Number, &task.Title, &task.CompletedAt, &task.SemanticState, &task.Version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return dependencyTask{}, dependencyNotFound("task not found", map[string]any{"reference": reference})
		}
		return dependencyTask{}, err
	}
	task.Key = fmt.Sprintf("%s-%d", task.ProjectKey, task.Number)
	return task, nil
}

func dependencySatisfied(task dependencyTask) bool {
	return task.CompletedAt.Valid && task.SemanticState == "completed"
}

func dependencyReference(task dependencyTask) TaskReference {
	return TaskReference{
		ID:          task.ID,
		Key:         task.Key,
		Title:       task.Title,
		CompletedAt: nullableString(task.CompletedAt),
		Satisfied:   dependencySatisfied(task),
	}
}

func dependencyTaskDetails(task dependencyTask) map[string]any {
	return map[string]any{
		"id":           task.ID,
		"key":          task.Key,
		"title":        task.Title,
		"completed_at": nullableString(task.CompletedAt),
		"satisfied":    dependencySatisfied(task),
	}
}

func dependencyCyclePath(ctx context.Context, q dependencySQL, prerequisiteID, dependentID string) ([]string, error) {
	// IDs are opaque hex strings and therefore safe in this delimiter-based
	// path representation. The depth bound makes malformed legacy data unable
	// to turn a validation failure into unbounded work.
	var encoded string
	err := q.QueryRowContext(ctx, `WITH RECURSIVE dependency_path(task_id, path, depth) AS (
		SELECT ?, ?, 0
		UNION ALL
		SELECT td.prerequisite_task_id,
		       dependency_path.path || ',' || td.prerequisite_task_id,
		       dependency_path.depth + 1
		FROM task_dependencies td
		JOIN dependency_path ON td.task_id=dependency_path.task_id
		JOIN tasks next_task ON next_task.id=td.prerequisite_task_id AND next_task.deleted_at IS NULL
		WHERE dependency_path.depth < ?
		  AND instr(',' || dependency_path.path || ',', ',' || td.prerequisite_task_id || ',') = 0
	)
	SELECT path FROM dependency_path WHERE task_id=? LIMIT 1`, prerequisiteID, prerequisiteID, maxDirectTaskDependencies, dependentID).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return []string{prerequisiteID, dependentID}, nil
	}
	if err != nil {
		return nil, err
	}
	parts := strings.Split(encoded, ",")
	parts = append(parts, prerequisiteID)
	return parts, nil
}

func dependencyCycleDetails(ctx context.Context, q dependencySQL, prerequisite, dependent dependencyTask) (map[string]any, error) {
	pathIDs, err := dependencyCyclePath(ctx, q, prerequisite.ID, dependent.ID)
	if err != nil {
		return nil, err
	}
	pathKeys := make([]string, 0, len(pathIDs))
	for _, id := range pathIDs {
		var projectKey string
		var number int
		if err := q.QueryRowContext(ctx, `SELECT p.key, t.number FROM tasks t JOIN projects p ON p.id=t.project_id WHERE t.id=?`, id).Scan(&projectKey, &number); err != nil {
			return nil, err
		}
		pathKeys = append(pathKeys, fmt.Sprintf("%s-%d", projectKey, number))
	}
	return map[string]any{
		"task_id":              dependent.ID,
		"task_key":             dependent.Key,
		"prerequisite_task_id": prerequisite.ID,
		"prerequisite_key":     prerequisite.Key,
		"path":                 pathKeys,
		"path_ids":             pathIDs,
	}, nil
}

func dependencyClaimConflict(ctx context.Context, q dependencySQL, taskID, actorID string) error {
	var claimedBy, claimExpiresAt string
	err := q.QueryRowContext(ctx, `SELECT claimed_by, claim_expires_at
		FROM tasks
		WHERE id=?
		  AND deleted_at IS NULL
		  AND claimed_by IS NOT NULL
		  AND claimed_by<>?
		  AND claim_expires_at IS NOT NULL
		  AND julianday(claim_expires_at) > julianday('now')`, taskID, actorID).Scan(&claimedBy, &claimExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return &Error{
		Kind:    ErrClaimUnavailable,
		Message: "task is currently claimed by another actor",
		Details: map[string]any{"task_id": taskID, "claimed_by": claimedBy, "claim_expires_at": claimExpiresAt},
	}
}

// dependencyLifecycleTarget identifies the mutation boundary that raised a
// database dependency guard. Task transitions pass TaskID; a semantic column
// update passes ColumnID because one statement may have affected many tasks.
// Keeping this target unexported prevents callers from bypassing the guarded
// lifecycle APIs with a hand-built detail shape.
type dependencyLifecycleTarget struct {
	TaskID            string
	ColumnID          string
	AllDependents     bool
	NextSemanticState string
}

// mapDependencyLifecycleError translates the fail-closed migration guards into
// stable store errors while retaining bounded, actionable relation details.
// The original trigger error is never allowed to replace the typed sentinel
// if a detail query fails; callers still receive a machine-readable conflict.
func mapDependencyLifecycleError(ctx context.Context, q dependencySQL, err error, target dependencyLifecycleTarget) error {
	if err == nil {
		return nil
	}
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "unmet_dependencies"):
		details, detailsErr := dependencyUnmetLifecycleDetails(ctx, q, target)
		if detailsErr != nil {
			details = map[string]any{"task_id": target.TaskID, "column_id": target.ColumnID}
		}
		return dependencyInvalid(ErrUnmetDependencies, "task prerequisites are not satisfied", details, ErrConflict)
	case strings.Contains(lower, "dependency_in_use"):
		details, detailsErr := dependencyInUseLifecycleDetails(ctx, q, target)
		if detailsErr != nil {
			details = map[string]any{"task_id": target.TaskID, "column_id": target.ColumnID}
		}
		return dependencyInvalid(ErrDependencyInUse, "dependency change would invalidate live dependent work", details, ErrConflict)
	default:
		return err
	}
}

func dependencyUnmetLifecycleDetails(ctx context.Context, q dependencySQL, target dependencyLifecycleTarget) (map[string]any, error) {
	if target.TaskID != "" {
		task, err := resolveDependencyTask(ctx, q, target.TaskID)
		if err != nil {
			return nil, err
		}
		prerequisites, err := dependencyUnmetPrerequisites(ctx, q, task)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"task_id":             task.ID,
			"task_key":            task.Key,
			"unmet_prerequisites": prerequisites,
		}, nil
	}
	if target.ColumnID == "" {
		return map[string]any{"unmet_prerequisites": []TaskReference{}}, nil
	}
	return dependencyUnmetColumnDetails(ctx, q, target.ColumnID)
}

func dependencyUnmetPrerequisites(ctx context.Context, q dependencySQL, task dependencyTask) ([]TaskReference, error) {
	rows, err := q.QueryContext(ctx, `SELECT prerequisite.id, prerequisite.number, prerequisite.title, prerequisite.completed_at, prerequisite_column.semantic_state, prerequisite_project.key
		FROM task_dependencies td
		JOIN tasks prerequisite ON prerequisite.id=td.prerequisite_task_id
		JOIN projects prerequisite_project ON prerequisite_project.id=prerequisite.project_id
		JOIN columns prerequisite_column ON prerequisite_column.id=prerequisite.column_id
		WHERE td.task_id=?
		  AND prerequisite.project_id=?
		  AND prerequisite.deleted_at IS NULL
		  AND (prerequisite.completed_at IS NULL OR prerequisite_column.semantic_state <> 'completed')
		ORDER BY prerequisite.number, prerequisite.id
		LIMIT ?`, task.ID, task.ProjectID, maxDirectTaskDependencies)
	if err != nil {
		return nil, err
	}
	result := make([]TaskReference, 0)
	for rows.Next() {
		var prerequisite dependencyTask
		if err := rows.Scan(&prerequisite.ID, &prerequisite.Number, &prerequisite.Title, &prerequisite.CompletedAt, &prerequisite.SemanticState, &prerequisite.ProjectKey); err != nil {
			rows.Close()
			return nil, err
		}
		prerequisite.ProjectID = task.ProjectID
		prerequisite.Key = fmt.Sprintf("%s-%d", prerequisite.ProjectKey, prerequisite.Number)
		result = append(result, dependencyReference(prerequisite))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	return result, nil
}

func dependencyUnmetColumnDetails(ctx context.Context, q dependencySQL, columnID string) (map[string]any, error) {
	rows, err := q.QueryContext(ctx, `SELECT dependent.id, dependent.number, dependent.title, dependent_project.key,
		prerequisite.id, prerequisite.number, prerequisite.title, prerequisite.completed_at, prerequisite_column.semantic_state, prerequisite_project.key
		FROM tasks dependent
		JOIN projects dependent_project ON dependent_project.id=dependent.project_id
		JOIN task_dependencies td ON td.task_id=dependent.id
		JOIN tasks prerequisite ON prerequisite.id=td.prerequisite_task_id
		JOIN projects prerequisite_project ON prerequisite_project.id=prerequisite.project_id
		JOIN columns prerequisite_column ON prerequisite_column.id=prerequisite.column_id
		WHERE dependent.column_id=?
		  AND dependent.deleted_at IS NULL
		  AND prerequisite.deleted_at IS NULL
		  AND prerequisite.project_id=dependent.project_id
		  AND (prerequisite.completed_at IS NULL OR prerequisite_column.semantic_state <> 'completed')
		ORDER BY dependent.number, dependent.id, prerequisite.number, prerequisite.id
		LIMIT ?`, columnID, maxDirectTaskDependencies)
	if err != nil {
		return nil, err
	}
	type blockedTask struct {
		ID            string
		Key           string
		Title         string
		Prerequisites []TaskReference
	}
	ordered := make([]blockedTask, 0)
	byID := make(map[string]int)
	for rows.Next() {
		var dependent dependencyTask
		var prerequisite dependencyTask
		if err := rows.Scan(&dependent.ID, &dependent.Number, &dependent.Title, &dependent.ProjectKey, &prerequisite.ID, &prerequisite.Number, &prerequisite.Title, &prerequisite.CompletedAt, &prerequisite.SemanticState, &prerequisite.ProjectKey); err != nil {
			rows.Close()
			return nil, err
		}
		dependent.Key = fmt.Sprintf("%s-%d", dependent.ProjectKey, dependent.Number)
		prerequisite.Key = fmt.Sprintf("%s-%d", prerequisite.ProjectKey, prerequisite.Number)
		index, ok := byID[dependent.ID]
		if !ok {
			index = len(ordered)
			byID[dependent.ID] = index
			ordered = append(ordered, blockedTask{ID: dependent.ID, Key: dependent.Key, Title: dependent.Title, Prerequisites: make([]TaskReference, 0)})
		}
		ordered[index].Prerequisites = append(ordered[index].Prerequisites, dependencyReference(prerequisite))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	tasks := make([]map[string]any, 0, len(ordered))
	for _, task := range ordered {
		tasks = append(tasks, map[string]any{
			"task_id":             task.ID,
			"task_key":            task.Key,
			"task_title":          task.Title,
			"unmet_prerequisites": task.Prerequisites,
		})
	}
	return map[string]any{"column_id": columnID, "tasks": tasks}, nil
}

func dependencyInUseLifecycleDetails(ctx context.Context, q dependencySQL, target dependencyLifecycleTarget) (map[string]any, error) {
	if target.TaskID != "" {
		task, err := resolveDependencyTask(ctx, q, target.TaskID)
		if err != nil {
			return nil, err
		}
		dependents, err := dependencyLiveDependents(ctx, q, task, target.AllDependents)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"task_id":    task.ID,
			"task_key":   task.Key,
			"dependents": dependents,
		}, nil
	}
	if target.ColumnID == "" {
		return map[string]any{"dependents": []TaskReference{}}, nil
	}
	return dependencyInUseColumnDetails(ctx, q, target.ColumnID, target.NextSemanticState)
}

func dependencyLiveDependents(ctx context.Context, q dependencySQL, prerequisite dependencyTask, includeAll bool) ([]TaskReference, error) {
	query := `SELECT dependent.id, dependent.number, dependent.title, dependent.completed_at, dependent_column.semantic_state, dependent_project.key
		FROM task_dependencies td
		JOIN tasks dependent ON dependent.id=td.task_id
		JOIN projects dependent_project ON dependent_project.id=dependent.project_id
		JOIN columns dependent_column ON dependent_column.id=dependent.column_id
		WHERE td.prerequisite_task_id=?
		  AND dependent.project_id=?
		  AND dependent.deleted_at IS NULL`
	if !includeAll {
		query += `
		  AND (
		      dependent_column.semantic_state='active'
		      OR (
		          dependent.claimed_by IS NOT NULL
		          AND dependent.claim_expires_at IS NOT NULL
		          AND julianday(dependent.claim_expires_at) > julianday('now')
		      )
		  )
		  AND (dependent_column.semantic_state <> 'completed' OR dependent.completed_at IS NULL)`
	}
	query += `
		ORDER BY dependent.number, dependent.id
		LIMIT ?`
	rows, err := q.QueryContext(ctx, query, prerequisite.ID, prerequisite.ProjectID, maxDirectTaskDependencies)
	if err != nil {
		return nil, err
	}
	result := make([]TaskReference, 0)
	for rows.Next() {
		var dependent dependencyTask
		if err := rows.Scan(&dependent.ID, &dependent.Number, &dependent.Title, &dependent.CompletedAt, &dependent.SemanticState, &dependent.ProjectKey); err != nil {
			rows.Close()
			return nil, err
		}
		dependent.ProjectID = prerequisite.ProjectID
		dependent.Key = fmt.Sprintf("%s-%d", dependent.ProjectKey, dependent.Number)
		result = append(result, dependencyReference(dependent))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	return result, nil
}

func dependencyInUseColumnDetails(ctx context.Context, q dependencySQL, columnID, nextSemanticState string) (map[string]any, error) {
	rows, err := q.QueryContext(ctx, `SELECT prerequisite.id, prerequisite.number, prerequisite.title, prerequisite_project.key,
		dependent.id, dependent.number, dependent.title, dependent.completed_at, dependent_column.semantic_state, dependent_project.key
		FROM tasks prerequisite
		JOIN projects prerequisite_project ON prerequisite_project.id=prerequisite.project_id
		JOIN task_dependencies td ON td.prerequisite_task_id=prerequisite.id
		JOIN tasks dependent ON dependent.id=td.task_id
		JOIN projects dependent_project ON dependent_project.id=dependent.project_id
		JOIN columns dependent_column ON dependent_column.id=dependent.column_id
		WHERE prerequisite.column_id=?
		  AND prerequisite.deleted_at IS NULL
		  AND dependent.deleted_at IS NULL
		  AND dependent.project_id=prerequisite.project_id
		  AND (
		      (CASE WHEN dependent.column_id=? THEN ? ELSE COALESCE(dependent_column.semantic_state, '') END)='active'
		      OR (
		          dependent.claimed_by IS NOT NULL
		          AND dependent.claim_expires_at IS NOT NULL
		          AND julianday(dependent.claim_expires_at) > julianday('now')
		      )
		  )
		  AND (
		      dependent.column_id=?
		      OR dependent.completed_at IS NULL
		      OR COALESCE(dependent_column.semantic_state, '') <> 'completed'
		  )
		ORDER BY prerequisite.number, prerequisite.id, dependent.number, dependent.id
		LIMIT ?`, columnID, columnID, nextSemanticState, columnID, maxDirectTaskDependencies)
	if err != nil {
		return nil, err
	}
	type blockingTask struct {
		ID         string
		Key        string
		Title      string
		Dependents []TaskReference
	}
	ordered := make([]blockingTask, 0)
	byID := make(map[string]int)
	for rows.Next() {
		var prerequisite dependencyTask
		var dependent dependencyTask
		if err := rows.Scan(&prerequisite.ID, &prerequisite.Number, &prerequisite.Title, &prerequisite.ProjectKey, &dependent.ID, &dependent.Number, &dependent.Title, &dependent.CompletedAt, &dependent.SemanticState, &dependent.ProjectKey); err != nil {
			rows.Close()
			return nil, err
		}
		prerequisite.Key = fmt.Sprintf("%s-%d", prerequisite.ProjectKey, prerequisite.Number)
		dependent.Key = fmt.Sprintf("%s-%d", dependent.ProjectKey, dependent.Number)
		index, ok := byID[prerequisite.ID]
		if !ok {
			index = len(ordered)
			byID[prerequisite.ID] = index
			ordered = append(ordered, blockingTask{ID: prerequisite.ID, Key: prerequisite.Key, Title: prerequisite.Title, Dependents: make([]TaskReference, 0)})
		}
		ordered[index].Dependents = append(ordered[index].Dependents, dependencyReference(dependent))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	tasks := make([]map[string]any, 0, len(ordered))
	for _, task := range ordered {
		tasks = append(tasks, map[string]any{
			"task_id":    task.ID,
			"task_key":   task.Key,
			"task_title": task.Title,
			"dependents": task.Dependents,
		})
	}
	return map[string]any{"column_id": columnID, "tasks": tasks}, nil
}

type dependencyStateChange struct {
	PrerequisiteID  string
	ProjectID       string
	PrerequisiteKey string
	Satisfied       bool
}

func dependencyTaskStateChange(ctx context.Context, q dependencySQL, before dependencyTask) (dependencyStateChange, bool, error) {
	after, err := resolveDependencyTask(ctx, q, before.ID)
	if err != nil {
		return dependencyStateChange{}, false, err
	}
	beforeSatisfied := dependencySatisfied(before)
	afterSatisfied := dependencySatisfied(after)
	if beforeSatisfied == afterSatisfied {
		return dependencyStateChange{}, false, nil
	}
	return dependencyStateChange{
		PrerequisiteID:  after.ID,
		ProjectID:       after.ProjectID,
		PrerequisiteKey: after.Key,
		Satisfied:       afterSatisfied,
	}, true, nil
}

func dependencyColumnStateChanges(ctx context.Context, q dependencySQL, column Column, nextState, completedAt string) ([]dependencyStateChange, error) {
	if column.SemanticState == nextState || (column.SemanticState != "completed" && nextState != "completed") {
		return nil, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT t.id, t.project_id, p.key, t.number, t.title, t.completed_at
		FROM tasks t
		JOIN projects p ON p.id=t.project_id
		WHERE t.column_id=? AND t.deleted_at IS NULL
		ORDER BY t.number, t.id`, column.ID)
	if err != nil {
		return nil, err
	}
	changes := make([]dependencyStateChange, 0)
	for rows.Next() {
		var before dependencyTask
		if err := rows.Scan(&before.ID, &before.ProjectID, &before.ProjectKey, &before.Number, &before.Title, &before.CompletedAt); err != nil {
			rows.Close()
			return nil, err
		}
		before.Key = fmt.Sprintf("%s-%d", before.ProjectKey, before.Number)
		before.SemanticState = column.SemanticState
		after := before
		after.SemanticState = nextState
		if nextState == "completed" {
			after.CompletedAt = sql.NullString{String: completedAt, Valid: true}
		} else if column.SemanticState == "completed" {
			after.CompletedAt = sql.NullString{}
		}
		if dependencySatisfied(before) != dependencySatisfied(after) {
			changes = append(changes, dependencyStateChange{
				PrerequisiteID:  before.ID,
				ProjectID:       before.ProjectID,
				PrerequisiteKey: before.Key,
				Satisfied:       dependencySatisfied(after),
			})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	return changes, nil
}

// emitDependencyStateChanges emits one event for every direct live dependent
// of each changed prerequisite. It reads the reverse edges in one bounded
// query and inserts the events only after closing the result set, keeping the
// helper safe for both *sql.Tx and *sql.Conn transactions.
func emitDependencyStateChanges(ctx context.Context, q dependencySQL, actorID string, changes []dependencyStateChange) error {
	if len(changes) == 0 {
		return nil
	}
	byPrerequisite := make(map[string]dependencyStateChange, len(changes))
	ids := make([]string, 0, len(changes))
	for _, change := range changes {
		if change.PrerequisiteID == "" {
			continue
		}
		if _, exists := byPrerequisite[change.PrerequisiteID]; exists {
			continue
		}
		byPrerequisite[change.PrerequisiteID] = change
		ids = append(ids, change.PrerequisiteID)
	}
	if len(ids) == 0 {
		return nil
	}
	type eventTarget struct {
		PrerequisiteID string
		DependentID    string
		DependentKey   string
	}
	targets := make([]eventTarget, 0)
	const batchSize = 500
	for start := 0; start < len(ids); start += batchSize {
		end := min(start+batchSize, len(ids))
		batch := ids[start:end]
		placeholders := make([]string, len(batch))
		args := make([]any, len(batch))
		for index, id := range batch {
			placeholders[index] = "?"
			args[index] = id
		}
		query := `SELECT td.prerequisite_task_id, dependent.id, dependent.number, dependent_project.key
			FROM task_dependencies td
			JOIN tasks prerequisite ON prerequisite.id=td.prerequisite_task_id AND prerequisite.deleted_at IS NULL
			JOIN tasks dependent ON dependent.id=td.task_id AND dependent.deleted_at IS NULL
			JOIN projects dependent_project ON dependent_project.id=dependent.project_id
			WHERE td.prerequisite_task_id IN (` + strings.Join(placeholders, ",") + `)
			  AND dependent.project_id=prerequisite.project_id
			ORDER BY td.prerequisite_task_id, dependent.number, dependent.id`
		rows, err := q.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var prerequisiteID, dependentID, projectKey string
			var number int
			if err := rows.Scan(&prerequisiteID, &dependentID, &number, &projectKey); err != nil {
				rows.Close()
				return err
			}
			targets = append(targets, eventTarget{
				PrerequisiteID: prerequisiteID,
				DependentID:    dependentID,
				DependentKey:   fmt.Sprintf("%s-%d", projectKey, number),
			})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	for _, target := range targets {
		change, ok := byPrerequisite[target.PrerequisiteID]
		if !ok {
			continue
		}
		payload := map[string]any{
			"dependent_id":     target.DependentID,
			"dependent_key":    target.DependentKey,
			"prerequisite_id":  change.PrerequisiteID,
			"prerequisite_key": change.PrerequisiteKey,
			"satisfied":        change.Satisfied,
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO events(id, type, actor_id, project_id, task_id, payload, created_at) VALUES (?, ?, NULLIF(?, ''), ?, ?, ?, ?)`, newID(), "task.dependency_state_changed", actorID, change.ProjectID, target.DependentID, eventPayload(payload), now()); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) addTaskDependency(ctx context.Context, dependentReference, prerequisiteReference string, expectedVersion int64, actorID string, allowClaimOverride bool) error {
	return s.withImmediateDependencyTx(ctx, func(q dependencySQL) error {
		dependent, err := resolveDependencyTask(ctx, q, dependentReference)
		if err != nil {
			return err
		}
		if !allowClaimOverride {
			if err := dependencyClaimConflict(ctx, q, dependent.ID, actorID); err != nil {
				return err
			}
		}
		prerequisite, err := resolveDependencyTask(ctx, q, prerequisiteReference)
		if err != nil {
			return err
		}
		if dependent.ID == prerequisite.ID {
			return dependencyInvalid(ErrDependencySelfReference, "a task cannot depend on itself", map[string]any{
				"task": dependencyTaskDetails(dependent),
			}, ErrInvalid)
		}
		if dependent.ProjectID != prerequisite.ProjectID {
			return dependencyInvalid(ErrDependencyCrossProject, "dependencies must stay within one project", map[string]any{
				"task_project_id":         dependent.ProjectID,
				"prerequisite_project_id": prerequisite.ProjectID,
				"task_id":                 dependent.ID,
				"prerequisite_task_id":    prerequisite.ID,
			}, ErrInvalid)
		}
		if dependent.Version != expectedVersion {
			return conflict("task has changed", map[string]any{
				"current_version":  dependent.Version,
				"expected_version": expectedVersion,
				"current": map[string]any{
					"id":      dependent.ID,
					"key":     dependent.Key,
					"version": dependent.Version,
				},
			})
		}

		var edgeExists int
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_dependencies WHERE task_id=? AND prerequisite_task_id=?)`, dependent.ID, prerequisite.ID).Scan(&edgeExists); err != nil {
			return err
		}
		if edgeExists != 0 {
			return dependencyInvalid(ErrDependencyAlreadyExists, "dependency already exists", map[string]any{
				"task":         dependencyTaskDetails(dependent),
				"prerequisite": dependencyTaskDetails(prerequisite),
			}, ErrAlreadyExists)
		}

		var prerequisiteCount, dependentCount int
		if err := q.QueryRowContext(ctx, `SELECT COUNT(1)
			FROM task_dependencies td
			JOIN tasks prerequisite ON prerequisite.id=td.prerequisite_task_id
			WHERE td.task_id=? AND prerequisite.project_id=? AND prerequisite.deleted_at IS NULL`, dependent.ID, dependent.ProjectID).Scan(&prerequisiteCount); err != nil {
			return err
		}
		if prerequisiteCount >= maxDirectTaskDependencies {
			return dependencyInvalid(ErrDependencyLimitExceeded, "task has reached its direct prerequisite limit", map[string]any{
				"direction": "prerequisites",
				"limit":     maxDirectTaskDependencies,
				"count":     prerequisiteCount,
				"task_id":   dependent.ID,
			}, ErrInvalid)
		}
		if err := q.QueryRowContext(ctx, `SELECT COUNT(1)
			FROM task_dependencies td
			JOIN tasks dependent_task ON dependent_task.id=td.task_id
			WHERE td.prerequisite_task_id=? AND dependent_task.project_id=? AND dependent_task.deleted_at IS NULL`, prerequisite.ID, prerequisite.ProjectID).Scan(&dependentCount); err != nil {
			return err
		}
		if dependentCount >= maxDirectTaskDependencies {
			return dependencyInvalid(ErrDependencyLimitExceeded, "task has reached its direct dependent limit", map[string]any{
				"direction": "dependents",
				"limit":     maxDirectTaskDependencies,
				"count":     dependentCount,
				"task_id":   prerequisite.ID,
			}, ErrInvalid)
		}

		var reachable int
		if err := q.QueryRowContext(ctx, `WITH RECURSIVE prerequisite_graph(task_id) AS (
			SELECT prerequisite_task_id FROM task_dependencies WHERE task_id=?
			UNION
			SELECT td.prerequisite_task_id
			FROM task_dependencies td
			JOIN prerequisite_graph graph ON graph.task_id=td.task_id
		)
		SELECT EXISTS(SELECT 1 FROM prerequisite_graph WHERE task_id=?)`, prerequisite.ID, dependent.ID).Scan(&reachable); err != nil {
			return err
		}
		if reachable != 0 {
			details, err := dependencyCycleDetails(ctx, q, prerequisite, dependent)
			if err != nil {
				return err
			}
			return dependencyInvalid(ErrDependencyCycle, "dependency would create a cycle", details, ErrConflict)
		}

		createdAt := now()
		if _, err := q.ExecContext(ctx, `INSERT INTO task_dependencies(task_id, prerequisite_task_id, created_by, created_at) VALUES (?, ?, NULLIF(?, ''), ?)`, dependent.ID, prerequisite.ID, actorID, createdAt); err != nil {
			return mapDependencyMutationError(ctx, q, err, dependent, prerequisite)
		}
		result, err := q.ExecContext(ctx, `UPDATE tasks SET version=version+1, updated_at=? WHERE id=? AND version=? AND deleted_at IS NULL`, createdAt, dependent.ID, expectedVersion)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return conflict("task has changed", map[string]any{
				"current_version":  dependent.Version,
				"expected_version": expectedVersion,
			})
		}
		return insertDependencyEvent(ctx, q, "task.dependency_added", actorID, dependent, prerequisite, dependent.Version+1)
	})
}

func (s *Store) removeTaskDependency(ctx context.Context, dependentReference, prerequisiteReference string, expectedVersion int64, actorID string, allowClaimOverride bool) error {
	return s.withImmediateDependencyTx(ctx, func(q dependencySQL) error {
		dependent, err := resolveDependencyTask(ctx, q, dependentReference)
		if err != nil {
			return err
		}
		if !allowClaimOverride {
			if err := dependencyClaimConflict(ctx, q, dependent.ID, actorID); err != nil {
				return err
			}
		}
		prerequisite, err := resolveDependencyTask(ctx, q, prerequisiteReference)
		if err != nil {
			return err
		}
		if dependent.ID == prerequisite.ID {
			return dependencyInvalid(ErrDependencySelfReference, "a task cannot depend on itself", map[string]any{
				"task": dependencyTaskDetails(dependent),
			}, ErrInvalid)
		}
		if dependent.ProjectID != prerequisite.ProjectID {
			return dependencyInvalid(ErrDependencyCrossProject, "dependencies must stay within one project", map[string]any{
				"task_project_id":         dependent.ProjectID,
				"prerequisite_project_id": prerequisite.ProjectID,
				"task_id":                 dependent.ID,
				"prerequisite_task_id":    prerequisite.ID,
			}, ErrInvalid)
		}
		if dependent.Version != expectedVersion {
			return conflict("task has changed", map[string]any{
				"current_version":  dependent.Version,
				"expected_version": expectedVersion,
			})
		}

		var edgeExists int
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_dependencies WHERE task_id=? AND prerequisite_task_id=?)`, dependent.ID, prerequisite.ID).Scan(&edgeExists); err != nil {
			return err
		}
		if edgeExists == 0 {
			return dependencyNotFound("dependency not found", map[string]any{
				"task_id":              dependent.ID,
				"prerequisite_task_id": prerequisite.ID,
			})
		}
		createdAt := now()
		query := `DELETE FROM task_dependencies WHERE task_id=? AND prerequisite_task_id=?`
		result, err := q.ExecContext(ctx, query, dependent.ID, prerequisite.ID)
		if err != nil {
			return err
		}
		removed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if removed != 1 {
			return dependencyNotFound("dependency not found", map[string]any{
				"task_id":              dependent.ID,
				"prerequisite_task_id": prerequisite.ID,
			})
		}
		updateQuery := `UPDATE tasks SET version=version+1, updated_at=? WHERE id=? AND deleted_at IS NULL`
		updateArgs := []any{createdAt, dependent.ID}
		updateQuery += ` AND version=?`
		updateArgs = append(updateArgs, expectedVersion)
		updated, err := q.ExecContext(ctx, updateQuery, updateArgs...)
		if err != nil {
			return err
		}
		changed, err := updated.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return conflict("task has changed", map[string]any{
				"current_version":  dependent.Version,
				"expected_version": expectedVersion,
			})
		}
		return insertDependencyEvent(ctx, q, "task.dependency_removed", actorID, dependent, prerequisite, dependent.Version+1)
	})
}

// AddTaskDependency adds prerequisiteReference as a direct prerequisite of
// dependentReference. The source version is required to preserve the same
// optimistic-concurrency contract as other task writes. The returned task is
// the dependent and only its editable version advances.
func (s *Store) AddTaskDependency(ctx context.Context, dependentReference, prerequisiteReference string, expectedVersion int64, actorID string) (Task, error) {
	return s.addTaskDependencyWithClaimOverride(ctx, dependentReference, prerequisiteReference, expectedVersion, actorID, false)
}

// AddTaskDependencyWithClaimOverride is the administrator variant used by the
// HTTP layer for a human administrator. The claim check remains inside the
// dependency transaction; callers must not pass true based only on a
// preflight read. Bearer identities are never granted this override by the
// HTTP layer.
func (s *Store) AddTaskDependencyWithClaimOverride(ctx context.Context, dependentReference, prerequisiteReference string, expectedVersion int64, actorID string, allowClaimOverride bool) (Task, error) {
	return s.addTaskDependencyWithClaimOverride(ctx, dependentReference, prerequisiteReference, expectedVersion, actorID, allowClaimOverride)
}

func (s *Store) addTaskDependencyWithClaimOverride(ctx context.Context, dependentReference, prerequisiteReference string, expectedVersion int64, actorID string, allowClaimOverride bool) (Task, error) {
	if expectedVersion <= 0 {
		return Task{}, ErrPrecondition
	}
	if err := s.addTaskDependency(ctx, dependentReference, prerequisiteReference, expectedVersion, actorID, allowClaimOverride); err != nil {
		return Task{}, err
	}
	return s.ResolveTaskReference(ctx, dependentReference)
}

// RemoveTaskDependency removes one direct prerequisite edge. The source
// version is required to preserve optimistic concurrency and is incremented
// only on the dependent task.
func (s *Store) RemoveTaskDependency(ctx context.Context, dependentReference, prerequisiteReference string, expectedVersion int64, actorID string) (Task, error) {
	return s.removeTaskDependencyWithClaimOverride(ctx, dependentReference, prerequisiteReference, expectedVersion, actorID, false)
}

// RemoveTaskDependencyWithClaimOverride is the administrator counterpart to
// AddTaskDependencyWithClaimOverride. See that method for the trust boundary.
func (s *Store) RemoveTaskDependencyWithClaimOverride(ctx context.Context, dependentReference, prerequisiteReference string, expectedVersion int64, actorID string, allowClaimOverride bool) (Task, error) {
	return s.removeTaskDependencyWithClaimOverride(ctx, dependentReference, prerequisiteReference, expectedVersion, actorID, allowClaimOverride)
}

func (s *Store) removeTaskDependencyWithClaimOverride(ctx context.Context, dependentReference, prerequisiteReference string, expectedVersion int64, actorID string, allowClaimOverride bool) (Task, error) {
	if expectedVersion <= 0 {
		return Task{}, ErrPrecondition
	}
	if err := s.removeTaskDependency(ctx, dependentReference, prerequisiteReference, expectedVersion, actorID, allowClaimOverride); err != nil {
		return Task{}, err
	}
	return s.ResolveTaskReference(ctx, dependentReference)
}

func insertDependencyEvent(ctx context.Context, q dependencySQL, eventType, actorID string, dependent, prerequisite dependencyTask, version int64) error {
	payload := map[string]any{
		"dependent_id":     dependent.ID,
		"dependent_key":    dependent.Key,
		"prerequisite_id":  prerequisite.ID,
		"prerequisite_key": prerequisite.Key,
		"version":          version,
	}
	_, err := q.ExecContext(ctx, `INSERT INTO events(id, type, actor_id, project_id, task_id, payload, created_at) VALUES (?, ?, NULLIF(?, ''), ?, ?, ?, ?)`, newID(), eventType, actorID, dependent.ProjectID, dependent.ID, eventPayload(payload), now())
	return err
}

// GetTaskDependencies returns direct live relations in both directions. The
// source and every relation are soft-delete safe; dangling edges left by a
// retained pre-cleanup writer are intentionally invisible.
func (s *Store) GetTaskDependencies(ctx context.Context, reference string) (TaskDependencies, error) {
	return s.listTaskDependencies(ctx, reference)
}

func (s *Store) listTaskDependencies(ctx context.Context, reference string) (TaskDependencies, error) {
	task, err := resolveDependencyTask(ctx, s.DB, reference)
	if err != nil {
		return TaskDependencies{}, err
	}
	result := TaskDependencies{
		Prerequisites: make([]TaskReference, 0),
		Dependents:    make([]TaskReference, 0),
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT prerequisite.id, prerequisite.number, prerequisite.title, prerequisite.completed_at, prerequisite_column.semantic_state, prerequisite_project.key
		FROM task_dependencies td
		JOIN tasks prerequisite ON prerequisite.id=td.prerequisite_task_id
		JOIN projects prerequisite_project ON prerequisite_project.id=prerequisite.project_id
		JOIN columns prerequisite_column ON prerequisite_column.id=prerequisite.column_id
		WHERE td.task_id=?
		  AND prerequisite.project_id=?
		  AND prerequisite.deleted_at IS NULL
		ORDER BY prerequisite.number, prerequisite.id
		LIMIT ?`, task.ID, task.ProjectID, maxDirectTaskDependencies)
	if err != nil {
		return TaskDependencies{}, err
	}
	for rows.Next() {
		var relation dependencyTask
		if err := rows.Scan(&relation.ID, &relation.Number, &relation.Title, &relation.CompletedAt, &relation.SemanticState, &relation.ProjectKey); err != nil {
			rows.Close()
			return TaskDependencies{}, err
		}
		relation.ProjectID = task.ProjectID
		relation.Key = fmt.Sprintf("%s-%d", relation.ProjectKey, relation.Number)
		result.Prerequisites = append(result.Prerequisites, dependencyReference(relation))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return TaskDependencies{}, err
	}
	rows.Close()

	rows, err = s.DB.QueryContext(ctx, `SELECT dependent.id, dependent.number, dependent.title, dependent.completed_at, dependent_column.semantic_state, dependent_project.key
		FROM task_dependencies td
		JOIN tasks dependent ON dependent.id=td.task_id
		JOIN projects dependent_project ON dependent_project.id=dependent.project_id
		JOIN columns dependent_column ON dependent_column.id=dependent.column_id
		WHERE td.prerequisite_task_id=?
		  AND dependent.project_id=?
		  AND dependent.deleted_at IS NULL
		ORDER BY dependent.number, dependent.id
		LIMIT ?`, task.ID, task.ProjectID, maxDirectTaskDependencies)
	if err != nil {
		return TaskDependencies{}, err
	}
	for rows.Next() {
		var relation dependencyTask
		if err := rows.Scan(&relation.ID, &relation.Number, &relation.Title, &relation.CompletedAt, &relation.SemanticState, &relation.ProjectKey); err != nil {
			rows.Close()
			return TaskDependencies{}, err
		}
		relation.ProjectID = task.ProjectID
		relation.Key = fmt.Sprintf("%s-%d", relation.ProjectKey, relation.Number)
		result.Dependents = append(result.Dependents, dependencyReference(relation))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return TaskDependencies{}, err
	}
	rows.Close()
	return result, nil
}

// populateTaskDependencySummaries fills summaries for a collection with one
// grouped query. Callers should invoke it after scanning their task page and
// before returning the page; an empty slice is a no-op. The source task and
// both relation directions are filtered for live, same-project rows.
func (s *Store) populateTaskDependencySummaries(ctx context.Context, tasks []Task) error {
	if len(tasks) == 0 {
		return nil
	}
	ids := make([]string, 0, len(tasks))
	seen := make(map[string]struct{}, len(tasks))
	for _, task := range tasks {
		if task.ID == "" {
			continue
		}
		if _, ok := seen[task.ID]; ok {
			continue
		}
		seen[task.ID] = struct{}{}
		ids = append(ids, task.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	for i := range tasks {
		tasks[i].DependencySummary = DependencySummary{}
	}
	placeholders := make([]string, len(ids))
	for i := range ids {
		placeholders[i] = "?"
	}
	// The candidate list is repeated for each aggregate and the final task
	// selection. This keeps the grouped work bounded to the requested page
	// rather than scanning all historical graph rows.
	args := make([]any, 0, len(ids)*3)
	for repeat := 0; repeat < 3; repeat++ {
		for _, id := range ids {
			args = append(args, id)
		}
	}
	query := `WITH direct_prerequisites AS (
		SELECT td.task_id,
		       COUNT(1) AS prerequisite_count,
		       SUM(CASE WHEN prerequisite.completed_at IS NULL OR prerequisite_column.semantic_state <> 'completed' THEN 1 ELSE 0 END) AS unmet_prerequisite_count
		FROM task_dependencies td
		JOIN tasks source ON source.id=td.task_id AND source.deleted_at IS NULL
		JOIN tasks prerequisite ON prerequisite.id=td.prerequisite_task_id
		JOIN columns prerequisite_column ON prerequisite_column.id=prerequisite.column_id
		WHERE td.task_id IN (` + strings.Join(placeholders, ",") + `)
		  AND prerequisite.deleted_at IS NULL
		  AND prerequisite.project_id=source.project_id
		GROUP BY td.task_id
	), direct_dependents AS (
		SELECT td.prerequisite_task_id,
		       COUNT(1) AS dependent_count
		FROM task_dependencies td
		JOIN tasks prerequisite ON prerequisite.id=td.prerequisite_task_id AND prerequisite.deleted_at IS NULL
		JOIN tasks dependent ON dependent.id=td.task_id
		WHERE td.prerequisite_task_id IN (` + strings.Join(placeholders, ",") + `)
		  AND dependent.deleted_at IS NULL
		  AND dependent.project_id=prerequisite.project_id
		GROUP BY td.prerequisite_task_id
	)
	SELECT task.id,
	       COALESCE(direct_prerequisites.prerequisite_count, 0),
	       COALESCE(direct_prerequisites.unmet_prerequisite_count, 0),
	       COALESCE(direct_dependents.dependent_count, 0)
	FROM tasks task
	LEFT JOIN direct_prerequisites ON direct_prerequisites.task_id=task.id
	LEFT JOIN direct_dependents ON direct_dependents.prerequisite_task_id=task.id
		WHERE task.id IN (` + strings.Join(placeholders, ",") + `)
	  AND task.deleted_at IS NULL`
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	byID := make(map[string]DependencySummary, len(ids))
	for rows.Next() {
		var id string
		var summary DependencySummary
		if err := rows.Scan(&id, &summary.PrerequisiteCount, &summary.UnmetPrerequisiteCount, &summary.DependentCount); err != nil {
			rows.Close()
			return err
		}
		summary.Blocked = summary.UnmetPrerequisiteCount > 0
		byID[id] = summary
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for i := range tasks {
		if summary, ok := byID[tasks[i].ID]; ok {
			tasks[i].DependencySummary = summary
		}
	}
	return nil
}

func (s *Store) populateDependencySummary(ctx context.Context, task *Task) error {
	if task == nil {
		return nil
	}
	tasks := []Task{*task}
	if err := s.populateTaskDependencySummaries(ctx, tasks); err != nil {
		return err
	}
	*task = tasks[0]
	return nil
}
