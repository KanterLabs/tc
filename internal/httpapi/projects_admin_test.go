package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/KanterLabs/helm/internal/store"
)

func TestProjectColumnAdministrationHTTPFlow(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatalf("ensure development actor: %v", err)
	}

	createdResponse := request(t, server, http.MethodPost, "/api/v1/projects", map[string]any{
		"key": "ADMINHTTP", "name": "Administration", "description": "Before", "color": "#123456",
	}, map[string]string{"Content-Type": "application/json"})
	if createdResponse.Code != http.StatusCreated || createdResponse.Header().Get("ETag") != `"v1"` {
		t.Fatalf("create project: status=%d etag=%q body=%s", createdResponse.Code, createdResponse.Header().Get("ETag"), createdResponse.Body.String())
	}
	var project store.Project
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &project); err != nil {
		t.Fatalf("decode project: %v", err)
	}
	if project.Slug == "" || project.Key != "ADMINHTTP" {
		t.Fatalf("created project = %+v", project)
	}

	renamed := request(t, server, http.MethodPatch, "/api/v1/projects/"+project.Slug, map[string]any{
		"name": "Administration renamed", "description": "After", "color": "#abcdef",
	}, map[string]string{"Content-Type": "application/json", "If-Match": `"v1"`})
	if renamed.Code != http.StatusOK || renamed.Header().Get("ETag") != `"v2"` {
		t.Fatalf("rename project: status=%d etag=%q body=%s", renamed.Code, renamed.Header().Get("ETag"), renamed.Body.String())
	}
	var updated store.Project
	if err := json.Unmarshal(renamed.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode renamed project: %v", err)
	}
	if updated.Name != "Administration renamed" || updated.Slug != project.Slug || updated.Key != project.Key {
		t.Fatalf("renamed project = %+v, want stable key/slug", updated)
	}
	stale := request(t, server, http.MethodPatch, "/api/v1/projects/"+project.Slug, map[string]any{"description": "stale"}, map[string]string{"Content-Type": "application/json", "If-Match": `"v1"`})
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale project update: status=%d body=%s", stale.Code, stale.Body.String())
	}

	columnsResponse := request(t, server, http.MethodGet, "/api/v1/projects/"+project.Slug+"/columns", nil, nil)
	if columnsResponse.Code != http.StatusOK {
		t.Fatalf("list columns: status=%d body=%s", columnsResponse.Code, columnsResponse.Body.String())
	}
	var columns struct {
		Data []store.Column `json:"data"`
	}
	if err := json.Unmarshal(columnsResponse.Body.Bytes(), &columns); err != nil {
		t.Fatalf("decode columns: %v", err)
	}
	if len(columns.Data) != 5 {
		t.Fatalf("default columns = %d, want 5", len(columns.Data))
	}

	createdColumnResponse := request(t, server, http.MethodPost, "/api/v1/projects/"+project.Slug+"/columns", map[string]any{
		"name": "Intake", "semantic_state": "backlog", "position": 1,
	}, map[string]string{"Content-Type": "application/json"})
	if createdColumnResponse.Code != http.StatusCreated || createdColumnResponse.Header().Get("ETag") != `"v1"` {
		t.Fatalf("create column: status=%d etag=%q body=%s", createdColumnResponse.Code, createdColumnResponse.Header().Get("ETag"), createdColumnResponse.Body.String())
	}
	var column store.Column
	if err := json.Unmarshal(createdColumnResponse.Body.Bytes(), &column); err != nil {
		t.Fatalf("decode column: %v", err)
	}
	renamedColumnResponse := request(t, server, http.MethodPatch, "/api/v1/columns/"+column.ID, map[string]any{
		"name": "Intake triage", "position": 0,
	}, map[string]string{"Content-Type": "application/json", "If-Match": `"v1"`})
	if renamedColumnResponse.Code != http.StatusOK || renamedColumnResponse.Header().Get("ETag") != `"v2"` {
		t.Fatalf("rename/reorder column: status=%d etag=%q body=%s", renamedColumnResponse.Code, renamedColumnResponse.Header().Get("ETag"), renamedColumnResponse.Body.String())
	}
	if err := json.Unmarshal(renamedColumnResponse.Body.Bytes(), &column); err != nil {
		t.Fatalf("decode renamed column: %v", err)
	}
	if column.Name != "Intake triage" || column.Position != 0 {
		t.Fatalf("renamed column = %+v, want Intake triage at position 0", column)
	}
	staleColumn := request(t, server, http.MethodPatch, "/api/v1/columns/"+column.ID, map[string]any{"name": "stale"}, map[string]string{"Content-Type": "application/json", "If-Match": `"v1"`})
	if staleColumn.Code != http.StatusConflict {
		t.Fatalf("stale column update: status=%d body=%s", staleColumn.Code, staleColumn.Body.String())
	}
	taskResponse := request(t, server, http.MethodPost, "/api/v1/projects/"+project.Slug+"/tasks", map[string]any{"title": "Rehome me", "column_id": column.ID}, map[string]string{"Content-Type": "application/json"})
	if taskResponse.Code != http.StatusCreated {
		t.Fatalf("create column task: status=%d body=%s", taskResponse.Code, taskResponse.Body.String())
	}
	var task store.Task
	if err := json.Unmarshal(taskResponse.Body.Bytes(), &task); err != nil {
		t.Fatalf("decode task: %v", err)
	}

	archived := request(t, server, http.MethodPatch, "/api/v1/columns/"+column.ID, map[string]any{"archived": true}, map[string]string{"Content-Type": "application/json", "If-Match": `"v2"`})
	if archived.Code != http.StatusOK || archived.Header().Get("ETag") != `"v3"` {
		t.Fatalf("archive column: status=%d etag=%q body=%s", archived.Code, archived.Header().Get("ETag"), archived.Body.String())
	}
	rehomed, err := data.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("read rehomed task: %v", err)
	}
	if rehomed.ColumnID == column.ID {
		t.Fatalf("task remained in archived column: %+v", rehomed)
	}
	liveColumns := request(t, server, http.MethodGet, "/api/v1/projects/"+project.Slug+"/columns", nil, nil)
	if liveColumns.Code != http.StatusOK || string(liveColumns.Body.Bytes()) == "" {
		t.Fatalf("list live columns after archive: status=%d body=%s", liveColumns.Code, liveColumns.Body.String())
	}
	var live struct {
		Data []store.Column `json:"data"`
	}
	if err := json.Unmarshal(liveColumns.Body.Bytes(), &live); err != nil {
		t.Fatalf("decode live columns: %v", err)
	}
	if len(live.Data) != 5 {
		t.Fatalf("live columns after archive = %d, want 5", len(live.Data))
	}
	allColumns := request(t, server, http.MethodGet, "/api/v1/projects/"+project.Slug+"/columns?archived=true", nil, nil)
	if allColumns.Code != http.StatusOK {
		t.Fatalf("list all columns: status=%d body=%s", allColumns.Code, allColumns.Body.String())
	}
	var all struct {
		Data []store.Column `json:"data"`
	}
	if err := json.Unmarshal(allColumns.Body.Bytes(), &all); err != nil {
		t.Fatalf("decode all columns: %v", err)
	}
	if len(all.Data) != 6 || all.Data[len(all.Data)-1].ID != column.ID || all.Data[len(all.Data)-1].ArchivedAt == nil {
		t.Fatalf("all columns after archive = %+v", all.Data)
	}

	restored := request(t, server, http.MethodPatch, "/api/v1/columns/"+column.ID, map[string]any{"archived": false}, map[string]string{"Content-Type": "application/json", "If-Match": `"v3"`})
	if restored.Code != http.StatusOK || restored.Header().Get("ETag") != `"v4"` {
		t.Fatalf("restore column: status=%d etag=%q body=%s", restored.Code, restored.Header().Get("ETag"), restored.Body.String())
	}

	archivedProject := request(t, server, http.MethodPatch, "/api/v1/projects/"+project.Slug, map[string]any{"archived": true}, map[string]string{"Content-Type": "application/json", "If-Match": `"v2"`})
	if archivedProject.Code != http.StatusOK {
		t.Fatalf("archive project: status=%d body=%s", archivedProject.Code, archivedProject.Body.String())
	}
	activeProjects := request(t, server, http.MethodGet, "/api/v1/projects", nil, nil)
	if activeProjects.Code != http.StatusOK || string(activeProjects.Body.Bytes()) == "" {
		t.Fatalf("list active projects: status=%d body=%s", activeProjects.Code, activeProjects.Body.String())
	}
	var active struct {
		Data []store.Project `json:"data"`
	}
	if err := json.Unmarshal(activeProjects.Body.Bytes(), &active); err != nil {
		t.Fatalf("decode active projects: %v", err)
	}
	for _, candidate := range active.Data {
		if candidate.ID == project.ID {
			t.Fatalf("archived project remained in active list: %+v", active.Data)
		}
	}
	allProjects := request(t, server, http.MethodGet, "/api/v1/projects?archived=true", nil, nil)
	if allProjects.Code != http.StatusOK || !json.Valid(allProjects.Body.Bytes()) {
		t.Fatalf("list all projects: status=%d body=%s", allProjects.Code, allProjects.Body.String())
	}
}
