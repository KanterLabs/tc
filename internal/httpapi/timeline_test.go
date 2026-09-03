package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/KanterLabs/helm/internal/store"
)

func TestTaskTimelineHTTPContractAndPagination(t *testing.T) {
	server, data, project, task := progressFixture(t)
	progress := request(t, server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/progress", progressPayload(), progressHeaders(task.Version, nil))
	if progress.Code != http.StatusOK {
		t.Fatalf("publish progress = %d %s", progress.Code, progress.Body.String())
	}
	if comment := request(t, server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/comments", map[string]any{"body": "ordinary comment"}, nil); comment.Code != http.StatusCreated {
		t.Fatalf("create comment = %d %s", comment.Code, comment.Body.String())
	}

	first := request(t, server, http.MethodGet, "/api/v1/tasks/"+task.ID+"/timeline?limit=1", nil, nil)
	if first.Code != http.StatusOK {
		t.Fatalf("first timeline page = %d %s", first.Code, first.Body.String())
	}
	var firstBody struct {
		Data       []store.TaskTimelineItem `json:"data"`
		NextCursor string                   `json:"next_cursor"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstBody); err != nil {
		t.Fatalf("decode first timeline page: %v", err)
	}
	if len(firstBody.Data) != 1 || firstBody.NextCursor == "" || firstBody.Data[0].Cursor == "" {
		t.Fatalf("first timeline page = %#v", firstBody)
	}

	second := request(t, server, http.MethodGet, "/api/v1/tasks/"+task.ID+"/timeline?limit=10&before="+firstBody.NextCursor, nil, nil)
	if second.Code != http.StatusOK {
		t.Fatalf("second timeline page = %d %s", second.Code, second.Body.String())
	}
	var secondBody struct {
		Data       []store.TaskTimelineItem `json:"data"`
		NextCursor string                   `json:"next_cursor"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondBody); err != nil {
		t.Fatalf("decode second timeline page: %v", err)
	}
	if len(secondBody.Data) == 0 || secondBody.Data[0].ID == firstBody.Data[0].ID {
		t.Fatalf("second page repeated first item: %#v", secondBody)
	}
	for _, item := range secondBody.Data {
		if item.Kind == "comment" && item.Comment != nil && item.Comment.Body == "Progress is persisted.\n\nNext: Run the API tests." {
			t.Fatalf("generated progress comment leaked into timeline: %+v", item)
		}
		if item.Kind == "task_change" && item.Change != nil && (item.Change.EventType == "comment.created" || item.Change.EventType == "task.progressed") {
			t.Fatalf("duplicate event leaked into timeline: %+v", item)
		}
	}

	filtered := request(t, server, http.MethodGet, "/api/v1/tasks/"+task.ID+"/timeline?kind=agent_progress", nil, nil)
	if filtered.Code != http.StatusOK {
		t.Fatalf("progress timeline filter = %d %s", filtered.Code, filtered.Body.String())
	}
	var filteredBody struct {
		Data []store.TaskTimelineItem `json:"data"`
	}
	if err := json.Unmarshal(filtered.Body.Bytes(), &filteredBody); err != nil {
		t.Fatal(err)
	}
	if len(filteredBody.Data) != 1 || filteredBody.Data[0].Kind != "agent_progress" || filteredBody.Data[0].Progress == nil {
		t.Fatalf("progress filter = %#v", filteredBody.Data)
	}

	invalid := request(t, server, http.MethodGet, "/api/v1/tasks/"+task.ID+"/timeline?kind=unknown", nil, nil)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid kind = %d %s", invalid.Code, invalid.Body.String())
	}
	invalid = request(t, server, http.MethodGet, "/api/v1/tasks/"+task.ID+"/timeline?before=bad-cursor", nil, nil)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid before = %d %s", invalid.Code, invalid.Body.String())
	}

	// The route accepts task keys as references and keeps project-scoped token
	// authorization on the same project boundary as task detail/comments.
	byKey := request(t, server, http.MethodGet, "/api/v1/tasks/"+project.Key+"-1/timeline", nil, nil)
	if byKey.Code != http.StatusOK {
		t.Fatalf("timeline by task key = %d %s", byKey.Code, byKey.Body.String())
	}
	_ = data
}

func TestTaskTimelineHTTPRequiresTaskReadAndProjectScope(t *testing.T) {
	server, data, project, task := progressFixture(t)
	ctx := context.Background()
	agent, err := data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "Timeline reader", ProjectIDs: []string{project.ID}}, "actor-disabled-mode", "")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := data.CreateTokenBy(ctx, agent.ID, "actor-disabled-mode", "timeline", []string{"events:read"}, []string{project.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	withoutTaskRead := request(t, server, http.MethodGet, "/api/v1/tasks/"+task.ID+"/timeline", nil, map[string]string{"Authorization": "Bearer " + token})
	if withoutTaskRead.Code != http.StatusForbidden || errorCode(t, withoutTaskRead) != "insufficient_scope" {
		t.Fatalf("timeline without tasks:read = %d %s", withoutTaskRead.Code, withoutTaskRead.Body.String())
	}

	outside, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("TLOUT"), Name: stringPtr("Timeline outside")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	outsideTask, err := data.CreateTask(ctx, outside.ID, store.TaskInput{Title: stringPtr("Outside timeline")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	_, scopedToken, err := data.CreateTokenBy(ctx, agent.ID, "actor-disabled-mode", "timeline-read", []string{"tasks:read"}, []string{project.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	denied := request(t, server, http.MethodGet, "/api/v1/tasks/"+outsideTask.ID+"/timeline", nil, map[string]string{"Authorization": "Bearer " + scopedToken})
	if denied.Code != http.StatusForbidden {
		t.Fatalf("outside-project timeline = %d %s", denied.Code, denied.Body.String())
	}

	// A deleted task remains unavailable even though its historical event rows
	// may have existed before deletion.
	deleted := request(t, server, http.MethodDelete, "/api/v1/tasks/"+task.ID, nil, map[string]string{"If-Match": fmt.Sprintf(`"v%d"`, task.Version)})
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete timeline task = %d %s", deleted.Code, deleted.Body.String())
	}
	gone := request(t, server, http.MethodGet, "/api/v1/tasks/"+task.ID+"/timeline", nil, nil)
	if gone.Code != http.StatusNotFound {
		t.Fatalf("deleted timeline task = %d %s", gone.Code, gone.Body.String())
	}
}

func TestProjectTimelineHTTPMergesTasksAndPaginates(t *testing.T) {
	server, data, project, firstTask := progressFixture(t)
	firstProgress := request(t, server, http.MethodPost, "/api/v1/tasks/"+firstTask.ID+"/progress", progressPayload(), progressHeaders(firstTask.Version, nil))
	if firstProgress.Code != http.StatusOK {
		t.Fatalf("publish first project progress = %d %s", firstProgress.Code, firstProgress.Body.String())
	}
	secondTask, err := data.CreateTask(context.Background(), project.ID, store.TaskInput{Title: stringPtr("Second project task")}, "actor-disabled-mode")
	if err != nil {
		t.Fatalf("create second project task: %v", err)
	}
	claimed, err := data.ClaimTask(context.Background(), secondTask.ID, "actor-disabled-mode", 0, secondTask.Version)
	if err != nil {
		t.Fatalf("claim second project task: %v", err)
	}
	progress := request(t, server, http.MethodPost, "/api/v1/tasks/"+secondTask.ID+"/progress", map[string]any{
		"operation_id": "project-timeline/second",
		"state":        "working",
		"summary":      "Second project progress",
	}, progressHeaders(claimed.Version, nil))
	if progress.Code != http.StatusOK {
		t.Fatalf("publish second project progress = %d %s", progress.Code, progress.Body.String())
	}

	type timelinePage struct {
		Data       []store.TaskTimelineItem `json:"data"`
		NextCursor string                   `json:"next_cursor"`
	}
	readPage := func(target string) timelinePage {
		t.Helper()
		response := request(t, server, http.MethodGet, target, nil, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("project timeline %s = %d %s", target, response.Code, response.Body.String())
		}
		var page timelinePage
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode project timeline page: %v", err)
		}
		return page
	}

	first := readPage("/api/v1/projects/" + project.Key + "/timeline?limit=1")
	if len(first.Data) != 1 || first.NextCursor == "" || first.Data[0].Cursor == "" {
		t.Fatalf("first project timeline page = %#v", first)
	}
	seen := make(map[string]bool)
	allItems := append([]store.TaskTimelineItem(nil), first.Data...)
	for _, item := range first.Data {
		seen[item.TaskID] = true
	}
	before := first.NextCursor
	for page := 0; page < 100 && before != ""; page++ {
		next := readPage("/api/v1/projects/" + project.ID + "/timeline?limit=2&before=" + before)
		allItems = append(allItems, next.Data...)
		for _, item := range next.Data {
			seen[item.TaskID] = true
			if item.Cursor == "" {
				t.Fatalf("project timeline item has empty cursor: %+v", item)
			}
			if item.Kind == "comment" && item.Comment != nil && item.Comment.Body == "Progress is persisted.\n\nNext: Run the API tests." {
				t.Fatalf("generated progress comment leaked into project timeline: %+v", item)
			}
			if item.Kind == "task_change" && item.Change != nil && (item.Change.EventType == "comment.created" || item.Change.EventType == "task.progressed") {
				t.Fatalf("duplicate project change leaked into timeline: %+v", item)
			}
		}
		before = next.NextCursor
	}
	if before != "" {
		t.Fatal("project timeline pagination did not terminate")
	}
	if !seen[firstTask.ID] || !seen[secondTask.ID] {
		t.Fatalf("project timeline task IDs = %v, want both active tasks", seen)
	}
	if len(allItems) < 2 {
		t.Fatalf("project timeline returned %d items, want activity from both tasks", len(allItems))
	}

	filtered := readPage("/api/v1/projects/" + project.ID + "/timeline?kind=agent_progress")
	if len(filtered.Data) != 2 {
		t.Fatalf("project progress filter = %#v, want one pulse per task", filtered.Data)
	}
	for _, item := range filtered.Data {
		if item.Kind != "agent_progress" || item.Progress == nil {
			t.Fatalf("project progress filter item = %+v", item)
		}
	}

	invalid := request(t, server, http.MethodGet, "/api/v1/projects/"+project.ID+"/timeline?kind=unknown", nil, nil)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("project timeline invalid kind = %d %s", invalid.Code, invalid.Body.String())
	}
	invalid = request(t, server, http.MethodGet, "/api/v1/projects/"+project.ID+"/timeline?before=bad-cursor", nil, nil)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("project timeline invalid before = %d %s", invalid.Code, invalid.Body.String())
	}
}

func TestProjectTimelineHTTPRequiresTaskReadAndProjectScope(t *testing.T) {
	server, data, project, _ := progressFixture(t)
	ctx := context.Background()
	agent, err := data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "Project timeline reader", ProjectIDs: []string{project.ID}}, "actor-disabled-mode", "")
	if err != nil {
		t.Fatal(err)
	}
	_, eventsToken, err := data.CreateTokenBy(ctx, agent.ID, "actor-disabled-mode", "timeline-events", []string{"events:read"}, []string{project.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	withoutTaskRead := request(t, server, http.MethodGet, "/api/v1/projects/"+project.ID+"/timeline", nil, map[string]string{"Authorization": "Bearer " + eventsToken})
	if withoutTaskRead.Code != http.StatusForbidden || errorCode(t, withoutTaskRead) != "insufficient_scope" {
		t.Fatalf("project timeline without tasks:read = %d %s", withoutTaskRead.Code, withoutTaskRead.Body.String())
	}

	_, scopedToken, err := data.CreateTokenBy(ctx, agent.ID, "actor-disabled-mode", "timeline-tasks", []string{"tasks:read"}, []string{project.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	allowed := request(t, server, http.MethodGet, "/api/v1/projects/"+project.Key+"/timeline", nil, map[string]string{"Authorization": "Bearer " + scopedToken})
	if allowed.Code != http.StatusOK {
		t.Fatalf("project-scoped timeline = %d %s", allowed.Code, allowed.Body.String())
	}

	outside, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("PTLOUT"), Name: stringPtr("Project timeline outside")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	denied := request(t, server, http.MethodGet, "/api/v1/projects/"+outside.ID+"/timeline", nil, map[string]string{"Authorization": "Bearer " + scopedToken})
	if denied.Code != http.StatusForbidden {
		t.Fatalf("outside-project timeline = %d %s", denied.Code, denied.Body.String())
	}
}
