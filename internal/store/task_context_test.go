package store

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/KanterLabs/helm/internal/db"
)

func TestTaskDraftContextIsBoundedRelevantAndProjectScoped(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	data := New(database)
	actor, err := data.EnsureDisabledActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, ProjectInput{Key: stringPtrForTest("CTX"), Name: stringPtrForTest("Context")}, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	other, err := data.CreateProject(ctx, ProjectInput{Key: stringPtrForTest("OTHER"), Name: stringPtrForTest("Other")}, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := data.StateColumn(ctx, project.ID, "completed")
	if err != nil {
		t.Fatal(err)
	}
	longDescription := strings.Repeat("x", taskContextDescriptionMax+100)
	matching, err := data.CreateTask(ctx, project.ID, TaskInput{
		Title: stringPtrForTest("Codex subscription connection"), Description: &longDescription,
		ColumnID: &completed.ID,
	}, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 28; i++ {
		title := fmt.Sprintf("routine task %02d", i)
		if _, err := data.CreateTask(ctx, project.ID, TaskInput{Title: &title}, actor.ID); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err := data.CreateTask(ctx, project.ID, TaskInput{Title: stringPtrForTest("Codex deleted secret")}, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := data.DeleteTask(ctx, deleted.ID, deleted.Version, actor.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := data.CreateTask(ctx, other.ID, TaskInput{Title: stringPtrForTest("Codex cross project")}, actor.ID); err != nil {
		t.Fatal(err)
	}

	pack, err := data.TaskDraftContext(ctx, project.ID, "codex subscription")
	if err != nil {
		t.Fatal(err)
	}
	if pack.Project.ID != project.ID || pack.CandidateCount != 29 || !pack.Truncated {
		t.Fatalf("metadata = %+v candidates=%d truncated=%v", pack.Project, pack.CandidateCount, pack.Truncated)
	}
	if len(pack.CompletedTasks) != 1 || pack.CompletedTasks[0].ID != matching.ID {
		t.Fatalf("completed = %+v", pack.CompletedTasks)
	}
	if len(pack.CompletedTasks[0].Description) != taskContextDescriptionMax {
		t.Fatalf("description length = %d", len(pack.CompletedTasks[0].Description))
	}
	if len(pack.OpenTasks) != TaskContextItemLimit/2 {
		t.Fatalf("open task count = %d", len(pack.OpenTasks))
	}
	for _, ref := range append(pack.CompletedTasks, pack.OpenTasks...) {
		if ref.ID == deleted.ID || strings.HasPrefix(ref.Key, "OTHER-") {
			t.Fatalf("context leaked excluded task: %+v", ref)
		}
	}
}

func TestTaskDraftContextEmptyProject(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	data := New(database)
	actor, _ := data.EnsureDisabledActor(ctx)
	project, err := data.CreateProject(ctx, ProjectInput{Key: stringPtrForTest("EMPTY"), Name: stringPtrForTest("Empty")}, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	pack, err := data.TaskDraftContext(ctx, project.ID, "first task")
	if err != nil {
		t.Fatal(err)
	}
	if pack.CandidateCount != 0 || pack.Truncated || pack.CompletedTasks == nil || pack.OpenTasks == nil {
		t.Fatalf("empty pack = %+v", pack)
	}
}
