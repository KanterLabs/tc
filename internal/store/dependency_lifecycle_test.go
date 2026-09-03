package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

type dependencyDurableTaskState struct {
	Kind           string
	Title          string
	Description    string
	Priority       string
	ColumnID       string
	Position       float64
	Assignee       sql.NullString
	ClaimedBy      sql.NullString
	ClaimExpiresAt sql.NullString
	DueAt          sql.NullString
	Version        int64
	CompletedAt    sql.NullString
	DeletedAt      sql.NullString
	UpdatedAt      string
	Comments       int
	Events         int
	Prerequisites  int
	Dependents     int
	BugSeverity    sql.NullString
	BugResolution  sql.NullString
	BugResolvedBy  sql.NullString
	BugResolvedAt  sql.NullString
	BugDuplicateOf sql.NullString
	DuplicateLinks int
}

func dependencyDurableState(t *testing.T, f dependencyFixture, taskID string) dependencyDurableTaskState {
	t.Helper()
	var state dependencyDurableTaskState
	err := f.store.DB.QueryRowContext(f.ctx, `SELECT
		t.kind, t.title, t.description, t.priority, t.column_id, t.position,
		t.assignee_id, t.claimed_by, t.claim_expires_at, t.due_at, t.version,
		t.completed_at, t.deleted_at, t.updated_at,
		(SELECT COUNT(1) FROM comments WHERE task_id=t.id),
		(SELECT COUNT(1) FROM events WHERE task_id=t.id),
		(SELECT COUNT(1) FROM task_dependencies WHERE task_id=t.id),
		(SELECT COUNT(1) FROM task_dependencies WHERE prerequisite_task_id=t.id),
		(SELECT severity FROM bug_details WHERE task_id=t.id),
		(SELECT resolution FROM bug_details WHERE task_id=t.id),
		(SELECT resolved_by FROM bug_details WHERE task_id=t.id),
		(SELECT resolved_at FROM bug_details WHERE task_id=t.id),
		(SELECT duplicate_of FROM bug_details WHERE task_id=t.id),
		(SELECT COUNT(1) FROM task_links WHERE source_task_id=t.id AND link_type='duplicate')
		FROM tasks t WHERE t.id=?`, taskID).Scan(
		&state.Kind, &state.Title, &state.Description, &state.Priority, &state.ColumnID, &state.Position,
		&state.Assignee, &state.ClaimedBy, &state.ClaimExpiresAt, &state.DueAt, &state.Version,
		&state.CompletedAt, &state.DeletedAt, &state.UpdatedAt,
		&state.Comments, &state.Events, &state.Prerequisites, &state.Dependents,
		&state.BugSeverity, &state.BugResolution, &state.BugResolvedBy, &state.BugResolvedAt,
		&state.BugDuplicateOf, &state.DuplicateLinks,
	)
	if err != nil {
		t.Fatalf("read durable task state: %v", err)
	}
	return state
}

func assertDependencyTaskUnchanged(t *testing.T, f dependencyFixture, taskID string, before dependencyDurableTaskState) {
	t.Helper()
	after := dependencyDurableState(t, f, taskID)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected dependency lifecycle mutation changed durable state:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func dependencyBug(t *testing.T, f dependencyFixture, title string) Task {
	t.Helper()
	kind := bugKind
	actual := title + " actual behavior"
	bug, err := f.store.CreateTask(f.ctx, f.project.ID, TaskInput{
		Title: dependencyStringPtr(title),
		Kind:  &kind,
		Bug:   &BugInput{ActualBehavior: &actual},
	}, f.actor.ID)
	if err != nil {
		t.Fatalf("create bug %q: %v", title, err)
	}
	return bug
}

func dependencyEventCount(t *testing.T, f dependencyFixture) int {
	t.Helper()
	var count int
	if err := f.store.DB.QueryRowContext(f.ctx, `SELECT COUNT(1) FROM events WHERE type='task.dependency_state_changed'`).Scan(&count); err != nil {
		t.Fatalf("count dependency state events: %v", err)
	}
	return count
}

func dependencyErrorDetails(t *testing.T, err error, kind error) map[string]any {
	t.Helper()
	if !errors.Is(err, kind) {
		t.Fatalf("dependency lifecycle error = %v, want %v", err, kind)
	}
	var typed *Error
	if !errors.As(err, &typed) {
		t.Fatalf("dependency lifecycle error type = %T, want *store.Error", err)
	}
	details, ok := typed.Details.(map[string]any)
	if !ok {
		t.Fatalf("dependency lifecycle details = %#v, want map", typed.Details)
	}
	return details
}

func TestDependencyLifecycleRejectsUnmetTaskTransitionsAtomically(t *testing.T) {
	for _, test := range []struct {
		name string
		call func(*testing.T, dependencyFixture, Task) error
	}{
		{
			name: "claim",
			call: func(t *testing.T, f dependencyFixture, dependent Task) error {
				_, err := f.store.ClaimTask(f.ctx, dependent.ID, f.actor.ID, time.Hour, dependent.Version)
				return err
			},
		},
		{
			name: "generic patch into active",
			call: func(t *testing.T, f dependencyFixture, dependent Task) error {
				column, err := f.store.StateColumn(f.ctx, f.project.ID, "active")
				if err != nil {
					t.Fatalf("read active column: %v", err)
				}
				_, err = f.store.UpdateTask(f.ctx, dependent.ID, TaskInput{ColumnID: &column.ID}, dependent.Version, f.actor.ID)
				return err
			},
		},
		{
			name: "administrator patch into completed",
			call: func(t *testing.T, f dependencyFixture, dependent Task) error {
				column, err := f.store.StateColumn(f.ctx, f.project.ID, "completed")
				if err != nil {
					t.Fatalf("read completed column: %v", err)
				}
				_, err = f.store.UpdateTaskWithClaimOverride(f.ctx, dependent.ID, TaskInput{ColumnID: &column.ID}, dependent.Version, f.actor.ID, true)
				return err
			},
		},
		{
			name: "complete with comment",
			call: func(t *testing.T, f dependencyFixture, dependent Task) error {
				_, err := f.store.CompleteTaskWithComment(f.ctx, dependent.ID, f.actor.ID, dependent.Version, "must roll back")
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newDependencyFixture(t, "TASKLIFE")
			prerequisite := f.task(t, "unfinished prerequisite")
			dependent := f.add(t, f.task(t, "dependent"), prerequisite)
			before := dependencyDurableState(t, f, dependent.ID)
			err := test.call(t, f, dependent)
			details := dependencyErrorDetails(t, err, ErrUnmetDependencies)
			if details["task_id"] != dependent.ID {
				t.Fatalf("unmet details task_id = %#v, want %q", details["task_id"], dependent.ID)
			}
			unmet, ok := details["unmet_prerequisites"].([]TaskReference)
			if !ok || len(unmet) != 1 || unmet[0].ID != prerequisite.ID {
				t.Fatalf("unmet prerequisite details = %#v, want %s", details["unmet_prerequisites"], prerequisite.Key)
			}
			assertDependencyTaskUnchanged(t, f, dependent.ID, before)
		})
	}
}

func TestDependencyLifecycleRejectsRenewAgainstCorruptUnmetState(t *testing.T) {
	f := newDependencyFixture(t, "RENEWDEP")
	prerequisite, err := f.store.CompleteTask(f.ctx, f.task(t, "prerequisite").ID, f.actor.ID, 1)
	if err != nil {
		t.Fatalf("complete prerequisite: %v", err)
	}
	dependent := f.add(t, f.task(t, "dependent"), prerequisite)
	dependent, err = f.store.ClaimTask(f.ctx, dependent.ID, f.actor.ID, time.Hour, dependent.Version)
	if err != nil {
		t.Fatalf("claim dependent: %v", err)
	}

	var triggerSQL string
	if err := f.store.DB.QueryRowContext(f.ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name='task_dependencies_guard_task_update'`).Scan(&triggerSQL); err != nil {
		t.Fatalf("read lifecycle trigger: %v", err)
	}
	backlog, err := f.store.StateColumn(f.ctx, f.project.ID, "backlog")
	if err != nil {
		t.Fatalf("read backlog column: %v", err)
	}
	if _, err := f.store.DB.ExecContext(f.ctx, `DROP TRIGGER task_dependencies_guard_task_update`); err != nil {
		t.Fatalf("temporarily drop task guard: %v", err)
	}
	if _, err := f.store.DB.ExecContext(f.ctx, `UPDATE tasks SET column_id=?, completed_at=NULL WHERE id=?`, backlog.ID, prerequisite.ID); err != nil {
		t.Fatalf("create corrupt unmet state: %v", err)
	}
	if _, err := f.store.DB.ExecContext(f.ctx, triggerSQL); err != nil {
		t.Fatalf("restore task guard: %v", err)
	}

	before := dependencyDurableState(t, f, dependent.ID)
	_, err = f.store.RenewTask(f.ctx, dependent.ID, f.actor.ID, 2*time.Hour, dependent.Version)
	dependencyErrorDetails(t, err, ErrUnmetDependencies)
	assertDependencyTaskUnchanged(t, f, dependent.ID, before)
}

func TestDependencyLifecycleRejectsUnmetBugTransitionsAtomically(t *testing.T) {
	for _, test := range []struct {
		name string
		call func(*testing.T, dependencyFixture, Task) error
	}{
		{
			name: "triage into active",
			call: func(t *testing.T, f dependencyFixture, bug Task) error {
				active, err := f.store.StateColumn(f.ctx, f.project.ID, "active")
				if err != nil {
					t.Fatalf("read active column: %v", err)
				}
				severity := "s1"
				priority := "urgent"
				_, err = f.store.TriageBug(f.ctx, bug.ID, TriageBugInput{Severity: &severity, SeveritySet: true, Priority: &priority, ColumnID: &active.ID}, bug.Version, f.actor.ID)
				return err
			},
		},
		{
			name: "resolve duplicate with note",
			call: func(t *testing.T, f dependencyFixture, bug Task) error {
				target := dependencyBug(t, f, "duplicate target")
				_, err := f.store.ResolveBug(f.ctx, bug.ID, ResolveBugInput{Resolution: "duplicate", DuplicateOf: &target.ID, Note: "must roll back"}, bug.Version, f.actor.ID)
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newDependencyFixture(t, "BUGLIFE")
			prerequisite := f.task(t, "unfinished prerequisite")
			bug := f.add(t, dependencyBug(t, f, "dependent bug"), prerequisite)
			before := dependencyDurableState(t, f, bug.ID)
			err := test.call(t, f, bug)
			dependencyErrorDetails(t, err, ErrUnmetDependencies)
			assertDependencyTaskUnchanged(t, f, bug.ID, before)
		})
	}
}

func TestDependencyLifecycleProtectsReopenDeleteAndBugReopen(t *testing.T) {
	f := newDependencyFixture(t, "PROTECT")
	prerequisite := f.task(t, "protected prerequisite")
	prerequisite, err := f.store.CompleteTask(f.ctx, prerequisite.ID, f.actor.ID, prerequisite.Version)
	if err != nil {
		t.Fatalf("complete prerequisite: %v", err)
	}
	backlogDependent := f.add(t, f.task(t, "not started dependent"), prerequisite)
	activeDependent := f.add(t, f.task(t, "started dependent"), prerequisite)
	active, err := f.store.StateColumn(f.ctx, f.project.ID, "active")
	if err != nil {
		t.Fatalf("read active column: %v", err)
	}
	activeDependent, err = f.store.UpdateTask(f.ctx, activeDependent.ID, TaskInput{ColumnID: &active.ID}, activeDependent.Version, f.actor.ID)
	if err != nil {
		t.Fatalf("start dependent: %v", err)
	}

	beforePrerequisite := dependencyDurableState(t, f, prerequisite.ID)
	beforeActive := dependencyDurableState(t, f, activeDependent.ID)
	_, err = f.store.BlockTask(f.ctx, prerequisite.ID, f.actor.ID, prerequisite.Version)
	details := dependencyErrorDetails(t, err, ErrDependencyInUse)
	dependents, ok := details["dependents"].([]TaskReference)
	if !ok || len(dependents) != 1 || dependents[0].ID != activeDependent.ID {
		t.Fatalf("reopen blockers = %#v, want only active dependent %s", details["dependents"], activeDependent.Key)
	}
	assertDependencyTaskUnchanged(t, f, prerequisite.ID, beforePrerequisite)
	assertDependencyTaskUnchanged(t, f, activeDependent.ID, beforeActive)

	err = f.store.DeleteTask(f.ctx, prerequisite.ID, prerequisite.Version, f.actor.ID)
	details = dependencyErrorDetails(t, err, ErrDependencyInUse)
	dependents, ok = details["dependents"].([]TaskReference)
	if !ok || len(dependents) != 2 || dependents[0].ID != backlogDependent.ID || dependents[1].ID != activeDependent.ID {
		t.Fatalf("delete blockers = %#v, want every live dependent", details["dependents"])
	}
	assertDependencyTaskUnchanged(t, f, prerequisite.ID, beforePrerequisite)

	bugPrerequisite := dependencyBug(t, f, "bug prerequisite")
	bugPrerequisite, err = f.store.ResolveBug(f.ctx, bugPrerequisite.ID, ResolveBugInput{Resolution: "fixed"}, bugPrerequisite.Version, f.actor.ID)
	if err != nil {
		t.Fatalf("resolve bug prerequisite: %v", err)
	}
	bugDependent := f.add(t, f.task(t, "bug dependent"), bugPrerequisite)
	bugDependent, err = f.store.ClaimTask(f.ctx, bugDependent.ID, f.actor.ID, time.Hour, bugDependent.Version)
	if err != nil {
		t.Fatalf("claim bug dependent: %v", err)
	}
	beforeBug := dependencyDurableState(t, f, bugPrerequisite.ID)
	_, err = f.store.ReopenBug(f.ctx, bugPrerequisite.ID, "must roll back", bugPrerequisite.Version, f.actor.ID)
	dependencyErrorDetails(t, err, ErrDependencyInUse)
	assertDependencyTaskUnchanged(t, f, bugPrerequisite.ID, beforeBug)
}

func TestDependencyLifecycleCompletionEventsAndImmediateEligibility(t *testing.T) {
	f := newDependencyFixture(t, "EVENTDEP")
	first := f.task(t, "first prerequisite")
	second := f.task(t, "second prerequisite")
	dependent := f.add(t, f.task(t, "dependent"), first)
	dependent = f.add(t, dependent, second)
	otherDependent := f.add(t, f.task(t, "other dependent"), second)
	dependentVersion := dependent.Version
	otherVersion := otherDependent.Version
	baseline := dependencyEventCount(t, f)

	first, err := f.store.CompleteTask(f.ctx, first.ID, f.actor.ID, first.Version)
	if err != nil {
		t.Fatalf("complete first prerequisite: %v", err)
	}
	if got := dependencyEventCount(t, f); got != baseline+1 {
		t.Fatalf("state events after first completion = %d, want %d", got, baseline+1)
	}
	if _, err := f.store.ClaimTask(f.ctx, dependent.ID, f.actor.ID, time.Hour, dependent.Version); !errors.Is(err, ErrUnmetDependencies) {
		t.Fatalf("claim before final prerequisite error = %v, want ErrUnmetDependencies", err)
	}

	second, err = f.store.CompleteTask(f.ctx, second.ID, f.actor.ID, second.Version)
	if err != nil {
		t.Fatalf("complete final prerequisite: %v", err)
	}
	if got := dependencyEventCount(t, f); got != baseline+3 {
		t.Fatalf("state events after final completion = %d, want %d", got, baseline+3)
	}
	for _, target := range []struct {
		id      string
		version int64
	}{
		{id: dependent.ID, version: dependentVersion},
		{id: otherDependent.ID, version: otherVersion},
	} {
		loaded, loadErr := f.store.GetTask(f.ctx, target.id)
		if loadErr != nil {
			t.Fatalf("reload dependent: %v", loadErr)
		}
		if loaded.Version != target.version || loaded.DependencySummary.Blocked {
			t.Fatalf("derived readiness mutated editable state: task=%s version=%d/%d summary=%+v", loaded.Key, loaded.Version, target.version, loaded.DependencySummary)
		}
	}

	var eventTaskID, payload string
	if err := f.store.DB.QueryRowContext(f.ctx, `SELECT task_id, payload FROM events WHERE type='task.dependency_state_changed' AND json_extract(payload, '$.prerequisite_id')=? AND task_id=?`, second.ID, dependent.ID).Scan(&eventTaskID, &payload); err != nil {
		t.Fatalf("read dependent invalidation event: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("decode invalidation event: %v", err)
	}
	if eventTaskID != dependent.ID || decoded["dependent_id"] != dependent.ID || decoded["dependent_key"] != dependent.Key || decoded["prerequisite_id"] != second.ID || decoded["prerequisite_key"] != second.Key || decoded["satisfied"] != true {
		t.Fatalf("invalidation event task/payload = %q/%#v", eventTaskID, decoded)
	}

	claimed, err := f.store.ClaimTask(f.ctx, dependent.ID, f.actor.ID, time.Hour, dependentVersion)
	if err != nil {
		t.Fatalf("claim immediately after final completion: %v", err)
	}
	if _, err := f.store.CompleteTaskWithClaim(f.ctx, dependent.ID, f.actor.ID, claimed.Version); err != nil {
		t.Fatalf("complete newly eligible dependent: %v", err)
	}

	beforeRepeat := dependencyEventCount(t, f)
	if _, err := f.store.CompleteTask(f.ctx, second.ID, f.actor.ID, second.Version); err != nil {
		t.Fatalf("repeat prerequisite completion: %v", err)
	}
	if got := dependencyEventCount(t, f); got != beforeRepeat {
		t.Fatalf("repeat completion emitted dependency invalidation: %d -> %d", beforeRepeat, got)
	}
}

func TestDependencyLifecycleAllowedReopenPublishesUnsatisfiedEvent(t *testing.T) {
	f := newDependencyFixture(t, "REOPENEVENT")
	prerequisite := f.task(t, "completed prerequisite")
	prerequisite, err := f.store.CompleteTask(f.ctx, prerequisite.ID, f.actor.ID, prerequisite.Version)
	if err != nil {
		t.Fatalf("complete prerequisite: %v", err)
	}
	dependent := f.add(t, f.task(t, "unstarted dependent"), prerequisite)
	dependentVersion := dependent.Version
	before := dependencyEventCount(t, f)
	reopened, err := f.store.BlockTask(f.ctx, prerequisite.ID, f.actor.ID, prerequisite.Version)
	if err != nil {
		t.Fatalf("reopen prerequisite with unstarted dependent: %v", err)
	}
	if reopened.CompletedAt != nil {
		t.Fatalf("reopened prerequisite completed_at = %v, want nil", reopened.CompletedAt)
	}
	if got := dependencyEventCount(t, f); got != before+1 {
		t.Fatalf("reopen state events = %d, want %d", got, before+1)
	}
	var payload string
	if err := f.store.DB.QueryRowContext(f.ctx, `SELECT payload FROM events WHERE type='task.dependency_state_changed' AND task_id=? ORDER BY cursor DESC LIMIT 1`, dependent.ID).Scan(&payload); err != nil {
		t.Fatalf("read reopen invalidation: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("decode reopen invalidation: %v", err)
	}
	if decoded["satisfied"] != false || decoded["prerequisite_id"] != prerequisite.ID {
		t.Fatalf("reopen invalidation payload = %#v", decoded)
	}
	loaded, err := f.store.GetTask(f.ctx, dependent.ID)
	if err != nil {
		t.Fatalf("reload dependent: %v", err)
	}
	if loaded.Version != dependentVersion || !loaded.DependencySummary.Blocked {
		t.Fatalf("reopen derived state/version = %+v/%d, want blocked/version %d", loaded.DependencySummary, loaded.Version, dependentVersion)
	}
}

func TestDependencyLifecycleGuardsBulkColumnTransitionsAtomically(t *testing.T) {
	for _, semantic := range []string{"active", "completed"} {
		t.Run("reject unmet into "+semantic, func(t *testing.T) {
			f := newDependencyFixture(t, "BULKUNMET")
			name, backlog := "Batch", "backlog"
			column, err := f.store.CreateColumn(f.ctx, f.project.ID, ColumnInput{Name: &name, SemanticState: &backlog}, f.actor.ID)
			if err != nil {
				t.Fatalf("create batch column: %v", err)
			}
			prerequisite := f.task(t, "unfinished prerequisite")
			dependent, err := f.store.CreateTask(f.ctx, f.project.ID, TaskInput{Title: dependencyStringPtr("batch dependent"), ColumnID: &column.ID}, f.actor.ID)
			if err != nil {
				t.Fatalf("create batch dependent: %v", err)
			}
			dependent = f.add(t, dependent, prerequisite)
			beforeTask := dependencyDurableState(t, f, dependent.ID)
			var beforeEvents int
			if err := f.store.DB.QueryRowContext(f.ctx, `SELECT COUNT(1) FROM events WHERE project_id=?`, f.project.ID).Scan(&beforeEvents); err != nil {
				t.Fatalf("count project events: %v", err)
			}
			_, err = f.store.UpdateColumn(f.ctx, column.ID, ColumnInput{SemanticState: &semantic}, f.actor.ID)
			details := dependencyErrorDetails(t, err, ErrUnmetDependencies)
			if details["column_id"] != column.ID {
				t.Fatalf("column unmet details = %#v", details)
			}
			afterColumn, getErr := f.store.GetColumn(f.ctx, column.ID)
			if getErr != nil {
				t.Fatalf("reload batch column: %v", getErr)
			}
			if afterColumn.SemanticState != backlog || afterColumn.UpdatedAt != column.UpdatedAt {
				t.Fatalf("rejected column update changed column: before=%+v after=%+v", column, afterColumn)
			}
			assertDependencyTaskUnchanged(t, f, dependent.ID, beforeTask)
			var afterEvents int
			if err := f.store.DB.QueryRowContext(f.ctx, `SELECT COUNT(1) FROM events WHERE project_id=?`, f.project.ID).Scan(&afterEvents); err != nil {
				t.Fatalf("recount project events: %v", err)
			}
			if afterEvents != beforeEvents {
				t.Fatalf("rejected bulk transition emitted events: %d -> %d", beforeEvents, afterEvents)
			}
		})
	}

	f := newDependencyFixture(t, "BULKREOPEN")
	name, completedState := "Completed batch", "completed"
	column, err := f.store.CreateColumn(f.ctx, f.project.ID, ColumnInput{Name: &name, SemanticState: &completedState}, f.actor.ID)
	if err != nil {
		t.Fatalf("create completed batch column: %v", err)
	}
	prerequisite, err := f.store.CreateTask(f.ctx, f.project.ID, TaskInput{Title: dependencyStringPtr("batch prerequisite"), ColumnID: &column.ID}, f.actor.ID)
	if err != nil {
		t.Fatalf("create completed batch prerequisite: %v", err)
	}
	dependent := f.add(t, f.task(t, "active dependent"), prerequisite)
	active, err := f.store.StateColumn(f.ctx, f.project.ID, "active")
	if err != nil {
		t.Fatalf("read active column: %v", err)
	}
	dependent, err = f.store.UpdateTask(f.ctx, dependent.ID, TaskInput{ColumnID: &active.ID}, dependent.Version, f.actor.ID)
	if err != nil {
		t.Fatalf("start dependent: %v", err)
	}
	beforeTask := dependencyDurableState(t, f, prerequisite.ID)
	blocked := "blocked"
	_, err = f.store.UpdateColumn(f.ctx, column.ID, ColumnInput{SemanticState: &blocked}, f.actor.ID)
	details := dependencyErrorDetails(t, err, ErrDependencyInUse)
	tasks, ok := details["tasks"].([]map[string]any)
	if !ok || len(tasks) != 1 {
		t.Fatalf("bulk reopen blockers = %#v", details["tasks"])
	}
	assertDependencyTaskUnchanged(t, f, prerequisite.ID, beforeTask)
	afterColumn, err := f.store.GetColumn(f.ctx, column.ID)
	if err != nil {
		t.Fatalf("reload completed batch column: %v", err)
	}
	if afterColumn.SemanticState != completedState || afterColumn.UpdatedAt != column.UpdatedAt {
		t.Fatalf("rejected bulk reopen changed column: before=%+v after=%+v", column, afterColumn)
	}
	_ = dependent
}

func TestDependencyLifecycleRacesSerializeCompletionClaimAndReopen(t *testing.T) {
	t.Run("completion and claim", func(t *testing.T) {
		f := newDependencyFixture(t, "RACECOMPLETE")
		prerequisite := f.task(t, "prerequisite")
		dependent := f.add(t, f.task(t, "dependent"), prerequisite)
		start := make(chan struct{})
		var group sync.WaitGroup
		group.Add(2)
		var completeErr, claimErr error
		go func() {
			defer group.Done()
			<-start
			_, completeErr = f.store.CompleteTask(f.ctx, prerequisite.ID, f.actor.ID, prerequisite.Version)
		}()
		go func() {
			defer group.Done()
			<-start
			_, claimErr = f.store.ClaimTask(f.ctx, dependent.ID, f.actor.ID, time.Hour, dependent.Version)
		}()
		close(start)
		group.Wait()
		if completeErr != nil {
			t.Fatalf("concurrent prerequisite completion: %v", completeErr)
		}
		if claimErr != nil && !errors.Is(claimErr, ErrUnmetDependencies) {
			t.Fatalf("concurrent dependent claim error = %v", claimErr)
		}
		loaded, err := f.store.GetTask(f.ctx, dependent.ID)
		if err != nil {
			t.Fatalf("reload raced dependent: %v", err)
		}
		if claimErr != nil {
			if _, err := f.store.ClaimTask(f.ctx, dependent.ID, f.actor.ID, time.Hour, loaded.Version); err != nil {
				t.Fatalf("claim after serialized completion: %v", err)
			}
		} else if loaded.ClaimedBy == nil || *loaded.ClaimedBy != f.actor.ID {
			t.Fatalf("successful concurrent claim not persisted: %+v", loaded)
		}
	})

	t.Run("reopen and claim", func(t *testing.T) {
		f := newDependencyFixture(t, "RACEREOPEN")
		prerequisite := f.task(t, "prerequisite")
		prerequisite, err := f.store.CompleteTask(f.ctx, prerequisite.ID, f.actor.ID, prerequisite.Version)
		if err != nil {
			t.Fatalf("complete prerequisite: %v", err)
		}
		dependent := f.add(t, f.task(t, "dependent"), prerequisite)
		start := make(chan struct{})
		var group sync.WaitGroup
		group.Add(2)
		var reopenErr, claimErr error
		go func() {
			defer group.Done()
			<-start
			_, reopenErr = f.store.BlockTask(f.ctx, prerequisite.ID, f.actor.ID, prerequisite.Version)
		}()
		go func() {
			defer group.Done()
			<-start
			_, claimErr = f.store.ClaimTask(f.ctx, dependent.ID, f.actor.ID, time.Hour, dependent.Version)
		}()
		close(start)
		group.Wait()
		switch {
		case reopenErr == nil && errors.Is(claimErr, ErrUnmetDependencies):
		case claimErr == nil && errors.Is(reopenErr, ErrDependencyInUse):
		default:
			t.Fatalf("reopen/claim race results = reopen:%v claim:%v, want exactly one invariant-safe winner", reopenErr, claimErr)
		}
	})
}
