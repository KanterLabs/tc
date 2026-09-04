package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/KanterLabs/helm/internal/store"
)

func TestTaskCollectionKeysetPaginationHonorsSortAndFilters(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("TASKPAGE"), Name: stringPtr("Task pages")}, "actor-disabled-mode")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("task")}, "actor-disabled-mode"); err != nil {
			t.Fatalf("create task %d: %v", i, err)
		}
	}
	keySearch := request(t, server, http.MethodGet, "/api/v1/projects/"+project.ID+"/tasks?q=TASKPAGE-1", nil, nil)
	if keySearch.Code != http.StatusOK || !strings.Contains(keySearch.Body.String(), "TASKPAGE-1") {
		t.Fatalf("task-key search = %d %s, want TASKPAGE-1", keySearch.Code, keySearch.Body.String())
	}
	decode := func(response *httptest.ResponseRecorder) (struct {
		Data       []store.Task `json:"data"`
		NextCursor string       `json:"next_cursor"`
	}, error) {
		var body struct {
			Data       []store.Task `json:"data"`
			NextCursor string       `json:"next_cursor"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			return body, err
		}
		return body, nil
	}
	firstResponse := request(t, server, http.MethodGet, "/api/v1/projects/"+project.ID+"/tasks?limit=2&sort=number&order=desc", nil, nil)
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("first task page status = %d, body=%s", firstResponse.Code, firstResponse.Body.String())
	}
	first, err := decode(firstResponse)
	if err != nil {
		t.Fatalf("decode first task page: %v", err)
	}
	if len(first.Data) != 2 || first.NextCursor == "" || first.Data[0].Number < first.Data[1].Number {
		t.Fatalf("first task page = %+v, want two descending tasks and cursor", first)
	}
	secondResponse := request(t, server, http.MethodGet, "/api/v1/projects/"+project.ID+"/tasks?limit=2&sort=number&order=desc&cursor="+url.QueryEscape(first.NextCursor), nil, nil)
	if secondResponse.Code != http.StatusOK {
		t.Fatalf("second task page status = %d, body=%s", secondResponse.Code, secondResponse.Body.String())
	}
	second, err := decode(secondResponse)
	if err != nil {
		t.Fatalf("decode second task page: %v", err)
	}
	if len(second.Data) != 2 || second.Data[0].Number < second.Data[1].Number || second.Data[0].ID == first.Data[0].ID || second.Data[0].ID == first.Data[1].ID {
		t.Fatalf("second task page = %+v, want the next two descending tasks", second)
	}
	conflict := request(t, server, http.MethodGet, "/api/v1/projects/"+project.ID+"/tasks?sort=number_asc&order=desc", nil, nil)
	if conflict.Code != http.StatusBadRequest {
		t.Fatalf("sort direction conflict status = %d, body=%s", conflict.Code, conflict.Body.String())
	}
}

func TestTaskCollectionKeysetMutationReturnsRestartableConflict(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("TASKCONFLICT"), Name: stringPtr("Task cursor conflict")}, "actor-disabled-mode")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("task")}, "actor-disabled-mode"); err != nil {
			t.Fatalf("create task %d: %v", i, err)
		}
	}
	firstResponse := request(t, server, http.MethodGet, "/api/v1/projects/"+project.ID+"/tasks?limit=1&sort=number", nil, nil)
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("first task page status = %d, body=%s", firstResponse.Code, firstResponse.Body.String())
	}
	var first struct {
		Data       []store.Task `json:"data"`
		NextCursor string       `json:"next_cursor"`
	}
	if err := json.Unmarshal(firstResponse.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode first task page: %v", err)
	}
	if len(first.Data) != 1 || first.NextCursor == "" {
		t.Fatalf("first task page = %+v, want one task and cursor", first)
	}
	title := "mutated after page"
	if _, err := data.UpdateTask(ctx, first.Data[0].ID, store.TaskInput{Title: &title}, first.Data[0].Version, "actor-disabled-mode"); err != nil {
		t.Fatalf("mutate task between pages: %v", err)
	}
	secondResponse := request(t, server, http.MethodGet, "/api/v1/projects/"+project.ID+"/tasks?limit=1&sort=number&cursor="+url.QueryEscape(first.NextCursor), nil, nil)
	if secondResponse.Code != http.StatusConflict {
		t.Fatalf("mutated continuation status = %d, body=%s", secondResponse.Code, secondResponse.Body.String())
	}
	var conflict struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(secondResponse.Body.Bytes(), &conflict); err != nil {
		t.Fatalf("decode continuation conflict: %v", err)
	}
	if conflict.Error.Code != "task_collection_changed" {
		t.Fatalf("continuation conflict code = %q, want task_collection_changed", conflict.Error.Code)
	}
	if restart, ok := conflict.Error.Details["restart"].(bool); !ok || !restart {
		t.Fatalf("continuation conflict details = %#v, want restart=true", conflict.Error.Details)
	}
}
