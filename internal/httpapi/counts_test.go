package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/KanterLabs/helm/internal/store"
)

func TestIssueMetricsAndSidebarCountsHonorProjectCeiling(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	owner, err := data.EnsureDisabledActor(ctx)
	if err != nil {
		t.Fatalf("ensure disabled actor: %v", err)
	}
	visible, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("METRICONE"), Name: stringPtr("Visible metrics")}, owner.ID)
	if err != nil {
		t.Fatalf("create visible project: %v", err)
	}
	hidden, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("METRICTWO"), Name: stringPtr("Hidden metrics")}, owner.ID)
	if err != nil {
		t.Fatalf("create hidden project: %v", err)
	}
	actual := "The issue still reproduces."
	createBug := func(project store.Project, title string) store.Task {
		t.Helper()
		bug, createErr := data.CreateTask(ctx, project.ID, store.TaskInput{
			Title: stringPtr(title),
			Kind:  stringPtr("bug"),
			Bug:   &store.BugInput{ActualBehavior: &actual},
		}, owner.ID)
		if createErr != nil {
			t.Fatalf("create bug %q: %v", title, createErr)
		}
		return bug
	}
	visibleBug := createBug(visible, "Visible reopened issue")
	hiddenBug := createBug(hidden, "Hidden reopened issue")
	for _, item := range []struct {
		id        string
		projectID string
		taskID    string
	}{
		{id: "visible-reopened", projectID: visible.ID, taskID: visibleBug.ID},
		{id: "hidden-reopened", projectID: hidden.ID, taskID: hiddenBug.ID},
	} {
		if _, err := data.DB.ExecContext(ctx, `INSERT INTO events(id, type, actor_id, project_id, task_id, payload, created_at) VALUES (?, 'bug.reopened', ?, ?, ?, '{}', ?)`, item.id, owner.ID, item.projectID, item.taskID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatalf("insert %s reopen event: %v", item.id, err)
		}
	}

	agent, err := data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "Metric agent"}, owner.ID, "")
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	work, err := data.CreateTask(ctx, visible.ID, store.TaskInput{Title: stringPtr("Visible live work")}, owner.ID)
	if err != nil {
		t.Fatalf("create live work: %v", err)
	}
	claimed, err := data.ClaimTask(ctx, work.ID, agent.ID, 0, work.Version)
	if err != nil {
		t.Fatalf("claim live work: %v", err)
	}
	if _, err := data.PublishAgentWork(ctx, work.ID, store.AgentWorkInput{OperationID: "counts/http", State: "working", Summary: "Count fixture"}, claimed.Version, agent.ID); err != nil {
		t.Fatalf("publish live work: %v", err)
	}
	_, token, err := data.CreateTokenBy(ctx, agent.ID, owner.ID, "metric reader", []string{"tasks:read"}, []string{visible.ID}, nil)
	if err != nil {
		t.Fatalf("create scoped token: %v", err)
	}

	metricsResponse := request(t, server, http.MethodGet, "/api/v1/issues/metrics", nil, map[string]string{"Authorization": "Bearer " + token})
	if metricsResponse.Code != http.StatusOK {
		t.Fatalf("scoped issue metrics = %d, body=%s", metricsResponse.Code, metricsResponse.Body.String())
	}
	var metrics map[string]any
	if err := json.Unmarshal(metricsResponse.Body.Bytes(), &metrics); err != nil {
		t.Fatalf("decode issue metrics: %v", err)
	}
	if metrics["reopened"] != float64(1) || metrics["window_days"] != float64(7) {
		t.Fatalf("scoped issue metrics = %#v, want one seven-day reopened issue", metrics)
	}
	if _, present := metrics["data"]; present {
		t.Fatalf("issue metrics unexpectedly returned a collection: %#v", metrics)
	}

	countsResponse := request(t, server, http.MethodGet, "/api/v1/sidebar-counts", nil, map[string]string{"Authorization": "Bearer " + token})
	if countsResponse.Code != http.StatusOK {
		t.Fatalf("scoped sidebar counts = %d, body=%s", countsResponse.Code, countsResponse.Body.String())
	}
	var counts struct {
		Issues int    `json:"issues"`
		MyWork int    `json:"my_work"`
		View   string `json:"view"`
	}
	if err := json.Unmarshal(countsResponse.Body.Bytes(), &counts); err != nil {
		t.Fatalf("decode sidebar counts: %v", err)
	}
	if counts.Issues != 1 || counts.MyWork != 1 || counts.View != "live" {
		t.Fatalf("scoped sidebar counts = %+v, want one issue/live task", counts)
	}

	for _, target := range []string{"/api/v1/issues/metrics?project=" + hidden.ID, "/api/v1/sidebar-counts?project=" + hidden.ID} {
		response := request(t, server, http.MethodGet, target, nil, map[string]string{"Authorization": "Bearer " + token})
		if response.Code != http.StatusForbidden {
			t.Fatalf("scoped request outside ceiling %s = %d, body=%s", target, response.Code, response.Body.String())
		}
	}

	globalMetrics := request(t, server, http.MethodGet, "/api/v1/issues/metrics", nil, nil)
	if globalMetrics.Code != http.StatusOK {
		t.Fatalf("global issue metrics = %d, body=%s", globalMetrics.Code, globalMetrics.Body.String())
	}
	var globalMetricsBody struct {
		Reopened int `json:"reopened"`
	}
	if err := json.Unmarshal(globalMetrics.Body.Bytes(), &globalMetricsBody); err != nil {
		t.Fatalf("decode global issue metrics: %v", err)
	}
	if globalMetricsBody.Reopened != 2 {
		t.Fatalf("global issue metrics = %+v, want two reopened issues", globalMetricsBody)
	}
	globalCounts := request(t, server, http.MethodGet, "/api/v1/sidebar-counts?view=assigned", nil, nil)
	if globalCounts.Code != http.StatusOK {
		t.Fatalf("global assigned sidebar counts = %d, body=%s", globalCounts.Code, globalCounts.Body.String())
	}
	var globalCountsBody struct {
		Issues int    `json:"issues"`
		View   string `json:"view"`
	}
	if err := json.Unmarshal(globalCounts.Body.Bytes(), &globalCountsBody); err != nil {
		t.Fatalf("decode global sidebar counts: %v", err)
	}
	if globalCountsBody.Issues != 2 || globalCountsBody.View != "assigned" {
		t.Fatalf("global assigned sidebar counts = %+v, want two issues and assigned view", globalCountsBody)
	}
}
