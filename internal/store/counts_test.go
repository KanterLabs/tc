package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/KanterLabs/helm/internal/db"
)

func TestCountReopenedIssuesIsBoundedScopedAndDistinct(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	data := New(database)
	actor, err := data.CreateActor(ctx, Actor{Kind: "human", Name: "Issue reader"}, "")
	if err != nil {
		t.Fatalf("create actor: %v", err)
	}
	visible, err := data.CreateProject(ctx, ProjectInput{Key: stringPtrForTest("COUNTONE"), Name: stringPtrForTest("Visible")}, actor.ID)
	if err != nil {
		t.Fatalf("create visible project: %v", err)
	}
	hidden, err := data.CreateProject(ctx, ProjectInput{Key: stringPtrForTest("COUNTTWO"), Name: stringPtrForTest("Hidden")}, actor.ID)
	if err != nil {
		t.Fatalf("create hidden project: %v", err)
	}

	const visibleRecent = 101
	asOf := time.Now().UTC().Truncate(time.Microsecond)
	since := asOf.Add(-7 * 24 * time.Hour)
	actual := "The issue remains reproducible."
	for i := 0; i < visibleRecent; i++ {
		title := fmt.Sprintf("Visible reopened issue %03d", i)
		bug, createErr := data.CreateTask(ctx, visible.ID, TaskInput{
			Title: stringPtrForTest(title),
			Kind:  stringPtrForTest("bug"),
			Bug:   &BugInput{ActualBehavior: &actual},
		}, actor.ID)
		if createErr != nil {
			t.Fatalf("create visible bug %d: %v", i, createErr)
		}
		createdAt := asOf.Add(-time.Duration(i+1) * time.Minute).Format(time.RFC3339Nano)
		if _, err := database.ExecContext(ctx, `INSERT INTO events(id, type, actor_id, project_id, task_id, payload, created_at) VALUES (?, 'bug.reopened', ?, ?, ?, '{}', ?)`, fmt.Sprintf("visible-reopened-%03d", i), actor.ID, visible.ID, bug.ID, createdAt); err != nil {
			t.Fatalf("insert visible reopen event %d: %v", i, err)
		}
	}

	oldTitle := "Old reopened issue"
	oldBug, err := data.CreateTask(ctx, visible.ID, TaskInput{Title: stringPtrForTest(oldTitle), Kind: stringPtrForTest("bug"), Bug: &BugInput{ActualBehavior: &actual}}, actor.ID)
	if err != nil {
		t.Fatalf("create old bug: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO events(id, type, actor_id, project_id, task_id, payload, created_at) VALUES ('old-reopened', 'bug.reopened', ?, ?, ?, '{}', ?)`, actor.ID, visible.ID, oldBug.ID, asOf.Add(-8*24*time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert old reopen event: %v", err)
	}

	hiddenTitle := "Hidden reopened issue"
	hiddenBug, err := data.CreateTask(ctx, hidden.ID, TaskInput{Title: stringPtrForTest(hiddenTitle), Kind: stringPtrForTest("bug"), Bug: &BugInput{ActualBehavior: &actual}}, actor.ID)
	if err != nil {
		t.Fatalf("create hidden bug: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO events(id, type, actor_id, project_id, task_id, payload, created_at) VALUES ('hidden-reopened', 'bug.reopened', ?, ?, ?, '{}', ?)`, actor.ID, hidden.ID, hiddenBug.ID, asOf.Add(-time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert hidden reopen event: %v", err)
	}

	got, err := data.CountReopenedIssues(ctx, []string{visible.ID}, since, asOf)
	if err != nil {
		t.Fatalf("count visible reopened issues: %v", err)
	}
	if got != visibleRecent {
		t.Fatalf("visible reopened count = %d, want %d despite >100 event rows", got, visibleRecent)
	}
	got, err = data.CountReopenedIssues(ctx, nil, since, asOf)
	if err != nil {
		t.Fatalf("count global reopened issues: %v", err)
	}
	if got != visibleRecent+1 {
		t.Fatalf("global reopened count = %d, want %d", got, visibleRecent+1)
	}
	got, err = data.CountReopenedIssues(ctx, []string{}, since, asOf)
	if err != nil {
		t.Fatalf("count empty reopened ceiling: %v", err)
	}
	if got != 0 {
		t.Fatalf("empty reopened ceiling count = %d, want 0", got)
	}
}

func TestCountMyWorkMatchesLiveAndAssignedViews(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	data := New(database)
	actor, err := data.CreateActor(ctx, Actor{Kind: "agent", Name: "Work agent"}, "")
	if err != nil {
		t.Fatalf("create actor: %v", err)
	}
	project, err := data.CreateProject(ctx, ProjectInput{Key: stringPtrForTest("WORKCOUNT"), Name: stringPtrForTest("Work count")}, actor.ID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	create := func(title string) Task {
		t.Helper()
		task, createErr := data.CreateTask(ctx, project.ID, TaskInput{Title: stringPtrForTest(title)}, actor.ID)
		if createErr != nil {
			t.Fatalf("create task %q: %v", title, createErr)
		}
		return task
	}
	liveTask := create("Live pulse")
	assignedTask := create("Assigned task")
	claimed, err := data.ClaimTask(ctx, liveTask.ID, actor.ID, 0, liveTask.Version)
	if err != nil {
		t.Fatalf("claim live task: %v", err)
	}
	if _, err := data.PublishAgentWork(ctx, liveTask.ID, AgentWorkInput{OperationID: "counts/live", State: "working", Summary: "Live count fixture"}, claimed.Version, actor.ID); err != nil {
		t.Fatalf("publish live task: %v", err)
	}
	if _, err := data.UpdateTask(ctx, assignedTask.ID, TaskInput{Assignee: &actor.ID, AssigneeSet: true}, assignedTask.Version, actor.ID); err != nil {
		t.Fatalf("assign task: %v", err)
	}
	live, err := data.CountMyWork(ctx, actor.ID, []string{project.ID}, true)
	if err != nil {
		t.Fatalf("count live work: %v", err)
	}
	if live != 1 {
		t.Fatalf("live work count = %d, want 1", live)
	}
	assigned, err := data.CountMyWork(ctx, actor.ID, []string{project.ID}, false)
	if err != nil {
		t.Fatalf("count assigned work: %v", err)
	}
	if assigned != 2 {
		t.Fatalf("assigned work count = %d, want 2", assigned)
	}
	if empty, err := data.CountMyWork(ctx, actor.ID, []string{}, true); err != nil {
		t.Fatalf("count empty work ceiling: %v", err)
	} else if empty != 0 {
		t.Fatalf("empty work ceiling count = %d, want 0", empty)
	}
}
