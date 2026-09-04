package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/KanterLabs/helm/internal/db"
)

func TestHeartbeatAgentWorkTouchesOnlySnapshotTimestamp(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, _, task := createAgentWorkFixture(t, data, ctx, "HEARTBEAT")
	claimed, err := data.ClaimTask(ctx, task.ID, actor.ID, time.Hour, task.Version)
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	_, err = data.PublishAgentWork(ctx, task.ID, AgentWorkInput{
		OperationID: "heartbeat/run-1",
		State:       "working",
		Phase:       "long operation",
		Summary:     "Keep the worker alive.",
		NextAction:  "Continue the operation.",
	}, claimed.Version, actor.ID)
	if err != nil {
		t.Fatalf("publish agent work: %v", err)
	}
	oldUpdatedAt := "2026-01-01T00:00:00Z"
	if _, err := database.ExecContext(ctx, `UPDATE task_agent_work SET updated_at=? WHERE task_id=?`, oldUpdatedAt, task.ID); err != nil {
		t.Fatalf("age snapshot: %v", err)
	}
	before, err := data.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("read before heartbeat: %v", err)
	}
	if before.AgentWork == nil || before.AgentWork.UpdatedAt != oldUpdatedAt {
		t.Fatalf("before heartbeat snapshot = %+v", before.AgentWork)
	}
	var commentsBefore, eventsBefore int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(1) FROM comments WHERE task_id=?`, task.ID).Scan(&commentsBefore); err != nil {
		t.Fatalf("count comments before heartbeat: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(1) FROM events WHERE task_id=?`, task.ID).Scan(&eventsBefore); err != nil {
		t.Fatalf("count events before heartbeat: %v", err)
	}
	var revisionBefore int64
	if err := database.QueryRowContext(ctx, `SELECT task_collection_revision FROM projects WHERE id=?`, task.ProjectID).Scan(&revisionBefore); err != nil {
		t.Fatalf("read project agent-work revision before heartbeat: %v", err)
	}

	heartbeated, err := data.HeartbeatAgentWork(ctx, task.ID, "heartbeat/run-1", actor.ID)
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if heartbeated.Version != before.Version || heartbeated.UpdatedAt != before.UpdatedAt {
		t.Fatalf("heartbeat changed task metadata: version %d/%d updated_at %q/%q", heartbeated.Version, before.Version, heartbeated.UpdatedAt, before.UpdatedAt)
	}
	if heartbeated.AgentWork == nil {
		t.Fatal("heartbeat response omitted agent work")
	}
	gotWork := *heartbeated.AgentWork
	wantWork := *before.AgentWork
	if gotWork.UpdatedAt == wantWork.UpdatedAt {
		t.Fatalf("heartbeat did not refresh timestamp: before=%q after=%q", wantWork.UpdatedAt, gotWork.UpdatedAt)
	}
	gotWork.UpdatedAt = wantWork.UpdatedAt
	// Stale and action_needed are read-time derivations, not persisted
	// snapshot content; refreshing an aged timestamp is expected to change
	// those response flags.
	gotWork.Stale = wantWork.Stale
	gotWork.ActionNeeded = wantWork.ActionNeeded
	if !reflect.DeepEqual(gotWork, wantWork) {
		t.Fatalf("heartbeat changed snapshot content: before=%+v after=%+v", wantWork, gotWork)
	}
	var commentsAfter, eventsAfter int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(1) FROM comments WHERE task_id=?`, task.ID).Scan(&commentsAfter); err != nil {
		t.Fatalf("count comments after heartbeat: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(1) FROM events WHERE task_id=?`, task.ID).Scan(&eventsAfter); err != nil {
		t.Fatalf("count events after heartbeat: %v", err)
	}
	if commentsAfter != commentsBefore || eventsAfter != eventsBefore {
		t.Fatalf("heartbeat added durable noise: comments %d/%d events %d/%d", commentsBefore, commentsAfter, eventsBefore, eventsAfter)
	}
	var revisionAfter int64
	if err := database.QueryRowContext(ctx, `SELECT task_collection_revision FROM projects WHERE id=?`, task.ProjectID).Scan(&revisionAfter); err != nil {
		t.Fatalf("read project agent-work revision after heartbeat: %v", err)
	}
	if revisionAfter != revisionBefore+1 {
		t.Fatalf("heartbeat project revision = %d, want %d", revisionAfter, revisionBefore+1)
	}
}

func TestTaskAgentWorkRevisionAdvancesForSameValueUpdate(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, _, task := createAgentWorkFixture(t, data, ctx, "HEARTBEATSAME")
	claimed, err := data.ClaimTask(ctx, task.ID, actor.ID, time.Hour, task.Version)
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	if _, err := data.PublishAgentWork(ctx, task.ID, AgentWorkInput{OperationID: "heartbeat/same-value", State: "working", Summary: "Same value trigger"}, claimed.Version, actor.ID); err != nil {
		t.Fatalf("publish agent work: %v", err)
	}
	var before int64
	if err := database.QueryRowContext(ctx, `SELECT task_collection_revision FROM projects WHERE id=?`, task.ProjectID).Scan(&before); err != nil {
		t.Fatalf("read revision before same-value update: %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE task_agent_work SET updated_at=updated_at WHERE task_id=?`, task.ID); err != nil {
		t.Fatalf("same-value snapshot update: %v", err)
	}
	var after int64
	if err := database.QueryRowContext(ctx, `SELECT task_collection_revision FROM projects WHERE id=?`, task.ProjectID).Scan(&after); err != nil {
		t.Fatalf("read revision after same-value update: %v", err)
	}
	if after != before+1 {
		t.Fatalf("same-value snapshot revision = %d, want %d", after, before+1)
	}
}

func TestHeartbeatAgentWorkGuardsAndFailuresAreQuiet(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, project, base := createAgentWorkFixture(t, data, ctx, "HEARTBEATGUARDS")

	newPublished := func(title, operationID string) Task {
		t.Helper()
		task, err := data.CreateTask(ctx, project.ID, TaskInput{Title: stringPtrForTest(title)}, actor.ID)
		if err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		claimed, err := data.ClaimTask(ctx, task.ID, actor.ID, time.Hour, task.Version)
		if err != nil {
			t.Fatalf("claim %s: %v", title, err)
		}
		published, err := data.PublishAgentWork(ctx, task.ID, AgentWorkInput{OperationID: operationID, State: "working", Summary: title}, claimed.Version, actor.ID)
		if err != nil {
			t.Fatalf("publish %s: %v", title, err)
		}
		return published
	}

	missing := base
	missingBefore, err := data.GetTask(ctx, missing.ID)
	if err != nil {
		t.Fatalf("read missing-snapshot task: %v", err)
	}
	if _, err := data.HeartbeatAgentWork(ctx, missing.ID, "missing", actor.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("missing snapshot heartbeat = %v, want forbidden", err)
	}
	assertHeartbeatTaskUnchanged(t, data, missingBefore)

	published := newPublished("mismatched operation", "heartbeat/match")
	publishedBefore, err := data.GetTask(ctx, published.ID)
	if err != nil {
		t.Fatalf("read mismatched-operation task: %v", err)
	}
	if _, err := data.HeartbeatAgentWork(ctx, published.ID, "heartbeat/other", actor.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("mismatched operation heartbeat = %v, want forbidden", err)
	}
	assertHeartbeatTaskUnchanged(t, data, publishedBefore)
	if _, err := data.HeartbeatAgentWork(ctx, published.ID, "bad operation", actor.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid operation heartbeat = %v, want invalid", err)
	}
	assertHeartbeatTaskUnchanged(t, data, publishedBefore)

	expired := newPublished("expired claim", "heartbeat/expired")
	if _, err := database.ExecContext(ctx, `UPDATE tasks SET claim_expires_at=? WHERE id=?`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), expired.ID); err != nil {
		t.Fatalf("expire claim: %v", err)
	}
	expiredBefore, err := data.GetTask(ctx, expired.ID)
	if err != nil {
		t.Fatalf("read expired task: %v", err)
	}
	if _, err := data.HeartbeatAgentWork(ctx, expired.ID, "heartbeat/expired", actor.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expired claim heartbeat = %v, want forbidden", err)
	}
	assertHeartbeatTaskUnchanged(t, data, expiredBefore)

	foreign := newPublished("foreign claim", "heartbeat/foreign")
	other, err := data.CreateActor(ctx, Actor{Kind: "agent", Name: "Foreign heartbeat actor"}, "")
	if err != nil {
		t.Fatalf("create foreign actor: %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE tasks SET claimed_by=?, claim_expires_at=? WHERE id=?`, other.ID, time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), foreign.ID); err != nil {
		t.Fatalf("replace claim owner: %v", err)
	}
	foreignBefore, err := data.GetTask(ctx, foreign.ID)
	if err != nil {
		t.Fatalf("read foreign-claim task: %v", err)
	}
	if _, err := data.HeartbeatAgentWork(ctx, foreign.ID, "heartbeat/foreign", actor.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign claim heartbeat = %v, want forbidden", err)
	}
	assertHeartbeatTaskUnchanged(t, data, foreignBefore)

	completed := newPublished("completed task", "heartbeat/completed")
	completed, err = data.CompleteTaskWithClaim(ctx, completed.ID, actor.ID, completed.Version)
	if err != nil {
		t.Fatalf("complete task: %v", err)
	}
	if _, err := data.HeartbeatAgentWork(ctx, completed.ID, "heartbeat/completed", actor.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("completed heartbeat = %v, want conflict", err)
	}
	assertHeartbeatTaskUnchanged(t, data, completed)
}

func TestHeartbeatAgentWorkRechecksLeaseAfterWaitingForWriter(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "roadmap.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, project, task := createAgentWorkFixture(t, data, ctx, "HEARTBEATCLOCK")
	claimed, err := data.ClaimTask(ctx, task.ID, actor.ID, time.Hour, task.Version)
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	published, err := data.PublishAgentWork(ctx, task.ID, AgentWorkInput{OperationID: "heartbeat/clock", State: "working", Summary: "Must not heartbeat after expiry."}, claimed.Version, actor.ID)
	if err != nil {
		t.Fatalf("publish agent work: %v", err)
	}
	expiresAt := time.Now().UTC().Add(350 * time.Millisecond)
	if _, err := database.ExecContext(ctx, `UPDATE tasks SET claim_expires_at=? WHERE id=?`, expiresAt.Format(time.RFC3339Nano), task.ID); err != nil {
		t.Fatalf("shorten claim: %v", err)
	}
	before, err := data.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("read before blocked heartbeat: %v", err)
	}

	blocker, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin blocking transaction: %v", err)
	}
	if _, err := blocker.ExecContext(ctx, `UPDATE projects SET updated_at=updated_at WHERE id=?`, project.ID); err != nil {
		_ = blocker.Rollback()
		t.Fatalf("acquire writer lock: %v", err)
	}
	result := make(chan error, 1)
	go func() {
		_, heartbeatErr := data.HeartbeatAgentWork(ctx, task.ID, "heartbeat/clock", actor.ID)
		result <- heartbeatErr
	}()
	time.Sleep(time.Until(expiresAt) + 150*time.Millisecond)
	if err := blocker.Commit(); err != nil {
		t.Fatalf("release writer lock: %v", err)
	}
	select {
	case heartbeatErr := <-result:
		if !errors.Is(heartbeatErr, ErrForbidden) {
			t.Fatalf("heartbeat after blocked lease expiry = %v, want forbidden", heartbeatErr)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("heartbeat did not finish after writer lock was released")
	}
	assertHeartbeatTaskUnchanged(t, data, before)
	if published.AgentWork == nil {
		t.Fatal("published task omitted agent work")
	}
}

func assertHeartbeatTaskUnchanged(t *testing.T, data *Store, before Task) {
	t.Helper()
	after, err := data.GetTask(context.Background(), before.ID)
	if err != nil {
		t.Fatalf("read task after rejected heartbeat: %v", err)
	}
	if after.Version != before.Version || after.UpdatedAt != before.UpdatedAt || after.CommentCount != before.CommentCount {
		t.Fatalf("rejected heartbeat changed task metadata: before=%+v after=%+v", before, after)
	}
	if before.AgentWork == nil {
		if after.AgentWork != nil {
			t.Fatalf("rejected heartbeat created snapshot: %+v", after.AgentWork)
		}
		return
	}
	if after.AgentWork == nil {
		t.Fatal("rejected heartbeat removed snapshot")
	}
	if !reflect.DeepEqual(*after.AgentWork, *before.AgentWork) {
		t.Fatalf("rejected heartbeat changed snapshot: before=%+v after=%+v", before.AgentWork, after.AgentWork)
	}
}
