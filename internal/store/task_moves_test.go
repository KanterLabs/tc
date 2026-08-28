package store

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestMoveTaskComputesDestinationPositionAndRecordsActivity(t *testing.T) {
	fixture := newTaskClaimLockFixture(t, "MOVE")
	ctx := fixture.ctx
	columns, err := fixture.store.ListColumns(ctx, fixture.project.ID)
	if err != nil || len(columns) < 2 {
		t.Fatalf("list columns: %v", err)
	}
	backlog, ready := columns[0], columns[1]
	other, err := fixture.store.CreateTask(ctx, fixture.project.ID, TaskInput{Title: taskMoveStringPtr("already ready"), ColumnID: &ready.ID, Position: taskMoveFloatPtr(4)}, fixture.owner.ID)
	if err != nil {
		t.Fatalf("create destination task: %v", err)
	}
	task, err := fixture.store.CreateTask(ctx, fixture.project.ID, TaskInput{Title: taskMoveStringPtr("move me"), ColumnID: &backlog.ID, Position: taskMoveFloatPtr(2)}, fixture.owner.ID)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	moved, err := fixture.store.MoveTask(ctx, task.ID, TaskMoveInput{
		DestinationColumnID:    ready.ID,
		ExpectedSourceColumnID: backlog.ID,
		Source:                 "board-ui",
		Reason:                 "operator reordered work",
	}, task.Version, fixture.owner.ID)
	if err != nil {
		t.Fatalf("move task: %v", err)
	}
	if moved.ColumnID != ready.ID || moved.Position != other.Position+1 || moved.Version != task.Version+1 {
		t.Fatalf("moved task = column=%q position=%v version=%d, want column=%q position=%v version=%d", moved.ColumnID, moved.Position, moved.Version, ready.ID, other.Position+1, task.Version+1)
	}
	if moved.ClaimedBy != nil || moved.ClaimExpiresAt != nil {
		t.Fatalf("move unexpectedly retained a claim: claimed_by=%v expires=%v", moved.ClaimedBy, moved.ClaimExpiresAt)
	}

	var eventType, payload string
	if err := fixture.db.QueryRowContext(ctx, `SELECT type, payload FROM events WHERE task_id=? ORDER BY cursor DESC LIMIT 1`, task.ID).Scan(&eventType, &payload); err != nil {
		t.Fatalf("read move event: %v", err)
	}
	if eventType != "task.moved" {
		t.Fatalf("event type = %q, want task.moved", eventType)
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		t.Fatalf("decode move event: %v", err)
	}
	for key, want := range map[string]any{
		"from_column_id":      backlog.ID,
		"to_column_id":        ready.ID,
		"from_column":         backlog.Name,
		"to_column":           ready.Name,
		"from_semantic_state": backlog.SemanticState,
		"to_semantic_state":   ready.SemanticState,
		"actor":               fixture.owner.ID,
		"actor_id":            fixture.owner.ID,
		"source":              "board-ui",
		"reason":              "operator reordered work",
		"resulting_version":   float64(moved.Version),
	} {
		if event[key] != want {
			t.Fatalf("event[%q] = %#v, want %#v", key, event[key], want)
		}
	}
	if got := event["old_position"]; got != float64(task.Position) {
		t.Fatalf("event old_position = %#v, want %v", got, task.Position)
	}
	if got := event["new_position"]; got != moved.Position {
		t.Fatalf("event new_position = %#v, want %v", got, moved.Position)
	}
	if nested, ok := event["from"].(map[string]any); !ok || nested["semantic_state"] != backlog.SemanticState {
		t.Fatalf("event from record = %#v, want source semantic state", event["from"])
	}
}

func TestMoveTaskRejectsOwnActiveClaimUntilExplicitRelease(t *testing.T) {
	fixture := newTaskClaimLockFixture(t, "MOVEOWNCLAIM")
	columns, err := fixture.store.ListColumns(fixture.ctx, fixture.project.ID)
	if err != nil {
		t.Fatalf("list columns: %v", err)
	}
	task := fixture.createTask(t, "owned claim")
	claimed, err := fixture.store.ClaimTask(fixture.ctx, task.ID, fixture.owner.ID, time.Hour, task.Version)
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	_, err = fixture.store.MoveTask(fixture.ctx, task.ID, TaskMoveInput{
		DestinationColumnID:    columns[1].ID,
		ExpectedSourceColumnID: columns[0].ID,
		Source:                 "board-ui",
	}, claimed.Version, fixture.owner.ID)
	if !errors.Is(err, ErrClaimUnavailable) {
		t.Fatalf("own-claimed move error = %v, want ErrClaimUnavailable", err)
	}
	released, err := fixture.store.ReleaseTask(fixture.ctx, task.ID, fixture.owner.ID, claimed.Version)
	if err != nil {
		t.Fatalf("release own claim: %v", err)
	}
	if _, err := fixture.store.MoveTask(fixture.ctx, task.ID, TaskMoveInput{
		DestinationColumnID:    columns[1].ID,
		ExpectedSourceColumnID: columns[0].ID,
		Source:                 "board-ui",
	}, released.Version, fixture.owner.ID); err != nil {
		t.Fatalf("move after explicit release: %v", err)
	}
}

func TestMoveTaskRejectsForeignClaimWithoutMutation(t *testing.T) {
	fixture := newTaskClaimLockFixture(t, "MOVECLAIM")
	foreign, err := fixture.store.CreateActor(fixture.ctx, Actor{Kind: "agent", Name: "foreign mover"}, fixture.owner.ID)
	if err != nil {
		t.Fatalf("create foreign actor: %v", err)
	}
	columns, err := fixture.store.ListColumns(fixture.ctx, fixture.project.ID)
	if err != nil {
		t.Fatalf("list columns: %v", err)
	}
	task := fixture.createTask(t, "claimed move")
	claimed, err := fixture.store.ClaimTask(fixture.ctx, task.ID, foreign.ID, time.Hour, task.Version)
	if err != nil {
		t.Fatalf("foreign claim: %v", err)
	}
	before := fixture.state(t, task.ID)
	_, err = fixture.store.MoveTask(fixture.ctx, task.ID, TaskMoveInput{
		DestinationColumnID:    columns[1].ID,
		ExpectedSourceColumnID: columns[0].ID,
		Source:                 "reconciler",
	}, claimed.Version, fixture.owner.ID)
	if !errors.Is(err, ErrClaimUnavailable) {
		t.Fatalf("foreign-claimed move error = %v, want ErrClaimUnavailable", err)
	}
	after := fixture.state(t, task.ID)
	if after != before {
		t.Fatalf("rejected move mutated task: before=%+v after=%+v", before, after)
	}
}

func TestMoveTaskRejectsStaleVersionOrSourceWithAuthoritativeConflict(t *testing.T) {
	fixture := newTaskClaimLockFixture(t, "MOVESTALE")
	columns, err := fixture.store.ListColumns(fixture.ctx, fixture.project.ID)
	if err != nil {
		t.Fatalf("list columns: %v", err)
	}
	task := fixture.createTask(t, "stale move")
	updated, err := fixture.store.UpdateTask(fixture.ctx, task.ID, TaskInput{Title: taskMoveStringPtr("changed")}, task.Version, fixture.owner.ID)
	if err != nil {
		t.Fatalf("update task: %v", err)
	}
	before := fixture.state(t, task.ID)
	_, err = fixture.store.MoveTask(fixture.ctx, task.ID, TaskMoveInput{
		DestinationColumnID:    columns[1].ID,
		ExpectedSourceColumnID: columns[0].ID,
		Source:                 "reconciler",
	}, task.Version, fixture.owner.ID)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale-version move error = %v, want ErrConflict", err)
	}
	if typed, ok := err.(*Error); !ok || typed.Details == nil {
		t.Fatalf("stale-version error details = %#v, want authoritative details", err)
	}
	after := fixture.state(t, task.ID)
	if after != before {
		t.Fatalf("stale-version move mutated task: before=%+v after=%+v", before, after)
	}
	_, err = fixture.store.MoveTask(fixture.ctx, task.ID, TaskMoveInput{
		DestinationColumnID:    columns[1].ID,
		ExpectedSourceColumnID: columns[1].ID,
		Source:                 "reconciler",
	}, updated.Version, fixture.owner.ID)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale-source move error = %v, want ErrConflict", err)
	}
	if afterSource := fixture.state(t, task.ID); afterSource != before {
		t.Fatalf("stale-source move mutated task: before=%+v after=%+v", before, afterSource)
	}
}

func TestMoveTaskRejectsCrossProjectAndLifecycleDestinations(t *testing.T) {
	fixture := newTaskClaimLockFixture(t, "MOVEDEST")
	columns, err := fixture.store.ListColumns(fixture.ctx, fixture.project.ID)
	if err != nil {
		t.Fatalf("list columns: %v", err)
	}
	task := fixture.createTask(t, "destination validation")
	for _, destination := range columns[2:] { // active, blocked, completed
		before := fixture.state(t, task.ID)
		_, err := fixture.store.MoveTask(fixture.ctx, task.ID, TaskMoveInput{
			DestinationColumnID:    destination.ID,
			ExpectedSourceColumnID: columns[0].ID,
			Source:                 "board-ui",
		}, task.Version, fixture.owner.ID)
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("destination %s move error = %v, want ErrInvalid", destination.SemanticState, err)
		}
		if after := fixture.state(t, task.ID); after != before {
			t.Fatalf("forbidden %s move mutated task: before=%+v after=%+v", destination.SemanticState, before, after)
		}
	}
	otherProject, err := fixture.store.CreateProject(fixture.ctx, ProjectInput{Key: taskMoveStringPtr("OTHER"), Name: taskMoveStringPtr("Other project")}, fixture.owner.ID)
	if err != nil {
		t.Fatalf("create other project: %v", err)
	}
	otherColumns, err := fixture.store.ListColumns(fixture.ctx, otherProject.ID)
	if err != nil {
		t.Fatalf("list other columns: %v", err)
	}
	_, err = fixture.store.MoveTask(fixture.ctx, task.ID, TaskMoveInput{
		DestinationColumnID:    otherColumns[0].ID,
		ExpectedSourceColumnID: columns[0].ID,
		Source:                 "board-ui",
	}, task.Version, fixture.owner.ID)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross-project move error = %v, want ErrInvalid", err)
	}
}

func TestMoveTaskUsesSQLiteStatementClockAfterWriterWait(t *testing.T) {
	fixture := newTaskClaimLockFixture(t, "MOVECLOCK")
	columns, err := fixture.store.ListColumns(fixture.ctx, fixture.project.ID)
	if err != nil {
		t.Fatalf("list columns: %v", err)
	}
	foreign, err := fixture.store.CreateActor(fixture.ctx, Actor{Kind: "agent", Name: "clock owner"}, fixture.owner.ID)
	if err != nil {
		t.Fatalf("create foreign actor: %v", err)
	}
	task := fixture.createTask(t, "clock move")
	claimed, err := fixture.store.ClaimTask(fixture.ctx, task.ID, foreign.ID, time.Hour, task.Version)
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	expiresAt := time.Now().UTC().Add(500 * time.Millisecond)
	if _, err := fixture.db.ExecContext(fixture.ctx, `UPDATE tasks SET claim_expires_at=? WHERE id=?`, expiresAt.Format(time.RFC3339Nano), task.ID); err != nil {
		t.Fatalf("shorten claim: %v", err)
	}
	blocker := fixture.holdWriterLock(t)
	result := make(chan struct {
		task Task
		err  error
	}, 1)
	go func() {
		moved, moveErr := fixture.store.MoveTask(fixture.ctx, task.ID, TaskMoveInput{
			DestinationColumnID:    columns[1].ID,
			ExpectedSourceColumnID: columns[0].ID,
			Source:                 "reconciler",
		}, claimed.Version, fixture.owner.ID)
		result <- struct {
			task Task
			err  error
		}{moved, moveErr}
	}()
	select {
	case result := <-result:
		t.Fatalf("move finished while writer lock was held: task=%+v err=%v", result.task, result.err)
	case <-time.After(100 * time.Millisecond):
	}
	if wait := time.Until(expiresAt) + 250*time.Millisecond; wait > 0 {
		time.Sleep(wait)
	}
	if err := blocker.Commit(); err != nil {
		t.Fatalf("release writer lock: %v", err)
	}
	select {
	case moved := <-result:
		if moved.err != nil {
			t.Fatalf("move after claim expiry: %v", moved.err)
		}
		if moved.task.ClaimedBy != nil || moved.task.ClaimExpiresAt != nil {
			t.Fatalf("expired claim was not cleared: claimed_by=%v expires=%v", moved.task.ClaimedBy, moved.task.ClaimExpiresAt)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("move did not finish after writer lock was released")
	}
}

func taskMoveStringPtr(value string) *string { return &value }

func taskMoveFloatPtr(value float64) *float64 { return &value }
