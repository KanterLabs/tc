package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestVersionedColumnReclassificationRejectsUnresolvedBugAtomically(t *testing.T) {
	f := newDependencyFixture(t, "SEMADMINUNRES")
	backlogState := "backlog"
	extraBacklog, err := f.store.CreateColumn(f.ctx, f.project.ID, ColumnInput{
		Name:          semanticRaceStringPtr("Bug intake"),
		SemanticState: &backlogState,
	}, f.actor.ID)
	if err != nil {
		t.Fatalf("create extra backlog column: %v", err)
	}
	bug := semanticRaceBug(t, f, extraBacklog.ID, "unresolved bug")
	beforeColumn, err := f.store.GetColumn(f.ctx, extraBacklog.ID)
	if err != nil {
		t.Fatalf("read bug column: %v", err)
	}
	beforeTask := dependencyDurableState(t, f, bug.ID)
	beforeEvents := semanticRaceProjectEventCount(t, f)

	completedState := "completed"
	_, err = f.store.UpdateColumnWithVersion(f.ctx, extraBacklog.ID, ColumnInput{SemanticState: &completedState}, f.actor.ID, true, semanticRaceVersionPtr(beforeColumn.Version))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("unresolved bug reclassification error = %v, want ErrConflict", err)
	}

	afterColumn, err := f.store.GetColumn(f.ctx, extraBacklog.ID)
	if err != nil {
		t.Fatalf("reload rejected bug column: %v", err)
	}
	if !reflect.DeepEqual(afterColumn, beforeColumn) {
		t.Fatalf("rejected unresolved-bug reclassification changed column:\nbefore=%+v\nafter=%+v", beforeColumn, afterColumn)
	}
	assertDependencyTaskUnchanged(t, f, bug.ID, beforeTask)
	if got := semanticRaceProjectEventCount(t, f); got != beforeEvents {
		t.Fatalf("rejected unresolved-bug reclassification emitted events: %d -> %d", beforeEvents, got)
	}
}

func TestVersionedColumnReclassificationRejectsResolvedBugAtomically(t *testing.T) {
	f := newDependencyFixture(t, "SEMADMINRESOLVED")
	bug := semanticRaceBug(t, f, "", "resolved bug")
	completed, err := f.store.StateColumn(f.ctx, f.project.ID, "completed")
	if err != nil {
		t.Fatalf("read completed column: %v", err)
	}
	resolved, err := f.store.ResolveBug(f.ctx, bug.ID, ResolveBugInput{Resolution: "fixed"}, bug.Version, f.actor.ID)
	if err != nil {
		t.Fatalf("resolve bug: %v", err)
	}
	if resolved.ColumnID != completed.ID || resolved.CompletedAt == nil || resolved.Bug == nil || resolved.Bug.Resolution == nil {
		t.Fatalf("resolved bug = %+v, want completed with resolution", resolved)
	}
	extraCompletedState := "completed"
	if _, err := f.store.CreateColumn(f.ctx, f.project.ID, ColumnInput{
		Name:          semanticRaceStringPtr("Verification complete"),
		SemanticState: &extraCompletedState,
	}, f.actor.ID); err != nil {
		t.Fatalf("create extra completed column: %v", err)
	}
	beforeColumn, err := f.store.GetColumn(f.ctx, completed.ID)
	if err != nil {
		t.Fatalf("read resolved bug column: %v", err)
	}
	beforeTask := dependencyDurableState(t, f, bug.ID)
	beforeEvents := semanticRaceProjectEventCount(t, f)

	readyState := "ready"
	_, err = f.store.UpdateColumnWithVersion(f.ctx, completed.ID, ColumnInput{SemanticState: &readyState}, f.actor.ID, true, semanticRaceVersionPtr(beforeColumn.Version))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("resolved bug reclassification error = %v, want ErrConflict", err)
	}

	afterColumn, err := f.store.GetColumn(f.ctx, completed.ID)
	if err != nil {
		t.Fatalf("reload resolved bug column: %v", err)
	}
	if !reflect.DeepEqual(afterColumn, beforeColumn) {
		t.Fatalf("rejected resolved-bug reclassification changed column:\nbefore=%+v\nafter=%+v", beforeColumn, afterColumn)
	}
	assertDependencyTaskUnchanged(t, f, bug.ID, beforeTask)
	if got := semanticRaceProjectEventCount(t, f); got != beforeEvents {
		t.Fatalf("rejected resolved-bug reclassification emitted events: %d -> %d", beforeEvents, got)
	}
}

func TestSemanticStateRaceRejectsGenericBugPatch(t *testing.T) {
	f := newDependencyFixture(t, "SEMRAcePATCH")
	bug := semanticRaceBug(t, f, "", "patch race bug")
	completed, err := f.store.StateColumn(f.ctx, f.project.ID, "completed")
	if err != nil {
		t.Fatalf("read completed column: %v", err)
	}
	resolved, err := f.store.ResolveBug(f.ctx, bug.ID, ResolveBugInput{Resolution: "fixed"}, bug.Version, f.actor.ID)
	if err != nil {
		t.Fatalf("resolve patch race bug: %v", err)
	}
	extra := semanticRaceColumn(t, f, "Patch destination", "completed", 0)
	if extra.ID == completed.ID {
		t.Fatalf("extra patch destination reused source column %q", extra.ID)
	}
	beforeTask := dependencyDurableState(t, f, resolved.ID)
	beforeEvents := semanticRaceProjectEventCount(t, f)
	title := "patch after destination reclassification"

	_, err = semanticRaceMutation(t, f, extra.ID, "ready", func(ctx context.Context) (Task, error) {
		return f.store.UpdateTask(ctx, resolved.ID, TaskInput{ColumnID: &extra.ID, Title: &title}, resolved.Version, f.actor.ID)
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("generic bug patch race error = %v, want ErrInvalid", err)
	}
	assertSemanticRaceTaskUnchanged(t, f, resolved.ID, beforeTask)
	if got := semanticRaceProjectEventCount(t, f); got != beforeEvents {
		t.Fatalf("rejected generic bug patch emitted events: %d -> %d", beforeEvents, got)
	}
	semanticRaceAssertColumnState(t, f, extra.ID, "ready")
}

func TestSemanticStateRaceRejectsBugMove(t *testing.T) {
	f := newDependencyFixture(t, "SEMRAceMOVE")
	bug := semanticRaceBug(t, f, "", "move race bug")
	extra := semanticRaceColumn(t, f, "Move destination", "ready", 0)
	beforeTask := dependencyDurableState(t, f, bug.ID)
	beforeEvents := semanticRaceProjectEventCount(t, f)

	_, err := semanticRaceMutation(t, f, extra.ID, "completed", func(ctx context.Context) (Task, error) {
		return f.store.MoveTask(ctx, bug.ID, TaskMoveInput{
			DestinationColumnID:    extra.ID,
			ExpectedSourceColumnID: bug.ColumnID,
			Source:                 "semantic-race-test",
		}, bug.Version, f.actor.ID)
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("bug move race error = %v, want ErrConflict", err)
	}
	assertSemanticRaceTaskUnchanged(t, f, bug.ID, beforeTask)
	if got := semanticRaceProjectEventCount(t, f); got != beforeEvents {
		t.Fatalf("rejected bug move emitted events: %d -> %d", beforeEvents, got)
	}
	semanticRaceAssertColumnState(t, f, extra.ID, "completed")
}

func TestSemanticStateRaceRejectsCompleteTask(t *testing.T) {
	f := newDependencyFixture(t, "SEMRAceCOMPLETE")
	task := f.task(t, "complete race task")
	extra := semanticRaceColumn(t, f, "Complete destination", "completed", 0)
	beforeTask := dependencyDurableState(t, f, task.ID)
	beforeEvents := semanticRaceProjectEventCount(t, f)

	_, err := semanticRaceMutation(t, f, extra.ID, "ready", func(ctx context.Context) (Task, error) {
		return f.store.CompleteTask(ctx, task.ID, f.actor.ID, task.Version)
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("complete task race error = %v, want ErrConflict", err)
	}
	assertSemanticRaceTaskUnchanged(t, f, task.ID, beforeTask)
	if got := semanticRaceProjectEventCount(t, f); got != beforeEvents {
		t.Fatalf("rejected complete task emitted events: %d -> %d", beforeEvents, got)
	}
	semanticRaceAssertColumnState(t, f, extra.ID, "ready")
}

func TestSemanticStateRaceRejectsResolveBug(t *testing.T) {
	f := newDependencyFixture(t, "SEMRAceRESOLVE")
	extra := semanticRaceColumn(t, f, "Resolve destination", "completed", 0)
	bug := semanticRaceBug(t, f, "", "resolve race bug")
	beforeTask := dependencyDurableState(t, f, bug.ID)
	beforeEvents := semanticRaceProjectEventCount(t, f)

	_, err := semanticRaceMutation(t, f, extra.ID, "ready", func(ctx context.Context) (Task, error) {
		return f.store.ResolveBug(ctx, bug.ID, ResolveBugInput{Resolution: "fixed", Note: "must roll back"}, bug.Version, f.actor.ID)
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("resolve bug race error = %v, want ErrConflict", err)
	}
	assertSemanticRaceTaskUnchanged(t, f, bug.ID, beforeTask)
	if got := semanticRaceProjectEventCount(t, f); got != beforeEvents {
		t.Fatalf("rejected resolve bug emitted events: %d -> %d", beforeEvents, got)
	}
	semanticRaceAssertColumnState(t, f, extra.ID, "ready")
}

func TestSemanticStateRaceRejectsReopenBug(t *testing.T) {
	f := newDependencyFixture(t, "SEMRAceREOPEN")
	bug := semanticRaceBug(t, f, "", "reopen race bug")
	resolved, err := f.store.ResolveBug(f.ctx, bug.ID, ResolveBugInput{Resolution: "fixed"}, bug.Version, f.actor.ID)
	if err != nil {
		t.Fatalf("resolve reopen race bug: %v", err)
	}
	extra := semanticRaceColumn(t, f, "Reopen destination", "backlog", 0)
	beforeTask := dependencyDurableState(t, f, resolved.ID)
	beforeEvents := semanticRaceProjectEventCount(t, f)

	_, err = semanticRaceMutation(t, f, extra.ID, "completed", func(ctx context.Context) (Task, error) {
		return f.store.ReopenBug(ctx, resolved.ID, "must roll back", resolved.Version, f.actor.ID)
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("reopen bug race error = %v, want ErrConflict", err)
	}
	assertSemanticRaceTaskUnchanged(t, f, resolved.ID, beforeTask)
	if got := semanticRaceProjectEventCount(t, f); got != beforeEvents {
		t.Fatalf("rejected reopen bug emitted events: %d -> %d", beforeEvents, got)
	}
	semanticRaceAssertColumnState(t, f, extra.ID, "completed")
}

func TestSemanticStateRaceRejectsBugTriage(t *testing.T) {
	f := newDependencyFixture(t, "SEMRAceTRIAGE")
	bug := semanticRaceBug(t, f, "", "triage race bug")
	extra := semanticRaceColumn(t, f, "Triage destination", "ready", 0)
	beforeTask := dependencyDurableState(t, f, bug.ID)
	beforeEvents := semanticRaceProjectEventCount(t, f)
	severity := "s1"

	_, err := semanticRaceMutation(t, f, extra.ID, "completed", func(ctx context.Context) (Task, error) {
		return f.store.TriageBug(ctx, bug.ID, TriageBugInput{
			Severity:    &severity,
			SeveritySet: true,
			ColumnID:    &extra.ID,
		}, bug.Version, f.actor.ID)
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("bug triage race error = %v, want ErrInvalid", err)
	}
	assertSemanticRaceTaskUnchanged(t, f, bug.ID, beforeTask)
	if got := semanticRaceProjectEventCount(t, f); got != beforeEvents {
		t.Fatalf("rejected bug triage race emitted events: %d -> %d", beforeEvents, got)
	}
	semanticRaceAssertColumnState(t, f, extra.ID, "completed")
}

func TestSemanticStateRaceRejectsBugTriageToArchivedColumn(t *testing.T) {
	f := newDependencyFixture(t, "SEMTRIAGEARCH")
	bug := semanticRaceBug(t, f, "", "triage archive race bug")
	extra := semanticRaceColumn(t, f, "Archived triage destination", "ready", 0)
	beforeTask := dependencyDurableState(t, f, bug.ID)
	beforeEvents := semanticRaceProjectEventCount(t, f)
	severity := "s1"

	_, err := semanticRaceMutationWithColumnUpdate(t, f, extra.ID, func(blocker *sql.Tx) error {
		_, err := blocker.ExecContext(f.ctx, `UPDATE columns SET archived_at=?, version=version+1, updated_at=? WHERE id=?`, now(), now(), extra.ID)
		return err
	}, func(ctx context.Context) (Task, error) {
		return f.store.TriageBug(ctx, bug.ID, TriageBugInput{
			Severity:    &severity,
			SeveritySet: true,
			ColumnID:    &extra.ID,
		}, bug.Version, f.actor.ID)
	})
	// The scheduled writer may commit before TriageBug's read-only preflight,
	// yielding the archive validation error, or after it, yielding the guarded
	// transaction's conflict. Both outcomes reject the mutation atomically.
	semanticRaceAssertArchiveRejection(t, err, "column is archived")
	assertSemanticRaceTaskUnchanged(t, f, bug.ID, beforeTask)
	if got := semanticRaceProjectEventCount(t, f); got != beforeEvents {
		t.Fatalf("rejected bug triage archive race emitted events: %d -> %d", beforeEvents, got)
	}
	column, err := f.store.GetColumn(f.ctx, extra.ID)
	if err != nil {
		t.Fatalf("reload archived triage destination: %v", err)
	}
	if column.ArchivedAt == nil {
		t.Fatalf("triage race destination is not archived: %+v", column)
	}
}

func TestSemanticStateRaceRejectsBugTriageFromArchivedColumn(t *testing.T) {
	f := newDependencyFixture(t, "SEMSOURCARCH")
	source := semanticRaceColumn(t, f, "Archived triage source", "backlog", 0)
	bug := semanticRaceBug(t, f, source.ID, "triage source archive race bug")
	extra := semanticRaceColumn(t, f, "Triage source destination", "ready", 0)
	beforeTask := dependencyDurableState(t, f, bug.ID)
	beforeEvents := semanticRaceProjectEventCount(t, f)
	severity := "s1"

	_, err := semanticRaceMutationWithColumnUpdate(t, f, source.ID, func(blocker *sql.Tx) error {
		_, err := blocker.ExecContext(f.ctx, `UPDATE columns SET archived_at=?, version=version+1, updated_at=? WHERE id=?`, now(), now(), source.ID)
		return err
	}, func(ctx context.Context) (Task, error) {
		return f.store.TriageBug(ctx, bug.ID, TriageBugInput{
			Severity:    &severity,
			SeveritySet: true,
			ColumnID:    &extra.ID,
		}, bug.Version, f.actor.ID)
	})
	// As with destination archives, either preflight validation or the guarded
	// transaction can observe the source archive first; both must reject.
	semanticRaceAssertArchiveRejection(t, err, "task is assigned to an archived column")
	assertSemanticRaceTaskUnchanged(t, f, bug.ID, beforeTask)
	if got := semanticRaceProjectEventCount(t, f); got != beforeEvents {
		t.Fatalf("rejected bug triage source archive race emitted events: %d -> %d", beforeEvents, got)
	}
	column, err := f.store.GetColumn(f.ctx, source.ID)
	if err != nil {
		t.Fatalf("reload archived triage source: %v", err)
	}
	if column.ArchivedAt == nil {
		t.Fatalf("triage source race column is not archived: %+v", column)
	}
}

func TestSemanticStateRaceRejectsBugCreate(t *testing.T) {
	f := newDependencyFixture(t, "SEMRAceCREATEBUG")
	extra := semanticRaceColumn(t, f, "Bug create destination", "ready", 0)
	beforeEvents := semanticRaceProjectEventCount(t, f)
	title := "create race bug"
	actual := "create race actual behavior"
	kind := bugKind

	_, err := semanticRaceMutation(t, f, extra.ID, "completed", func(ctx context.Context) (Task, error) {
		return f.store.CreateTask(ctx, f.project.ID, TaskInput{
			Title:    &title,
			Kind:     &kind,
			ColumnID: &extra.ID,
			Bug:      &BugInput{ActualBehavior: &actual},
		}, f.actor.ID)
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("bug create race error = %v, want ErrInvalid", err)
	}
	var taskCount int
	if err := f.store.DB.QueryRowContext(f.ctx, `SELECT COUNT(1) FROM tasks WHERE project_id=? AND title=?`, f.project.ID, title).Scan(&taskCount); err != nil {
		t.Fatalf("count rejected bug create: %v", err)
	}
	if taskCount != 0 {
		t.Fatalf("rejected bug create inserted %d tasks", taskCount)
	}
	if got := semanticRaceProjectEventCount(t, f); got != beforeEvents {
		t.Fatalf("rejected bug create emitted events: %d -> %d", beforeEvents, got)
	}
	semanticRaceAssertColumnState(t, f, extra.ID, "completed")
}

func TestSemanticStateRaceRejectsBugCreateToArchivedColumn(t *testing.T) {
	f := newDependencyFixture(t, "SEMCREATEARCH")
	extra := semanticRaceColumn(t, f, "Archived bug create destination", "ready", 0)
	beforeEvents := semanticRaceProjectEventCount(t, f)
	title := "create archive race bug"
	actual := "create archive race actual behavior"
	kind := bugKind

	_, err := semanticRaceMutationWithColumnUpdate(t, f, extra.ID, func(blocker *sql.Tx) error {
		_, err := blocker.ExecContext(f.ctx, `UPDATE columns SET archived_at=?, version=version+1, updated_at=? WHERE id=?`, now(), now(), extra.ID)
		return err
	}, func(ctx context.Context) (Task, error) {
		return f.store.CreateTask(ctx, f.project.ID, TaskInput{
			Title:    &title,
			Kind:     &kind,
			ColumnID: &extra.ID,
			Bug:      &BugInput{ActualBehavior: &actual},
		}, f.actor.ID)
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("bug create archive race error = %v, want ErrConflict", err)
	}
	var taskCount int
	if err := f.store.DB.QueryRowContext(f.ctx, `SELECT COUNT(1) FROM tasks WHERE project_id=? AND title=?`, f.project.ID, title).Scan(&taskCount); err != nil {
		t.Fatalf("count rejected archived bug create: %v", err)
	}
	if taskCount != 0 {
		t.Fatalf("rejected archived bug create inserted %d tasks", taskCount)
	}
	if got := semanticRaceProjectEventCount(t, f); got != beforeEvents {
		t.Fatalf("rejected archived bug create emitted events: %d -> %d", beforeEvents, got)
	}
	column, err := f.store.GetColumn(f.ctx, extra.ID)
	if err != nil {
		t.Fatalf("reload archived bug create destination: %v", err)
	}
	if column.ArchivedAt == nil {
		t.Fatalf("bug create race destination is not archived: %+v", column)
	}
}

func TestSemanticStateRaceCreateTaskDerivesCompletedAt(t *testing.T) {
	f := newDependencyFixture(t, "SEMCTASKCREATE")
	extra := semanticRaceColumn(t, f, "Task create destination", "ready", 0)
	title := "create completed task race"
	beforeEvents := semanticRaceProjectEventCount(t, f)

	created, err := semanticRaceMutation(t, f, extra.ID, "completed", func(ctx context.Context) (Task, error) {
		return f.store.CreateTask(ctx, f.project.ID, TaskInput{
			Title:    &title,
			ColumnID: &extra.ID,
		}, f.actor.ID)
	})
	if err != nil {
		t.Fatalf("ordinary task create race: %v", err)
	}
	if created.ColumnID != extra.ID || created.CompletedAt == nil {
		t.Fatalf("ordinary task create race result = %+v, want destination %q with completed_at", created, extra.ID)
	}
	if got := semanticRaceProjectEventCount(t, f); got != beforeEvents+1 {
		t.Fatalf("ordinary task create race events = %d, want %d", got, beforeEvents+1)
	}
	semanticRaceAssertColumnState(t, f, extra.ID, "completed")
}

func TestSemanticStateRaceCreateTaskClearsCompletedAt(t *testing.T) {
	f := newDependencyFixture(t, "SEMCTASKCLEAR")
	extra := semanticRaceColumn(t, f, "Task create ready destination", "completed", 0)
	title := "create ready task race"
	beforeEvents := semanticRaceProjectEventCount(t, f)

	created, err := semanticRaceMutation(t, f, extra.ID, "ready", func(ctx context.Context) (Task, error) {
		return f.store.CreateTask(ctx, f.project.ID, TaskInput{
			Title:    &title,
			ColumnID: &extra.ID,
		}, f.actor.ID)
	})
	if err != nil {
		t.Fatalf("ordinary task create ready race: %v", err)
	}
	if created.ColumnID != extra.ID || created.CompletedAt != nil {
		t.Fatalf("ordinary task create ready race result = %+v, want destination %q without completed_at", created, extra.ID)
	}
	if got := semanticRaceProjectEventCount(t, f); got != beforeEvents+1 {
		t.Fatalf("ordinary task create ready race events = %d, want %d", got, beforeEvents+1)
	}
	semanticRaceAssertColumnState(t, f, extra.ID, "ready")
}

func TestSemanticStateRaceResolveDuplicateWaitsForWriter(t *testing.T) {
	f := newDependencyFixture(t, "SEMRAceDUPLICATE")
	bug := semanticRaceBug(t, f, "", "duplicate race bug")
	target := semanticRaceBug(t, f, "", "duplicate target")
	completed, err := f.store.StateColumn(f.ctx, f.project.ID, "completed")
	if err != nil {
		t.Fatalf("read duplicate completion column: %v", err)
	}
	beforeTask := dependencyDurableState(t, f, bug.ID)
	beforeEvents := semanticRaceProjectEventCount(t, f)

	_, err = semanticRaceMutation(t, f, completed.ID, "ready", func(ctx context.Context) (Task, error) {
		return f.store.ResolveBug(ctx, bug.ID, ResolveBugInput{
			Resolution:  "duplicate",
			DuplicateOf: &target.ID,
		}, bug.Version, f.actor.ID)
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate resolution race error = %v, want ErrConflict", err)
	}
	assertSemanticRaceTaskUnchanged(t, f, bug.ID, beforeTask)
	if got := semanticRaceProjectEventCount(t, f); got != beforeEvents {
		t.Fatalf("rejected duplicate resolution race emitted events: %d -> %d", beforeEvents, got)
	}
	semanticRaceAssertColumnState(t, f, completed.ID, "ready")
}

type semanticRaceResult struct {
	task Task
	err  error
}

func semanticRaceMutation(t *testing.T, f dependencyFixture, destinationID, reclassifiedState string, call func(context.Context) (Task, error)) (Task, error) {
	t.Helper()
	return semanticRaceMutationWithColumnUpdate(t, f, destinationID, func(blocker *sql.Tx) error {
		_, err := blocker.ExecContext(f.ctx, `UPDATE columns SET semantic_state=?, version=version+1, updated_at=? WHERE id=?`, reclassifiedState, now(), destinationID)
		return err
	}, call)
}

func semanticRaceMutationWithColumnUpdate(t *testing.T, f dependencyFixture, destinationID string, updateColumn func(*sql.Tx) error, call func(context.Context) (Task, error)) (Task, error) {
	t.Helper()
	blocker, err := f.store.DB.BeginTx(f.ctx, nil)
	if err != nil {
		t.Fatalf("begin semantic race blocker: %v", err)
	}
	defer blocker.Rollback()
	if _, err := blocker.ExecContext(f.ctx, `UPDATE projects SET updated_at=updated_at WHERE id=?`, f.project.ID); err != nil {
		t.Fatalf("acquire semantic race writer lock: %v", err)
	}

	ctx, cancel := context.WithTimeout(f.ctx, 4*time.Second)
	defer cancel()
	result := make(chan semanticRaceResult, 1)
	go func() {
		task, callErr := call(ctx)
		result <- semanticRaceResult{task: task, err: callErr}
	}()
	semanticRaceWaitForWriter(t, f.store.DB, result)

	if err := updateColumn(blocker); err != nil {
		t.Fatalf("reclassify semantic race destination: %v", err)
	}
	if err := blocker.Commit(); err != nil {
		t.Fatalf("release semantic race writer lock: %v", err)
	}

	select {
	case outcome := <-result:
		return outcome.task, outcome.err
	case <-time.After(4 * time.Second):
		t.Fatalf("semantic race operation did not finish after writer release")
		return Task{}, context.DeadlineExceeded
	}
}

func semanticRaceWaitForWriter(t *testing.T, database *sql.DB, result <-chan semanticRaceResult) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	var stableSince time.Time
	for {
		select {
		case outcome := <-result:
			t.Fatalf("semantic race operation finished before destination reclassification: task=%+v err=%v", outcome.task, outcome.err)
		case <-ticker.C:
			if database.Stats().InUse < 2 {
				stableSince = time.Time{}
				continue
			}
			if stableSince.IsZero() {
				stableSince = time.Now()
				continue
			}
			if time.Since(stableSince) >= 75*time.Millisecond {
				select {
				case outcome := <-result:
					t.Fatalf("semantic race operation finished before destination reclassification: task=%+v err=%v", outcome.task, outcome.err)
				default:
				}
				return
			}
		case <-deadline.C:
			t.Fatalf("semantic race operation did not reach its writer wait")
		}
	}
}

func semanticRaceBug(t *testing.T, f dependencyFixture, columnID, title string) Task {
	t.Helper()
	kind := bugKind
	actual := title + " actual behavior"
	input := TaskInput{
		Title: semanticRaceStringPtr(title),
		Kind:  &kind,
		Bug:   &BugInput{ActualBehavior: &actual},
	}
	if columnID != "" {
		input.ColumnID = &columnID
	}
	bug, err := f.store.CreateTask(f.ctx, f.project.ID, input, f.actor.ID)
	if err != nil {
		t.Fatalf("create semantic race bug %q: %v", title, err)
	}
	return bug
}

func semanticRaceColumn(t *testing.T, f dependencyFixture, name, state string, position int) Column {
	t.Helper()
	column, err := f.store.CreateColumn(f.ctx, f.project.ID, ColumnInput{
		Name:          semanticRaceStringPtr(name),
		SemanticState: semanticRaceStringPtr(state),
		Position:      semanticRaceIntPtr(position),
	}, f.actor.ID)
	if err != nil {
		t.Fatalf("create semantic race column %q: %v", name, err)
	}
	return column
}

func assertSemanticRaceTaskUnchanged(t *testing.T, f dependencyFixture, taskID string, before dependencyDurableTaskState) {
	t.Helper()
	after := dependencyDurableState(t, f, taskID)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected semantic race mutation changed durable task state:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func semanticRaceAssertColumnState(t *testing.T, f dependencyFixture, columnID, state string) {
	t.Helper()
	column, err := f.store.GetColumn(f.ctx, columnID)
	if err != nil {
		t.Fatalf("reload reclassified semantic race column: %v", err)
	}
	if column.SemanticState != state {
		t.Fatalf("semantic race destination state = %q, want %q", column.SemanticState, state)
	}
}

func semanticRaceAssertArchiveRejection(t *testing.T, err error, invalidMessage string) {
	t.Helper()
	if errors.Is(err, ErrConflict) {
		return
	}
	var storeErr *Error
	if errors.Is(err, ErrInvalid) && errors.As(err, &storeErr) && storeErr.Message == invalidMessage {
		return
	}
	t.Fatalf("archive race error = %v, want ErrConflict or ErrInvalid(%q)", err, invalidMessage)
}

func semanticRaceProjectEventCount(t *testing.T, f dependencyFixture) int {
	t.Helper()
	var count int
	if err := f.store.DB.QueryRowContext(f.ctx, `SELECT COUNT(1) FROM events WHERE project_id=?`, f.project.ID).Scan(&count); err != nil {
		t.Fatalf("count semantic race project events: %v", err)
	}
	return count
}

func semanticRaceStringPtr(value string) *string { return &value }

func semanticRaceIntPtr(value int) *int { return &value }

func semanticRaceVersionPtr(value int64) *int64 { return &value }
