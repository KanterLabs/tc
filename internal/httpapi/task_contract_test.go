package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/KanterLabs/helm/internal/store"
)

func TestTaskRoutesResolveProjectTaskKeysToCanonicalID(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatalf("ensure disabled actor: %v", err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("OPS"), Name: stringPtr("Operations")}, "actor-disabled-mode")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	task, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("Ship API")}, "actor-disabled-mode")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	getResponse := request(t, server, http.MethodGet, "/api/v1/tasks/ops-1", nil, nil)
	if getResponse.Code != http.StatusOK {
		t.Fatalf("get by task key status = %d, body=%s", getResponse.Code, getResponse.Body.String())
	}
	var got store.Task
	if err := json.Unmarshal(getResponse.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode get by key: %v", err)
	}
	if got.ID != task.ID || got.Key != "OPS-1" {
		t.Fatalf("get by task key = %+v, want ID %q and key OPS-1", got, task.ID)
	}

	updatedResponse := request(t, server, http.MethodPatch, "/api/v1/tasks/ops-1", map[string]any{"title": "Updated"}, map[string]string{
		"Content-Type": "application/json",
		"If-Match":     `"v1"`,
	})
	if updatedResponse.Code != http.StatusOK {
		t.Fatalf("patch by task key status = %d, body=%s", updatedResponse.Code, updatedResponse.Body.String())
	}
	if err := json.Unmarshal(updatedResponse.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode patch by key: %v", err)
	}
	if got.ID != task.ID || got.Title != "Updated" {
		t.Fatalf("patch by task key = %+v, want ID %q and title Updated", got, task.ID)
	}

	commentResponse := request(t, server, http.MethodPost, "/api/v1/tasks/OPS-1/comments", map[string]any{"body": "Ready"}, map[string]string{
		"Content-Type": "application/json",
	})
	if commentResponse.Code != http.StatusCreated {
		t.Fatalf("comment by task key status = %d, body=%s", commentResponse.Code, commentResponse.Body.String())
	}
	var comment store.Comment
	if err := json.Unmarshal(commentResponse.Body.Bytes(), &comment); err != nil {
		t.Fatalf("decode comment by key: %v", err)
	}
	if comment.TaskID != task.ID {
		t.Fatalf("comment task ID = %q, want canonical ID %q", comment.TaskID, task.ID)
	}
	if location := commentResponse.Header().Get("Location"); location != "" {
		t.Fatalf("comment location = %q, want no unresolvable resource location", location)
	}

	actionResponse := request(t, server, http.MethodPost, "/api/v1/tasks/ops-1/complete", map[string]any{"comment": "Done"}, map[string]string{
		"Content-Type": "application/json",
		"If-Match":     `"v2"`,
	})
	if actionResponse.Code != http.StatusOK {
		t.Fatalf("action by task key status = %d, body=%s", actionResponse.Code, actionResponse.Body.String())
	}
	if err := json.Unmarshal(actionResponse.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode action by key: %v", err)
	}
	if got.ID != task.ID || got.Version != 3 {
		t.Fatalf("action by task key = %+v, want canonical ID %q and version 3", got, task.ID)
	}
	for i := 0; i < 50; i++ {
		if _, err := data.CreateComment(ctx, task.ID, "actor-disabled-mode", "seed comment"); err != nil {
			t.Fatalf("create seed comment %d: %v", i, err)
		}
	}

	commentsResponse := request(t, server, http.MethodGet, "/api/v1/tasks/OPS-1/comments", nil, nil)
	if commentsResponse.Code != http.StatusOK {
		t.Fatalf("list comments by task key status = %d, body=%s", commentsResponse.Code, commentsResponse.Body.String())
	}
	var comments struct {
		Data       []store.Comment `json:"data"`
		NextCursor string          `json:"next_cursor"`
	}
	if err := json.Unmarshal(commentsResponse.Body.Bytes(), &comments); err != nil {
		t.Fatalf("decode comments page: %v", err)
	}
	if len(comments.Data) != 50 || comments.NextCursor == "" {
		t.Fatalf("default comments page rows=%d cursor=%q, want 50 rows and a cursor", len(comments.Data), comments.NextCursor)
	}
}

func TestTaskPatchCanClearNullableFieldsAndRejectsEmptyOrNullColumn(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatalf("ensure disabled actor: %v", err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("PATCH"), Name: stringPtr("Patch contract")}, "actor-disabled-mode")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	actor, err := data.CreateActor(ctx, store.Actor{Kind: "agent", Name: "assignee"}, "")
	if err != nil {
		t.Fatalf("create actor: %v", err)
	}
	dueAt := "2030-01-02T03:04:05Z"
	task, err := data.CreateTask(ctx, project.ID, store.TaskInput{
		Title:       stringPtr("Nullable fields"),
		Assignee:    &actor.ID,
		AssigneeSet: true,
		DueAt:       &dueAt,
		DueAtSet:    true,
	}, "actor-disabled-mode")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	nullColumn := request(t, server, http.MethodPatch, "/api/v1/tasks/"+task.ID, map[string]any{"column_id": nil}, map[string]string{
		"Content-Type": "application/json",
		"If-Match":     `"v1"`,
	})
	if nullColumn.Code != http.StatusBadRequest {
		t.Fatalf("null column patch status = %d, body=%s", nullColumn.Code, nullColumn.Body.String())
	}
	unchanged, err := data.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task after null column patch: %v", err)
	}
	if unchanged.Version != 1 || unchanged.ColumnID == "" {
		t.Fatalf("null column patch changed required task column/version: %+v", unchanged)
	}

	emptyPatch := request(t, server, http.MethodPatch, "/api/v1/tasks/"+task.ID, map[string]any{}, map[string]string{
		"Content-Type": "application/json",
		"If-Match":     `"v1"`,
	})
	if emptyPatch.Code != http.StatusBadRequest {
		t.Fatalf("empty patch status = %d, body=%s", emptyPatch.Code, emptyPatch.Body.String())
	}

	cleared := request(t, server, http.MethodPatch, "/api/v1/tasks/"+task.ID, map[string]any{
		"assignee": nil,
		"due_at":   nil,
	}, map[string]string{
		"Content-Type": "application/json",
		"If-Match":     `"v1"`,
	})
	if cleared.Code != http.StatusOK {
		t.Fatalf("clear nullable fields status = %d, body=%s", cleared.Code, cleared.Body.String())
	}
	var updated store.Task
	if err := json.Unmarshal(cleared.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode clear nullable fields: %v", err)
	}
	if updated.Assignee != nil || updated.DueAt != nil {
		t.Fatalf("clear nullable fields response = %+v, want nil assignee/due_at", updated)
	}
}

func TestTaskClaimLeaseDefaultsAndEnforcesInclusiveBounds(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatalf("ensure disabled actor: %v", err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("LEASE"), Name: stringPtr("Lease contract")}, "actor-disabled-mode")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	task, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("Lease me")}, "actor-disabled-mode")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	claimedResponse := request(t, server, http.MethodPost, "/api/v1/tasks/LEASE-1/claim", nil, map[string]string{
		"If-Match": `"v1"`,
	})
	if claimedResponse.Code != http.StatusOK {
		t.Fatalf("default claim status = %d, body=%s", claimedResponse.Code, claimedResponse.Body.String())
	}
	var claimed store.Task
	if err := json.Unmarshal(claimedResponse.Body.Bytes(), &claimed); err != nil {
		t.Fatalf("decode default claim: %v", err)
	}
	if claimed.ClaimExpiresAt == nil {
		t.Fatal("default claim omitted claim expiry")
	}
	defaultExpiry, err := time.Parse(time.RFC3339Nano, *claimed.ClaimExpiresAt)
	if err != nil {
		t.Fatalf("parse default claim expiry: %v", err)
	}
	defaultRemaining := time.Until(defaultExpiry)
	if defaultRemaining < 29*time.Minute || defaultRemaining > 31*time.Minute {
		t.Fatalf("default claim remaining = %s, want about 30m", defaultRemaining)
	}

	minimumResponse := request(t, server, http.MethodPost, "/api/v1/tasks/lease-1/renew", map[string]any{"lease_seconds": 30}, map[string]string{
		"Content-Type": "application/json",
		"If-Match":     `"v2"`,
	})
	if minimumResponse.Code != http.StatusOK {
		t.Fatalf("minimum renew status = %d, body=%s", minimumResponse.Code, minimumResponse.Body.String())
	}
	if err := json.Unmarshal(minimumResponse.Body.Bytes(), &claimed); err != nil {
		t.Fatalf("decode minimum renew: %v", err)
	}
	minimumExpiry, err := time.Parse(time.RFC3339Nano, *claimed.ClaimExpiresAt)
	if err != nil {
		t.Fatalf("parse minimum renew expiry: %v", err)
	}
	minimumRemaining := time.Until(minimumExpiry)
	if minimumRemaining < 29*time.Second || minimumRemaining > 31*time.Second {
		t.Fatalf("minimum renew remaining = %s, want about 30s", minimumRemaining)
	}

	maximumResponse := request(t, server, http.MethodPost, "/api/v1/tasks/LEASE-1/renew", map[string]any{"duration_seconds": 604800}, map[string]string{
		"Content-Type": "application/json",
		"If-Match":     `"v3"`,
	})
	if maximumResponse.Code != http.StatusOK {
		t.Fatalf("maximum renew status = %d, body=%s", maximumResponse.Code, maximumResponse.Body.String())
	}
	if err := json.Unmarshal(maximumResponse.Body.Bytes(), &claimed); err != nil {
		t.Fatalf("decode maximum renew: %v", err)
	}
	maximumExpiry, err := time.Parse(time.RFC3339Nano, *claimed.ClaimExpiresAt)
	if err != nil {
		t.Fatalf("parse maximum renew expiry: %v", err)
	}
	maximumRemaining := time.Until(maximumExpiry)
	if maximumRemaining < 604799*time.Second || maximumRemaining > 604801*time.Second {
		t.Fatalf("maximum renew remaining = %s, want about 604800s", maximumRemaining)
	}

	for _, seconds := range []int{29, 604801} {
		response := request(t, server, http.MethodPost, "/api/v1/tasks/LEASE-1/renew", map[string]any{"lease_seconds": seconds}, map[string]string{
			"Content-Type": "application/json",
			"If-Match":     `"v4"`,
		})
		if response.Code != http.StatusBadRequest {
			t.Fatalf("out-of-range renew %d status = %d, body=%s", seconds, response.Code, response.Body.String())
		}
	}
	current, err := data.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task after rejected lease renewals: %v", err)
	}
	if current.Version != 4 {
		t.Fatalf("rejected lease renewals changed version to %d, want 4", current.Version)
	}
}
