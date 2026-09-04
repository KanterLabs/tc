package store

import (
	"encoding/json"
	"errors"
	"testing"
)

func checklistEventCountForTest(t *testing.T, f checklistFixture) int {
	t.Helper()
	var count int
	if err := f.store.DB.QueryRowContext(f.ctx, `SELECT COUNT(1) FROM events WHERE project_id=?`, f.project.ID).Scan(&count); err != nil {
		t.Fatalf("count project events: %v", err)
	}
	return count
}

func checklistIncompleteDetailsForTest(t *testing.T, err error, wantOpen int) {
	t.Helper()
	if !errors.Is(err, ErrChecklistIncomplete) {
		t.Fatalf("completion error = %v, want checklist incomplete", err)
	}
	var typed *Error
	if !errors.As(err, &typed) {
		t.Fatalf("completion error type = %T, want *store.Error", err)
	}
	details, ok := typed.Details.(map[string]any)
	if !ok {
		t.Fatalf("completion error details = %#v, want map", typed.Details)
	}
	if details["open_items"] != wantOpen {
		t.Fatalf("completion error details = %#v, want open_items=%d", details, wantOpen)
	}
}

func TestTaskChecklistCompletionPolicyCoversDirectTaskCompletion(t *testing.T) {
	t.Run("require rejects task patch atomically", func(t *testing.T) {
		f := newChecklistFixture(t, "CHECKPATCHREQ", "require")
		before, err := f.store.AddTaskChecklistItem(f.ctx, f.task.ID, ChecklistItemInput{Text: checklistStringPtr("ship it")}, f.task.Version, f.actor.ID)
		if err != nil {
			t.Fatalf("add checklist item: %v", err)
		}
		completedColumn, err := f.store.StateColumn(f.ctx, f.project.ID, "completed")
		if err != nil {
			t.Fatalf("completed column: %v", err)
		}
		beforeEvents := checklistEventCountForTest(t, f)
		_, err = f.store.UpdateTask(f.ctx, before.ID, TaskInput{ColumnID: &completedColumn.ID}, before.Version, f.actor.ID)
		checklistIncompleteDetailsForTest(t, err, 1)

		after, err := f.store.GetTask(f.ctx, before.ID)
		if err != nil {
			t.Fatalf("reload rejected task patch: %v", err)
		}
		if after.Version != before.Version || after.ColumnID != before.ColumnID || after.CompletedAt != nil || after.ChecklistSummary.Open != 1 {
			t.Fatalf("rejected task patch changed state: before=%+v after=%+v", before, after)
		}
		if got := checklistEventCountForTest(t, f); got != beforeEvents {
			t.Fatalf("rejected task patch emitted activity: %d -> %d", beforeEvents, got)
		}
	})

	t.Run("warn permits task patch and records warning", func(t *testing.T) {
		f := newChecklistFixture(t, "CHECKPATCHWARN")
		before, err := f.store.AddTaskChecklistItem(f.ctx, f.task.ID, ChecklistItemInput{Text: checklistStringPtr("ship it")}, f.task.Version, f.actor.ID)
		if err != nil {
			t.Fatalf("add checklist item: %v", err)
		}
		completedColumn, err := f.store.StateColumn(f.ctx, f.project.ID, "completed")
		if err != nil {
			t.Fatalf("completed column: %v", err)
		}
		moved, err := f.store.UpdateTask(f.ctx, before.ID, TaskInput{ColumnID: &completedColumn.ID}, before.Version, f.actor.ID)
		if err != nil {
			t.Fatalf("warn task patch: %v", err)
		}
		if moved.CompletedAt == nil || !moved.ChecklistSummary.Warning || moved.ChecklistSummary.Open != 1 {
			t.Fatalf("warn task patch summary = %+v completed_at=%v", moved.ChecklistSummary, moved.CompletedAt)
		}

		events, _, err := f.store.ListEvents(f.ctx, EventFilter{ProjectID: f.project.ID, Limit: 100})
		if err != nil {
			t.Fatalf("list task patch events: %v", err)
		}
		found := false
		for _, event := range events {
			if event.Type != "task.updated" || event.TaskID == nil || *event.TaskID != moved.ID {
				continue
			}
			var payload map[string]any
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatalf("decode task patch event: %v", err)
			}
			if payload["checklist_warning"] != true || payload["open_checklist_items"] != float64(1) || payload["checklist_completion_policy"] != "warn" {
				t.Fatalf("task patch warning payload = %#v", payload)
			}
			found = true
		}
		if !found {
			t.Fatal("task patch emitted no warning activity")
		}
	})
}

func TestTaskChecklistCompletionPolicyCoversBugResolution(t *testing.T) {
	f := newChecklistFixture(t, "CHECKBUGREQ", "require")
	title, actual, kind := "Checklist bug", "The bug remains open", bugKind
	bug, err := f.store.CreateTask(f.ctx, f.project.ID, TaskInput{
		Title: &title,
		Kind:  &kind,
		Bug:   &BugInput{ActualBehavior: &actual},
	}, f.actor.ID)
	if err != nil {
		t.Fatalf("create bug: %v", err)
	}
	before, err := f.store.AddTaskChecklistItem(f.ctx, bug.ID, ChecklistItemInput{Text: checklistStringPtr("verify fix")}, bug.Version, f.actor.ID)
	if err != nil {
		t.Fatalf("add bug checklist item: %v", err)
	}
	beforeEvents := checklistEventCountForTest(t, f)
	_, err = f.store.ResolveBug(f.ctx, before.ID, ResolveBugInput{Resolution: "fixed"}, before.Version, f.actor.ID)
	checklistIncompleteDetailsForTest(t, err, 1)

	after, err := f.store.GetTask(f.ctx, before.ID)
	if err != nil {
		t.Fatalf("reload rejected bug resolution: %v", err)
	}
	if after.Version != before.Version || after.CompletedAt != nil || after.ColumnID != before.ColumnID || after.Bug == nil || after.Bug.Resolution != nil || after.ChecklistSummary.Open != 1 {
		t.Fatalf("rejected bug resolution changed state: before=%+v after=%+v", before, after)
	}
	if got := checklistEventCountForTest(t, f); got != beforeEvents {
		t.Fatalf("rejected bug resolution emitted activity: %d -> %d", beforeEvents, got)
	}
}

func TestTaskChecklistCompletionPolicyGuardsBulkColumnTransitionAtomically(t *testing.T) {
	t.Run("require rejects every task before any write", func(t *testing.T) {
		f := newChecklistFixture(t, "CHECKBULKREQ", "require")
		backlog, err := f.store.StateColumn(f.ctx, f.project.ID, "backlog")
		if err != nil {
			t.Fatalf("backlog column: %v", err)
		}
		first, err := f.store.AddTaskChecklistItem(f.ctx, f.task.ID, ChecklistItemInput{Text: checklistStringPtr("first task")}, f.task.Version, f.actor.ID)
		if err != nil {
			t.Fatalf("add first checklist item: %v", err)
		}
		secondTitle := "Second checklist task"
		second, err := f.store.CreateTask(f.ctx, f.project.ID, TaskInput{Title: &secondTitle, ColumnID: &backlog.ID}, f.actor.ID)
		if err != nil {
			t.Fatalf("create second task: %v", err)
		}
		second, err = f.store.AddTaskChecklistItem(f.ctx, second.ID, ChecklistItemInput{Text: checklistStringPtr("second task")}, second.Version, f.actor.ID)
		if err != nil {
			t.Fatalf("add second checklist item: %v", err)
		}
		beforeColumn, err := f.store.GetColumn(f.ctx, backlog.ID)
		if err != nil {
			t.Fatalf("reload backlog column: %v", err)
		}
		beforeEvents := checklistEventCountForTest(t, f)
		completedState := "completed"
		_, err = f.store.UpdateColumn(f.ctx, backlog.ID, ColumnInput{SemanticState: &completedState}, f.actor.ID)
		checklistIncompleteDetailsForTest(t, err, 2)

		afterColumn, err := f.store.GetColumn(f.ctx, backlog.ID)
		if err != nil {
			t.Fatalf("reload rejected column: %v", err)
		}
		if afterColumn.SemanticState != beforeColumn.SemanticState || afterColumn.UpdatedAt != beforeColumn.UpdatedAt {
			t.Fatalf("rejected bulk transition changed column: before=%+v after=%+v", beforeColumn, afterColumn)
		}
		for _, beforeTask := range []Task{first, second} {
			afterTask, getErr := f.store.GetTask(f.ctx, beforeTask.ID)
			if getErr != nil {
				t.Fatalf("reload rejected bulk task %s: %v", beforeTask.ID, getErr)
			}
			if afterTask.Version != beforeTask.Version || afterTask.ColumnID != beforeTask.ColumnID || afterTask.CompletedAt != nil || afterTask.ChecklistSummary.Open != 1 {
				t.Fatalf("rejected bulk transition changed task: before=%+v after=%+v", beforeTask, afterTask)
			}
		}
		if got := checklistEventCountForTest(t, f); got != beforeEvents {
			t.Fatalf("rejected bulk transition emitted activity: %d -> %d", beforeEvents, got)
		}
	})

	t.Run("warn completes every task and records aggregate warning", func(t *testing.T) {
		f := newChecklistFixture(t, "CHECKBULKWARN")
		backlog, err := f.store.StateColumn(f.ctx, f.project.ID, "backlog")
		if err != nil {
			t.Fatalf("backlog column: %v", err)
		}
		first, err := f.store.AddTaskChecklistItem(f.ctx, f.task.ID, ChecklistItemInput{Text: checklistStringPtr("first task")}, f.task.Version, f.actor.ID)
		if err != nil {
			t.Fatalf("add first checklist item: %v", err)
		}
		secondTitle := "Second checklist task"
		second, err := f.store.CreateTask(f.ctx, f.project.ID, TaskInput{Title: &secondTitle, ColumnID: &backlog.ID}, f.actor.ID)
		if err != nil {
			t.Fatalf("create second task: %v", err)
		}
		second, err = f.store.AddTaskChecklistItem(f.ctx, second.ID, ChecklistItemInput{Text: checklistStringPtr("second task")}, second.Version, f.actor.ID)
		if err != nil {
			t.Fatalf("add second checklist item: %v", err)
		}
		completedState := "completed"
		if _, err := f.store.UpdateColumn(f.ctx, backlog.ID, ColumnInput{SemanticState: &completedState}, f.actor.ID); err != nil {
			t.Fatalf("warn bulk transition: %v", err)
		}
		for _, beforeTask := range []Task{first, second} {
			afterTask, getErr := f.store.GetTask(f.ctx, beforeTask.ID)
			if getErr != nil {
				t.Fatalf("reload completed bulk task %s: %v", beforeTask.ID, getErr)
			}
			if afterTask.Version != beforeTask.Version+1 || afterTask.CompletedAt == nil || !afterTask.ChecklistSummary.Warning || afterTask.ChecklistSummary.Open != 1 {
				t.Fatalf("warn bulk task = %+v", afterTask)
			}
		}

		events, _, err := f.store.ListEvents(f.ctx, EventFilter{ProjectID: f.project.ID, Limit: 100})
		if err != nil {
			t.Fatalf("list warn bulk events: %v", err)
		}
		found := false
		for _, event := range events {
			if event.Type != "column.updated" {
				continue
			}
			var payload map[string]any
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatalf("decode warn bulk event: %v", err)
			}
			if payload["checklist_warning"] != true || payload["open_checklist_items"] != float64(2) || payload["checklist_completion_policy"] != "warn" {
				t.Fatalf("warn bulk event payload = %#v", payload)
			}
			found = true
		}
		if !found {
			t.Fatal("warn bulk transition emitted no warning activity")
		}
	})
}
