package store

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func TestReorderTaskSupportsPreciseSameAndCrossColumnPlacement(t *testing.T) {
	fixture := newTaskClaimLockFixture(t, "ORDER")
	ctx := fixture.ctx
	columns, err := fixture.store.ListColumns(ctx, fixture.project.ID)
	if err != nil || len(columns) < 2 {
		t.Fatalf("list columns: %v", err)
	}
	backlog, ready := columns[0], columns[1]
	newAt := func(title string, column Column, position float64) Task {
		t.Helper()
		task, createErr := fixture.store.CreateTask(ctx, fixture.project.ID, TaskInput{Title: taskMoveStringPtr(title), ColumnID: &column.ID, Position: &position}, fixture.owner.ID)
		if createErr != nil {
			t.Fatalf("create %s: %v", title, createErr)
		}
		return task
	}
	first := newAt("first", backlog, 0)
	second := newAt("second", backlog, 1)
	third := newAt("third", backlog, 2)
	fourth := newAt("fourth", backlog, 3)

	backlog, err = fixture.store.GetColumn(ctx, backlog.ID)
	if err != nil {
		t.Fatal(err)
	}
	moved, err := fixture.store.ReorderTask(ctx, fourth.ID, TaskReorderInput{
		DestinationColumnID: fourth.ColumnID, ExpectedSourceColumnID: backlog.ID,
		BeforeTaskID: second.ID, AfterTaskID: first.ID, Source: "board", Reason: "move between visible cards",
		ExpectedDestinationOrderingVersion: backlog.OrderingVersion,
	}, fourth.Version, fixture.owner.ID)
	if err != nil {
		t.Fatalf("same-column reorder: %v", err)
	}
	if moved.ColumnID != backlog.ID || !(moved.Position > first.Position && moved.Position < second.Position) || moved.Version != fourth.Version+1 {
		t.Fatalf("same-column result = %+v, want between first and second", moved)
	}
	ordered, _, err := fixture.store.ListTasks(ctx, fixture.project.ID, TaskFilter{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if got := taskIDsInColumn(ordered, backlog.ID); len(got) != 4 || got[0] != first.ID || got[1] != fourth.ID || got[2] != second.ID || got[3] != third.ID {
		t.Fatalf("same-column order = %v, want [%s %s %s %s]", got, first.ID, fourth.ID, second.ID, third.ID)
	}

	ready, err = fixture.store.GetColumn(ctx, ready.ID)
	if err != nil {
		t.Fatal(err)
	}
	backlog, err = fixture.store.GetColumn(ctx, backlog.ID)
	if err != nil {
		t.Fatal(err)
	}
	crossed, err := fixture.store.ReorderTask(ctx, third.ID, TaskReorderInput{
		DestinationColumnID: ready.ID, ExpectedSourceColumnID: backlog.ID,
		Placement: "first", Source: "board", Reason: "move to first",
		ExpectedSourceOrderingVersion:      backlog.OrderingVersion,
		ExpectedDestinationOrderingVersion: ready.OrderingVersion,
	}, third.Version, fixture.owner.ID)
	if err != nil {
		t.Fatalf("cross-column reorder: %v", err)
	}
	if crossed.ColumnID != ready.ID || crossed.Position != 0 || crossed.Version != third.Version+1 {
		t.Fatalf("cross-column result = %+v, want first ready card", crossed)
	}
	if got, err := fixture.store.GetColumn(ctx, ready.ID); err != nil || got.OrderingVersion != ready.OrderingVersion+1 {
		t.Fatalf("destination ordering revision = %+v, %v; want %d", got, err, ready.OrderingVersion+1)
	}

	var eventType, payload string
	if err := fixture.db.QueryRowContext(ctx, `SELECT type, payload FROM events WHERE task_id=? ORDER BY cursor DESC LIMIT 1`, third.ID).Scan(&eventType, &payload); err != nil {
		t.Fatalf("read reorder event: %v", err)
	}
	if eventType != "task.moved" {
		t.Fatalf("reorder event type = %q, want task.moved", eventType)
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		t.Fatal(err)
	}
	if event["placement"] != "first" || event["to_column_id"] != ready.ID || event["rebalanced"] != false {
		t.Fatalf("reorder event metadata = %#v", event)
	}
}

func TestReorderTaskRejectsStaleColumnRevisionWithoutChangingUnrelatedCards(t *testing.T) {
	fixture := newTaskClaimLockFixture(t, "ORDERSTALE")
	ctx := fixture.ctx
	columns, err := fixture.store.ListColumns(ctx, fixture.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	backlog := columns[0]
	position0, position1, position2 := 0.0, 1.0, 2.0
	tasks := make([]Task, 3)
	for index, input := range []TaskInput{
		{Title: taskMoveStringPtr("one"), ColumnID: &backlog.ID, Position: &position0},
		{Title: taskMoveStringPtr("two"), ColumnID: &backlog.ID, Position: &position1},
		{Title: taskMoveStringPtr("three"), ColumnID: &backlog.ID, Position: &position2},
	} {
		tasks[index], err = fixture.store.CreateTask(ctx, fixture.project.ID, input, fixture.owner.ID)
		if err != nil {
			t.Fatalf("create task %d: %v", index, err)
		}
	}
	backlog, err = fixture.store.GetColumn(ctx, backlog.ID)
	if err != nil {
		t.Fatal(err)
	}
	staleRevision := backlog.OrderingVersion
	if _, err := fixture.store.ReorderTask(ctx, tasks[1].ID, TaskReorderInput{
		DestinationColumnID: backlog.ID, ExpectedSourceColumnID: backlog.ID,
		Placement: "first", Source: "board", ExpectedOrderingVersion: staleRevision,
	}, tasks[1].Version, fixture.owner.ID); err != nil {
		t.Fatalf("first reorder: %v", err)
	}
	unchanged := fixture.state(t, tasks[2].ID)
	_, err = fixture.store.ReorderTask(ctx, tasks[2].ID, TaskReorderInput{
		DestinationColumnID: backlog.ID, ExpectedSourceColumnID: backlog.ID,
		Placement: "first", Source: "board", ExpectedOrderingVersion: staleRevision,
	}, tasks[2].Version, fixture.owner.ID)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale ordering revision error = %v, want ErrConflict", err)
	}
	if got := fixture.state(t, tasks[2].ID); got != unchanged {
		t.Fatalf("stale reorder changed unrelated task: before=%+v after=%+v", unchanged, got)
	}
}

func TestReorderTaskKeepsHiddenNeighborPositionWhilePlacingBetweenAnchors(t *testing.T) {
	fixture := newTaskClaimLockFixture(t, "ORDERFILTER")
	ctx := fixture.ctx
	columns, err := fixture.store.ListColumns(ctx, fixture.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	backlog, ready := columns[0], columns[1]
	newAt := func(title string, column Column, position float64) Task {
		t.Helper()
		task, createErr := fixture.store.CreateTask(ctx, fixture.project.ID, TaskInput{Title: taskMoveStringPtr(title), ColumnID: &column.ID, Position: &position}, fixture.owner.ID)
		if createErr != nil {
			t.Fatal(createErr)
		}
		return task
	}
	before := newAt("visible before", ready, 0)
	hidden := newAt("filtered card", ready, 1)
	after := newAt("visible after", ready, 2)
	moving := newAt("move into filtered gap", backlog, 0)
	ready, err = fixture.store.GetColumn(ctx, ready.ID)
	if err != nil {
		t.Fatal(err)
	}
	backlog, err = fixture.store.GetColumn(ctx, backlog.ID)
	if err != nil {
		t.Fatal(err)
	}
	moved, err := fixture.store.ReorderTask(ctx, moving.ID, TaskReorderInput{
		DestinationColumnID: ready.ID, ExpectedSourceColumnID: backlog.ID,
		BeforeTaskID: after.ID, AfterTaskID: before.ID, Placement: "between", Source: "filtered-board",
		ExpectedSourceOrderingVersion: backlog.OrderingVersion, ExpectedDestinationOrderingVersion: ready.OrderingVersion,
	}, moving.Version, fixture.owner.ID)
	if err != nil {
		t.Fatalf("filtered reorder: %v", err)
	}
	ordered, _, err := fixture.store.ListTasks(ctx, fixture.project.ID, TaskFilter{Column: ready.ID, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if got := taskIDsInColumn(ordered, ready.ID); len(got) != 4 || got[0] != before.ID || got[1] != moved.ID || got[2] != hidden.ID || got[3] != after.ID {
		t.Fatalf("filtered order = %v, want visible-before/moved/hidden/visible-after", got)
	}
	if moved.Position != 0.5 || hidden.Position != 1 {
		t.Fatalf("filtered positions moved=%v hidden=%v, want 0.5/1", moved.Position, hidden.Position)
	}
}

func TestReorderTaskRebalancesDensePositionsAndKeepsStableOrder(t *testing.T) {
	fixture := newTaskClaimLockFixture(t, "ORDERDENSE")
	ctx := fixture.ctx
	columns, err := fixture.store.ListColumns(ctx, fixture.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	ready := columns[1]
	positions := []float64{0, math.SmallestNonzeroFloat64, 2 * math.SmallestNonzeroFloat64}
	tasks := make([]Task, len(positions))
	for index, position := range positions {
		task, createErr := fixture.store.CreateTask(ctx, fixture.project.ID, TaskInput{Title: taskMoveStringPtr("dense"), ColumnID: &ready.ID, Position: &position}, fixture.owner.ID)
		if createErr != nil {
			t.Fatal(createErr)
		}
		tasks[index] = task
	}
	ready, err = fixture.store.GetColumn(ctx, ready.ID)
	if err != nil {
		t.Fatal(err)
	}
	moved, err := fixture.store.ReorderTask(ctx, tasks[2].ID, TaskReorderInput{
		DestinationColumnID: ready.ID, ExpectedSourceColumnID: ready.ID,
		BeforeTaskID: tasks[1].ID, AfterTaskID: tasks[0].ID, Source: "board",
		ExpectedOrderingVersion: ready.OrderingVersion,
	}, tasks[2].Version, fixture.owner.ID)
	if err != nil {
		t.Fatalf("dense reorder: %v", err)
	}
	rows, _, err := fixture.store.ListTasks(ctx, fixture.project.ID, TaskFilter{Column: ready.ID, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index < len(rows); index++ {
		if rows[index-1].Position >= rows[index].Position {
			t.Fatalf("dense order has non-increasing positions at %d: %#v", index, rows)
		}
	}
	if len(rows) != 3 || rows[1].ID != moved.ID {
		t.Fatalf("dense reorder order = %v, want moved card in middle", taskIDsInColumn(rows, ready.ID))
	}
	if rows[0].Position >= moved.Position || moved.Position >= rows[2].Position {
		t.Fatalf("dense reorder position = %.18g, want between %.18g and %.18g", moved.Position, rows[0].Position, rows[2].Position)
	}
}

func TestReorderTaskStressPreservesEveryCardAndRecoversConcurrentConflict(t *testing.T) {
	fixture := newTaskClaimLockFixture(t, "ORDERSTRESS")
	ctx := fixture.ctx
	columns, err := fixture.store.ListColumns(ctx, fixture.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	backlog := columns[0]
	created := make([]Task, 0, 32)
	for index := 0; index < 32; index++ {
		task, createErr := fixture.store.CreateTask(ctx, fixture.project.ID, TaskInput{Title: taskMoveStringPtr("stress"), ColumnID: &backlog.ID}, fixture.owner.ID)
		if createErr != nil {
			t.Fatalf("create stress task %d: %v", index, createErr)
		}
		created = append(created, task)
	}
	for iteration := 0; iteration < 120; iteration++ {
		candidate := created[iteration%len(created)]
		candidate, err = fixture.store.GetTask(ctx, candidate.ID)
		if err != nil {
			t.Fatal(err)
		}
		backlog, err = fixture.store.GetColumn(ctx, backlog.ID)
		if err != nil {
			t.Fatal(err)
		}
		placement := "first"
		if iteration%2 == 1 {
			placement = "last"
		}
		if _, err := fixture.store.ReorderTask(ctx, candidate.ID, TaskReorderInput{
			DestinationColumnID: backlog.ID, ExpectedSourceColumnID: backlog.ID,
			Placement: placement, Source: "stress", ExpectedOrderingVersion: backlog.OrderingVersion,
		}, candidate.Version, fixture.owner.ID); err != nil {
			t.Fatalf("stress reorder %d: %v", iteration, err)
		}
	}
	rows, _, err := fixture.store.ListTasks(ctx, fixture.project.ID, TaskFilter{Column: backlog.ID, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(created) {
		t.Fatalf("stress row count = %d, want %d", len(rows), len(created))
	}
	seen := make(map[string]bool, len(rows))
	for index, row := range rows {
		if seen[row.ID] {
			t.Fatalf("stress order repeated task %s at index %d", row.ID, index)
		}
		seen[row.ID] = true
		if index > 0 && rows[index-1].Position >= row.Position {
			t.Fatalf("stress positions not increasing at %d: %.18g >= %.18g", index, rows[index-1].Position, row.Position)
		}
	}

	first, second := rows[0], rows[1]
	backlog, err = fixture.store.GetColumn(ctx, backlog.ID)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, candidate := range []Task{first, second} {
		candidate := candidate
		go func() {
			<-start
			_, reorderErr := fixture.store.ReorderTask(ctx, candidate.ID, TaskReorderInput{
				DestinationColumnID: backlog.ID, ExpectedSourceColumnID: backlog.ID,
				Placement: "last", Source: "concurrent", ExpectedOrderingVersion: backlog.OrderingVersion,
			}, candidate.Version, fixture.owner.ID)
			results <- reorderErr
		}()
	}
	close(start)
	firstErr, secondErr := <-results, <-results
	if (errors.Is(firstErr, ErrConflict) && secondErr == nil) || (errors.Is(secondErr, ErrConflict) && firstErr == nil) {
		return
	}
	t.Fatalf("concurrent reorder results = %v, %v; want one success and one conflict", firstErr, secondErr)
}

func taskIDsInColumn(tasks []Task, columnID string) []string {
	ids := make([]string, 0)
	for _, task := range tasks {
		if task.ColumnID == columnID {
			ids = append(ids, task.ID)
		}
	}
	return ids
}
