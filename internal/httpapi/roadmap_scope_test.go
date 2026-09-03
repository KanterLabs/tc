package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/KanterLabs/helm/internal/store"
)

func TestRoadmapScopeRedactsEmbeddedTaskAndEventCollections(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{
		Key:         stringPtr("ROADMAPSCOPE"),
		Name:        stringPtr("Roadmap scope project"),
		Description: stringPtr("Aggregate description"),
	}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	otherProject, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("ROADMAPOTHER"), Name: stringPtr("Other project")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	dueAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	taskSecret := "roadmap-task-title-secret"
	descriptionSecret := "roadmap-task-description-secret"
	if _, err := data.CreateTask(ctx, project.ID, store.TaskInput{
		Title:       stringPtr(taskSecret),
		Description: stringPtr(descriptionSecret),
		DueAt:       &dueAt,
		DueAtSet:    true,
	}, "actor-disabled-mode"); err != nil {
		t.Fatal(err)
	}
	eventSecret := "roadmap-event-payload-secret"
	if _, err := data.CreateLabel(ctx, project.ID, store.LabelInput{Name: eventSecret}, "actor-disabled-mode"); err != nil {
		t.Fatal(err)
	}

	createToken := func(name string, scopes []string) string {
		t.Helper()
		agent, err := data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: name, ProjectIDs: []string{project.ID}}, "actor-disabled-mode", "")
		if err != nil {
			t.Fatal(err)
		}
		_, token, err := data.CreateTokenBy(ctx, agent.ID, "actor-disabled-mode", name, scopes, []string{project.ID}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	projectsOnly := createToken("Roadmap projects only", []string{"projects:read"})
	tasksOnly := createToken("Roadmap tasks", []string{"projects:read", "tasks:read"})
	eventsOnly := createToken("Roadmap events", []string{"projects:read", "events:read"})
	fullToken := createToken("Roadmap full", []string{"projects:read", "tasks:read", "events:read"})

	routes := []string{
		"/api/v1/projects/" + project.ID + "/roadmap",
		"/api/v1/roadmap?project=" + project.ID,
	}
	for _, route := range routes {
		route := route
		t.Run(route+" projects-read-only", func(t *testing.T) {
			response := request(t, server, http.MethodGet, route, nil, map[string]string{"Authorization": "Bearer " + projectsOnly})
			assertRoadmapScopeResponse(t, response, false, false, taskSecret, descriptionSecret, eventSecret)
		})
		t.Run(route+" tasks-read", func(t *testing.T) {
			response := request(t, server, http.MethodGet, route, nil, map[string]string{"Authorization": "Bearer " + tasksOnly})
			assertRoadmapScopeResponse(t, response, true, false, eventSecret)
		})
		t.Run(route+" events-read", func(t *testing.T) {
			response := request(t, server, http.MethodGet, route, nil, map[string]string{"Authorization": "Bearer " + eventsOnly})
			assertRoadmapScopeResponse(t, response, false, true)
			if !strings.Contains(response.Body.String(), eventSecret) {
				t.Fatalf("events-read roadmap omitted event payload secret: %s", response.Body.String())
			}
		})
		t.Run(route+" full-token", func(t *testing.T) {
			response := request(t, server, http.MethodGet, route, nil, map[string]string{"Authorization": "Bearer " + fullToken})
			assertRoadmapScopeResponse(t, response, true, true)
			if !strings.Contains(response.Body.String(), taskSecret) || !strings.Contains(response.Body.String(), eventSecret) {
				t.Fatalf("full-token roadmap omitted task/event data: %s", response.Body.String())
			}
		})
		t.Run(route+" human", func(t *testing.T) {
			response := request(t, server, http.MethodGet, route, nil, nil)
			assertRoadmapScopeResponse(t, response, true, true)
			if !strings.Contains(response.Body.String(), taskSecret) || !strings.Contains(response.Body.String(), eventSecret) {
				t.Fatalf("human roadmap omitted task/event data: %s", response.Body.String())
			}
		})
	}

	for _, route := range []string{
		"/api/v1/projects/" + otherProject.ID + "/roadmap",
		"/api/v1/roadmap?project=" + otherProject.ID,
	} {
		response := request(t, server, http.MethodGet, route, nil, map[string]string{"Authorization": "Bearer " + projectsOnly})
		if response.Code != http.StatusForbidden || errorCode(t, response) != "forbidden" {
			t.Fatalf("out-of-ceiling roadmap %s = %d, want 403 forbidden", route, response.Code)
		}
	}
}

type roadmapScopeResponse struct {
	TaskTotal     int               `json:"task_total"`
	Upcoming      []json.RawMessage `json:"upcoming"`
	UpcomingTasks []json.RawMessage `json:"upcoming_tasks"`
	Recent        []json.RawMessage `json:"recent_activity"`
}

func assertRoadmapScopeResponse(t *testing.T, response interface{ Result() *http.Response }, wantTasks, wantEvents bool, absentSecrets ...string) {
	t.Helper()
	result := response.Result()
	defer result.Body.Close()
	if result.StatusCode != http.StatusOK {
		t.Fatalf("roadmap status = %d, want 200; body=%s", result.StatusCode, readResponseBody(result))
	}
	bodyBytes, err := io.ReadAll(result.Body)
	if err != nil {
		t.Fatalf("read roadmap response: %v", err)
	}
	encoded := string(bodyBytes)
	for _, secret := range absentSecrets {
		if strings.Contains(encoded, secret) {
			t.Fatalf("roadmap leaked redacted secret %q: %s", secret, encoded)
		}
	}
	var body roadmapScopeResponse
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		t.Fatalf("decode roadmap response: %v", err)
	}
	if body.TaskTotal == 0 {
		t.Fatal("roadmap omitted project task aggregate")
	}
	if body.Upcoming == nil || body.UpcomingTasks == nil || body.Recent == nil {
		t.Fatalf("roadmap collection fields must be JSON arrays: upcoming=%v upcoming_tasks=%v recent=%v", body.Upcoming, body.UpcomingTasks, body.Recent)
	}
	if got := len(body.Upcoming) > 0; got != wantTasks {
		t.Fatalf("roadmap upcoming present=%v, want %v", got, wantTasks)
	}
	if got := len(body.UpcomingTasks) > 0; got != wantTasks {
		t.Fatalf("roadmap upcoming_tasks present=%v, want %v", got, wantTasks)
	}
	if got := len(body.Recent) > 0; got != wantEvents {
		t.Fatalf("roadmap recent_activity present=%v, want %v", got, wantEvents)
	}
}
