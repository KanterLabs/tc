package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"roadmap/internal/db"
)

type taskClaimCompletionFixture struct {
	ctx     context.Context
	db      *sql.DB
	store   *Store
	owner   Actor
	foreign Actor
	project Project
}

type taskClaimCompletionState struct {
	version      int64
	claimedBy    sql.NullString
	claimExpires sql.NullString
	completedAt  sql.NullString
	updatedAt    string
	events       int
}

func newTaskClaimCompletionFixture(t *testing.T, key string) taskClaimCompletionFixture {
	t.Helper()
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	data := New(database)
	owner, err := data.CreateActor(ctx, Actor{Kind: "agent", Name: "claim owner"}, "")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	foreign, err := data.CreateActor(ctx, Actor{Kind: "agent", Name: "foreign claimant"}, "")
	if err != nil {
		t.Fatalf("create foreign claimant: %v", err)
	}
	project, err := data.CreateProject(ctx, ProjectInput{Key: claimCompletionStringPtr(key), Name: claimCompletionStringPtr("Claim completion")}, owner.ID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return taskClaimCompletionFixture{ctx: ctx, db: database, store: data, owner: owner, foreign: foreign, project: project}
}

func (f taskClaimCompletionFixture) createTask(t *testing.T, title string, completed bool) Task {
	t.Helper()
	input := TaskInput{Title: claimCompletionStringPtr(title)}
	if completed {
		column, err := f.store.StateColumn(f.ctx, f.project.ID, "completed")
		if err != nil {
			t.Fatalf("get completed column: %v", err)
		}
		input.ColumnID = &column.ID
	}
	task, err := f.store.CreateTask(f.ctx, f.project.ID, input, f.owner.ID)
	if err != nil {
		t.Fatalf("create task %q: %v", title, err)
	}
	return task
}

func (f taskClaimCompletionFixture) state(t *testing.T, taskID string) taskClaimCompletionState {
	t.Helper()
	var state taskClaimCompletionState
	if err := f.db.QueryRowContext(f.ctx, `SELECT version, claimed_by, claim_expires_at, completed_at, updated_at FROM tasks WHERE id=?`, taskID).Scan(&state.version, &state.claimedBy, &state.claimExpires, &state.completedAt, &state.updatedAt); err != nil {
		t.Fatalf("read task claim state: %v", err)
	}
	if err := f.db.QueryRowContext(f.ctx, `SELECT COUNT(1) FROM events WHERE task_id=?`, taskID).Scan(&state.events); err != nil {
		t.Fatalf("count task events: %v", err)
	}
	return state
}

func (f taskClaimCompletionFixture) assertUnchanged(t *testing.T, taskID string, before taskClaimCompletionState) {
	t.Helper()
	after := f.state(t, taskID)
	if after != before {
		t.Fatalf("rejected claim mutation changed state: before=%+v after=%+v", before, after)
	}
}

func TestClaimTaskRejectsUnclaimedCompletedTaskWithoutMutation(t *testing.T) {
	fixture := newTaskClaimCompletionFixture(t, "DONECLAIM")
	task := fixture.createTask(t, "completed task", true)
	before := fixture.state(t, task.ID)

	if _, err := fixture.store.ClaimTask(fixture.ctx, task.ID, fixture.owner.ID, time.Hour, task.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("claim completed task error = %v, want ErrConflict", err)
	} else if err.Error() != "task is already finished" {
		t.Fatalf("claim completed task error = %q, want finished-task conflict", err)
	}
	fixture.assertUnchanged(t, task.ID, before)
}

func TestRenewTaskRejectsCompletedTaskWithoutMutation(t *testing.T) {
	fixture := newTaskClaimCompletionFixture(t, "DONERENEW")
	task := fixture.createTask(t, "completed task", true)
	before := fixture.state(t, task.ID)

	for _, renew := range []struct {
		name string
		call func() error
	}{
		{name: "human", call: func() error {
			_, err := fixture.store.RenewTask(fixture.ctx, task.ID, fixture.owner.ID, time.Hour, task.Version)
			return err
		}},
		{name: "bearer", call: func() error {
			_, err := fixture.store.RenewTaskWithClaim(fixture.ctx, task.ID, fixture.owner.ID, time.Hour, task.Version)
			return err
		}},
	} {
		t.Run(renew.name, func(t *testing.T) {
			err := renew.call()
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("renew completed task error = %v, want ErrConflict", err)
			}
			if err.Error() != "task is already finished" {
				t.Fatalf("renew completed task error = %q, want finished-task conflict", err)
			}
			fixture.assertUnchanged(t, task.ID, before)
		})
	}
}

func TestClaimTaskRejectsAlreadyClaimedThenCompletedTaskWithoutMutation(t *testing.T) {
	fixture := newTaskClaimCompletionFixture(t, "DONEAFTERCLAIM")
	task := fixture.createTask(t, "claimed then completed", false)
	claimed, err := fixture.store.ClaimTask(fixture.ctx, task.ID, fixture.owner.ID, time.Hour, task.Version)
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	completed, err := fixture.store.CompleteTaskWithClaim(fixture.ctx, task.ID, fixture.owner.ID, claimed.Version)
	if err != nil {
		t.Fatalf("complete claimed task: %v", err)
	}
	before := fixture.state(t, completed.ID)

	if _, err := fixture.store.ClaimTask(fixture.ctx, completed.ID, fixture.owner.ID, time.Hour, completed.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("reclaim completed task error = %v, want ErrConflict", err)
	}
	fixture.assertUnchanged(t, completed.ID, before)
	if _, err := fixture.store.RenewTaskWithClaim(fixture.ctx, completed.ID, fixture.owner.ID, time.Hour, completed.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("renew completed task error = %v, want ErrConflict", err)
	}
	fixture.assertUnchanged(t, completed.ID, before)
}

func TestClaimTaskAllowsSameOwnerActiveReclaim(t *testing.T) {
	fixture := newTaskClaimCompletionFixture(t, "SAMEOWNER")
	task := fixture.createTask(t, "same owner", false)
	claimed, err := fixture.store.ClaimTask(fixture.ctx, task.ID, fixture.owner.ID, time.Hour, task.Version)
	if err != nil {
		t.Fatalf("initial claim: %v", err)
	}
	before := fixture.state(t, task.ID)

	reclaimed, err := fixture.store.ClaimTask(fixture.ctx, task.ID, fixture.owner.ID, time.Hour, claimed.Version)
	if err != nil {
		t.Fatalf("same-owner active reclaim: %v", err)
	}
	if reclaimed.Version != claimed.Version+1 || reclaimed.ClaimedBy == nil || *reclaimed.ClaimedBy != fixture.owner.ID || reclaimed.CompletedAt != nil {
		t.Fatalf("same-owner reclaim = version %d claimed_by=%v completed_at=%v, want version %d and active owner", reclaimed.Version, reclaimed.ClaimedBy, reclaimed.CompletedAt, claimed.Version+1)
	}
	if after := fixture.state(t, task.ID); after.events != before.events+1 {
		t.Fatalf("same-owner reclaim event count = %d, want %d", after.events, before.events+1)
	}
}

func TestClaimTaskAllowsExpiredOwnClaimReclaim(t *testing.T) {
	fixture := newTaskClaimCompletionFixture(t, "EXPIREDOWN")
	task := fixture.createTask(t, "expired own claim", false)
	claimed, err := fixture.store.ClaimTask(fixture.ctx, task.ID, fixture.owner.ID, time.Hour, task.Version)
	if err != nil {
		t.Fatalf("initial claim: %v", err)
	}
	expiredAt := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	if _, err := fixture.db.ExecContext(fixture.ctx, `UPDATE tasks SET claim_expires_at=? WHERE id=?`, expiredAt, task.ID); err != nil {
		t.Fatalf("expire own claim: %v", err)
	}

	reclaimed, err := fixture.store.ClaimTask(fixture.ctx, task.ID, fixture.owner.ID, time.Hour, claimed.Version)
	if err != nil {
		t.Fatalf("expired own reclaim: %v", err)
	}
	if reclaimed.Version != claimed.Version+1 || reclaimed.ClaimedBy == nil || *reclaimed.ClaimedBy != fixture.owner.ID || reclaimed.ClaimExpiresAt == nil {
		t.Fatalf("expired own reclaim = version %d claimed_by=%v claim_expires_at=%v, want version %d and active owner", reclaimed.Version, reclaimed.ClaimedBy, reclaimed.ClaimExpiresAt, claimed.Version+1)
	}
	if expiry, err := time.Parse(time.RFC3339Nano, *reclaimed.ClaimExpiresAt); err != nil || !expiry.After(time.Now().UTC()) {
		t.Fatalf("expired own reclaim expiry = %v (parse err=%v), want future expiry", reclaimed.ClaimExpiresAt, err)
	}
}

func TestClaimTaskRejectsForeignActiveClaimWithoutMutation(t *testing.T) {
	fixture := newTaskClaimCompletionFixture(t, "FOREIGNCLAIM")
	task := fixture.createTask(t, "foreign claim", false)
	claimed, err := fixture.store.ClaimTask(fixture.ctx, task.ID, fixture.owner.ID, time.Hour, task.Version)
	if err != nil {
		t.Fatalf("initial claim: %v", err)
	}
	before := fixture.state(t, task.ID)

	if _, err := fixture.store.ClaimTask(fixture.ctx, task.ID, fixture.foreign.ID, time.Hour, claimed.Version); !errors.Is(err, ErrClaimUnavailable) {
		t.Fatalf("foreign active claim error = %v, want ErrClaimUnavailable", err)
	}
	fixture.assertUnchanged(t, task.ID, before)
}

func claimCompletionStringPtr(value string) *string { return &value }
