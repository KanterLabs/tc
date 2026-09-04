package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/KanterLabs/helm/internal/store"
)

func assertChecklistHTTPIncomplete(t *testing.T, response interface{ Result() *http.Response }, wantOpen int) {
	t.Helper()
	result := response.Result()
	defer result.Body.Close()
	if result.StatusCode != http.StatusConflict {
		t.Fatalf("completion status = %d, want %d; body=%s", result.StatusCode, http.StatusConflict, readResponseBody(result))
	}
	var envelope struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	body := readResponseBody(result)
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("decode checklist completion error: %v; body=%s", err, body)
	}
	if envelope.Error.Code != "checklist_incomplete" || envelope.Error.Details["open_items"] != float64(wantOpen) {
		t.Fatalf("completion error = %#v, want checklist_incomplete/open_items=%d", envelope.Error, wantOpen)
	}
}

func TestChecklistCompletionPolicyHTTPAlternatePaths(t *testing.T) {
	t.Run("generic task patch enforces require", func(t *testing.T) {
		server, data := testServer(t, "disabled")
		ctx := context.Background()
		if _, err := data.EnsureDisabledActor(ctx); err != nil {
			t.Fatal(err)
		}
		projectResponse := request(t, server, http.MethodPost, "/api/v1/projects", map[string]any{
			"key": "HTTPREQ", "name": "HTTP require", "checklist_completion_policy": "require",
		}, map[string]string{"Content-Type": "application/json"})
		if projectResponse.Code != http.StatusCreated {
			t.Fatalf("create require project: %d %s", projectResponse.Code, projectResponse.Body.String())
		}
		var project store.Project
		if err := json.Unmarshal(projectResponse.Body.Bytes(), &project); err != nil {
			t.Fatal(err)
		}
		taskResponse := request(t, server, http.MethodPost, "/api/v1/projects/"+project.ID+"/tasks", map[string]any{"title": "Patch completion"}, map[string]string{"Content-Type": "application/json"})
		if taskResponse.Code != http.StatusCreated {
			t.Fatalf("create task: %d %s", taskResponse.Code, taskResponse.Body.String())
		}
		var task store.Task
		if err := json.Unmarshal(taskResponse.Body.Bytes(), &task); err != nil {
			t.Fatal(err)
		}
		completedColumn, err := data.StateColumn(ctx, project.ID, "completed")
		if err != nil {
			t.Fatalf("completed column: %v", err)
		}
		addedResponse := request(t, server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/checklist", map[string]any{"text": "verify"}, map[string]string{
			"Content-Type": "application/json", "If-Match": `"v1"`,
		})
		if addedResponse.Code != http.StatusOK || addedResponse.Header().Get("ETag") != `"v2"` {
			t.Fatalf("add checklist item: %d/%q %s", addedResponse.Code, addedResponse.Header().Get("ETag"), addedResponse.Body.String())
		}
		patch := request(t, server, http.MethodPatch, "/api/v1/tasks/"+task.ID, map[string]any{"column_id": completedColumn.ID}, map[string]string{
			"Content-Type": "application/json", "If-Match": `"v2"`,
		})
		assertChecklistHTTPIncomplete(t, patch, 1)

		read := request(t, server, http.MethodGet, "/api/v1/tasks/"+task.ID, nil, nil)
		if read.Code != http.StatusOK || read.Header().Get("ETag") != `"v2"` {
			t.Fatalf("rejected task patch read: %d/%q %s", read.Code, read.Header().Get("ETag"), read.Body.String())
		}
		var after store.Task
		if err := json.Unmarshal(read.Body.Bytes(), &after); err != nil {
			t.Fatal(err)
		}
		if after.ColumnID != task.ColumnID || after.Version != 2 || after.CompletedAt != nil || after.ChecklistSummary.Open != 1 {
			t.Fatalf("rejected task patch changed task: before=%+v after=%+v", task, after)
		}
	})

	t.Run("column transition rejects all require tasks atomically", func(t *testing.T) {
		server, data := testServer(t, "disabled")
		ctx := context.Background()
		if _, err := data.EnsureDisabledActor(ctx); err != nil {
			t.Fatal(err)
		}
		projectResponse := request(t, server, http.MethodPost, "/api/v1/projects", map[string]any{
			"key": "HTTPBULKREQ", "name": "HTTP bulk require", "checklist_completion_policy": "require",
		}, map[string]string{"Content-Type": "application/json"})
		if projectResponse.Code != http.StatusCreated {
			t.Fatalf("create bulk require project: %d %s", projectResponse.Code, projectResponse.Body.String())
		}
		var project store.Project
		if err := json.Unmarshal(projectResponse.Body.Bytes(), &project); err != nil {
			t.Fatal(err)
		}
		backlog, err := data.StateColumn(ctx, project.ID, "backlog")
		if err != nil {
			t.Fatalf("backlog column: %v", err)
		}
		actorID := "actor-disabled-mode"
		firstTitle, secondTitle := "Bulk first", "Bulk second"
		first, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: &firstTitle, ColumnID: &backlog.ID}, actorID)
		if err != nil {
			t.Fatalf("create first task: %v", err)
		}
		second, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: &secondTitle, ColumnID: &backlog.ID}, actorID)
		if err != nil {
			t.Fatalf("create second task: %v", err)
		}
		first, err = data.AddTaskChecklistItem(ctx, first.ID, store.ChecklistItemInput{Text: stringPtr("first")}, first.Version, actorID)
		if err != nil {
			t.Fatalf("add first checklist: %v", err)
		}
		second, err = data.AddTaskChecklistItem(ctx, second.ID, store.ChecklistItemInput{Text: stringPtr("second")}, second.Version, actorID)
		if err != nil {
			t.Fatalf("add second checklist: %v", err)
		}
		beforeEvents := countProjectEventsForHTTPTest(t, data, project.ID)
		patch := request(t, server, http.MethodPatch, "/api/v1/columns/"+backlog.ID, map[string]any{"semantic_state": "completed"}, map[string]string{"Content-Type": "application/json"})
		assertChecklistHTTPIncomplete(t, patch, 2)
		afterColumn, err := data.GetColumn(ctx, backlog.ID)
		if err != nil {
			t.Fatal(err)
		}
		if afterColumn.SemanticState != "backlog" {
			t.Fatalf("rejected bulk column state = %q, want backlog", afterColumn.SemanticState)
		}
		for _, before := range []store.Task{first, second} {
			after, getErr := data.GetTask(ctx, before.ID)
			if getErr != nil {
				t.Fatal(getErr)
			}
			if after.Version != before.Version || after.ColumnID != before.ColumnID || after.CompletedAt != nil {
				t.Fatalf("rejected bulk task changed: before=%+v after=%+v", before, after)
			}
		}
		if got := countProjectEventsForHTTPTest(t, data, project.ID); got != beforeEvents {
			t.Fatalf("rejected bulk column emitted activity: %d -> %d", beforeEvents, got)
		}
	})

	t.Run("column transition exposes warn metadata", func(t *testing.T) {
		server, data := testServer(t, "disabled")
		ctx := context.Background()
		if _, err := data.EnsureDisabledActor(ctx); err != nil {
			t.Fatal(err)
		}
		projectResponse := request(t, server, http.MethodPost, "/api/v1/projects", map[string]any{
			"key": "HTTPBULKWARN", "name": "HTTP bulk warn",
		}, map[string]string{"Content-Type": "application/json"})
		if projectResponse.Code != http.StatusCreated {
			t.Fatalf("create bulk warn project: %d %s", projectResponse.Code, projectResponse.Body.String())
		}
		var project store.Project
		if err := json.Unmarshal(projectResponse.Body.Bytes(), &project); err != nil {
			t.Fatal(err)
		}
		backlog, err := data.StateColumn(ctx, project.ID, "backlog")
		if err != nil {
			t.Fatalf("backlog column: %v", err)
		}
		actorID := "actor-disabled-mode"
		firstTitle, secondTitle := "Warn first", "Warn second"
		first, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: &firstTitle, ColumnID: &backlog.ID}, actorID)
		if err != nil {
			t.Fatalf("create first warn task: %v", err)
		}
		second, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: &secondTitle, ColumnID: &backlog.ID}, actorID)
		if err != nil {
			t.Fatalf("create second warn task: %v", err)
		}
		first, err = data.AddTaskChecklistItem(ctx, first.ID, store.ChecklistItemInput{Text: stringPtr("first")}, first.Version, actorID)
		if err != nil {
			t.Fatalf("add first warn checklist: %v", err)
		}
		second, err = data.AddTaskChecklistItem(ctx, second.ID, store.ChecklistItemInput{Text: stringPtr("second")}, second.Version, actorID)
		if err != nil {
			t.Fatalf("add second warn checklist: %v", err)
		}
		patch := request(t, server, http.MethodPatch, "/api/v1/columns/"+backlog.ID, map[string]any{"semantic_state": "completed"}, map[string]string{"Content-Type": "application/json"})
		if patch.Code != http.StatusOK {
			t.Fatalf("warn column transition: %d %s", patch.Code, patch.Body.String())
		}
		for _, before := range []store.Task{first, second} {
			read := request(t, server, http.MethodGet, "/api/v1/tasks/"+before.ID, nil, nil)
			if read.Code != http.StatusOK || read.Header().Get("ETag") != `"v3"` {
				t.Fatalf("warn task read: %d/%q %s", read.Code, read.Header().Get("ETag"), read.Body.String())
			}
			var after store.Task
			if err := json.Unmarshal(read.Body.Bytes(), &after); err != nil {
				t.Fatal(err)
			}
			if after.CompletedAt == nil || !after.ChecklistSummary.Warning || after.ChecklistSummary.Open != 1 {
				t.Fatalf("warn task summary = %+v completed_at=%v", after.ChecklistSummary, after.CompletedAt)
			}
		}
		events := request(t, server, http.MethodGet, "/api/v1/events?project="+project.Key+"&after=0&limit=100", nil, nil)
		if events.Code != http.StatusOK {
			t.Fatalf("warn events: %d %s", events.Code, events.Body.String())
		}
		var envelope struct {
			Data []store.Event `json:"data"`
		}
		if err := json.Unmarshal(events.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, event := range envelope.Data {
			if event.Type != "column.updated" {
				continue
			}
			var payload map[string]any
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload["checklist_warning"] == true && payload["open_checklist_items"] == float64(2) && payload["checklist_completion_policy"] == "warn" {
				found = true
			}
		}
		if !found {
			t.Fatal("warn column event omitted checklist warning metadata")
		}
	})

	t.Run("bug resolve enforces require", func(t *testing.T) {
		server, _ := testServer(t, "disabled")
		projectResponse := request(t, server, http.MethodPost, "/api/v1/projects", map[string]any{
			"key": "HTTPBUGREQ", "name": "HTTP bug require", "checklist_completion_policy": "require",
		}, map[string]string{"Content-Type": "application/json"})
		if projectResponse.Code != http.StatusCreated {
			t.Fatalf("create bug require project: %d %s", projectResponse.Code, projectResponse.Body.String())
		}
		var project store.Project
		if err := json.Unmarshal(projectResponse.Body.Bytes(), &project); err != nil {
			t.Fatal(err)
		}
		bugResponse := request(t, server, http.MethodPost, "/api/v1/projects/"+project.ID+"/tasks", map[string]any{
			"title": "Checklist bug", "kind": "bug", "bug": map[string]any{"actual_behavior": "still broken"},
		}, map[string]string{"Content-Type": "application/json"})
		if bugResponse.Code != http.StatusCreated {
			t.Fatalf("create bug: %d %s", bugResponse.Code, bugResponse.Body.String())
		}
		var bug store.Task
		if err := json.Unmarshal(bugResponse.Body.Bytes(), &bug); err != nil {
			t.Fatal(err)
		}
		added := request(t, server, http.MethodPost, "/api/v1/tasks/"+bug.ID+"/checklist", map[string]any{"text": "verify fix"}, map[string]string{
			"Content-Type": "application/json", "If-Match": `"v1"`,
		})
		if added.Code != http.StatusOK || added.Header().Get("ETag") != `"v2"` {
			t.Fatalf("add bug checklist: %d/%q %s", added.Code, added.Header().Get("ETag"), added.Body.String())
		}
		resolve := request(t, server, http.MethodPost, "/api/v1/tasks/"+bug.ID+"/resolve", map[string]any{"resolution": "fixed"}, map[string]string{
			"Content-Type": "application/json", "If-Match": `"v2"`,
		})
		assertChecklistHTTPIncomplete(t, resolve, 1)
		read := request(t, server, http.MethodGet, "/api/v1/tasks/"+bug.ID, nil, nil)
		if read.Code != http.StatusOK || read.Header().Get("ETag") != `"v2"` {
			t.Fatalf("rejected bug resolve read: %d/%q %s", read.Code, read.Header().Get("ETag"), read.Body.String())
		}
		var after store.Task
		if err := json.Unmarshal(read.Body.Bytes(), &after); err != nil {
			t.Fatal(err)
		}
		if after.CompletedAt != nil || after.Bug == nil || after.Bug.Resolution != nil {
			t.Fatalf("rejected bug resolve changed task: %+v", after)
		}
	})
}

func countProjectEventsForHTTPTest(t *testing.T, data *store.Store, projectID string) int {
	t.Helper()
	var count int
	if err := data.DB.QueryRowContext(context.Background(), `SELECT COUNT(1) FROM events WHERE project_id=?`, projectID).Scan(&count); err != nil {
		t.Fatalf("count project events: %v", err)
	}
	return count
}
