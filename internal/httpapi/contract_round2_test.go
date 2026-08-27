package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"roadmap/internal/store"
)

func errorCode(t *testing.T, response interface{ Result() *http.Response }) string {
	t.Helper()
	result := response.Result()
	defer result.Body.Close()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(result.Body).Decode(&body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	return body.Error.Code
}

func TestContractRound2AuthValidationSeparatesInputFromCredentials(t *testing.T) {
	server, _ := testServer(t, "local")

	invalidSetup := []map[string]any{
		{},
		{"email": nil, "password": "correct horse battery staple"},
		{"email": "not-an-email", "password": "correct horse battery staple"},
		{"email": "ada@example.com", "password": "short"},
		{"email": "ada@example.com", "password": "correct horse battery staple", "name": nil},
	}
	for _, payload := range invalidSetup {
		response := request(t, server, http.MethodPost, "/api/v1/auth/setup", payload, nil)
		if response.Code != http.StatusBadRequest || errorCode(t, response) != "invalid_request" {
			t.Fatalf("invalid setup %#v = %d, want 400 invalid_request", payload, response.Code)
		}
	}
	created := request(t, server, http.MethodPost, "/api/v1/auth/setup", map[string]any{
		"email": "ada@example.com", "password": "correct horse battery staple",
	}, nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("valid setup = %d, body=%s", created.Code, created.Body.String())
	}

	invalidLogin := []map[string]any{
		{},
		{"email": nil, "password": "wrong"},
		{"email": "not-an-email", "password": "wrong"},
		{"email": "ada@example.com", "password": ""},
	}
	for _, payload := range invalidLogin {
		response := request(t, server, http.MethodPost, "/api/v1/auth/login", payload, nil)
		if response.Code != http.StatusBadRequest || errorCode(t, response) != "invalid_request" {
			t.Fatalf("invalid login %#v = %d, want 400 invalid_request", payload, response.Code)
		}
	}
	wrongPassword := request(t, server, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"email": "ada@example.com", "password": "wrong password",
	}, nil)
	if wrongPassword.Code != http.StatusUnauthorized || errorCode(t, wrongPassword) != "invalid_credentials" {
		t.Fatalf("wrong password = %d, want 401 invalid_credentials", wrongPassword.Code)
	}
	unknownAccount := request(t, server, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"email": "other@example.com", "password": "wrong password",
	}, nil)
	if unknownAccount.Code != http.StatusUnauthorized || errorCode(t, unknownAccount) != "invalid_credentials" {
		t.Fatalf("unknown account = %d, want 401 invalid_credentials", unknownAccount.Code)
	}
}

func TestContractRound2RejectsNullEmptyAndMalformedBodyValues(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("VALID"), Name: stringPtr("Valid project")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	columns, err := data.ListColumns(ctx, project.ID)
	if err != nil || len(columns) == 0 {
		t.Fatalf("list project columns: %v", err)
	}
	agent, err := data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "Valid agent"}, "actor-disabled-mode", "")
	if err != nil {
		t.Fatal(err)
	}
	actionTask, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("Action validation")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodPost, "/api/v1/projects", map[string]any{"key": "NULL", "name": "Null", "description": nil}},
		{http.MethodPost, "/api/v1/projects", map[string]any{"key": "EMPTY", "name": "Empty", "slug": ""}},
		{http.MethodPost, "/api/v1/projects/VALID/columns", map[string]any{"name": nil, "semantic_state": "active"}},
		{http.MethodPost, "/api/v1/projects/VALID/columns", map[string]any{"name": "Mixed state", "semantic_state": "ACTIVE"}},
		{http.MethodPatch, "/api/v1/columns/" + columns[0].ID, map[string]any{"position": nil}},
		{http.MethodPost, "/api/v1/projects/VALID/tasks", map[string]any{"title": "Bad labels", "labels": []any{nil}}},
		{http.MethodPost, "/api/v1/projects/VALID/tasks", map[string]any{"title": "Null description", "description": nil}},
		{http.MethodPost, "/api/v1/projects/VALID/tasks", map[string]any{"title": "Mixed priority", "priority": "HIGH"}},
		{http.MethodPost, "/api/v1/projects/VALID/tasks", map[string]any{"title": "Empty column", "column_id": ""}},
		{http.MethodPost, "/api/v1/projects/VALID/tasks", map[string]any{"title": "Empty assignee", "assignee_id": ""}},
		{http.MethodPost, "/api/v1/tasks/" + actionTask.ID + "/complete", map[string]any{"comment": nil}},
		{http.MethodPost, "/api/v1/tasks/" + actionTask.ID + "/claim", map[string]any{"lease_seconds": nil}},
		{http.MethodPost, "/api/v1/agents", map[string]any{"name": nil}},
		{http.MethodPost, "/api/v1/agents", map[string]any{"name": "Null projects", "project_ids": []any{nil}}},
		{http.MethodPost, "/api/v1/agents/" + agent.ID + "/tokens", map[string]any{"name": "Null scope", "scopes": []any{nil}}},
		{http.MethodPost, "/api/v1/agents/" + agent.ID + "/tokens", map[string]any{"name": "Empty expiry", "scopes": []string{"tasks:read"}, "expires_at": ""}},
	}
	for _, tc := range cases {
		headers := map[string]string{"Content-Type": "application/json"}
		if tc.method == http.MethodPatch || strings.HasSuffix(tc.path, "/complete") || strings.HasSuffix(tc.path, "/claim") {
			headers["If-Match"] = `"v1"`
		}
		response := request(t, server, tc.method, tc.path, tc.body, headers)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s %s body=%#v status=%d, want 400 (%s)", tc.method, tc.path, tc.body, response.Code, response.Body.String())
		}
	}
	caseInsensitiveColumn := request(t, server, http.MethodGet, "/api/v1/projects/VALID/tasks?column=BACKLOG", nil, nil)
	if caseInsensitiveColumn.Code != http.StatusOK || !strings.Contains(caseInsensitiveColumn.Body.String(), actionTask.ID) {
		t.Fatalf("case-insensitive column filter = %d %s, want task %s", caseInsensitiveColumn.Code, caseInsensitiveColumn.Body.String(), actionTask.ID)
	}
}

func TestContractRound2RejectsInvalidFiltersAndEmptyCursors(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("FILTER"), Name: stringPtr("Filter project")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	task, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("Filter task")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}

	targets := []string{
		"/api/v1/projects?archived=maybe",
		"/api/v1/projects?favorite=",
		"/api/v1/projects?favorite=true&favorite=false",
		"/api/v1/projects?cursor=",
		"/api/v1/events?after=",
		"/api/v1/projects/FILTER/tasks?state=unknown",
		"/api/v1/projects/FILTER/tasks?priority=unknown",
		"/api/v1/projects/FILTER/tasks?column=",
		"/api/v1/projects/FILTER/tasks?assignee=",
		"/api/v1/projects/FILTER/tasks?q=" + strings.Repeat("x", 201),
		"/api/v1/projects/FILTER/tasks?q=one&q=two",
		"/api/v1/my-work?updated_after=",
	}
	for _, target := range targets {
		response := request(t, server, http.MethodGet, target, nil, nil)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("filter %s = %d, want 400 (%s)", target, response.Code, response.Body.String())
		}
	}
	_ = task
}

func TestContractRound2IdempotencyReplaysBeforeResourceLookup(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("RETRY"), Name: stringPtr("Retry project")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}

	patchTask, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("Patch me")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	patchHeaders := map[string]string{"Idempotency-Key": "patch-retry", "If-Match": `"v1"`}
	firstPatch := request(t, server, http.MethodPatch, "/api/v1/tasks/"+patchTask.ID, map[string]any{"title": "Patched"}, patchHeaders)
	if firstPatch.Code != http.StatusOK {
		t.Fatalf("first patch = %d, body=%s", firstPatch.Code, firstPatch.Body.String())
	}
	var patched store.Task
	if err := json.Unmarshal(firstPatch.Body.Bytes(), &patched); err != nil {
		t.Fatal(err)
	}
	if err := data.DeleteTask(ctx, patchTask.ID, patched.Version, "actor-disabled-mode"); err != nil {
		t.Fatal(err)
	}
	replayedPatch := request(t, server, http.MethodPatch, "/api/v1/tasks/"+patchTask.ID, map[string]any{"title": "Patched"}, patchHeaders)
	if replayedPatch.Code != firstPatch.Code || replayedPatch.Body.String() != firstPatch.Body.String() {
		t.Fatalf("patch replay = %d %s, want %d %s", replayedPatch.Code, replayedPatch.Body.String(), firstPatch.Code, firstPatch.Body.String())
	}
	missingIfMatch := request(t, server, http.MethodPatch, "/api/v1/tasks/"+patchTask.ID, map[string]any{"title": "Patched"}, map[string]string{
		"Idempotency-Key": "patch-retry",
		"Content-Type":    "application/json",
	})
	if missingIfMatch.Code != http.StatusPreconditionRequired || errorCode(t, missingIfMatch) != "if_match_required" {
		t.Fatalf("replay without If-Match = %d, want 428 if_match_required", missingIfMatch.Code)
	}

	commentTask, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("Comment me")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	commentHeaders := map[string]string{"Idempotency-Key": "comment-retry"}
	firstComment := request(t, server, http.MethodPost, "/api/v1/tasks/"+commentTask.ID+"/comments", map[string]any{"body": "hello"}, commentHeaders)
	if firstComment.Code != http.StatusCreated {
		t.Fatalf("first comment = %d, body=%s", firstComment.Code, firstComment.Body.String())
	}
	if err := data.DeleteTask(ctx, commentTask.ID, commentTask.Version, "actor-disabled-mode"); err != nil {
		t.Fatal(err)
	}
	replayedComment := request(t, server, http.MethodPost, "/api/v1/tasks/"+commentTask.ID+"/comments", map[string]any{"body": "hello"}, commentHeaders)
	if replayedComment.Code != firstComment.Code || replayedComment.Body.String() != firstComment.Body.String() {
		t.Fatalf("comment replay = %d %s, want %d %s", replayedComment.Code, replayedComment.Body.String(), firstComment.Code, firstComment.Body.String())
	}

	actionTask, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("Complete me")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	actionHeaders := map[string]string{"Idempotency-Key": "action-retry", "If-Match": `"v1"`}
	firstAction := request(t, server, http.MethodPost, "/api/v1/tasks/"+actionTask.ID+"/complete", nil, actionHeaders)
	if firstAction.Code != http.StatusOK {
		t.Fatalf("first action = %d, body=%s", firstAction.Code, firstAction.Body.String())
	}
	replayedAction := request(t, server, http.MethodPost, "/api/v1/tasks/"+actionTask.ID+"/complete", nil, actionHeaders)
	if replayedAction.Code != firstAction.Code || replayedAction.Body.String() != firstAction.Body.String() {
		t.Fatalf("action replay = %d %s, want %d %s", replayedAction.Code, replayedAction.Body.String(), firstAction.Code, firstAction.Body.String())
	}
}

func TestContractRound2ScopedTokenReplayAndCredentialIsolation(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	firstProject, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("TOKENA"), Name: stringPtr("Token project A")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	secondProject, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("TOKENB"), Name: stringPtr("Token project B")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "Scoped retry agent", ProjectIDs: []string{firstProject.ID, secondProject.ID}}, "actor-disabled-mode", "")
	if err != nil {
		t.Fatal(err)
	}
	_, firstToken, err := data.CreateTokenBy(ctx, agent.ID, "actor-disabled-mode", "multi-project", []string{"tasks:write"}, []string{firstProject.ID, secondProject.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, secondToken, err := data.CreateTokenBy(ctx, agent.ID, "actor-disabled-mode", "narrow-project", []string{"tasks:write"}, []string{firstProject.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, readToken, err := data.CreateTokenBy(ctx, agent.ID, "actor-disabled-mode", "my-work", []string{"tasks:read"}, []string{firstProject.ID, secondProject.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	myWorkWithoutProject := request(t, server, http.MethodGet, "/api/v1/my-work", nil, map[string]string{"Authorization": "Bearer " + readToken})
	if myWorkWithoutProject.Code != http.StatusForbidden || errorCode(t, myWorkWithoutProject) != "forbidden" {
		t.Fatalf("scoped my-work without project = %d, want 403 forbidden", myWorkWithoutProject.Code)
	}
	myWorkWithProject := request(t, server, http.MethodGet, "/api/v1/my-work?project=TOKENA", nil, map[string]string{"Authorization": "Bearer " + readToken})
	if myWorkWithProject.Code != http.StatusOK {
		t.Fatalf("scoped my-work with project = %d, body=%s", myWorkWithProject.Code, myWorkWithProject.Body.String())
	}
	task, err := data.CreateTask(ctx, firstProject.ID, store.TaskInput{Title: stringPtr("Scoped retry")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{
		"Authorization":   "Bearer " + firstToken,
		"Content-Type":    "application/json",
		"Idempotency-Key": "shared-token-key",
		"If-Match":        `"v1"`,
	}
	first := request(t, server, http.MethodPatch, "/api/v1/tasks/"+task.ID, map[string]any{"title": "Scoped retry applied"}, headers)
	if first.Code != http.StatusOK {
		t.Fatalf("first scoped token mutation = %d, body=%s", first.Code, first.Body.String())
	}
	var updated store.Task
	if err := json.Unmarshal(first.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if err := data.DeleteTask(ctx, task.ID, updated.Version, "actor-disabled-mode"); err != nil {
		t.Fatal(err)
	}
	replay := request(t, server, http.MethodPatch, "/api/v1/tasks/"+task.ID, map[string]any{"title": "Scoped retry applied"}, headers)
	if replay.Code != first.Code || replay.Body.String() != first.Body.String() {
		t.Fatalf("multi-project scoped replay = %d %s, want %d %s", replay.Code, replay.Body.String(), first.Code, first.Body.String())
	}

	otherCredentialHeaders := map[string]string{
		"Authorization":   "Bearer " + secondToken,
		"Content-Type":    "application/json",
		"Idempotency-Key": "shared-token-key",
		"If-Match":        `"v1"`,
	}
	notReplay := request(t, server, http.MethodPatch, "/api/v1/tasks/"+task.ID, map[string]any{"title": "Scoped retry applied"}, otherCredentialHeaders)
	if notReplay.Code != http.StatusNotFound {
		t.Fatalf("same-actor different-token response = %d, body=%s; token replay leaked", notReplay.Code, notReplay.Body.String())
	}
}

func TestContractRound2RedactsTaskConflictSnapshotsWithoutReadScope(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("SECRET"), Name: stringPtr("Secret project")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	writer, err := data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "Write-only", ProjectIDs: []string{project.ID}}, "actor-disabled-mode", "")
	if err != nil {
		t.Fatal(err)
	}
	_, writeToken, err := data.CreateTokenBy(ctx, writer.ID, "actor-disabled-mode", "write-only", []string{"tasks:write"}, []string{project.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	claimer, err := data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "Claim-only", ProjectIDs: []string{project.ID}}, "actor-disabled-mode", "")
	if err != nil {
		t.Fatal(err)
	}
	_, claimToken, err := data.CreateTokenBy(ctx, claimer.ID, "actor-disabled-mode", "claim-only", []string{"tasks:claim"}, []string{project.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}

	stalePatch, err := data.CreateTask(ctx, project.ID, store.TaskInput{
		Title:       stringPtr("patch-secret-title"),
		Description: stringPtr("patch-secret-description"),
		Priority:    stringPtr("high"),
	}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := data.UpdateTask(ctx, stalePatch.ID, store.TaskInput{Title: stringPtr("server version")}, stalePatch.Version, "actor-disabled-mode"); err != nil {
		t.Fatal(err)
	}
	patchResponse := request(t, server, http.MethodPatch, "/api/v1/tasks/"+stalePatch.ID, map[string]any{"title": "write-only update"}, map[string]string{
		"Authorization": "Bearer " + writeToken,
		"Content-Type":  "application/json",
		"If-Match":      `"v1"`,
	})
	assertRedactedTaskConflict(t, patchResponse, "stale_task", "patch-secret-title", "patch-secret-description")

	deleteTask, err := data.CreateTask(ctx, project.ID, store.TaskInput{
		Title:       stringPtr("delete-secret-title"),
		Description: stringPtr("delete-secret-description"),
	}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := data.UpdateTask(ctx, deleteTask.ID, store.TaskInput{Title: stringPtr("server delete version")}, deleteTask.Version, "actor-disabled-mode"); err != nil {
		t.Fatal(err)
	}
	deleteResponse := request(t, server, http.MethodDelete, "/api/v1/tasks/"+deleteTask.ID, nil, map[string]string{
		"Authorization": "Bearer " + writeToken,
		"If-Match":      `"v1"`,
	})
	assertRedactedTaskConflict(t, deleteResponse, "stale_task", "delete-secret-title", "delete-secret-description")

	staleAction, err := data.CreateTask(ctx, project.ID, store.TaskInput{
		Title:       stringPtr("action-secret-title"),
		Description: stringPtr("action-secret-description"),
	}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := data.UpdateTask(ctx, staleAction.ID, store.TaskInput{Description: stringPtr("new server description")}, staleAction.Version, "actor-disabled-mode"); err != nil {
		t.Fatal(err)
	}
	actionResponse := request(t, server, http.MethodPost, "/api/v1/tasks/"+staleAction.ID+"/complete", nil, map[string]string{
		"Authorization": "Bearer " + claimToken,
		"If-Match":      `"v1"`,
	})
	assertRedactedTaskConflict(t, actionResponse, "stale_task", "action-secret-title", "action-secret-description")

	claimedTask, err := data.CreateTask(ctx, project.ID, store.TaskInput{
		Title:       stringPtr("claim-secret-title"),
		Description: stringPtr("claim-secret-description"),
	}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	claimedTask, err = data.ClaimTask(ctx, claimedTask.ID, "actor-disabled-mode", time.Minute, claimedTask.Version)
	if err != nil {
		t.Fatal(err)
	}
	activePatchResponse := request(t, server, http.MethodPatch, "/api/v1/tasks/"+claimedTask.ID, map[string]any{"title": "write-only active-claim update"}, map[string]string{
		"Authorization": "Bearer " + writeToken,
		"Content-Type":  "application/json",
		"If-Match":      fmt.Sprintf(`"v%d"`, claimedTask.Version),
	})
	assertRedactedTaskClaimConflict(t, activePatchResponse, "task_already_claimed", "claim-secret-title", "claim-secret-description", "actor-disabled-mode")
	activeDeleteResponse := request(t, server, http.MethodDelete, "/api/v1/tasks/"+claimedTask.ID, nil, map[string]string{
		"Authorization": "Bearer " + writeToken,
		"If-Match":      fmt.Sprintf(`"v%d"`, claimedTask.Version),
	})
	assertRedactedTaskClaimConflict(t, activeDeleteResponse, "task_already_claimed", "claim-secret-title", "claim-secret-description", "actor-disabled-mode")
	claimResponse := request(t, server, http.MethodPost, "/api/v1/tasks/"+claimedTask.ID+"/claim", nil, map[string]string{
		"Authorization": "Bearer " + claimToken,
		"If-Match":      fmt.Sprintf(`"v%d"`, claimedTask.Version),
	})
	assertRedactedTaskConflict(t, claimResponse, "task_already_claimed", "claim-secret-title", "claim-secret-description")
}

func TestContractRound2RedactsSuccessfulTaskMutationsWithoutReadScope(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("SAFE"), Name: stringPtr("Safe project")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	writer, err := data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "Write-only success", ProjectIDs: []string{project.ID}}, "actor-disabled-mode", "")
	if err != nil {
		t.Fatal(err)
	}
	_, writeToken, err := data.CreateTokenBy(ctx, writer.ID, "actor-disabled-mode", "write-only-success", []string{"tasks:write"}, []string{project.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	claimer, err := data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "Claim-only success", ProjectIDs: []string{project.ID}}, "actor-disabled-mode", "")
	if err != nil {
		t.Fatal(err)
	}
	_, claimToken, err := data.CreateTokenBy(ctx, claimer.ID, "actor-disabled-mode", "claim-only-success", []string{"tasks:claim"}, []string{project.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}

	createHeaders := map[string]string{
		"Authorization":   "Bearer " + writeToken,
		"Content-Type":    "application/json",
		"Idempotency-Key": "safe-create",
	}
	createPayload := map[string]any{"title": "create-secret-title", "description": "create-secret-description"}
	createResponse := request(t, server, http.MethodPost, "/api/v1/projects/SAFE/tasks", createPayload, createHeaders)
	createdID, createdVersion := assertReducedTaskResponse(t, createResponse, http.StatusCreated, "", "create-secret-title", "create-secret-description")
	if createdVersion != 1 || createResponse.Header().Get("ETag") != `"v1"` {
		t.Fatalf("reduced create version/etag = %d/%q, want 1/\"v1\"", createdVersion, createResponse.Header().Get("ETag"))
	}
	getWriteOnly := request(t, server, http.MethodGet, "/api/v1/tasks/"+createdID, nil, map[string]string{"Authorization": "Bearer " + writeToken})
	if getWriteOnly.Code != http.StatusForbidden || errorCode(t, getWriteOnly) != "insufficient_scope" {
		t.Fatalf("write-only task GET = %d, want 403 insufficient_scope", getWriteOnly.Code)
	}
	if strings.Contains(getWriteOnly.Body.String(), "create-secret-") {
		t.Fatalf("write-only task GET leaked task fields: %s", getWriteOnly.Body.String())
	}
	if err := data.DeleteTask(ctx, createdID, createdVersion, "actor-disabled-mode"); err != nil {
		t.Fatal(err)
	}
	replayedCreate := request(t, server, http.MethodPost, "/api/v1/projects/SAFE/tasks", createPayload, createHeaders)
	if replayedCreate.Code != createResponse.Code || replayedCreate.Body.String() != createResponse.Body.String() || replayedCreate.Header().Get("ETag") != createResponse.Header().Get("ETag") {
		t.Fatalf("replayed reduced create = %d %q etag=%q, want %d %q etag=%q", replayedCreate.Code, replayedCreate.Body.String(), replayedCreate.Header().Get("ETag"), createResponse.Code, createResponse.Body.String(), createResponse.Header().Get("ETag"))
	}

	patchTask, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("patch-secret-title"), Description: stringPtr("patch-secret-description")}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	patchHeaders := map[string]string{
		"Authorization":   "Bearer " + writeToken,
		"Content-Type":    "application/json",
		"Idempotency-Key": "safe-patch",
		"If-Match":        `"v1"`,
	}
	patchResponse := request(t, server, http.MethodPatch, "/api/v1/tasks/"+patchTask.ID, map[string]any{"title": "patch-updated"}, patchHeaders)
	_, patchVersion := assertReducedTaskResponse(t, patchResponse, http.StatusOK, patchTask.ID, "patch-secret-title", "patch-secret-description")
	if patchVersion != 2 || patchResponse.Header().Get("ETag") != `"v2"` {
		t.Fatalf("reduced patch version/etag = %d/%q, want 2/\"v2\"", patchVersion, patchResponse.Header().Get("ETag"))
	}
	if _, err := data.UpdateTask(ctx, patchTask.ID, store.TaskInput{Description: stringPtr("server patch description")}, patchVersion, "actor-disabled-mode"); err != nil {
		t.Fatal(err)
	}
	replayedPatch := request(t, server, http.MethodPatch, "/api/v1/tasks/"+patchTask.ID, map[string]any{"title": "patch-updated"}, patchHeaders)
	if replayedPatch.Code != patchResponse.Code || replayedPatch.Body.String() != patchResponse.Body.String() || replayedPatch.Header().Get("ETag") != patchResponse.Header().Get("ETag") {
		t.Fatalf("replayed reduced patch = %d %q etag=%q, want %d %q etag=%q", replayedPatch.Code, replayedPatch.Body.String(), replayedPatch.Header().Get("ETag"), patchResponse.Code, patchResponse.Body.String(), patchResponse.Header().Get("ETag"))
	}

	claimTask := func(title, description string) store.Task {
		t.Helper()
		task, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr(title), Description: stringPtr(description)}, "actor-disabled-mode")
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
	claimHeaders := map[string]string{"Authorization": "Bearer " + claimToken, "Content-Type": "application/json"}

	leaseTask := claimTask("lease-secret-title", "lease-secret-description")
	claimResponse := request(t, server, http.MethodPost, "/api/v1/tasks/"+leaseTask.ID+"/claim", map[string]any{"lease_seconds": 60}, withIfMatch(claimHeaders, 1))
	_, leaseVersion := assertReducedTaskResponse(t, claimResponse, http.StatusOK, leaseTask.ID, "lease-secret-title", "lease-secret-description")
	if leaseVersion != 2 || claimResponse.Header().Get("ETag") != `"v2"` {
		t.Fatalf("reduced claim version/etag = %d/%q, want 2/\"v2\"", leaseVersion, claimResponse.Header().Get("ETag"))
	}
	getClaimOnly := request(t, server, http.MethodGet, "/api/v1/tasks/"+leaseTask.ID, nil, map[string]string{"Authorization": "Bearer " + claimToken})
	if getClaimOnly.Code != http.StatusForbidden || errorCode(t, getClaimOnly) != "insufficient_scope" {
		t.Fatalf("claim-only task GET = %d, want 403 insufficient_scope", getClaimOnly.Code)
	}
	if strings.Contains(getClaimOnly.Body.String(), "lease-secret-") {
		t.Fatalf("claim-only task GET leaked task fields: %s", getClaimOnly.Body.String())
	}
	renewResponse := request(t, server, http.MethodPost, "/api/v1/tasks/"+leaseTask.ID+"/renew", map[string]any{"lease_seconds": 60}, withIfMatch(claimHeaders, leaseVersion))
	_, leaseVersion = assertReducedTaskResponse(t, renewResponse, http.StatusOK, leaseTask.ID, "lease-secret-title", "lease-secret-description")
	releaseResponse := request(t, server, http.MethodPost, "/api/v1/tasks/"+leaseTask.ID+"/release", nil, withIfMatch(claimHeaders, leaseVersion))
	_, leaseVersion = assertReducedTaskResponse(t, releaseResponse, http.StatusOK, leaseTask.ID, "lease-secret-title", "lease-secret-description")
	if leaseVersion != 4 {
		t.Fatalf("claim/renew/release versions ended at %d, want 4", leaseVersion)
	}

	completeTask := claimTask("complete-secret-title", "complete-secret-description")
	completeClaim := request(t, server, http.MethodPost, "/api/v1/tasks/"+completeTask.ID+"/claim", map[string]any{"lease_seconds": 60}, withIfMatch(claimHeaders, 1))
	_, completeVersion := assertReducedTaskResponse(t, completeClaim, http.StatusOK, completeTask.ID, "complete-secret-title", "complete-secret-description")
	completeResponse := request(t, server, http.MethodPost, "/api/v1/tasks/"+completeTask.ID+"/complete", nil, withIfMatch(claimHeaders, completeVersion))
	_, completeVersion = assertReducedTaskResponse(t, completeResponse, http.StatusOK, completeTask.ID, "complete-secret-title", "complete-secret-description")
	if completeVersion != 3 {
		t.Fatalf("claim/complete versions ended at %d, want 3", completeVersion)
	}

	blockTask := claimTask("block-secret-title", "block-secret-description")
	blockClaim := request(t, server, http.MethodPost, "/api/v1/tasks/"+blockTask.ID+"/claim", map[string]any{"lease_seconds": 60}, withIfMatch(claimHeaders, 1))
	_, blockVersion := assertReducedTaskResponse(t, blockClaim, http.StatusOK, blockTask.ID, "block-secret-title", "block-secret-description")
	blockResponse := request(t, server, http.MethodPost, "/api/v1/tasks/"+blockTask.ID+"/block", map[string]any{"reason": "blocked"}, withIfMatch(claimHeaders, blockVersion))
	_, blockVersion = assertReducedTaskResponse(t, blockResponse, http.StatusOK, blockTask.ID, "block-secret-title", "block-secret-description")
	if blockVersion != 3 {
		t.Fatalf("claim/block versions ended at %d, want 3", blockVersion)
	}
}

func TestContractRound2RedactsSuccessfulProjectAndColumnMutationsWithoutReadScope(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{
		Key:         stringPtr("PROJSAFE"),
		Name:        stringPtr("Project secret name"),
		Description: stringPtr("Project secret description"),
	}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	writer, err := data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "Project write-only"}, "actor-disabled-mode", "")
	if err != nil {
		t.Fatal(err)
	}
	_, writeToken, err := data.CreateTokenBy(ctx, writer.ID, "actor-disabled-mode", "project-write-only", []string{"projects:write"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	writeHeaders := map[string]string{
		"Authorization": "Bearer " + writeToken,
		"Content-Type":  "application/json",
	}

	projectPatchHeaders := cloneHeaders(writeHeaders)
	projectPatchHeaders["Idempotency-Key"] = "project-safe-patch"
	projectResponse := request(t, server, http.MethodPatch, "/api/v1/projects/"+project.ID, map[string]any{
		"name":        "Updated secret project",
		"description": "Updated secret description",
	}, projectPatchHeaders)
	assertReducedProjectResponse(t, projectResponse, http.StatusOK, project.ID, "Updated secret", "Project secret")
	getProject := request(t, server, http.MethodGet, "/api/v1/projects/"+project.ID, nil, map[string]string{"Authorization": "Bearer " + writeToken})
	if getProject.Code != http.StatusForbidden || errorCode(t, getProject) != "insufficient_scope" {
		t.Fatalf("write-only project GET = %d, want 403 insufficient_scope", getProject.Code)
	}
	if strings.Contains(getProject.Body.String(), "secret") {
		t.Fatalf("write-only project GET leaked fields: %s", getProject.Body.String())
	}
	if _, err := data.UpdateProject(ctx, project.ID, store.ProjectInput{Name: stringPtr("Server project name")}, "actor-disabled-mode"); err != nil {
		t.Fatal(err)
	}
	replayedProject := request(t, server, http.MethodPatch, "/api/v1/projects/"+project.ID, map[string]any{
		"name":        "Updated secret project",
		"description": "Updated secret description",
	}, projectPatchHeaders)
	if replayedProject.Code != projectResponse.Code || replayedProject.Body.String() != projectResponse.Body.String() {
		t.Fatalf("replayed reduced project = %d %s, want %d %s", replayedProject.Code, replayedProject.Body.String(), projectResponse.Code, projectResponse.Body.String())
	}

	projectCreateHeaders := cloneHeaders(writeHeaders)
	projectCreateHeaders["Idempotency-Key"] = "project-safe-create"
	createdProjectResponse := request(t, server, http.MethodPost, "/api/v1/projects", map[string]any{
		"key":         "PROJNEW",
		"name":        "Created secret project",
		"description": "Created secret description",
	}, projectCreateHeaders)
	createdProjectID := assertReducedProjectResponse(t, createdProjectResponse, http.StatusCreated, "", "Created secret", "Created secret description")
	replayedCreatedProject := request(t, server, http.MethodPost, "/api/v1/projects", map[string]any{
		"key":         "PROJNEW",
		"name":        "Created secret project",
		"description": "Created secret description",
	}, projectCreateHeaders)
	if replayedCreatedProject.Code != createdProjectResponse.Code || replayedCreatedProject.Body.String() != createdProjectResponse.Body.String() {
		t.Fatalf("replayed reduced project create = %d %s, want %d %s", replayedCreatedProject.Code, replayedCreatedProject.Body.String(), createdProjectResponse.Code, createdProjectResponse.Body.String())
	}
	if createdProjectID == "" {
		t.Fatal("reduced project create omitted id")
	}

	columns, err := data.ListColumns(ctx, project.ID)
	if err != nil || len(columns) == 0 {
		t.Fatalf("list project columns: %v", err)
	}
	column := columns[0]
	columnPatchHeaders := cloneHeaders(writeHeaders)
	columnPatchHeaders["Idempotency-Key"] = "column-safe-patch"
	columnResponse := request(t, server, http.MethodPatch, "/api/v1/columns/"+column.ID, map[string]any{
		"name": "Updated secret column",
	}, columnPatchHeaders)
	assertReducedColumnResponse(t, columnResponse, http.StatusOK, column.ID, project.ID, "Updated secret", "Project secret")
	getColumn := request(t, server, http.MethodGet, "/api/v1/columns/"+column.ID, nil, map[string]string{"Authorization": "Bearer " + writeToken})
	if getColumn.Code != http.StatusForbidden || errorCode(t, getColumn) != "insufficient_scope" {
		t.Fatalf("write-only column GET = %d, want 403 insufficient_scope", getColumn.Code)
	}
	if strings.Contains(getColumn.Body.String(), "secret") {
		t.Fatalf("write-only column GET leaked fields: %s", getColumn.Body.String())
	}
	if _, err := data.UpdateColumn(ctx, column.ID, store.ColumnInput{Name: stringPtr("Server column name")}, "actor-disabled-mode"); err != nil {
		t.Fatal(err)
	}
	replayedColumn := request(t, server, http.MethodPatch, "/api/v1/columns/"+column.ID, map[string]any{
		"name": "Updated secret column",
	}, columnPatchHeaders)
	if replayedColumn.Code != columnResponse.Code || replayedColumn.Body.String() != columnResponse.Body.String() {
		t.Fatalf("replayed reduced column = %d %s, want %d %s", replayedColumn.Code, replayedColumn.Body.String(), columnResponse.Code, columnResponse.Body.String())
	}

	columnCreateHeaders := cloneHeaders(writeHeaders)
	columnCreateHeaders["Idempotency-Key"] = "column-safe-create"
	createdColumnResponse := request(t, server, http.MethodPost, "/api/v1/projects/"+project.ID+"/columns", map[string]any{
		"name":           "Created secret column",
		"semantic_state": "active",
	}, columnCreateHeaders)
	createdColumnID, createdColumnProjectID := assertReducedColumnResponse(t, createdColumnResponse, http.StatusCreated, "", project.ID, "Created secret", "Project secret")
	replayedCreatedColumn := request(t, server, http.MethodPost, "/api/v1/projects/"+project.ID+"/columns", map[string]any{
		"name":           "Created secret column",
		"semantic_state": "active",
	}, columnCreateHeaders)
	if replayedCreatedColumn.Code != createdColumnResponse.Code || replayedCreatedColumn.Body.String() != createdColumnResponse.Body.String() {
		t.Fatalf("replayed reduced column create = %d %s, want %d %s", replayedCreatedColumn.Code, replayedCreatedColumn.Body.String(), createdColumnResponse.Code, createdColumnResponse.Body.String())
	}
	if createdColumnID == "" || createdColumnProjectID != project.ID {
		t.Fatalf("reduced column create identifiers = %q/%q", createdColumnID, createdColumnProjectID)
	}

	claimedTask, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("Claimed secret task"), ColumnID: stringPtr(column.ID)}, "actor-disabled-mode")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := data.ClaimTask(ctx, claimedTask.ID, "actor-disabled-mode", time.Minute, claimedTask.Version); err != nil {
		t.Fatal(err)
	}
	claimConflict := request(t, server, http.MethodPatch, "/api/v1/columns/"+column.ID, map[string]any{
		"semantic_state": "completed",
	}, writeHeaders)
	assertRedactedColumnClaimConflict(t, claimConflict, "task_already_claimed", "Claimed secret task", claimedTask.ID, "actor-disabled-mode")
}

func cloneHeaders(headers map[string]string) map[string]string {
	result := make(map[string]string, len(headers))
	for name, value := range headers {
		result[name] = value
	}
	return result
}

func assertReducedProjectResponse(t *testing.T, response interface{ Result() *http.Response }, wantStatus int, wantID string, secrets ...string) string {
	t.Helper()
	result := response.Result()
	defer result.Body.Close()
	if result.StatusCode != wantStatus {
		t.Fatalf("project mutation status = %d, want %d; body=%s", result.StatusCode, wantStatus, readResponseBody(result))
	}
	bodyBytes, err := io.ReadAll(result.Body)
	if err != nil {
		t.Fatalf("read reduced project: %v", err)
	}
	encoded := string(bodyBytes)
	for _, secret := range secrets {
		if strings.Contains(encoded, secret) {
			t.Fatalf("project mutation leaked secret %q: %s", secret, encoded)
		}
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		t.Fatalf("decode reduced project: %v", err)
	}
	if len(body) != 1 {
		t.Fatalf("reduced project fields = %v, want only id", body)
	}
	if _, ok := body["id"]; !ok {
		t.Fatalf("reduced project omitted id: %s", encoded)
	}
	var id string
	if err := json.Unmarshal(body["id"], &id); err != nil {
		t.Fatalf("decode reduced project id: %v", err)
	}
	if wantID != "" && id != wantID {
		t.Fatalf("reduced project id = %q, want %q", id, wantID)
	}
	return id
}

func assertReducedColumnResponse(t *testing.T, response interface{ Result() *http.Response }, wantStatus int, wantID, wantProjectID string, secrets ...string) (string, string) {
	t.Helper()
	result := response.Result()
	defer result.Body.Close()
	if result.StatusCode != wantStatus {
		t.Fatalf("column mutation status = %d, want %d; body=%s", result.StatusCode, wantStatus, readResponseBody(result))
	}
	bodyBytes, err := io.ReadAll(result.Body)
	if err != nil {
		t.Fatalf("read reduced column: %v", err)
	}
	encoded := string(bodyBytes)
	for _, secret := range secrets {
		if strings.Contains(encoded, secret) {
			t.Fatalf("column mutation leaked secret %q: %s", secret, encoded)
		}
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		t.Fatalf("decode reduced column: %v", err)
	}
	if len(body) != 2 {
		t.Fatalf("reduced column fields = %v, want only id/project_id", body)
	}
	for _, field := range []string{"id", "project_id"} {
		if _, ok := body[field]; !ok {
			t.Fatalf("reduced column omitted %s: %s", field, encoded)
		}
	}
	var id, projectID string
	if err := json.Unmarshal(body["id"], &id); err != nil {
		t.Fatalf("decode reduced column id: %v", err)
	}
	if err := json.Unmarshal(body["project_id"], &projectID); err != nil {
		t.Fatalf("decode reduced column project_id: %v", err)
	}
	if wantID != "" && id != wantID {
		t.Fatalf("reduced column id = %q, want %q", id, wantID)
	}
	if wantProjectID != "" && projectID != wantProjectID {
		t.Fatalf("reduced column project_id = %q, want %q", projectID, wantProjectID)
	}
	return id, projectID
}

func assertRedactedColumnClaimConflict(t *testing.T, response interface{ Result() *http.Response }, wantCode string, secrets ...string) {
	t.Helper()
	result := response.Result()
	defer result.Body.Close()
	if result.StatusCode != http.StatusConflict {
		t.Fatalf("column claim conflict status = %d, want 409; body=%s", result.StatusCode, readResponseBody(result))
	}
	var body struct {
		Error struct {
			Code    string                     `json:"code"`
			Details map[string]json.RawMessage `json:"details"`
		} `json:"error"`
	}
	if err := json.NewDecoder(result.Body).Decode(&body); err != nil {
		t.Fatalf("decode column claim conflict: %v", err)
	}
	if body.Error.Code != wantCode {
		t.Fatalf("column claim conflict code = %q, want %q", body.Error.Code, wantCode)
	}
	encoded := mustMarshalJSON(t, body)
	for _, secret := range secrets {
		if strings.Contains(encoded, secret) {
			t.Fatalf("column claim conflict leaked secret %q: %s", secret, encoded)
		}
	}
	if len(body.Error.Details) != 0 {
		t.Fatalf("column claim conflict details = %s, want empty", encoded)
	}
}

func withIfMatch(headers map[string]string, version int64) map[string]string {
	result := make(map[string]string, len(headers)+1)
	for name, value := range headers {
		result[name] = value
	}
	result["If-Match"] = fmt.Sprintf(`"v%d"`, version)
	return result
}

func assertReducedTaskResponse(t *testing.T, response interface{ Result() *http.Response }, wantStatus int, wantID string, secrets ...string) (string, int64) {
	t.Helper()
	result := response.Result()
	defer result.Body.Close()
	if result.StatusCode != wantStatus {
		t.Fatalf("task success status = %d, want %d; body=%s", result.StatusCode, wantStatus, readResponseBody(result))
	}
	bodyBytes, err := io.ReadAll(result.Body)
	if err != nil {
		t.Fatalf("read task success: %v", err)
	}
	encoded := string(bodyBytes)
	for _, secret := range secrets {
		if strings.Contains(encoded, secret) {
			t.Fatalf("task success leaked secret %q: %s", secret, encoded)
		}
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		t.Fatalf("decode reduced task success: %v", err)
	}
	if len(body) != 2 {
		t.Fatalf("reduced task success fields = %v, want only id/version", body)
	}
	for _, field := range []string{"title", "description", "priority", "column_id", "labels", "assignee", "claimed_by", "claim_expires_at", "due_at", "created_at", "updated_at"} {
		if _, ok := body[field]; ok {
			t.Fatalf("reduced task success contains protected field %q: %s", field, encoded)
		}
	}
	var id string
	if err := json.Unmarshal(body["id"], &id); err != nil {
		t.Fatalf("decode reduced task id: %v", err)
	}
	if wantID != "" && id != wantID {
		t.Fatalf("reduced task id = %q, want %q", id, wantID)
	}
	var version int64
	if err := json.Unmarshal(body["version"], &version); err != nil {
		t.Fatalf("decode reduced task version: %v", err)
	}
	return id, version
}

func readResponseBody(response *http.Response) string {
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err.Error()
	}
	return string(body)
}

func assertRedactedTaskConflict(t *testing.T, response interface{ Result() *http.Response }, wantCode string, secrets ...string) {
	t.Helper()
	result := response.Result()
	defer result.Body.Close()
	if result.StatusCode != http.StatusConflict {
		t.Fatalf("task conflict status = %d, want 409", result.StatusCode)
	}
	var body struct {
		Error struct {
			Code    string                     `json:"code"`
			Details map[string]json.RawMessage `json:"details"`
		} `json:"error"`
	}
	if err := json.NewDecoder(result.Body).Decode(&body); err != nil {
		t.Fatalf("decode task conflict: %v", err)
	}
	if body.Error.Code != wantCode {
		t.Fatalf("task conflict code = %q, want %q", body.Error.Code, wantCode)
	}
	encoded := mustMarshalJSON(t, body)
	for _, secret := range secrets {
		if strings.Contains(encoded, secret) {
			t.Fatalf("task conflict leaked secret %q: %s", secret, encoded)
		}
	}
	currentRaw, ok := body.Error.Details["current"]
	if !ok {
		t.Fatalf("task conflict omitted current retry metadata: %s", encoded)
	}
	var current map[string]json.RawMessage
	if err := json.Unmarshal(currentRaw, &current); err != nil {
		t.Fatalf("decode redacted current task: %v", err)
	}
	for _, field := range []string{"title", "description", "priority", "column_id", "labels", "assignee", "due_at", "created_at", "updated_at"} {
		if _, ok := current[field]; ok {
			t.Fatalf("redacted current task contains read-protected field %q: %s", field, string(currentRaw))
		}
	}
	if _, ok := current["id"]; !ok {
		t.Fatalf("redacted current task omitted id: %s", string(currentRaw))
	}
	if _, ok := current["version"]; !ok {
		t.Fatalf("redacted current task omitted version: %s", string(currentRaw))
	}
}

func assertRedactedTaskClaimConflict(t *testing.T, response interface{ Result() *http.Response }, wantCode string, secrets ...string) {
	t.Helper()
	result := response.Result()
	defer result.Body.Close()
	if result.StatusCode != http.StatusConflict {
		t.Fatalf("task claim conflict status = %d, want 409; body=%s", result.StatusCode, readResponseBody(result))
	}
	var body struct {
		Error struct {
			Code    string                     `json:"code"`
			Details map[string]json.RawMessage `json:"details"`
		} `json:"error"`
	}
	if err := json.NewDecoder(result.Body).Decode(&body); err != nil {
		t.Fatalf("decode task claim conflict: %v", err)
	}
	if body.Error.Code != wantCode {
		t.Fatalf("task claim conflict code = %q, want %q", body.Error.Code, wantCode)
	}
	encoded := mustMarshalJSON(t, body)
	for _, secret := range secrets {
		if strings.Contains(encoded, secret) {
			t.Fatalf("task claim conflict leaked secret %q: %s", secret, encoded)
		}
	}
	if len(body.Error.Details) != 0 {
		t.Fatalf("task claim conflict details = %s, want empty", encoded)
	}
}

func mustMarshalJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	return string(encoded)
}
