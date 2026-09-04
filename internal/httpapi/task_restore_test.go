package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/KanterLabs/helm/internal/store"
)

func TestRestoreTaskRouteRequiresGuardsAndReplaysUndoSafely(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatalf("ensure disabled actor: %v", err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("UNDO"), Name: stringPtr("Undo")}, "actor-disabled-mode")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	task, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("Undo this task")}, "actor-disabled-mode")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	deleted := request(t, server, http.MethodDelete, "/api/v1/tasks/"+task.ID, nil, map[string]string{
		"If-Match":        `"v1"`,
		"Idempotency-Key": "delete-undo-task",
	})
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body=%s", deleted.Code, deleted.Body.String())
	}

	missingKey := request(t, server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/restore", nil, map[string]string{
		"If-Match": `"v2"`,
	})
	if missingKey.Code != http.StatusBadRequest {
		t.Fatalf("restore without idempotency key = %d, body=%s", missingKey.Code, missingKey.Body.String())
	}
	missingVersion := request(t, server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/restore", nil, map[string]string{
		"Idempotency-Key": "restore-undo-task-missing-version",
	})
	if missingVersion.Code != http.StatusPreconditionRequired {
		t.Fatalf("restore without If-Match = %d, body=%s", missingVersion.Code, missingVersion.Body.String())
	}

	restored := request(t, server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/restore", nil, map[string]string{
		"If-Match":        `"v2"`,
		"Idempotency-Key": "restore-undo-task",
	})
	if restored.Code != http.StatusOK || restored.Header().Get("ETag") != `"v3"` {
		t.Fatalf("restore status=%d etag=%q body=%s", restored.Code, restored.Header().Get("ETag"), restored.Body.String())
	}
	var restoredTask store.Task
	if err := json.Unmarshal(restored.Body.Bytes(), &restoredTask); err != nil {
		t.Fatalf("decode restored task: %v", err)
	}
	if restoredTask.ID != task.ID || restoredTask.Title != task.Title || restoredTask.Version != 3 {
		t.Fatalf("restored body = %+v, want task %q at v3", restoredTask, task.ID)
	}

	replay := request(t, server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/restore", nil, map[string]string{
		"If-Match":        `"v2"`,
		"Idempotency-Key": "restore-undo-task",
	})
	if replay.Code != restored.Code || replay.Body.String() != restored.Body.String() || replay.Header().Get("ETag") != restored.Header().Get("ETag") {
		t.Fatalf("restore replay differs: first=%d/%s replay=%d/%s", restored.Code, restored.Body.String(), replay.Code, replay.Body.String())
	}

	stale := request(t, server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/restore", nil, map[string]string{
		"If-Match":        `"v2"`,
		"Idempotency-Key": "restore-undo-task-stale",
	})
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale restore = %d, body=%s", stale.Code, stale.Body.String())
	}
}
