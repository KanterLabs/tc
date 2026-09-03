package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KanterLabs/helm/internal/auth"
	"github.com/KanterLabs/helm/internal/codexruntime"
	"github.com/KanterLabs/helm/internal/store"
)

func TestTaskDraftReturnsValidatedPreviewWithoutMutation(t *testing.T) {
	server, data := testServer(t, "disabled")
	actor, _ := data.EnsureDisabledActor(context.Background())
	project, err := data.CreateProject(context.Background(), store.ProjectInput{Key: stringPtr("LUNA"), Name: stringPtr("Luna")}, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	contextTask, err := data.CreateTask(context.Background(), project.ID, store.TaskInput{Title: stringPtr("Existing connection work")}, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeCodexAccounts{draftOutput: `{"title":"Draft from history","description":"A useful description.","acceptance_criteria":["One measurable outcome"],"priority":"high","rationale":"Related work is already active.","supporting_task_keys":["` + contextTask.Key + `"]}`}
	server.Codex = fake
	response := request(t, server, http.MethodPost, "/api/v1/projects/LUNA/task-draft", map[string]any{"query": "help plan the next connection task"}, map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"priority":"high"`) || !strings.Contains(response.Body.String(), contextTask.Key) {
		t.Fatalf("suggestion=%s", response.Body.String())
	}
	pack, err := data.TaskDraftContext(context.Background(), project.ID, "anything")
	if err != nil || pack.CandidateCount != 1 {
		t.Fatalf("draft mutated tasks: pack=%+v err=%v", pack, err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.draftActors) != 1 || fake.draftActors[0] != actor.ID {
		t.Fatalf("draft actors=%v", fake.draftActors)
	}
	if len(fake.draftRequests) != 1 || fake.draftRequests[0].Model != "gpt-5.6-luna" || !strings.Contains(fake.draftRequests[0].Prompt, "untrusted quoted historical data") {
		t.Fatalf("draft request=%+v", fake.draftRequests)
	}
}

func TestTaskDraftFailureModes(t *testing.T) {
	server, data := testServer(t, "disabled")
	actor, _ := data.EnsureDisabledActor(context.Background())
	project, _ := data.CreateProject(context.Background(), store.ProjectInput{Key: stringPtr("FAIL"), Name: stringPtr("Failures")}, actor.ID)
	identity := auth.Identity{Actor: actor}
	tests := []struct {
		name   string
		fake   *fakeCodexAccounts
		status int
		code   string
	}{
		{"disconnected", &fakeCodexAccounts{accountStatus: &codexruntime.AccountStatus{RequiresOpenAIAuth: true}}, http.StatusConflict, "codex_not_connected"},
		{"rate limited", &fakeCodexAccounts{draftErr: errors.New("rate limit reached")}, http.StatusTooManyRequests, "codex_limit_reached"},
		{"canceled", &fakeCodexAccounts{draftErr: context.Canceled}, http.StatusServiceUnavailable, "luna_unavailable"},
		{"malformed", &fakeCodexAccounts{draftOutput: `not-json`}, http.StatusServiceUnavailable, "luna_invalid_output"},
		{"hallucinated evidence", &fakeCodexAccounts{draftOutput: `{"title":"Draft","description":"Description","acceptance_criteria":["Outcome"],"priority":"normal","rationale":"Reason","supporting_task_keys":["OTHER-99"]}`}, http.StatusServiceUnavailable, "luna_invalid_output"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server.Codex = test.fake
			req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/FAIL/task-draft", strings.NewReader(`{"query":"draft it"}`))
			response := httptest.NewRecorder()
			server.taskDraft(response, req, identity, project.ID)
			if response.Code != test.status || !strings.Contains(response.Body.String(), test.code) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}

	server.Codex = &fakeCodexAccounts{}
	response := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/FAIL/task-draft", strings.NewReader(`{"query":"draft it"}`))
	server.taskDraft(response, req, auth.Identity{Actor: store.Actor{ID: "agent", Kind: "agent"}, IsToken: true}, project.ID)
	if response.Code != http.StatusForbidden {
		t.Fatalf("bearer status=%d", response.Code)
	}
}

func TestTaskDraftRoutesEveryRunToRequestingActor(t *testing.T) {
	server, data := testServer(t, "disabled")
	owner, _ := data.EnsureDisabledActor(context.Background())
	project, _ := data.CreateProject(context.Background(), store.ProjectInput{Key: stringPtr("USERS"), Name: stringPtr("Users")}, owner.ID)
	fake := &fakeCodexAccounts{}
	server.Codex = fake
	for _, actorID := range []string{"human-a", "human-b"} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/USERS/task-draft", strings.NewReader(`{"query":"draft it"}`))
		response := httptest.NewRecorder()
		server.taskDraft(response, req, auth.Identity{Actor: store.Actor{ID: actorID, Kind: "human"}}, project.ID)
		if response.Code != http.StatusOK {
			t.Fatalf("actor=%s status=%d body=%s", actorID, response.Code, response.Body.String())
		}
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.draftActors) != 2 || fake.draftActors[0] != "human-a" || fake.draftActors[1] != "human-b" {
		t.Fatalf("draft actors=%v", fake.draftActors)
	}
}

func TestTaskDraftOperatorKillSwitch(t *testing.T) {
	server, data := testServer(t, "disabled")
	actor, _ := data.EnsureDisabledActor(context.Background())
	project, _ := data.CreateProject(context.Background(), store.ProjectInput{Key: stringPtr("OFF"), Name: stringPtr("Disabled")}, actor.ID)
	server.Codex = &fakeCodexAccounts{}
	server.Cfg.LunaDisabled = true
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/OFF/task-draft", strings.NewReader(`{"query":"draft it"}`))
	response := httptest.NewRecorder()
	server.taskDraft(response, req, auth.Identity{Actor: actor}, project.ID)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "luna_disabled") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
