package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/KanterLabs/helm/internal/store"
)

func TestPortableRoutesExportImportBoardsAndTrello(t *testing.T) {
	ctx := context.Background()
	server, data := testServer(t, "disabled")
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{
		Key:  stringPtr("PORTAPI"),
		Name: stringPtr("Portable API"),
	}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}

	exported := request(t, server, http.MethodGet, "/api/v1/projects/"+project.ID+"/export", nil, nil)
	if exported.Code != http.StatusOK {
		t.Fatalf("project export status = %d, body=%s", exported.Code, exported.Body.String())
	}
	if exported.Header().Get("X-Helm-Portable-Format") != store.PortableFormat || exported.Header().Get("X-Helm-Portable-Version") != "1" {
		t.Fatalf("portable headers = format=%q version=%q", exported.Header().Get("X-Helm-Portable-Format"), exported.Header().Get("X-Helm-Portable-Version"))
	}
	if exported.Header().Get("Content-Disposition") == "" || exported.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("portable response headers = %#v", exported.Header())
	}
	var archive store.PortableArchive
	if err := json.Unmarshal(exported.Body.Bytes(), &archive); err != nil {
		t.Fatalf("decode portable export: %v", err)
	}
	if len(archive.Projects) != 1 || archive.Projects[0].ID != project.ID {
		t.Fatalf("exported projects = %+v", archive.Projects)
	}

	boards := request(t, server, http.MethodGet, "/api/v1/projects/"+project.Key+"/boards", nil, nil)
	if boards.Code != http.StatusOK || !strings.Contains(boards.Body.String(), `"decision":"deferred"`) || !strings.Contains(boards.Body.String(), `"default":true`) {
		t.Fatalf("boards status=%d body=%s", boards.Code, boards.Body.String())
	}

	preview := request(t, server, http.MethodPost, "/api/v1/import?dry_run=true&conflict=fail&target_project="+project.ID, archive, map[string]string{
		"Content-Type": "application/json",
	})
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), `"dry_run":true`) || !strings.Contains(preview.Body.String(), `"projects_skipped":1`) {
		t.Fatalf("portable dry-run status=%d body=%s", preview.Code, preview.Body.String())
	}

	projectImport := request(t, server, http.MethodPost, "/api/v1/projects/"+project.ID+"/import?conflict=fail", archive, map[string]string{
		"Content-Type":    "application/json",
		"Idempotency-Key": "portable-project-import-1",
	})
	if projectImport.Code != http.StatusOK || !strings.Contains(projectImport.Body.String(), `"projects_skipped":1`) {
		t.Fatalf("project import status=%d body=%s", projectImport.Code, projectImport.Body.String())
	}

	trello := request(t, server, http.MethodPost, "/api/v1/import/trello?dry_run=true&target_project="+project.ID, map[string]any{
		"id":    "trello-board-1",
		"name":  "Trello board",
		"lists": []map[string]any{{"id": "list-1", "name": "To do"}},
		"cards": []map[string]any{{"id": "card-1", "name": "Imported card", "idList": "list-1"}},
	}, map[string]string{"Content-Type": "application/json"})
	if trello.Code != http.StatusOK || !strings.Contains(trello.Body.String(), `"dry_run":true`) {
		t.Fatalf("Trello dry-run status=%d body=%s", trello.Code, trello.Body.String())
	}
}

func TestPortableImportRouteRejectsUnknownArchiveFields(t *testing.T) {
	server, _ := testServer(t, "disabled")
	response := request(t, server, http.MethodPost, "/api/v1/import", map[string]any{
		"format":        store.PortableFormat,
		"version":       store.PortableVersion,
		"exported_at":   "2026-09-04T00:00:00Z",
		"source":        map[string]string{"product": "helm", "api": "/api/v1"},
		"projects":      []any{},
		"columns":       []any{},
		"tasks":         []any{},
		"labels":        []any{},
		"relationships": map[string]any{"task_labels": []any{}, "dependencies": []any{}, "task_links": []any{}},
		"activity":      map[string]any{"events": []any{}, "agent_work": []any{}, "agent_work_history": []any{}},
		"comments":      []any{},
		"unexpected":    true,
	}, map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "portable import body is invalid") {
		t.Fatalf("unknown field status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestPortableImportIdempotencyIncludesQueryOptions(t *testing.T) {
	ctx := context.Background()
	server, data := testServer(t, "disabled")
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("IDEMPORT"), Name: stringPtr("Idempotent import")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	otherProject, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("IDEMOTHER"), Name: stringPtr("Other import target")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	archive, err := data.ExportPortable(ctx, []string{project.ID})
	if err != nil {
		t.Fatal(err)
	}
	baseHeaders := map[string]string{"Content-Type": "application/json", "Idempotency-Key": "portable-query-options-1"}
	first := request(t, server, http.MethodPost, "/api/v1/projects/"+project.ID+"/import?conflict=remap", archive, baseHeaders)
	if first.Code != http.StatusOK {
		t.Fatalf("first idempotent import status=%d body=%s", first.Code, first.Body.String())
	}
	conflictChanged := request(t, server, http.MethodPost, "/api/v1/projects/"+project.ID+"/import?conflict=fail", archive, baseHeaders)
	if conflictChanged.Code != http.StatusConflict || responseErrorCode(t, conflictChanged.Body.Bytes()) != "idempotency_key_reused" {
		t.Fatalf("changed conflict option status=%d body=%s", conflictChanged.Code, conflictChanged.Body.String())
	}
	dryRunChanged := request(t, server, http.MethodPost, "/api/v1/projects/"+project.ID+"/import?conflict=remap&dry_run=true", archive, map[string]string{"Content-Type": "application/json", "Idempotency-Key": "portable-query-options-2"})
	if dryRunChanged.Code != http.StatusOK {
		t.Fatalf("dry-run import status=%d body=%s", dryRunChanged.Code, dryRunChanged.Body.String())
	}
	dryRunReplayChanged := request(t, server, http.MethodPost, "/api/v1/projects/"+project.ID+"/import?conflict=remap", archive, map[string]string{"Content-Type": "application/json", "Idempotency-Key": "portable-query-options-2"})
	if dryRunReplayChanged.Code != http.StatusConflict || responseErrorCode(t, dryRunReplayChanged.Body.Bytes()) != "idempotency_key_reused" {
		t.Fatalf("changed dry_run option status=%d body=%s", dryRunReplayChanged.Code, dryRunReplayChanged.Body.String())
	}
	targetChanged := request(t, server, http.MethodPost, "/api/v1/import?target_project="+project.ID+"&conflict=remap", archive, map[string]string{"Content-Type": "application/json", "Idempotency-Key": "portable-query-options-3"})
	if targetChanged.Code != http.StatusOK {
		t.Fatalf("target import status=%d body=%s", targetChanged.Code, targetChanged.Body.String())
	}
	targetReplayChanged := request(t, server, http.MethodPost, "/api/v1/import?target_project="+otherProject.ID+"&conflict=remap", archive, map[string]string{"Content-Type": "application/json", "Idempotency-Key": "portable-query-options-3"})
	if targetReplayChanged.Code != http.StatusConflict || responseErrorCode(t, targetReplayChanged.Body.Bytes()) != "idempotency_key_reused" {
		t.Fatalf("changed target option status=%d body=%s", targetReplayChanged.Code, targetReplayChanged.Body.String())
	}
}

func TestPortableTrelloFailureRetainsAdapterWarnings(t *testing.T) {
	ctx := context.Background()
	server, data := testServer(t, "disabled")
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	response := request(t, server, http.MethodPost, "/api/v1/import/trello?dry_run=true", map[string]any{
		"id":     "trello-warning-failure",
		"name":   "Warning failure board",
		"closed": false,
		"prefs":  map[string]any{"permissionLevel": "private"},
		"lists":  []map[string]any{{"id": "list-1", "name": "To do"}},
		"cards":  []map[string]any{{"id": "duplicate-card", "name": "First", "idList": "list-1", "pos": 1}, {"id": "duplicate-card", "name": "Second", "idList": "list-1", "pos": 2}},
	}, map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "duplicate task id") || !strings.Contains(response.Body.String(), "pos") || !strings.Contains(response.Body.String(), "closed") || !strings.Contains(response.Body.String(), "prefs") {
		t.Fatalf("Trello failure did not retain warnings status=%d body=%s", response.Code, response.Body.String())
	}
}
