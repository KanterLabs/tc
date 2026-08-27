package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"roadmap/internal/store"
)

func TestClaimBlocksGenericTaskMutationAndExpiredClaimsDoNot(t *testing.T) {
	_, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("MUTATE"), Name: stringPtr("Claim mutations")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := data.CreateActor(ctx, store.Actor{Kind: "agent", Name: "owner"}, "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := data.CreateActor(ctx, store.Actor{Kind: "agent", Name: "other"}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("original")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := data.ClaimTask(ctx, task.ID, owner.ID, time.Hour, task.Version)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := data.UpdateTask(ctx, task.ID, store.TaskInput{Title: stringPtr("blocked update")}, claimed.Version, other.ID); !errors.Is(err, store.ErrClaimUnavailable) {
		t.Fatalf("update under another actor's active claim = %v, want ErrClaimUnavailable", err)
	}
	if err := data.DeleteTask(ctx, task.ID, claimed.Version, other.ID); !errors.Is(err, store.ErrClaimUnavailable) {
		t.Fatalf("delete under another actor's active claim = %v, want ErrClaimUnavailable", err)
	}
	unchanged, err := data.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Title != "original" || unchanged.Version != claimed.Version {
		t.Fatalf("active claim mutation changed task: title=%q version=%d", unchanged.Title, unchanged.Version)
	}

	if _, err := data.DB.ExecContext(ctx, `UPDATE tasks SET claim_expires_at=? WHERE id=?`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), task.ID); err != nil {
		t.Fatal(err)
	}
	updated, err := data.UpdateTask(ctx, task.ID, store.TaskInput{Title: stringPtr("expired claim allowed")}, claimed.Version, other.ID)
	if err != nil {
		t.Fatalf("update after claim expiry: %v", err)
	}
	if updated.Title != "expired claim allowed" {
		t.Fatalf("expired-claim update title = %q", updated.Title)
	}
}

func TestBearerTaskActionsRequireActiveOwnedClaim(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("ACTIONS"), Name: stringPtr("Claim actions")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "action agent", ProjectIDs: []string{project.ID}}, "actor-disabled-mode", "")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := data.CreateTokenBy(ctx, agent.ID, "actor-disabled-mode", "actions", []string{"tasks:claim"}, []string{project.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, action := range []string{"complete", "block", "renew", "release"} {
		task, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr(action)}, "actor-disabled-mode")
		if err != nil {
			t.Fatal(err)
		}
		response := request(t, server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/"+action, nil, map[string]string{
			"Authorization": "Bearer " + token,
			"If-Match":      `"v1"`,
		})
		if response.Code == http.StatusOK {
			t.Fatalf("unclaimed bearer %s status = 200, body=%s", action, response.Body.String())
		}
		current, err := data.GetTask(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Version != task.Version || current.Title != action || current.CompletedAt != nil {
			t.Fatalf("unclaimed bearer %s mutated task: version=%d title=%q completed=%v", action, current.Version, current.Title, current.CompletedAt)
		}
	}

	ownedTask, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("owned")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	ownedClaim, err := data.ClaimTask(ctx, ownedTask.ID, agent.ID, time.Hour, ownedTask.Version)
	if err != nil {
		t.Fatal(err)
	}
	ownedResponse := request(t, server, http.MethodPost, "/api/v1/tasks/"+ownedTask.ID+"/complete", nil, map[string]string{
		"Authorization": "Bearer " + token,
		"If-Match":      fmt.Sprintf(`"v%d"`, ownedClaim.Version),
	})
	if ownedResponse.Code != http.StatusOK {
		t.Fatalf("owned bearer claim complete status = %d, body=%s", ownedResponse.Code, ownedResponse.Body.String())
	}

	task, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("expired")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := data.ClaimTask(ctx, task.ID, agent.ID, time.Hour, task.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := data.DB.ExecContext(ctx, `UPDATE tasks SET claim_expires_at=? WHERE id=?`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), task.ID); err != nil {
		t.Fatal(err)
	}
	response := request(t, server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/complete", nil, map[string]string{
		"Authorization": "Bearer " + token,
		"If-Match":      `"v2"`,
	})
	if response.Code == http.StatusOK {
		t.Fatalf("expired bearer claim complete status = 200, body=%s", response.Body.String())
	}
	current, err := data.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Version != claimed.Version || current.CompletedAt != nil {
		t.Fatalf("expired bearer claim mutated task: version=%d completed=%v", current.Version, current.CompletedAt)
	}

	// Human UI actions remain available without a preclaim.
	humanTask, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("human")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	humanResponse := request(t, server, http.MethodPost, "/api/v1/tasks/"+humanTask.ID+"/complete", nil, map[string]string{"If-Match": `"v1"`})
	if humanResponse.Code != http.StatusOK {
		t.Fatalf("human unclaimed complete status = %d, body=%s", humanResponse.Code, humanResponse.Body.String())
	}
}

func TestScopedProjectAndEventPagesFilterBeforeLimit(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	disallowed, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("AAA"), Name: stringPtr("A disallowed")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("ZZZ"), Name: stringPtr("Z allowed")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	if disallowed.ID == allowed.ID {
		t.Fatal("test projects unexpectedly share an ID")
	}
	agent, err := data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "scoped", ProjectIDs: []string{allowed.ID}}, "actor-disabled-mode", "")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := data.CreateTokenBy(ctx, agent.ID, "actor-disabled-mode", "scoped", []string{"projects:read", "events:read"}, []string{allowed.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"Authorization": "Bearer " + token}

	projectsResponse := request(t, server, http.MethodGet, "/api/v1/projects?limit=1", nil, headers)
	if projectsResponse.Code != http.StatusOK {
		t.Fatalf("scoped projects status = %d, body=%s", projectsResponse.Code, projectsResponse.Body.String())
	}
	var projects struct {
		Data       []store.Project `json:"data"`
		NextCursor string          `json:"next_cursor"`
	}
	if err := json.Unmarshal(projectsResponse.Body.Bytes(), &projects); err != nil {
		t.Fatal(err)
	}
	if len(projects.Data) != 1 || projects.Data[0].ID != allowed.ID {
		t.Fatalf("scoped project page = %+v, want only allowed project", projects.Data)
	}
	if projects.NextCursor != "" {
		t.Fatalf("scoped project page unexpectedly has next cursor %q", projects.NextCursor)
	}

	eventsResponse := request(t, server, http.MethodGet, "/api/v1/events?after=0&limit=1", nil, headers)
	if eventsResponse.Code != http.StatusOK {
		t.Fatalf("scoped events status = %d, body=%s", eventsResponse.Code, eventsResponse.Body.String())
	}
	var events struct {
		Data       []store.Event `json:"data"`
		NextCursor string        `json:"next_cursor"`
	}
	if err := json.Unmarshal(eventsResponse.Body.Bytes(), &events); err != nil {
		t.Fatal(err)
	}
	if len(events.Data) != 1 || events.Data[0].ProjectID == nil || *events.Data[0].ProjectID != allowed.ID {
		t.Fatalf("scoped event page = %+v, want allowed project event", events.Data)
	}
	if events.NextCursor != "" {
		t.Fatalf("scoped event page unexpectedly has next cursor %q", events.NextCursor)
	}
}

func TestProjectPaginationFetchesSentinelAtMaximumLimit(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 201; i++ {
		key := fmt.Sprintf("P%03d", i)
		name := fmt.Sprintf("Project %03d", i)
		if _, err := data.CreateProject(ctx, store.ProjectInput{Key: &key, Name: &name}, "actor-disabled-mode"); err != nil {
			t.Fatalf("create project %d: %v", i, err)
		}
	}

	firstResponse := request(t, server, http.MethodGet, "/api/v1/projects?limit=200", nil, nil)
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("first maximum project page status = %d, body=%s", firstResponse.Code, firstResponse.Body.String())
	}
	var first struct {
		Data       []store.Project `json:"data"`
		NextCursor string          `json:"next_cursor"`
	}
	if err := json.Unmarshal(firstResponse.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Data) != 200 || first.NextCursor == "" {
		t.Fatalf("first maximum project page rows=%d cursor=%q, want 200 rows and a cursor", len(first.Data), first.NextCursor)
	}

	secondResponse := request(t, server, http.MethodGet, "/api/v1/projects?limit=200&cursor="+first.NextCursor, nil, nil)
	if secondResponse.Code != http.StatusOK {
		t.Fatalf("second maximum project page status = %d, body=%s", secondResponse.Code, secondResponse.Body.String())
	}
	var second struct {
		Data       []store.Project `json:"data"`
		NextCursor string          `json:"next_cursor"`
	}
	if err := json.Unmarshal(secondResponse.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Data) != 1 || second.NextCursor != "" {
		t.Fatalf("second maximum project page rows=%d cursor=%q, want one final row", len(second.Data), second.NextCursor)
	}
}

func TestMyWorkOmitsExpiredUnassignedClaims(t *testing.T) {
	_, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("MYWORK"), Name: stringPtr("My work claims")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	actor, err := data.CreateActor(ctx, store.Actor{Kind: "agent", Name: "my work actor"}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("expired only")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := data.ClaimTask(ctx, task.ID, actor.ID, time.Hour, task.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := data.DB.ExecContext(ctx, `UPDATE tasks SET claim_expires_at=? WHERE id=?`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), task.ID); err != nil {
		t.Fatal(err)
	}
	work, _, err := data.ListMyWork(ctx, actor.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(work) != 0 {
		t.Fatalf("expired unassigned claim still appears in my work: %+v", work)
	}
}

func TestTaskEditPreservesCompletedAtUntilLeavingCompleted(t *testing.T) {
	_, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("COMPLETE"), Name: stringPtr("Completion timestamps")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	columns, err := data.ListColumns(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	var backlog, completed store.Column
	for _, column := range columns {
		switch column.SemanticState {
		case "backlog":
			backlog = column
		case "completed":
			completed = column
		}
	}
	task, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("timestamps")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	completedColumnID := completed.ID
	entered, err := data.UpdateTask(ctx, task.ID, store.TaskInput{ColumnID: &completedColumnID}, task.Version, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	if entered.CompletedAt == nil {
		t.Fatal("entering completed column did not set completed_at")
	}
	completedAt := *entered.CompletedAt
	edited, err := data.UpdateTask(ctx, task.ID, store.TaskInput{Title: stringPtr("edited")}, entered.Version, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	if edited.CompletedAt == nil || *edited.CompletedAt != completedAt {
		t.Fatalf("editing completed task changed completed_at from %q to %v", completedAt, edited.CompletedAt)
	}
	backlogColumnID := backlog.ID
	left, err := data.UpdateTask(ctx, task.ID, store.TaskInput{ColumnID: &backlogColumnID}, edited.Version, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	if left.CompletedAt != nil {
		t.Fatalf("leaving completed column retained completed_at %q", *left.CompletedAt)
	}
}

func TestCreateTaskInCompletedColumnSetsCompletedAt(t *testing.T) {
	_, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("CREATECOMPLETE"), Name: stringPtr("Create completed task")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	columns, err := data.ListColumns(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	var backlog, completed store.Column
	for _, column := range columns {
		switch column.SemanticState {
		case "backlog":
			backlog = column
		case "completed":
			completed = column
		}
	}
	if backlog.ID == "" || completed.ID == "" {
		t.Fatal("default board columns are missing")
	}
	completedColumnID := completed.ID
	created, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("already complete"), ColumnID: &completedColumnID}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	if created.CompletedAt == nil {
		t.Fatal("creating in completed column did not set completed_at")
	}
	completedAt := *created.CompletedAt
	edited, err := data.UpdateTask(ctx, created.ID, store.TaskInput{Title: stringPtr("renamed complete")}, created.Version, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	if edited.CompletedAt == nil || *edited.CompletedAt != completedAt {
		t.Fatalf("title-only edit changed completed_at from %q to %v", completedAt, edited.CompletedAt)
	}

	backlogColumnID := backlog.ID
	open, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("not complete"), ColumnID: &backlogColumnID}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	if open.CompletedAt != nil {
		t.Fatalf("creating in backlog set completed_at to %q", *open.CompletedAt)
	}
}

func TestColumnSemanticStateUpdatesTaskCompletionAndVersion(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("COLUMNSTATE"), Name: stringPtr("Column state")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	columns, err := data.ListColumns(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	var backlog store.Column
	for _, column := range columns {
		if column.SemanticState == "backlog" {
			backlog = column
			break
		}
	}
	if backlog.ID == "" {
		t.Fatal("default backlog column is missing")
	}
	task, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("reclassify")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	if task.CompletedAt != nil || task.Version != 1 {
		t.Fatalf("initial task = version %d completed_at %v, want v1 and nil", task.Version, task.CompletedAt)
	}

	completedResponse := request(t, server, http.MethodPatch, "/api/v1/columns/"+backlog.ID, map[string]any{"semantic_state": "completed"}, map[string]string{"Content-Type": "application/json"})
	if completedResponse.Code != http.StatusOK {
		t.Fatalf("backlog to completed status = %d, body=%s", completedResponse.Code, completedResponse.Body.String())
	}
	completedTask, err := data.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completedTask.Version != task.Version+1 {
		t.Fatalf("completed task version = %d, want %d", completedTask.Version, task.Version+1)
	}
	if completedTask.CompletedAt == nil {
		t.Fatal("backlog to completed did not set completed_at")
	}
	completedAt := *completedTask.CompletedAt
	if completedTask.UpdatedAt == task.UpdatedAt {
		t.Fatalf("completed task updated_at did not change: %q", completedTask.UpdatedAt)
	}
	if got := request(t, server, http.MethodGet, "/api/v1/tasks/"+task.ID, nil, nil).Header().Get("ETag"); got != `"v2"` {
		t.Fatalf("completed task ETag = %q, want %q", got, `"v2"`)
	}

	backlogResponse := request(t, server, http.MethodPatch, "/api/v1/columns/"+backlog.ID, map[string]any{"semantic_state": "backlog"}, map[string]string{"Content-Type": "application/json"})
	if backlogResponse.Code != http.StatusOK {
		t.Fatalf("completed to backlog status = %d, body=%s", backlogResponse.Code, backlogResponse.Body.String())
	}
	openTask, err := data.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if openTask.Version != completedTask.Version+1 {
		t.Fatalf("reopened task version = %d, want %d", openTask.Version, completedTask.Version+1)
	}
	if openTask.CompletedAt != nil {
		t.Fatalf("completed to backlog retained completed_at %q", *openTask.CompletedAt)
	}
	if completedAt == "" {
		t.Fatal("completed timestamp was empty")
	}
	if got := request(t, server, http.MethodGet, "/api/v1/tasks/"+task.ID, nil, nil).Header().Get("ETag"); got != `"v3"` {
		t.Fatalf("reopened task ETag = %q, want %q", got, `"v3"`)
	}
}

func TestBearerColumnReclassificationRejectsActiveClaim(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("COLUMNCLAIM"), Name: stringPtr("Column claim")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	columns, err := data.ListColumns(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	var backlog store.Column
	for _, column := range columns {
		if column.SemanticState == "backlog" {
			backlog = column
			break
		}
	}
	if backlog.ID == "" {
		t.Fatal("default backlog column is missing")
	}
	owner, err := data.CreateActor(ctx, store.Actor{Kind: "agent", Name: "column claim owner"}, "")
	if err != nil {
		t.Fatal(err)
	}
	reclassifier, err := data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "column reclassifier", ProjectIDs: []string{project.ID}}, "actor-disabled-mode", "")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := data.CreateTokenBy(ctx, reclassifier.ID, "actor-disabled-mode", "column", []string{"projects:write"}, []string{project.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	task, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("claimed task")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := data.ClaimTask(ctx, task.ID, owner.ID, time.Hour, task.Version)
	if err != nil {
		t.Fatal(err)
	}

	response := request(t, server, http.MethodPatch, "/api/v1/columns/"+backlog.ID, map[string]any{"semantic_state": "completed"}, map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	})
	if response.Code != http.StatusConflict {
		t.Fatalf("bearer reclassification status = %d, body=%s", response.Code, response.Body.String())
	}
	unchangedColumn, err := data.GetColumn(ctx, backlog.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchangedColumn.SemanticState != "backlog" {
		t.Fatalf("active-claim reclassification changed column state to %q", unchangedColumn.SemanticState)
	}
	unchangedTask, err := data.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchangedTask.Version != claimed.Version || unchangedTask.CompletedAt != nil {
		t.Fatalf("active-claim reclassification changed task: version=%d completed_at=%v", unchangedTask.Version, unchangedTask.CompletedAt)
	}
}

func TestRepeatCompletePreservesCompletedAt(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("REPEAT"), Name: stringPtr("Repeat completion")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	task, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("complete once")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	firstResponse := request(t, server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/complete", nil, map[string]string{"If-Match": `"v1"`})
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("first complete status = %d, body=%s", firstResponse.Code, firstResponse.Body.String())
	}
	var first store.Task
	if err := json.Unmarshal(firstResponse.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if first.CompletedAt == nil {
		t.Fatal("first complete did not set completed_at")
	}

	secondResponse := request(t, server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/complete", nil, map[string]string{"If-Match": `"v2"`})
	if secondResponse.Code != http.StatusOK {
		t.Fatalf("repeat complete status = %d, body=%s", secondResponse.Code, secondResponse.Body.String())
	}
	var second store.Task
	if err := json.Unmarshal(secondResponse.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if second.CompletedAt == nil || *second.CompletedAt != *first.CompletedAt {
		t.Fatalf("repeat complete changed completed_at from %q to %v", *first.CompletedAt, second.CompletedAt)
	}
}

func TestTaskAndMyWorkCursorsFollowOrdering(t *testing.T) {
	_, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("CURSOR"), Name: stringPtr("Cursor ordering")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	actor, err := data.CreateActor(ctx, store.Actor{Kind: "agent", Name: "cursor actor"}, "")
	if err != nil {
		t.Fatal(err)
	}
	columns, err := data.ListColumns(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	var backlog, active store.Column
	for _, column := range columns {
		switch column.SemanticState {
		case "backlog":
			backlog = column
		case "active":
			active = column
		}
	}
	if backlog.ID == "" || active.ID == "" {
		t.Fatal("default board columns are missing")
	}

	// Number 1 is in a later board column and number 2 is in the first
	// column. A number-based cursor permanently omitted number 1 here.
	firstColumnID, secondColumnID := active.ID, backlog.ID
	firstTask, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("later"), ColumnID: &firstColumnID, Assignee: &actor.ID, AssigneeSet: true}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	secondTask, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("first"), ColumnID: &secondColumnID, Assignee: &actor.ID, AssigneeSet: true}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}

	page, more, err := data.ListTasks(ctx, project.ID, store.TaskFilter{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !more || len(page) != 1 || page[0].ID != secondTask.ID {
		t.Fatalf("first task page = %+v, more=%v", page, more)
	}
	page, more, err = data.ListTasks(ctx, project.ID, store.TaskFilter{Limit: 1, Cursor: 1})
	if err != nil {
		t.Fatal(err)
	}
	if more || len(page) != 1 || page[0].ID != firstTask.ID {
		t.Fatalf("second task page = %+v, more=%v", page, more)
	}

	page, more, err = data.ListMyWorkFiltered(ctx, actor.ID, nil, store.TaskFilter{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !more || len(page) != 1 || page[0].ID != secondTask.ID {
		t.Fatalf("first my-work page = %+v, more=%v", page, more)
	}
	page, more, err = data.ListMyWorkFiltered(ctx, actor.ID, nil, store.TaskFilter{Limit: 1, Cursor: 1})
	if err != nil {
		t.Fatal(err)
	}
	if more || len(page) != 1 || page[0].ID != firstTask.ID {
		t.Fatalf("second my-work page = %+v, more=%v", page, more)
	}
}
