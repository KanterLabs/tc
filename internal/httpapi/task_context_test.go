package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KanterLabs/helm/internal/auth"
	"github.com/KanterLabs/helm/internal/store"
)

func TestTaskContextEndpoint(t *testing.T) {
	server, data := testServer(t, "disabled")
	actor, err := data.EnsureDisabledActor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(context.Background(), store.ProjectInput{Key: stringPtr("CTX"), Name: stringPtr("Context")}, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := data.CreateTask(context.Background(), project.ID, store.TaskInput{Title: stringPtr("Existing task")}, actor.ID); err != nil {
		t.Fatal(err)
	}

	response := request(t, server, http.MethodPost, "/api/v1/projects/CTX/task-context", map[string]any{"query": "new task"}, map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var pack store.TaskDraftContext
	if err := json.Unmarshal(response.Body.Bytes(), &pack); err != nil {
		t.Fatal(err)
	}
	if pack.Project.ID != project.ID || pack.CandidateCount != 1 || len(pack.OpenTasks) != 1 {
		t.Fatalf("pack = %+v", pack)
	}

	for name, payload := range map[string]any{
		"missing":  map[string]any{},
		"empty":    map[string]any{"query": "  "},
		"null":     map[string]any{"query": nil},
		"unknown":  map[string]any{"query": "ok", "extra": true},
		"too-long": map[string]any{"query": strings.Repeat("x", maxTaskContextQueryBytes+1)},
	} {
		t.Run(name, func(t *testing.T) {
			got := request(t, server, http.MethodPost, "/api/v1/projects/CTX/task-context", payload, map[string]string{"Content-Type": "application/json"})
			if got.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", got.Code, got.Body.String())
			}
		})
	}
	wrongMethod := request(t, server, http.MethodGet, "/api/v1/projects/CTX/task-context", nil, nil)
	if wrongMethod.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status=%d", wrongMethod.Code)
	}
}

func TestTaskContextEnforcesProjectScopedToken(t *testing.T) {
	server, data := testServer(t, "disabled")
	actor, _ := data.EnsureDisabledActor(context.Background())
	project, err := data.CreateProject(context.Background(), store.ProjectInput{Key: stringPtr("PRIVATE"), Name: stringPtr("Private")}, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	identity := auth.Identity{Actor: actor, IsToken: true, Token: store.AuthToken{
		Scopes: map[string]bool{"tasks:read": true}, ProjectsScoped: true, Projects: map[string]bool{"some-other-project": true},
	}}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/PRIVATE/task-context", strings.NewReader(`{"query":"secret"}`))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.taskContext(response, req, identity, project.ID)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
