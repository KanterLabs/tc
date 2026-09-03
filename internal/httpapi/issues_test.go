package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/KanterLabs/helm/internal/store"
)

func TestIssueLifecycleHTTP(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("BUG"), Name: stringPtr("Bug tracker")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "Triage agent"}, "actor-disabled-mode", "")
	if err != nil {
		t.Fatal(err)
	}
	columns, err := data.ListColumns(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	ready, completed := stateColumnForTest(t, columns, "ready"), stateColumnForTest(t, columns, "completed")

	createdResponse := request(t, server, http.MethodPost, "/api/v1/projects/BUG/tasks", map[string]any{
		"title": "Preview cannot authenticate",
		"kind":  "bug",
		"bug": map[string]any{
			"actual_behavior":   "The preview asks for authentication repeatedly.",
			"expected_behavior": "Authenticate once and retain the session.",
			"environment":       "preview",
		},
	}, map[string]string{"Content-Type": "application/json"})
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create bug = %d, body=%s", createdResponse.Code, createdResponse.Body.String())
	}
	var bug store.Task
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &bug); err != nil {
		t.Fatal(err)
	}
	if bug.Kind != "bug" || bug.Bug == nil || bug.Bug.ReporterID != "actor-disabled-mode" || bug.Bug.ActualBehavior == "" {
		t.Fatalf("created bug shape = %#v", bug)
	}
	genericComplete := request(t, server, http.MethodPost, "/api/v1/tasks/"+bug.ID+"/complete", map[string]any{}, map[string]string{"Content-Type": "application/json", "If-Match": `"v1"`})
	if genericComplete.Code != http.StatusBadRequest {
		t.Fatalf("generic bug completion = %d, body=%s", genericComplete.Code, genericComplete.Body.String())
	}

	triaged := request(t, server, http.MethodPost, "/api/v1/tasks/"+bug.ID+"/triage", map[string]any{
		"severity": "s2", "priority": "urgent", "assignee": agent.ID, "column_id": ready.ID,
	}, map[string]string{"Content-Type": "application/json", "If-Match": `"v1"`})
	if triaged.Code != http.StatusOK || triaged.Header().Get("ETag") != `"v2"` {
		t.Fatalf("triage bug = %d etag=%q body=%s", triaged.Code, triaged.Header().Get("ETag"), triaged.Body.String())
	}
	if err := json.Unmarshal(triaged.Body.Bytes(), &bug); err != nil {
		t.Fatal(err)
	}
	if bug.Bug == nil || bug.Bug.Severity == nil || *bug.Bug.Severity != "s2" || bug.Priority != "urgent" || bug.Assignee == nil || *bug.Assignee != agent.ID || bug.ColumnID != ready.ID {
		t.Fatalf("triaged bug = %#v", bug)
	}

	resolved := request(t, server, http.MethodPost, "/api/v1/tasks/"+bug.ID+"/resolve", map[string]any{
		"resolution": "fixed", "note": "Verified against the authenticated preview.",
	}, map[string]string{"Content-Type": "application/json", "If-Match": `"v2"`})
	if resolved.Code != http.StatusOK || resolved.Header().Get("ETag") != `"v3"` {
		t.Fatalf("resolve bug = %d etag=%q body=%s", resolved.Code, resolved.Header().Get("ETag"), resolved.Body.String())
	}
	if err := json.Unmarshal(resolved.Body.Bytes(), &bug); err != nil {
		t.Fatal(err)
	}
	if bug.Bug == nil || bug.Bug.Resolution == nil || *bug.Bug.Resolution != "fixed" || bug.Bug.ResolvedBy == nil || bug.Bug.ResolvedAt == nil || bug.ColumnID != completed.ID || bug.CommentCount != 1 {
		t.Fatalf("resolved bug = %#v", bug)
	}

	reopened := request(t, server, http.MethodPost, "/api/v1/tasks/"+bug.ID+"/reopen", map[string]any{
		"reason": "The regression returned in the next preview build.",
	}, map[string]string{"Content-Type": "application/json", "If-Match": `"v3"`})
	if reopened.Code != http.StatusOK || reopened.Header().Get("ETag") != `"v4"` {
		t.Fatalf("reopen bug = %d etag=%q body=%s", reopened.Code, reopened.Header().Get("ETag"), reopened.Body.String())
	}
	bug = store.Task{}
	if err := json.Unmarshal(reopened.Body.Bytes(), &bug); err != nil {
		t.Fatal(err)
	}
	if bug.Bug == nil || bug.Bug.Resolution != nil || bug.CompletedAt != nil || bug.CommentCount != 2 {
		t.Fatalf("reopened bug = %#v", bug)
	}
}

func TestGlobalIssuesHonorsProjectCeilingAndFilters(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	first, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("ONE"), Name: stringPtr("One")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	second, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("TWO"), Name: stringPtr("Two")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	actual := "broken"
	severity := "s1"
	for _, item := range []struct {
		project store.Project
		title   string
	}{
		{first, "Visible outage"},
		{second, "Hidden outage"},
	} {
		if _, err := data.CreateTask(ctx, item.project.ID, store.TaskInput{Title: &item.title, Kind: stringPtr("bug"), Bug: &store.BugInput{ActualBehavior: &actual, Severity: &severity}}, "actor-disabled-mode"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := data.CreateTask(ctx, first.ID, store.TaskInput{Title: stringPtr("Ordinary task")}, "actor-disabled-mode"); err != nil {
		t.Fatal(err)
	}
	agent, err := data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "Issue reader"}, "actor-disabled-mode", "")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := data.CreateTokenBy(ctx, agent.ID, "actor-disabled-mode", "issues", []string{"tasks:read"}, []string{first.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	response := request(t, server, http.MethodGet, "/api/v1/issues?severity=s1&q=outage", nil, map[string]string{"Authorization": "Bearer " + token})
	if response.Code != http.StatusOK {
		t.Fatalf("global issues = %d, body=%s", response.Code, response.Body.String())
	}
	var collection struct {
		Data []store.Task `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &collection); err != nil {
		t.Fatal(err)
	}
	if len(collection.Data) != 1 || collection.Data[0].ProjectID != first.ID || collection.Data[0].Kind != "bug" {
		t.Fatalf("scoped issue collection = %#v", collection.Data)
	}
	invalid := request(t, server, http.MethodGet, "/api/v1/issues?severity=critical", nil, map[string]string{"Authorization": "Bearer " + token})
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid severity = %d, body=%s", invalid.Code, invalid.Body.String())
	}
}

func TestGlobalIssuesLivenessFiltersExcludeCompletedSnapshots(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	actor, err := data.EnsureDisabledActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("DONEWORK"), Name: stringPtr("Completed work")}, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	actual := "The old pulse remains stored."
	bug, err := data.CreateTask(ctx, project.ID, store.TaskInput{
		Title: stringPtr("Completed issue with retained pulse"),
		Kind:  stringPtr("bug"),
		Bug:   &store.BugInput{ActualBehavior: &actual},
	}, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := data.ClaimTask(ctx, bug.ID, actor.ID, 0, bug.Version)
	if err != nil {
		t.Fatal(err)
	}
	published, err := data.PublishAgentWork(ctx, bug.ID, store.AgentWorkInput{
		OperationID: "issues/completed-filter",
		State:       "handoff",
		Summary:     "Retain this snapshot as history.",
	}, claimed.Version, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := data.ResolveBug(ctx, bug.ID, store.ResolveBugInput{Resolution: "fixed"}, published.Version, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.AgentWork == nil || completed.AgentWork.ActionNeeded || completed.AgentWork.Stale {
		t.Fatalf("completed work = %+v, want retained inactive snapshot", completed.AgentWork)
	}

	for _, query := range []string{"agent_state=handoff", "action_needed=true"} {
		response := request(t, server, http.MethodGet, "/api/v1/issues?"+query, nil, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("issues %s = %d, body=%s", query, response.Code, response.Body.String())
		}
		var collection struct {
			Data []store.Task `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &collection); err != nil {
			t.Fatal(err)
		}
		if len(collection.Data) != 0 {
			t.Fatalf("issues %s included completed task: %#v", query, collection.Data)
		}
	}
}

func stateColumnForTest(t *testing.T, columns []store.Column, state string) store.Column {
	t.Helper()
	for _, column := range columns {
		if column.SemanticState == state {
			return column
		}
	}
	t.Fatalf("missing %s column", state)
	return store.Column{}
}
