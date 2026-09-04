package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KanterLabs/helm/internal/db"
)

type checklistFixture struct {
	ctx     context.Context
	store   *Store
	actor   Actor
	project Project
	task    Task
}

func newChecklistFixture(t *testing.T, key string, policy ...string) checklistFixture {
	t.Helper()
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "checklists.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	data := New(database)
	actor, err := data.CreateActor(ctx, Actor{Kind: "human", Name: "Checklist tester"}, "")
	if err != nil {
		t.Fatalf("create actor: %v", err)
	}
	projectInput := ProjectInput{Key: checklistStringPtr(key), Name: checklistStringPtr("Checklist project")}
	if len(policy) > 0 {
		projectInput.ChecklistCompletionPolicy = checklistStringPtr(policy[0])
	}
	project, err := data.CreateProject(ctx, projectInput, actor.ID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	task, err := data.CreateTask(ctx, project.ID, TaskInput{Title: checklistStringPtr("Checklist task")}, actor.ID)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	return checklistFixture{ctx: ctx, store: data, actor: actor, project: project, task: task}
}

func checklistStringPtr(value string) *string { return &value }

func TestTaskChecklistRoundTripVersionOrderAndActivity(t *testing.T) {
	f := newChecklistFixture(t, "CHECKLIST")
	if len(f.task.Checklist) != 0 || f.task.ChecklistSummary.Total != 0 || f.task.ChecklistSummary.CompletionPolicy != "warn" {
		t.Fatalf("initial checklist = %#v summary=%#v", f.task.Checklist, f.task.ChecklistSummary)
	}

	first, err := f.store.AddTaskChecklistItem(f.ctx, f.task.ID, ChecklistItemInput{Text: checklistStringPtr("Build the API")}, f.task.Version, f.actor.ID)
	if err != nil {
		t.Fatalf("add first item: %v", err)
	}
	if first.Version != 2 || len(first.Checklist) != 1 || first.Checklist[0].Position != 0 || first.ChecklistSummary.Open != 1 {
		t.Fatalf("first item task = version %d checklist=%#v summary=%#v", first.Version, first.Checklist, first.ChecklistSummary)
	}
	second, err := f.store.AddTaskChecklistItem(f.ctx, f.task.ID, ChecklistItemInput{Text: checklistStringPtr("Verify keyboard access"), Position: checklistIntPtr(0)}, first.Version, f.actor.ID)
	if err != nil {
		t.Fatalf("add second item: %v", err)
	}
	if second.Version != 3 || len(second.Checklist) != 2 || second.Checklist[0].Text != "Verify keyboard access" || second.Checklist[1].Text != "Build the API" {
		t.Fatalf("ordered checklist = version %d items=%#v", second.Version, second.Checklist)
	}
	itemID := second.Checklist[0].ID
	completed, err := f.store.UpdateTaskChecklistItem(f.ctx, f.task.ID, itemID, ChecklistItemInput{Completed: checklistBoolPtr(true)}, second.Version, f.actor.ID)
	if err != nil {
		t.Fatalf("complete item: %v", err)
	}
	if completed.Version != 4 || !completed.Checklist[0].Completed || completed.Checklist[0].CompletedAt == nil || completed.Checklist[0].CompletedBy == nil || *completed.Checklist[0].CompletedBy != f.actor.ID {
		t.Fatalf("completed item = version %d item=%#v", completed.Version, completed.Checklist[0])
	}

	reordered, err := f.store.ReorderTaskChecklist(f.ctx, f.task.ID, ChecklistReorderInput{ItemIDs: []string{completed.Checklist[1].ID, completed.Checklist[0].ID}}, completed.Version, f.actor.ID)
	if err != nil {
		t.Fatalf("reorder checklist: %v", err)
	}
	if reordered.Version != 5 || reordered.Checklist[0].Text != "Build the API" || reordered.Checklist[0].Position != 0 || reordered.Checklist[1].Position != 1 {
		t.Fatalf("reordered checklist = version %d items=%#v", reordered.Version, reordered.Checklist)
	}

	removed, err := f.store.DeleteTaskChecklistItem(f.ctx, f.task.ID, itemID, reordered.Version, f.actor.ID)
	if err != nil {
		t.Fatalf("remove checklist item: %v", err)
	}
	if removed.Version != 6 || len(removed.Checklist) != 1 || removed.Checklist[0].Position != 0 || removed.ChecklistSummary.Total != 1 {
		t.Fatalf("removed checklist = version %d items=%#v summary=%#v", removed.Version, removed.Checklist, removed.ChecklistSummary)
	}

	events, _, err := f.store.ListEvents(f.ctx, EventFilter{ProjectID: f.project.ID, Limit: 100})
	if err != nil {
		t.Fatalf("list checklist events: %v", err)
	}
	wanted := map[string]bool{
		"task.checklist_item_added":   false,
		"task.checklist_item_updated": false,
		"task.checklist_reordered":    false,
		"task.checklist_item_removed": false,
	}
	for _, event := range events {
		if _, ok := wanted[event.Type]; !ok {
			continue
		}
		wanted[event.Type] = true
		if event.ActorID == nil || *event.ActorID != f.actor.ID || event.TaskID == nil || *event.TaskID != f.task.ID || event.CreatedAt == "" {
			t.Fatalf("checklist event attribution = %#v", event)
		}
	}
	for eventType, found := range wanted {
		if !found {
			t.Errorf("missing checklist activity event %q", eventType)
		}
	}
}

func TestTaskChecklistRejectsStaleVersionWithoutMutation(t *testing.T) {
	f := newChecklistFixture(t, "CHECKSTALE")
	updated, err := f.store.AddTaskChecklistItem(f.ctx, f.task.ID, ChecklistItemInput{Text: checklistStringPtr("first")}, f.task.Version, f.actor.ID)
	if err != nil {
		t.Fatalf("add item: %v", err)
	}
	if _, err := f.store.AddTaskChecklistItem(f.ctx, f.task.ID, ChecklistItemInput{Text: checklistStringPtr("stale")}, f.task.Version, f.actor.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale checklist mutation error = %v, want conflict", err)
	}
	current, err := f.store.GetTask(f.ctx, f.task.ID)
	if err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if current.Version != updated.Version || len(current.Checklist) != 1 || current.Checklist[0].Text != "first" {
		t.Fatalf("stale mutation changed task = version %d checklist=%#v", current.Version, current.Checklist)
	}
}

func TestTaskChecklistCompletionPolicyWarnAndRequire(t *testing.T) {
	warn := newChecklistFixture(t, "CHECKWARN")
	warnTask, err := warn.store.AddTaskChecklistItem(warn.ctx, warn.task.ID, ChecklistItemInput{Text: checklistStringPtr("ship it")}, warn.task.Version, warn.actor.ID)
	if err != nil {
		t.Fatalf("add warn item: %v", err)
	}
	completed, err := warn.store.CompleteTask(warn.ctx, warnTask.ID, warn.actor.ID, warnTask.Version)
	if err != nil {
		t.Fatalf("warn completion: %v", err)
	}
	if !completed.ChecklistSummary.Warning || completed.ChecklistSummary.Open != 1 || completed.CompletedAt == nil {
		t.Fatalf("warn completion summary = %#v completed_at=%v", completed.ChecklistSummary, completed.CompletedAt)
	}
	warnEvents, _, err := warn.store.ListEvents(warn.ctx, EventFilter{ProjectID: warn.project.ID, Limit: 100})
	if err != nil {
		t.Fatalf("list warn completion events: %v", err)
	}
	var warningEventPayload map[string]any
	for _, event := range warnEvents {
		if event.Type == "task.completed" && event.TaskID != nil && *event.TaskID == warn.task.ID {
			if err := json.Unmarshal(event.Payload, &warningEventPayload); err != nil {
				t.Fatalf("decode warn completion event: %v", err)
			}
			break
		}
	}
	if warningEventPayload["checklist_warning"] != true || warningEventPayload["open_checklist_items"] != float64(1) || warningEventPayload["checklist_completion_policy"] != "warn" {
		t.Fatalf("warn completion event payload = %#v", warningEventPayload)
	}

	require := newChecklistFixture(t, "CHECKREQ", "require")
	requireTask, err := require.store.AddTaskChecklistItem(require.ctx, require.task.ID, ChecklistItemInput{Text: checklistStringPtr("ship it")}, require.task.Version, require.actor.ID)
	if err != nil {
		t.Fatalf("add require item: %v", err)
	}
	if _, err := require.store.CompleteTask(require.ctx, requireTask.ID, require.actor.ID, requireTask.Version); !errors.Is(err, ErrChecklistIncomplete) {
		t.Fatalf("require completion error = %v, want checklist incomplete", err)
	}
	unchanged, err := require.store.GetTask(require.ctx, requireTask.ID)
	if err != nil {
		t.Fatalf("reload require task: %v", err)
	}
	if unchanged.Version != requireTask.Version || unchanged.CompletedAt != nil || unchanged.ChecklistSummary.Warning {
		t.Fatalf("rejected require completion changed task = %#v", unchanged)
	}
	completedItem, err := require.store.UpdateTaskChecklistItem(require.ctx, requireTask.ID, requireTask.Checklist[0].ID, ChecklistItemInput{Completed: checklistBoolPtr(true)}, requireTask.Version, require.actor.ID)
	if err != nil {
		t.Fatalf("complete require item: %v", err)
	}
	if _, err := require.store.CompleteTask(require.ctx, completedItem.ID, require.actor.ID, completedItem.Version); err != nil {
		t.Fatalf("require completion after checklist: %v", err)
	}
}

func TestTaskChecklistPayloadLimits(t *testing.T) {
	f := newChecklistFixture(t, "CHECKLIMIT")
	tooLong := make([]byte, MaxTaskChecklistItemText+1)
	for i := range tooLong {
		tooLong[i] = 'x'
	}
	if _, err := f.store.AddTaskChecklistItem(f.ctx, f.task.ID, ChecklistItemInput{Text: checklistStringPtr(string(tooLong))}, f.task.Version, f.actor.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized item error = %v, want invalid", err)
	}
	if _, err := f.store.AddTaskChecklistItem(f.ctx, f.task.ID, ChecklistItemInput{Text: checklistStringPtr("valid")}, f.task.Version, f.actor.ID); err != nil {
		t.Fatalf("valid item after rejected oversized item: %v", err)
	}

	// Confirm activity payloads remain valid JSON for event-feed consumers.
	projectEvents, _, err := f.store.ListEvents(f.ctx, EventFilter{ProjectID: f.project.ID, Limit: 100})
	if err != nil {
		t.Fatalf("list payload events: %v", err)
	}
	for _, event := range projectEvents {
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("event %s payload is invalid JSON: %v", event.Type, err)
		}
	}

	// Aggregate limits are byte-based, not rune-based. Twenty-five maximum
	// length emoji items occupy exactly 100,000 bytes; the next mutation must
	// be rejected even though SQLite's text length would report only 25,000.
	unicodeFixture := newChecklistFixture(t, "CHECKUNICODE")
	emojiText := strings.Repeat("🙂", MaxTaskChecklistItemText)
	created := now()
	for position := 0; position < MaxTaskChecklistTextBytes/(MaxTaskChecklistItemText*4); position++ {
		if _, err := unicodeFixture.store.DB.ExecContext(unicodeFixture.ctx, `INSERT INTO task_checklist_items(id, task_id, text, position, completed, created_at, updated_at) VALUES (?, ?, ?, ?, 0, ?, ?)`, newID(), unicodeFixture.task.ID, emojiText, position, created, created); err != nil {
			t.Fatalf("insert maximum unicode item %d: %v", position, err)
		}
	}
	if _, err := unicodeFixture.store.AddTaskChecklistItem(unicodeFixture.ctx, unicodeFixture.task.ID, ChecklistItemInput{Text: checklistStringPtr("🙂")}, unicodeFixture.task.Version, unicodeFixture.actor.ID); !errors.Is(err, ErrChecklistLimitExceeded) {
		t.Fatalf("unicode aggregate overflow error = %v, want checklist limit", err)
	}
}

func checklistIntPtr(value int) *int    { return &value }
func checklistBoolPtr(value bool) *bool { return &value }
