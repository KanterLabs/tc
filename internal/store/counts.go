package store

import (
	"context"
	"strings"
	"time"
)

// CountIssues returns the number of live bug tasks in the supplied project
// ceiling. A nil project list means that the caller is unscoped; a non-nil
// empty list intentionally matches no projects.
func (s *Store) CountIssues(ctx context.Context, projectIDs []string) (int, error) {
	query := `SELECT COUNT(1) FROM tasks t WHERE t.deleted_at IS NULL AND t.kind='bug'`
	args := make([]any, 0, len(projectIDs))
	query, args = appendProjectCeiling(query, args, projectIDs)
	var count int
	if err := s.DB.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// CountMyWork returns the count represented by the selected My Work view
// without materializing task rows. Live work follows the collection's
// cross-project pulse semantics; assigned work follows the legacy actor
// assignment/active-claim semantics.
func (s *Store) CountMyWork(ctx context.Context, actorID string, projectIDs []string, live bool) (int, error) {
	var query string
	args := make([]any, 0, len(projectIDs)+3)
	if live {
		query = `SELECT COUNT(1) FROM tasks t
			JOIN task_agent_work aw ON aw.task_id=t.id
			JOIN projects p ON p.id=t.project_id
			WHERE t.deleted_at IS NULL AND t.completed_at IS NULL AND p.archived_at IS NULL`
	} else {
		query = `SELECT COUNT(1) FROM tasks t
			WHERE t.deleted_at IS NULL
			AND (t.assignee_id=? OR (t.claimed_by=? AND t.claim_expires_at IS NOT NULL AND julianday(t.claim_expires_at) > julianday(?)))`
		now := time.Now().UTC().Format(time.RFC3339Nano)
		args = append(args, actorID, actorID, now)
	}
	query, args = appendProjectCeiling(query, args, projectIDs)
	var count int
	if err := s.DB.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// CountReopenedIssues counts distinct live bug task IDs with at least one
// bug.reopened event in the explicit inclusive [since, asOf] UTC interval.
// The distinct task identity matches the Issues view's historical metric
// while the bounded server query avoids the client's finite event buffer.
func (s *Store) CountReopenedIssues(ctx context.Context, projectIDs []string, since, asOf time.Time) (int, error) {
	query := `SELECT COUNT(DISTINCT e.task_id)
		FROM events e
		JOIN tasks t ON t.id=e.task_id
		WHERE e.type='bug.reopened'
		  AND e.task_id IS NOT NULL
		  AND e.created_at >= ?
		  AND e.created_at <= ?
		  AND t.deleted_at IS NULL
		  AND t.kind='bug'`
	args := []any{since.UTC().Format(time.RFC3339Nano), asOf.UTC().Format(time.RFC3339Nano)}
	query, args = appendProjectCeiling(query, args, projectIDs)
	var count int
	if err := s.DB.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// appendProjectCeiling adds the same allow-list semantics used by global
// collection queries. Keep this helper local to count queries so aggregate
// responses cannot accidentally bypass bearer project restrictions.
func appendProjectCeiling(query string, args []any, projectIDs []string) (string, []any) {
	if projectIDs != nil && len(projectIDs) == 0 {
		return query + ` AND 1=0`, args
	}
	if len(projectIDs) == 0 {
		return query, args
	}
	placeholders := make([]string, len(projectIDs))
	for i, projectID := range projectIDs {
		placeholders[i] = "?"
		args = append(args, projectID)
	}
	return query + ` AND t.project_id IN (` + strings.Join(placeholders, ",") + `)`, args
}
