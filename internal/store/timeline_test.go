package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/KanterLabs/helm/internal/db"
)

func TestTaskTimelinePublishesStructuredProgressAndDeduplicatesGeneratedComment(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, _, task := createAgentWorkFixture(t, data, ctx, "TIMELINE")
	if _, err := data.CreateComment(ctx, task.ID, actor.ID, "Human-readable context"); err != nil {
		t.Fatalf("create ordinary comment: %v", err)
	}
	claimed, err := data.ClaimTask(ctx, task.ID, actor.ID, time.Hour, task.Version)
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	completed, total := 1, 2
	_, err = data.PublishAgentWork(ctx, task.ID, AgentWorkInput{
		OperationID:         "timeline/run",
		State:               "working",
		Phase:               "Implementing timeline",
		Summary:             "The durable timeline is ready.",
		NextAction:          "Run timeline tests.",
		CheckpointRefs:      []string{"store", "http"},
		CheckpointCompleted: &completed,
		CheckpointTotal:     &total,
	}, claimed.Version, actor.ID)
	if err != nil {
		t.Fatalf("publish progress: %v", err)
	}
	if _, err := data.CreateComment(ctx, task.ID, actor.ID, "A later ordinary comment"); err != nil {
		t.Fatalf("create later comment: %v", err)
	}

	items, more, err := data.ListTaskTimeline(ctx, task.ID, TaskTimelineFilter{Limit: 50})
	if err != nil {
		t.Fatalf("list timeline: %v", err)
	}
	if more {
		t.Fatal("timeline unexpectedly has another page")
	}
	progressCount, commentCount, changeCount := 0, 0, 0
	seen := make(map[string]bool)
	for _, item := range items {
		if seen[item.ID] {
			t.Fatalf("timeline duplicated item %q", item.ID)
		}
		seen[item.ID] = true
		switch item.Kind {
		case "agent_progress":
			progressCount++
			if item.Progress == nil || item.Progress.OperationID != "timeline/run" || item.Progress.Summary != "The durable timeline is ready." {
				t.Fatalf("progress item = %+v", item)
			}
			if item.Actor == nil || item.Actor.ID != actor.ID || item.Actor.Name != actor.Name {
				t.Fatalf("progress actor = %+v", item.Actor)
			}
		case "comment":
			commentCount++
			if item.Comment == nil || item.Comment.Body == "The durable timeline is ready.\n\nNext: Run timeline tests." {
				t.Fatalf("generated progress comment leaked as comment item: %+v", item)
			}
		case "task_change":
			changeCount++
			if item.Change == nil || item.Change.EventType == "comment.created" || item.Change.EventType == "task.progressed" {
				t.Fatalf("un-de-duplicated task change = %+v", item)
			}
		default:
			t.Fatalf("unknown timeline kind %q", item.Kind)
		}
		if item.Cursor == "" {
			t.Fatalf("timeline item %q has empty cursor", item.ID)
		}
		if item.Progress == nil && item.Comment == nil && item.Change == nil {
			t.Fatalf("timeline item %q has no typed payload", item.ID)
		}
	}
	if progressCount != 1 || commentCount != 2 || changeCount < 2 {
		t.Fatalf("timeline kinds = progress %d comments %d changes %d, want 1/2/at least 2", progressCount, commentCount, changeCount)
	}

	var historyCount int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(1) FROM task_agent_work_history WHERE task_id=?`, task.ID).Scan(&historyCount); err != nil {
		t.Fatalf("count history rows: %v", err)
	}
	if historyCount != 1 {
		t.Fatalf("history rows = %d, want 1", historyCount)
	}
	var refs string
	if err := database.QueryRowContext(ctx, `SELECT checkpoint_refs FROM task_agent_work_history WHERE task_id=?`, task.ID).Scan(&refs); err != nil {
		t.Fatalf("read history refs: %v", err)
	}
	var decodedRefs []string
	if err := json.Unmarshal([]byte(refs), &decodedRefs); err != nil || len(decodedRefs) != 2 {
		t.Fatalf("history refs = %q/%v", refs, err)
	}

	// The item cursor is a keyset boundary, not an offset. Walking one item at
	// a time must return every row exactly once in the same order.
	var paged []string
	before := ""
	for page := 0; page < len(items)+1; page++ {
		pageItems, hasMore, err := data.ListTaskTimeline(ctx, task.ID, TaskTimelineFilter{Before: before, Limit: 1})
		if err != nil {
			t.Fatalf("list timeline page %d: %v", page, err)
		}
		if len(pageItems) == 0 {
			if hasMore {
				t.Fatal("empty timeline page reported more rows")
			}
			break
		}
		paged = append(paged, pageItems[0].ID)
		if !hasMore {
			break
		}
		before = pageItems[0].Cursor
	}
	if len(paged) != len(items) {
		t.Fatalf("paged timeline rows = %d, want %d (%v)", len(paged), len(items), paged)
	}
	for index := range items {
		if paged[index] != items[index].ID {
			t.Fatalf("paged timeline[%d] = %q, want %q", index, paged[index], items[index].ID)
		}
	}

	progressOnly, more, err := data.ListTaskTimeline(ctx, task.ID, TaskTimelineFilter{Kind: "agent_progress", Limit: 10})
	if err != nil || more || len(progressOnly) != 1 {
		t.Fatalf("progress filter = %#v, more=%v, err=%v", progressOnly, more, err)
	}
	if _, _, err := data.ListTaskTimeline(ctx, task.ID, TaskTimelineFilter{Kind: "unknown", Limit: 10}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid kind error = %v, want ErrInvalid", err)
	}
}

func TestTaskTimelineLeavesLegacyProgressEventsAsGenericChanges(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, project, task := createAgentWorkFixture(t, data, ctx, "LEGACYTIMELINE")
	if _, err := database.ExecContext(ctx, `INSERT INTO events(id, type, actor_id, project_id, task_id, payload, created_at) VALUES (?, 'task.progressed', ?, ?, ?, ?, ?)`, "legacy-progress", actor.ID, project.ID, task.ID, `{"state":"working","summary":"legacy"}`, "2026-01-01T00:00:10Z"); err != nil {
		t.Fatalf("seed legacy progress event: %v", err)
	}
	items, _, err := data.ListTaskTimeline(ctx, task.ID, TaskTimelineFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list legacy timeline: %v", err)
	}
	found := false
	for _, item := range items {
		if item.ID == "legacy-progress" {
			found = true
			if item.Kind != "task_change" || item.Change == nil || item.Change.EventType != "task.progressed" || item.Progress != nil {
				t.Fatalf("legacy progress item = %+v", item)
			}
		}
	}
	if !found {
		t.Fatal("legacy progress event was omitted")
	}
}

func TestProjectTimelineMergesNonDeletedTaskActivity(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, project, first := createAgentWorkFixture(t, data, ctx, "BOARDTIMELINE")
	second, err := data.CreateTask(ctx, project.ID, TaskInput{Title: stringPtrForTest("Second")}, actor.ID)
	if err != nil {
		t.Fatalf("create second task: %v", err)
	}
	deleted, err := data.CreateTask(ctx, project.ID, TaskInput{Title: stringPtrForTest("Deleted")}, actor.ID)
	if err != nil {
		t.Fatalf("create deleted task: %v", err)
	}

	firstClaim, err := data.ClaimTask(ctx, first.ID, actor.ID, time.Hour, first.Version)
	if err != nil {
		t.Fatalf("claim first task: %v", err)
	}
	if _, err := data.PublishAgentWork(ctx, first.ID, AgentWorkInput{
		OperationID: "board/first",
		State:       "working",
		Summary:     "First task progress",
		NextAction:  "Continue first task",
	}, firstClaim.Version, actor.ID); err != nil {
		t.Fatalf("publish first progress: %v", err)
	}
	secondClaim, err := data.ClaimTask(ctx, second.ID, actor.ID, time.Hour, second.Version)
	if err != nil {
		t.Fatalf("claim second task: %v", err)
	}
	if _, err := data.PublishAgentWork(ctx, second.ID, AgentWorkInput{
		OperationID: "board/second",
		State:       "verifying",
		Summary:     "Second task progress",
	}, secondClaim.Version, actor.ID); err != nil {
		t.Fatalf("publish second progress: %v", err)
	}
	if _, err := data.CreateComment(ctx, second.ID, actor.ID, "Shared board context"); err != nil {
		t.Fatalf("create ordinary comment: %v", err)
	}
	if err := data.DeleteTask(ctx, deleted.ID, deleted.Version, actor.ID); err != nil {
		t.Fatalf("delete task: %v", err)
	}

	items, more, err := data.ListProjectTimeline(ctx, project.ID, TaskTimelineFilter{Limit: 50})
	if err != nil {
		t.Fatalf("list project timeline: %v", err)
	}
	if more {
		t.Fatal("project timeline unexpectedly has another page")
	}
	seenTasks := make(map[string]bool)
	progressCount, commentCount := 0, 0
	for _, item := range items {
		if item.TaskID == deleted.ID {
			t.Fatalf("deleted task leaked into project timeline: %+v", item)
		}
		seenTasks[item.TaskID] = true
		if item.Kind == "agent_progress" {
			progressCount++
			if item.Progress == nil || item.Actor == nil || item.Actor.ID != actor.ID {
				t.Fatalf("project progress item = %+v", item)
			}
		}
		if item.Kind == "comment" {
			commentCount++
			if item.Comment == nil || item.Comment.Body != "Shared board context" {
				t.Fatalf("project comment item = %+v", item)
			}
		}
		if item.Kind == "task_change" && item.Change != nil && (item.Change.EventType == "comment.created" || item.Change.EventType == "task.progressed") {
			t.Fatalf("duplicate project change item = %+v", item)
		}
	}
	if !seenTasks[first.ID] || !seenTasks[second.ID] || seenTasks[deleted.ID] {
		t.Fatalf("project task IDs = %v, want only active tasks", seenTasks)
	}
	if progressCount != 2 || commentCount != 1 {
		t.Fatalf("project timeline kinds = progress %d comments %d, want 2/1", progressCount, commentCount)
	}

	// A project cursor walks the merged stream exactly once, even when rows
	// from different tasks share the same page boundary.
	var paged []string
	before := ""
	for page := 0; page < len(items)+1; page++ {
		pageItems, hasMore, err := data.ListProjectTimeline(ctx, project.ID, TaskTimelineFilter{Before: before, Limit: 1})
		if err != nil {
			t.Fatalf("list project timeline page %d: %v", page, err)
		}
		if len(pageItems) == 0 {
			if hasMore {
				t.Fatal("empty project timeline page reported more rows")
			}
			break
		}
		paged = append(paged, pageItems[0].ID)
		if !hasMore {
			break
		}
		before = pageItems[0].Cursor
	}
	if len(paged) != len(items) {
		t.Fatalf("paged project timeline rows = %d, want %d", len(paged), len(items))
	}
	for index := range items {
		if paged[index] != items[index].ID {
			t.Fatalf("paged project timeline[%d] = %q, want %q", index, paged[index], items[index].ID)
		}
	}

	if _, _, err := data.ListProjectTimeline(ctx, "missing-project", TaskTimelineFilter{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing project timeline error = %v, want ErrNotFound", err)
	}
}
