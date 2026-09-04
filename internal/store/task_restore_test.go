package store

import (
	"context"
	"errors"
	"testing"

	"github.com/KanterLabs/helm/internal/db"
)

func TestRestoreTaskReversesSoftDeleteWithVersionGuard(t *testing.T) {
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
	project, err := data.CreateProject(ctx, ProjectInput{Key: stringPtrForTest("RESTORE"), Name: stringPtrForTest("Restore")}, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	task, err := data.CreateTask(ctx, project.ID, TaskInput{Title: stringPtrForTest("Undo me")}, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := data.DeleteTask(ctx, task.ID, task.Version, actor.ID); err != nil {
		t.Fatalf("delete task: %v", err)
	}

	deleted, isDeleted, err := data.GetTaskForRestore(ctx, task.ID)
	if err != nil || !isDeleted || deleted.Version != task.Version+1 {
		t.Fatalf("deleted snapshot = task=%+v deleted=%v err=%v, want version %d", deleted, isDeleted, err, task.Version+1)
	}
	if _, err := data.GetTask(ctx, task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("normal get after delete = %v, want ErrNotFound", err)
	}

	restored, err := data.RestoreTask(ctx, task.ID, deleted.Version, actor.ID)
	if err != nil {
		t.Fatalf("restore task: %v", err)
	}
	if restored.ID != task.ID || restored.Title != task.Title || restored.ColumnID != task.ColumnID || restored.Position != task.Position || restored.Version != deleted.Version+1 {
		t.Fatalf("restored task = %+v, want original identity/placement and version %d", restored, deleted.Version+1)
	}
	if _, isDeleted, err := data.GetTaskForRestore(ctx, task.ID); err != nil || isDeleted {
		t.Fatalf("restored snapshot deleted=%v err=%v, want live", isDeleted, err)
	}

	if _, err := data.RestoreTask(ctx, task.ID, deleted.Version, actor.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale second restore = %v, want ErrConflict", err)
	}
	var eventType string
	if err := database.QueryRowContext(ctx, `SELECT type FROM events WHERE task_id=? ORDER BY cursor DESC LIMIT 1`, task.ID).Scan(&eventType); err != nil {
		t.Fatalf("read restore event: %v", err)
	}
	if eventType != "task.restored" {
		t.Fatalf("latest event = %q, want task.restored", eventType)
	}
}

func TestRestoreTaskRejectsStaleDeletedVersionWithoutMutation(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	data := New(database)
	actor, _ := data.EnsureDisabledActor(ctx)
	project, err := data.CreateProject(ctx, ProjectInput{Key: stringPtrForTest("STALE"), Name: stringPtrForTest("Stale restore")}, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	task, err := data.CreateTask(ctx, project.ID, TaskInput{Title: stringPtrForTest("Stale undo")}, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := data.DeleteTask(ctx, task.ID, task.Version, actor.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := data.RestoreTask(ctx, task.ID, task.Version, actor.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale restore = %v, want ErrConflict", err)
	}
	if _, isDeleted, err := data.GetTaskForRestore(ctx, task.ID); err != nil || !isDeleted {
		t.Fatalf("stale restore changed deleted state: deleted=%v err=%v", isDeleted, err)
	}
}
