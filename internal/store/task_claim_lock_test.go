package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"roadmap/internal/db"
)

type taskClaimLockFixture struct {
	ctx     context.Context
	db      *sql.DB
	store   *Store
	owner   Actor
	project Project
}

type taskClaimLockState struct {
	version      int64
	columnID     string
	claimedBy    sql.NullString
	claimExpires sql.NullString
	completedAt  sql.NullString
	updatedAt    string
	comments     int
	events       int
}

func newTaskClaimLockFixture(t *testing.T, key string) taskClaimLockFixture {
	t.Helper()
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "roadmap.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	data := New(database)
	owner, err := data.CreateActor(ctx, Actor{Kind: "agent", Name: "claim owner"}, "")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	project, err := data.CreateProject(ctx, ProjectInput{Key: taskClaimLockStringPtr(key), Name: taskClaimLockStringPtr("Claim lock")}, owner.ID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return taskClaimLockFixture{ctx: ctx, db: database, store: data, owner: owner, project: project}
}

func (f taskClaimLockFixture) createTask(t *testing.T, title string) Task {
	t.Helper()
	task, err := f.store.CreateTask(f.ctx, f.project.ID, TaskInput{Title: taskClaimLockStringPtr(title)}, f.owner.ID)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	return task
}

func (f taskClaimLockFixture) state(t *testing.T, taskID string) taskClaimLockState {
	t.Helper()
	var state taskClaimLockState
	if err := f.db.QueryRowContext(f.ctx, `SELECT version, column_id, claimed_by, claim_expires_at, completed_at, updated_at FROM tasks WHERE id=?`, taskID).Scan(&state.version, &state.columnID, &state.claimedBy, &state.claimExpires, &state.completedAt, &state.updatedAt); err != nil {
		t.Fatalf("read task state: %v", err)
	}
	if err := f.db.QueryRowContext(f.ctx, `SELECT COUNT(1) FROM comments WHERE task_id=?`, taskID).Scan(&state.comments); err != nil {
		t.Fatalf("count task comments: %v", err)
	}
	if err := f.db.QueryRowContext(f.ctx, `SELECT COUNT(1) FROM events WHERE task_id=?`, taskID).Scan(&state.events); err != nil {
		t.Fatalf("count task events: %v", err)
	}
	return state
}

func (f taskClaimLockFixture) holdWriterLock(t *testing.T) *sql.Tx {
	t.Helper()
	blocker, err := f.db.BeginTx(f.ctx, nil)
	if err != nil {
		t.Fatalf("begin blocking transaction: %v", err)
	}
	if _, err := blocker.ExecContext(f.ctx, `UPDATE projects SET updated_at=updated_at WHERE id=?`, f.project.ID); err != nil {
		_ = blocker.Rollback()
		t.Fatalf("acquire writer lock: %v", err)
	}
	return blocker
}

func TestClaimGatedTaskMutationsRejectExpiredLeaseAfterWriterWait(t *testing.T) {
	for _, test := range []struct {
		name string
		call func(*taskClaimLockFixture, Task) error
	}{
		{name: "complete", call: func(fixture *taskClaimLockFixture, task Task) error {
			_, err := fixture.store.CompleteTaskWithClaim(fixture.ctx, task.ID, fixture.owner.ID, task.Version)
			return err
		}},
		{name: "block", call: func(fixture *taskClaimLockFixture, task Task) error {
			_, err := fixture.store.BlockTaskWithClaim(fixture.ctx, task.ID, fixture.owner.ID, task.Version)
			return err
		}},
		{name: "release", call: func(fixture *taskClaimLockFixture, task Task) error {
			_, err := fixture.store.ReleaseTaskWithClaim(fixture.ctx, task.ID, fixture.owner.ID, task.Version)
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTaskClaimLockFixture(t, "LOCK"+test.name)
			task := fixture.createTask(t, test.name)
			claimed, err := fixture.store.ClaimTask(fixture.ctx, task.ID, fixture.owner.ID, time.Hour, task.Version)
			if err != nil {
				t.Fatalf("claim task: %v", err)
			}
			expiresAt := time.Now().UTC().Add(250 * time.Millisecond)
			if _, err := fixture.db.ExecContext(fixture.ctx, `UPDATE tasks SET claim_expires_at=? WHERE id=?`, expiresAt.Format(time.RFC3339Nano), task.ID); err != nil {
				t.Fatalf("shorten claim: %v", err)
			}
			before := fixture.state(t, task.ID)

			blocker := fixture.holdWriterLock(t)
			result := make(chan error, 1)
			go func() { result <- test.call(&fixture, claimed) }()
			// The read-side task lookup can complete while the writer lock is held,
			// but the guarded mutation must still be waiting on that lock.
			select {
			case err := <-result:
				t.Fatalf("%s finished while writer lock was held: %v", test.name, err)
			case <-time.After(100 * time.Millisecond):
			}
			if wait := time.Until(expiresAt) + 200*time.Millisecond; wait > 0 {
				time.Sleep(wait)
			}
			if err := blocker.Commit(); err != nil {
				t.Fatalf("release writer lock: %v", err)
			}

			select {
			case err := <-result:
				if !errors.Is(err, ErrForbidden) {
					t.Fatalf("%s after blocked lease expiry = %v, want forbidden", test.name, err)
				}
			case <-time.After(6 * time.Second):
				t.Fatalf("%s did not finish after writer lock was released", test.name)
			}
			after := fixture.state(t, task.ID)
			if after != before {
				t.Fatalf("rejected %s mutated task: before=%+v after=%+v", test.name, before, after)
			}
		})
	}
}

func TestClaimTaskUsesSQLiteStatementClockAfterWriterWait(t *testing.T) {
	fixture := newTaskClaimLockFixture(t, "CLAIMCLOCK")
	task := fixture.createTask(t, "claim clock")
	duration := MinTaskClaimDuration
	blocker := fixture.holdWriterLock(t)
	result := make(chan struct {
		task Task
		err  error
	}, 1)
	startedAt := time.Now().UTC()
	go func() {
		claimed, err := fixture.store.ClaimTask(fixture.ctx, task.ID, fixture.owner.ID, duration, task.Version)
		result <- struct {
			task Task
			err  error
		}{claimed, err}
	}()
	select {
	case result := <-result:
		t.Fatalf("claim finished while writer lock was held: task=%+v err=%v", result.task, result.err)
	case <-time.After(100 * time.Millisecond):
	}
	// Keep the lock long enough that a pre-lock application timestamp is
	// measurably older than the statement timestamp.
	time.Sleep(700 * time.Millisecond)
	releasedAt := time.Now().UTC()
	if err := blocker.Commit(); err != nil {
		t.Fatalf("release writer lock: %v", err)
	}

	var claimed struct {
		task Task
		err  error
	}
	select {
	case claimed = <-result:
	case <-time.After(6 * time.Second):
		t.Fatal("claim did not finish after writer lock was released")
	}
	if claimed.err != nil {
		t.Fatalf("claim after writer wait: %v", claimed.err)
	}
	assertLeaseStartsAt(t, claimed.task, releasedAt, duration, startedAt)
}

func TestRenewTaskUsesSQLiteStatementClockAfterWriterWait(t *testing.T) {
	fixture := newTaskClaimLockFixture(t, "RENEWCLOCK")
	task := fixture.createTask(t, "renew clock")
	initial, err := fixture.store.ClaimTask(fixture.ctx, task.ID, fixture.owner.ID, time.Hour, task.Version)
	if err != nil {
		t.Fatalf("initial claim: %v", err)
	}
	duration := MinTaskClaimDuration
	blocker := fixture.holdWriterLock(t)
	result := make(chan struct {
		task Task
		err  error
	}, 1)
	startedAt := time.Now().UTC()
	go func() {
		renewed, err := fixture.store.RenewTask(fixture.ctx, task.ID, fixture.owner.ID, duration, initial.Version)
		result <- struct {
			task Task
			err  error
		}{renewed, err}
	}()
	select {
	case result := <-result:
		t.Fatalf("renew finished while writer lock was held: task=%+v err=%v", result.task, result.err)
	case <-time.After(100 * time.Millisecond):
	}
	time.Sleep(700 * time.Millisecond)
	releasedAt := time.Now().UTC()
	if err := blocker.Commit(); err != nil {
		t.Fatalf("release writer lock: %v", err)
	}

	var renewed struct {
		task Task
		err  error
	}
	select {
	case renewed = <-result:
	case <-time.After(6 * time.Second):
		t.Fatal("renew did not finish after writer lock was released")
	}
	if renewed.err != nil {
		t.Fatalf("renew after writer wait: %v", renewed.err)
	}
	assertLeaseStartsAt(t, renewed.task, releasedAt, duration, startedAt)
}

func assertLeaseStartsAt(t *testing.T, task Task, releasedAt time.Time, duration time.Duration, startedAt time.Time) {
	t.Helper()
	if task.ClaimExpiresAt == nil {
		t.Fatal("claim expiry is missing")
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, *task.ClaimExpiresAt)
	if err != nil {
		t.Fatalf("parse claim expiry %q: %v", *task.ClaimExpiresAt, err)
	}
	if !expiresAt.After(releasedAt.Add(duration - 300*time.Millisecond)) {
		t.Fatalf("claim expiry %s was based before writer release %s (started %s)", expiresAt, releasedAt, startedAt)
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, task.UpdatedAt)
	if err != nil {
		t.Fatalf("parse updated_at %q: %v", task.UpdatedAt, err)
	}
	if !updatedAt.After(releasedAt.Add(-300 * time.Millisecond)) {
		t.Fatalf("updated_at %s was based before writer release %s (started %s)", updatedAt, releasedAt, startedAt)
	}
}

func taskClaimLockStringPtr(value string) *string { return &value }
