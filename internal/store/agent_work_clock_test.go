package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/KanterLabs/helm/internal/db"
)

func TestPublishAgentWorkRechecksClaimAfterWaitingForWriter(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "roadmap.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, project, task := createAgentWorkFixture(t, data, ctx, "LEASECLOCK")
	claimed, err := data.ClaimTask(ctx, task.ID, actor.ID, time.Hour, task.Version)
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	expiresAt := time.Now().UTC().Add(300 * time.Millisecond)
	if _, err := database.ExecContext(ctx, `UPDATE tasks SET claim_expires_at=? WHERE id=?`, expiresAt.Format(time.RFC3339Nano), task.ID); err != nil {
		t.Fatalf("shorten claim: %v", err)
	}

	// Hold SQLite's single writer lock on a different row. PublishAgentWork
	// must evaluate lease validity only after this transaction releases it.
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
		_, publishErr := data.PublishAgentWork(ctx, task.ID, AgentWorkInput{
			OperationID: "lease-clock",
			State:       "working",
			Summary:     "Must not publish after expiry.",
		}, claimed.Version, actor.ID)
		result <- publishErr
	}()
	time.Sleep(time.Until(expiresAt) + 150*time.Millisecond)
	if err := blocker.Commit(); err != nil {
		t.Fatalf("release writer lock: %v", err)
	}

	select {
	case publishErr := <-result:
		if !errors.Is(publishErr, ErrForbidden) {
			t.Fatalf("publish after blocked lease expiry = %v, want forbidden", publishErr)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("publish did not finish after writer lock was released")
	}
	current, err := data.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("read task after rejected publish: %v", err)
	}
	if current.Version != claimed.Version || current.AgentWork != nil || current.CommentCount != 0 {
		t.Fatalf("rejected publish mutated task: version=%d work=%+v comments=%d", current.Version, current.AgentWork, current.CommentCount)
	}
}

func TestAgentWorkStaleFlagUsesFilterPredicateAtSubMillisecondBoundary(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, _, task := createAgentWorkFixture(t, data, ctx, "STALECLOCK")
	claimed, err := data.ClaimTask(ctx, task.ID, actor.ID, time.Hour, task.Version)
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	published, err := data.PublishAgentWork(ctx, task.ID, AgentWorkInput{OperationID: "stale-clock", State: "working", Summary: "Boundary pulse"}, claimed.Version, actor.ID)
	if err != nil {
		t.Fatalf("publish work: %v", err)
	}

	readAt := time.Date(2026, 8, 28, 12, 0, 0, 500_000, time.UTC)
	updatedAt := readAt.Add(-AgentWorkStaleAfter).Add(100 * time.Microsecond)
	if _, err := database.ExecContext(ctx, `UPDATE task_agent_work SET updated_at=? WHERE task_id=?`, updatedAt.Format(time.RFC3339Nano), published.ID); err != nil {
		t.Fatalf("set boundary timestamp: %v", err)
	}
	work, err := data.agentWorkAt(ctx, published.ID, readAt)
	if err != nil {
		t.Fatalf("read boundary pulse: %v", err)
	}
	if work == nil {
		t.Fatal("boundary pulse is missing")
	}
	var selected int
	if err := database.QueryRowContext(ctx, `SELECT CASE WHEN julianday(updated_at) <= julianday(?) THEN 1 ELSE 0 END FROM task_agent_work WHERE task_id=?`, agentWorkStaleCutoff(readAt), published.ID).Scan(&selected); err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("evaluate filter predicate: %v", err)
	}
	if work.Stale != (selected != 0) {
		t.Fatalf("response stale=%v but SQL stale filter=%v at %s", work.Stale, selected != 0, readAt.Format(time.RFC3339Nano))
	}
}
