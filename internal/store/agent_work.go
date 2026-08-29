package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// AgentWorkStaleAfter is the server-side liveness window for an agent pulse.
// Keep SQL callers on the same value by deriving their cutoff from this
// exported constant rather than duplicating a duration literal.
const AgentWorkStaleAfter = 15 * time.Minute

var agentWorkOperationIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]*$`)

func validateAgentWorkOperationID(operationID string) (string, error) {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" || utf8.RuneCountInString(operationID) > 128 || !agentWorkOperationIDPattern.MatchString(operationID) {
		return "", invalid("operation_id must be between 1 and 128 safe identifier characters", nil)
	}
	return operationID, nil
}

func validateAgentWorkInput(input AgentWorkInput) (AgentWorkInput, error) {
	var err error
	input.OperationID, err = validateAgentWorkOperationID(input.OperationID)
	if err != nil {
		return AgentWorkInput{}, err
	}

	input.State = strings.TrimSpace(input.State)
	switch input.State {
	case "working", "waiting", "verifying", "handoff":
	default:
		return AgentWorkInput{}, invalid("state must be working, waiting, verifying, or handoff", nil)
	}

	input.Phase = strings.TrimSpace(input.Phase)
	if utf8.RuneCountInString(input.Phase) > 120 {
		return AgentWorkInput{}, invalid("phase is too long", nil)
	}

	input.Summary = strings.TrimSpace(input.Summary)
	if input.Summary == "" {
		return AgentWorkInput{}, invalid("summary is required", nil)
	}
	if utf8.RuneCountInString(input.Summary) > 1000 {
		return AgentWorkInput{}, invalid("summary is too long", nil)
	}

	input.NextAction = strings.TrimSpace(input.NextAction)
	if utf8.RuneCountInString(input.NextAction) > 1000 {
		return AgentWorkInput{}, invalid("next_action is too long", nil)
	}

	refs := input.CheckpointRefs
	if refs == nil {
		refs = []string{}
	}
	if len(refs) > 100 {
		return AgentWorkInput{}, invalid("checkpoint_refs must contain at most 100 items", nil)
	}
	normalizedRefs := make([]string, 0, len(refs))
	seenRefs := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" || utf8.RuneCountInString(ref) > 128 {
			return AgentWorkInput{}, invalid("checkpoint_refs items must be between 1 and 128 characters", nil)
		}
		if _, exists := seenRefs[ref]; exists {
			return AgentWorkInput{}, invalid("checkpoint_refs must not contain duplicate items", nil)
		}
		seenRefs[ref] = struct{}{}
		normalizedRefs = append(normalizedRefs, ref)
	}
	input.CheckpointRefs = normalizedRefs

	if (input.CheckpointCompleted == nil) != (input.CheckpointTotal == nil) {
		return AgentWorkInput{}, invalid("checkpoint_completed and checkpoint_total must be provided together", nil)
	}
	if input.CheckpointCompleted != nil {
		completed, total := *input.CheckpointCompleted, *input.CheckpointTotal
		if total < 1 || total > 100 || completed < 0 || completed > total {
			return AgentWorkInput{}, invalid("checkpoint progress must satisfy 0 <= completed <= total <= 100", nil)
		}
		if len(normalizedRefs) > 0 && len(normalizedRefs) != total {
			return AgentWorkInput{}, invalid("checkpoint_refs length must equal checkpoint_total", nil)
		}
	} else if len(normalizedRefs) > 0 {
		return AgentWorkInput{}, invalid("checkpoint_total is required when checkpoint_refs is non-empty", nil)
	}
	return input, nil
}

func agentWorkStaleCutoff(at time.Time) string {
	return at.UTC().Add(-AgentWorkStaleAfter).Format(time.RFC3339Nano)
}

func scanAgentWork(scanner interface{ Scan(...any) error }) (AgentWork, error) {
	var work AgentWork
	var refsJSON string
	var completed, total sql.NullInt64
	var stale int
	var taskCompleted int
	if err := scanner.Scan(&work.OperationID, &work.ActorID, &work.State, &work.Phase, &work.Summary, &work.NextAction, &refsJSON, &completed, &total, &work.StartedAt, &work.UpdatedAt, &stale, &taskCompleted); err != nil {
		return AgentWork{}, err
	}
	if err := json.Unmarshal([]byte(refsJSON), &work.CheckpointRefs); err != nil {
		return AgentWork{}, fmt.Errorf("decode agent work checkpoint_refs: %w", err)
	}
	if work.CheckpointRefs == nil {
		work.CheckpointRefs = []string{}
	}
	if completed.Valid {
		value := int(completed.Int64)
		work.CheckpointCompleted = &value
	}
	if total.Valid {
		value := int(total.Int64)
		work.CheckpointTotal = &value
	}
	completedTask := taskCompleted != 0
	if completedTask {
		// A completed task is not actionable or stale, even when its retained
		// snapshot is old or was waiting/handoff at completion time.
		work.Stale = false
		work.ActionNeeded = false
	} else {
		work.Stale = stale != 0
		work.ActionNeeded = work.Stale || work.State == "waiting" || work.State == "handoff"
	}
	return work, nil
}

func (s *Store) agentWork(ctx context.Context, taskID string) (*AgentWork, error) {
	return s.agentWorkAt(ctx, taskID, time.Now().UTC())
}

// agentWorkAt derives stale with the same SQLite predicate and request-time
// cutoff used by collection filters. This keeps a returned snapshot's stale
// flag consistent with the query that selected it, including SQLite's
// fractional-second handling at the inclusive 15-minute boundary.
func (s *Store) agentWorkAt(ctx context.Context, taskID string, at time.Time) (*AgentWork, error) {
	cutoff := agentWorkStaleCutoff(at)
	row := s.DB.QueryRowContext(ctx, `SELECT aw.operation_id, aw.actor_id, aw.state, aw.phase, aw.summary, aw.next_action, aw.checkpoint_refs, aw.checkpoint_completed, aw.checkpoint_total, aw.started_at, aw.updated_at, CASE WHEN julianday(aw.updated_at) <= julianday(?) THEN 1 ELSE 0 END, CASE WHEN t.completed_at IS NOT NULL THEN 1 ELSE 0 END FROM task_agent_work aw JOIN tasks t ON t.id=aw.task_id WHERE aw.task_id=? AND t.deleted_at IS NULL`, cutoff, taskID)
	work, err := scanAgentWork(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &work, nil
}

// HeartbeatAgentWork refreshes only the liveness timestamp of an existing
// agent-work snapshot. The guarded UPDATE is the authorization boundary: the
// task must still be unfinished, the caller must own an unexpired claim, and
// the snapshot must belong to both the caller and operation ID. Keeping all
// predicates in the UPDATE makes expiry safe when this transaction waits for
// SQLite's single writer lock; a failed heartbeat performs no write.
func (s *Store) HeartbeatAgentWork(ctx context.Context, taskID, operationID, actorID string) (Task, error) {
	validatedOperationID, err := validateAgentWorkOperationID(operationID)
	if err != nil {
		return Task{}, err
	}

	err = s.withTx(ctx, func(tx *sql.Tx) error {
		// SQLite evaluates every 'now' expression in this statement from the
		// same clock reading. Both the timestamp assignment and lease predicate
		// therefore happen after this UPDATE acquires SQLite's write lock; time
		// spent waiting behind another writer cannot permit a heartbeat after the
		// claim has expired or write an old application-side timestamp.
		result, err := tx.ExecContext(ctx, `UPDATE task_agent_work SET updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE task_id=? AND operation_id=? AND actor_id=? AND EXISTS (SELECT 1 FROM tasks t WHERE t.id=task_agent_work.task_id AND t.deleted_at IS NULL AND t.completed_at IS NULL AND t.claimed_by=? AND t.claim_expires_at IS NOT NULL AND julianday(t.claim_expires_at) > julianday('now'))`, taskID, validatedOperationID, actorID, actorID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 0 {
			return nil
		}

		// Classify the failed guard without issuing any write. These checks are
		// intentionally ordered like task progress: a missing/deleted task is
		// not-found, a completed task is a conflict, and all claim/snapshot
		// authorization failures are forbidden without disclosing snapshot
		// details.
		var completed sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT completed_at FROM tasks WHERE id=? AND deleted_at IS NULL`, taskID).Scan(&completed)
		if errors.Is(err, sql.ErrNoRows) {
			return notFound("task not found")
		}
		if err != nil {
			return err
		}
		if completed.Valid {
			return conflict("task is already finished", nil)
		}

		var snapshotOperationID, snapshotActorID string
		err = tx.QueryRowContext(ctx, `SELECT operation_id, actor_id FROM task_agent_work WHERE task_id=?`, taskID).Scan(&snapshotOperationID, &snapshotActorID)
		if errors.Is(err, sql.ErrNoRows) {
			return forbidden("a matching agent work snapshot is required")
		}
		if err != nil {
			return err
		}
		if snapshotOperationID != validatedOperationID || snapshotActorID != actorID {
			return forbidden("a matching agent work snapshot is required")
		}
		return forbidden("an active claim owned by this actor is required")
	})
	if err != nil {
		return Task{}, err
	}
	return s.GetTask(ctx, taskID)
}

// PublishAgentWork atomically advances a task version and replaces its live
// agent-work snapshot, comment, and progress event. The guarded task UPDATE
// is the authorization boundary: a pulse requires an unexpired claim owned by
// actorID, the expected task version, and an unfinished task.
func (s *Store) PublishAgentWork(ctx context.Context, taskID string, input AgentWorkInput, expectedTaskVersion int64, actorID string) (Task, error) {
	validated, err := validateAgentWorkInput(input)
	if err != nil {
		return Task{}, err
	}
	if expectedTaskVersion <= 0 {
		return Task{}, ErrPrecondition
	}
	// Resolve the task before entering the write transaction so a missing task
	// gets the same redacted not-found behavior as other task mutations. The
	// transaction still re-checks the row and supplies a current version on a
	// concurrent stale write.
	current, err := s.GetTask(ctx, taskID)
	if err != nil {
		return Task{}, err
	}
	refsJSON, err := json.Marshal(validated.CheckpointRefs)
	if err != nil {
		return Task{}, err
	}
	var needsFreshConflictTask bool
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		// SQLite evaluates every 'now' expression in one statement from the
		// same clock reading. The guarded UPDATE acquires the write lock before
		// authorizing the lease, so time spent waiting behind another writer
		// cannot let an already-expired claim publish progress.
		result, err := tx.ExecContext(ctx, `UPDATE tasks SET version=version+1 WHERE id=? AND version=? AND deleted_at IS NULL AND completed_at IS NULL AND claimed_by=? AND claim_expires_at IS NOT NULL AND julianday(claim_expires_at) > julianday('now')`, taskID, expectedTaskVersion, actorID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed == 0 {
			var latest Task
			latest, err = taskFromRow(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks t WHERE t.id=? AND t.deleted_at IS NULL`, taskID))
			if errors.Is(err, sql.ErrNoRows) {
				return notFound("task not found")
			}
			if err != nil {
				return err
			}
			if latest.Version != expectedTaskVersion {
				needsFreshConflictTask = true
				return conflict("task has changed", map[string]any{"current": latest})
			}
			if latest.CompletedAt != nil {
				return conflict("task is already finished", nil)
			}
			return forbidden("an active claim owned by this actor is required")
		}

		// Capture the application timestamp only after the guarded UPDATE has
		// acquired SQLite's write lock. RFC3339Nano avoids collapsing rapid
		// consecutive pulses into the same millisecond while the lease decision
		// remains bound to SQLite's in-statement clock above.
		timestamp := now()
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET updated_at=? WHERE id=?`, timestamp, taskID); err != nil {
			return err
		}

		var oldOperationID, oldActorID, oldStartedAt sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT operation_id, actor_id, started_at FROM task_agent_work WHERE task_id=?`, taskID).Scan(&oldOperationID, &oldActorID, &oldStartedAt)
		if errors.Is(err, sql.ErrNoRows) {
			err = nil
		}
		if err != nil {
			return err
		}
		startedAt := timestamp
		if oldOperationID.Valid && oldActorID.Valid && oldOperationID.String == validated.OperationID && oldActorID.String == actorID && oldStartedAt.Valid {
			startedAt = oldStartedAt.String
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_agent_work(task_id, operation_id, actor_id, state, phase, summary, next_action, checkpoint_refs, checkpoint_completed, checkpoint_total, started_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(task_id) DO UPDATE SET operation_id=excluded.operation_id, actor_id=excluded.actor_id, state=excluded.state, phase=excluded.phase, summary=excluded.summary, next_action=excluded.next_action, checkpoint_refs=excluded.checkpoint_refs, checkpoint_completed=excluded.checkpoint_completed, checkpoint_total=excluded.checkpoint_total, started_at=excluded.started_at, updated_at=excluded.updated_at`, taskID, validated.OperationID, actorID, validated.State, validated.Phase, validated.Summary, validated.NextAction, string(refsJSON), nullableIntArg(validated.CheckpointCompleted), nullableIntArg(validated.CheckpointTotal), startedAt, timestamp); err != nil {
			return err
		}

		commentBody := validated.Summary
		if validated.NextAction != "" {
			commentBody += "\n\nNext: " + validated.NextAction
		}
		commentID := newID()
		if _, err := tx.ExecContext(ctx, `INSERT INTO comments(id, task_id, actor_id, body, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, commentID, taskID, actorID, commentBody, timestamp, timestamp); err != nil {
			return err
		}
		var projectID string
		if err := tx.QueryRowContext(ctx, `SELECT project_id FROM tasks WHERE id=?`, taskID).Scan(&projectID); err != nil {
			return err
		}
		if _, err := insertEvent(ctx, tx, "comment.created", actorID, projectID, taskID, map[string]any{"comment_id": commentID}); err != nil {
			return err
		}
		payload := map[string]any{
			"version":   expectedTaskVersion + 1,
			"state":     validated.State,
			"completed": nil,
			"total":     nil,
		}
		if validated.CheckpointCompleted != nil {
			payload["completed"] = *validated.CheckpointCompleted
			payload["total"] = *validated.CheckpointTotal
		}
		_, err = insertEvent(ctx, tx, "task.progressed", actorID, projectID, taskID, payload)
		return err
	})
	if err != nil {
		if needsFreshConflictTask {
			// The write transaction has rolled back now, so a normal enriched read
			// gives callers a usable current task (including its key and labels)
			// instead of the sparse task row available inside the transaction.
			if latest, readErr := s.GetTask(ctx, taskID); readErr == nil {
				return Task{}, conflict("task has changed", map[string]any{"current": latest})
			} else if errors.Is(readErr, ErrNotFound) {
				return Task{}, notFound("task not found")
			}
			return Task{}, conflict("task has changed", map[string]any{"current": current})
		}
		return Task{}, err
	}
	return s.GetTask(ctx, taskID)
}

func nullableIntArg(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}
