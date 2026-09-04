package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/KanterLabs/helm/internal/auth"
	"github.com/KanterLabs/helm/internal/store"
)

func TestDecodeTaskMoveInputAcceptsVisibleOrderingAnchorsAndRevisions(t *testing.T) {
	req := taskMoveRequest(t, http.MethodPost, "/api/v1/tasks/task/reorder", map[string]any{
		"destination_column_id":                 "destination",
		"expected_source_column_id":             "source",
		"source":                                "board",
		"before_task_id":                        "next",
		"after_task_id":                         "previous",
		"placement":                             "between",
		"expected_source_ordering_version":      4,
		"expected_destination_ordering_version": 7,
	}, nil)
	input, err := decodeTaskMoveInput(req.Request)
	if err != nil {
		t.Fatalf("decode ordering input: %v", err)
	}
	if input.BeforeTaskID != "next" || input.AfterTaskID != "previous" || input.Placement != "between" || input.ExpectedSourceOrderingVersion != 4 || input.ExpectedDestinationOrderingVersion != 7 {
		t.Fatalf("decoded ordering input = %+v", input)
	}
}

func TestTaskReorderReturnsETagAndRejectsStaleOrderingRevision(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	actor, err := data.EnsureDisabledActor(ctx)
	if err != nil {
		t.Fatalf("ensure actor: %v", err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("ORDERHTTP"), Name: stringPtr("Ordering HTTP")}, actor.ID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	columns, err := data.ListColumns(ctx, project.ID)
	if err != nil {
		t.Fatalf("list columns: %v", err)
	}
	position0, position1, position2 := 0.0, 1.0, 2.0
	create := func(title string, position *float64) store.Task {
		t.Helper()
		task, createErr := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr(title), ColumnID: &columns[0].ID, Position: position}, actor.ID)
		if createErr != nil {
			t.Fatalf("create task %s: %v", title, createErr)
		}
		return task
	}
	one := create("one", &position0)
	two := create("two", &position1)
	three := create("three", &position2)
	columns, err = data.ListColumns(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	identity := auth.Identity{Actor: actor}
	payload := map[string]any{
		"destination_column_id":     columns[0].ID,
		"expected_source_column_id": columns[0].ID,
		"source":                    "board",
		"placement":                 "first",
		"expected_ordering_version": columns[0].OrderingVersion,
	}
	first := taskMoveRequest(t, http.MethodPost, "/api/v1/tasks/"+two.ID+"/reorder", payload, map[string]string{
		"If-Match": taskETag(two), "Idempotency-Key": "order-first",
	})
	server.taskReorder(first, first.Request, identity, two.ID)
	if first.Code != http.StatusOK || first.Header().Get("ETag") != `"v2"` {
		t.Fatalf("first reorder = %d etag=%q body=%s", first.Code, first.Header().Get("ETag"), first.Body.String())
	}
	var firstResponse store.Task
	if err := json.Unmarshal(first.Body.Bytes(), &firstResponse); err != nil {
		t.Fatal(err)
	}
	if firstResponse.ID != two.ID {
		t.Fatalf("first reorder response = %+v", firstResponse)
	}
	ordered, _, err := data.ListTasks(ctx, project.ID, store.TaskFilter{Column: columns[0].ID, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(ordered) != 3 || ordered[0].ID != two.ID {
		t.Fatalf("first reorder list = %v, want %s first", taskIDsInHTTPColumn(ordered, columns[0].ID), two.ID)
	}

	stalePayload := map[string]any{
		"destination_column_id":     columns[0].ID,
		"expected_source_column_id": columns[0].ID,
		"source":                    "board",
		"placement":                 "last",
		"expected_ordering_version": columns[0].OrderingVersion,
	}
	stale := taskMoveRequest(t, http.MethodPost, "/api/v1/tasks/"+three.ID+"/reorder", stalePayload, map[string]string{
		"If-Match": taskETag(three), "Idempotency-Key": "order-stale",
	})
	server.taskReorder(stale, stale.Request, identity, three.ID)
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), "stale_task") {
		t.Fatalf("stale reorder = %d body=%s, want conflict/stale_task", stale.Code, stale.Body.String())
	}
	var latest store.Task
	if err := json.Unmarshal(first.Body.Bytes(), &latest); err != nil {
		t.Fatal(err)
	}
	if latest.ID != two.ID {
		t.Fatalf("first response task changed while handling stale reorder: %+v", latest)
	}
	_ = one
}

func taskIDsInHTTPColumn(tasks []store.Task, columnID string) []string {
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		if task.ColumnID == columnID {
			ids = append(ids, task.ID)
		}
	}
	return ids
}
