package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/KanterLabs/helm/internal/auth"
	"github.com/KanterLabs/helm/internal/store"
)

func progressFixture(t *testing.T) (*Server, *store.Store, store.Project, store.Task) {
	t.Helper()
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("PROG"), Name: stringPtr("Progress")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	task, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("Publish progress")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := data.ClaimTask(ctx, task.ID, "actor-disabled-mode", time.Hour, task.Version)
	if err != nil {
		t.Fatal(err)
	}
	return server, data, project, claimed
}

func progressPayload() map[string]any {
	return map[string]any{
		"operation_id":         "codex/progress-test",
		"state":                "working",
		"phase":                "Implementing",
		"summary":              "Progress is persisted.",
		"next_action":          "Run the API tests.",
		"checkpoint_refs":      []string{"api", "tests"},
		"checkpoint_completed": 1,
		"checkpoint_total":     2,
	}
}

func progressHeaders(version int64, extra map[string]string) map[string]string {
	headers := map[string]string{
		"Content-Type": "application/json",
		"If-Match":     fmt.Sprintf(`"v%d"`, version),
	}
	for name, value := range extra {
		headers[name] = value
	}
	return headers
}

func TestTaskProgressPublishesSnapshotAndIncrementsVersion(t *testing.T) {
	server, data, project, task := progressFixture(t)
	response := request(t, server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/progress", progressPayload(), progressHeaders(task.Version, nil))
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"v3"` {
		t.Fatalf("progress response = %d etag=%q body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	var updated store.Task
	if err := json.Unmarshal(response.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Version != 3 || updated.AgentWork == nil {
		t.Fatalf("updated task = %#v, want version 3 with agent work", updated)
	}
	if updated.AgentWork.State != "working" || updated.AgentWork.Summary != "Progress is persisted." || len(updated.AgentWork.CheckpointRefs) != 2 {
		t.Fatalf("agent work = %#v", updated.AgentWork)
	}
	detail := request(t, server, http.MethodGet, "/api/v1/tasks/"+task.ID, nil, nil)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"agent_work"`) {
		t.Fatalf("task detail = %d %s", detail.Code, detail.Body.String())
	}
	events, _, err := data.ListEvents(context.Background(), store.EventFilter{ProjectID: project.ID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	progressEvents, comments := 0, 0
	for _, event := range events {
		switch event.Type {
		case "task.progressed":
			progressEvents++
		case "comment.created":
			comments++
		}
	}
	if progressEvents != 1 || comments != 1 {
		t.Fatalf("progress side effects = events=%d comments=%d, want one each", progressEvents, comments)
	}
}

func TestTaskProgressValidationAndIfMatch(t *testing.T) {
	server, _, _, task := progressFixture(t)
	valid := progressPayload()
	cases := []map[string]any{
		{"state": "working", "summary": "missing operation"},
		{"operation_id": "codex/test", "state": "stale", "summary": "invalid publish state"},
		{"operation_id": "codex/test", "state": "working", "summary": "summary", "unknown": true},
		{"operation_id": "codex/test", "state": "working", "summary": nil},
		{"operation_id": "codex/test", "state": "working", "summary": "summary", "phase": nil},
		{"operation_id": "codex/test", "state": "working", "summary": "summary", "checkpoint_completed": 1},
		{"operation_id": "codex/test", "state": "working", "summary": "summary", "checkpoint_completed": 3, "checkpoint_total": 2},
		{"operation_id": "codex/test", "state": "working", "summary": "summary", "checkpoint_refs": []string{"api"}},
	}
	for _, payload := range cases {
		response := request(t, server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/progress", payload, progressHeaders(task.Version, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid progress %#v = %d %s, want 400", payload, response.Code, response.Body.String())
		}
	}
	missing := request(t, server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/progress", valid, map[string]string{"Content-Type": "application/json"})
	if missing.Code != http.StatusPreconditionRequired || errorCode(t, missing) != "if_match_required" {
		t.Fatalf("missing If-Match = %d %s, want 428 if_match_required", missing.Code, missing.Body.String())
	}
	stale := request(t, server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/progress", valid, progressHeaders(task.Version-1, nil))
	if stale.Code != http.StatusConflict || errorCode(t, stale) != "stale_task" {
		t.Fatalf("stale If-Match = %d %s, want 409 stale_task", stale.Code, stale.Body.String())
	}
}

func TestTaskProgressRequiresOwnedClaimAndProjectCeiling(t *testing.T) {
	server, data, project, task := progressFixture(t)
	other, err := data.CreateActor(context.Background(), store.Actor{Kind: "agent", Name: "other"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := data.DB.ExecContext(context.Background(), `UPDATE tasks SET claimed_by=?, claim_expires_at=? WHERE id=?`, other.ID, time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), task.ID); err != nil {
		t.Fatal(err)
	}
	response := request(t, server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/progress", progressPayload(), progressHeaders(task.Version, nil))
	if response.Code != http.StatusForbidden {
		t.Fatalf("non-owner progress = %d %s, want 403", response.Code, response.Body.String())
	}

	outside, err := data.CreateProject(context.Background(), store.ProjectInput{Key: stringPtr("OUT"), Name: stringPtr("Outside")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	outsideTask, err := data.CreateTask(context.Background(), outside.ID, store.TaskInput{Title: stringPtr("Outside task")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := data.CreateAgent(context.Background(), store.Actor{Kind: "agent", Name: "scoped", ProjectIDs: []string{project.ID}}, "actor-disabled-mode", "")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := data.CreateTokenBy(context.Background(), agent.ID, "actor-disabled-mode", "progress", []string{"tasks:claim"}, []string{project.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ceiling := request(t, server, http.MethodPost, "/api/v1/tasks/"+outsideTask.ID+"/progress", progressPayload(), map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
		"If-Match":      `"v1"`,
	})
	if ceiling.Code != http.StatusForbidden {
		t.Fatalf("project ceiling progress = %d %s, want 403", ceiling.Code, ceiling.Body.String())
	}
}

func TestTaskProgressReducedResponseAndExactIdempotentReplay(t *testing.T) {
	server, data, project, task := progressFixture(t)
	replayKey := "progress-replay"
	first := request(t, server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/progress", progressPayload(), progressHeaders(task.Version, map[string]string{"Idempotency-Key": replayKey}))
	if first.Code != http.StatusOK {
		t.Fatalf("first progress = %d %s", first.Code, first.Body.String())
	}
	replay := request(t, server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/progress", progressPayload(), progressHeaders(task.Version, map[string]string{"Idempotency-Key": replayKey}))
	if replay.Code != first.Code || replay.Body.String() != first.Body.String() || replay.Header().Get("ETag") != first.Header().Get("ETag") {
		t.Fatalf("replay differs: first=%d %s/%s replay=%d %s/%s", first.Code, first.Header().Get("ETag"), first.Body.String(), replay.Code, replay.Header().Get("ETag"), replay.Body.String())
	}
	events, _, err := data.ListEvents(context.Background(), store.EventFilter{ProjectID: project.ID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	progressEvents, comments := 0, 0
	for _, event := range events {
		if event.Type == "task.progressed" {
			progressEvents++
		}
		if event.Type == "comment.created" {
			comments++
		}
	}
	if progressEvents != 1 || comments != 1 {
		t.Fatalf("replay side effects = events=%d comments=%d", progressEvents, comments)
	}

	agent, err := data.CreateAgent(context.Background(), store.Actor{Kind: "agent", Name: "write only", ProjectIDs: []string{project.ID}}, "actor-disabled-mode", "")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := data.CreateTokenBy(context.Background(), agent.ID, "actor-disabled-mode", "claim", []string{"tasks:claim"}, []string{project.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := data.CreateTask(context.Background(), project.ID, store.TaskInput{Title: stringPtr("Reduced")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := data.ClaimTask(context.Background(), second.ID, agent.ID, time.Hour, second.Version)
	if err != nil {
		t.Fatal(err)
	}
	reduced := request(t, server, http.MethodPost, "/api/v1/tasks/"+second.ID+"/progress", progressPayload(), map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
		"If-Match":      fmt.Sprintf(`"v%d"`, claimed.Version),
	})
	if reduced.Code != http.StatusOK || reduced.Header().Get("ETag") != `"v3"` || reduced.Body.String() != fmt.Sprintf(`{"id":%q,"version":3}`, second.ID) {
		t.Fatalf("reduced progress = %d etag=%q body=%s", reduced.Code, reduced.Header().Get("ETag"), reduced.Body.String())
	}
}

func TestTaskProgressFiltersAndLiveMyWork(t *testing.T) {
	server, _, project, task := progressFixture(t)
	payload := progressPayload()
	payload["state"] = "waiting"
	response := request(t, server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/progress", payload, progressHeaders(task.Version, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("waiting progress = %d %s", response.Code, response.Body.String())
	}
	board := request(t, server, http.MethodGet, "/api/v1/projects/"+project.ID+"/tasks?agent_state=waiting&action_needed=true", nil, nil)
	if board.Code != http.StatusOK || !strings.Contains(board.Body.String(), task.ID) {
		t.Fatalf("board progress filter = %d %s", board.Code, board.Body.String())
	}
	assigned := request(t, server, http.MethodGet, "/api/v1/my-work?view=assigned&agent_state=waiting&action_needed=true", nil, nil)
	if assigned.Code != http.StatusOK || !strings.Contains(assigned.Body.String(), task.ID) {
		t.Fatalf("assigned progress filter = %d %s", assigned.Code, assigned.Body.String())
	}
	live := request(t, server, http.MethodGet, "/api/v1/my-work?view=live&agent_state=waiting&action_needed=true", nil, nil)
	if live.Code != http.StatusOK || !strings.Contains(live.Body.String(), task.ID) {
		t.Fatalf("live progress filter = %d %s", live.Code, live.Body.String())
	}
	for _, target := range []string{
		"/api/v1/projects/" + project.ID + "/tasks?agent_state=unknown",
		"/api/v1/projects/" + project.ID + "/tasks?action_needed=TRUE",
		"/api/v1/projects/" + project.ID + "/tasks?action_needed=true&action_needed=false",
		"/api/v1/my-work?view=unknown",
	} {
		invalid := request(t, server, http.MethodGet, target, nil, nil)
		if invalid.Code != http.StatusBadRequest {
			t.Fatalf("invalid progress filter %s = %d %s", target, invalid.Code, invalid.Body.String())
		}
	}
}

func TestProgressEventNarrativeRedaction(t *testing.T) {
	identity := authIdentityForProgressTest()
	events := []store.Event{{Type: "task.progressed", Payload: json.RawMessage(`{"state":"working","summary":"secret","phase":"private","next_action":"secret next","checkpoint_refs":["private"]}`)}}
	redacted := redactEventsForIdentity(identity, events)
	if strings.Contains(string(redacted[0].Payload), "secret") || strings.Contains(string(redacted[0].Payload), "private") {
		t.Fatalf("redacted progress payload leaked narrative: %s", redacted[0].Payload)
	}
	if !strings.Contains(string(redacted[0].Payload), "working") {
		t.Fatalf("redacted progress payload lost state: %s", redacted[0].Payload)
	}
}

func authIdentityForProgressTest() (identity auth.Identity) {
	identity.IsToken = true
	identity.Token.Scopes = map[string]bool{"events:read": true}
	return identity
}
