package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KanterLabs/helm/internal/auth"
	"github.com/KanterLabs/helm/internal/store"
)

func TestDecodeTaskMoveInputRequiresBoundedFields(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]any
		wantErr string
	}{
		{name: "missing destination", payload: map[string]any{"expected_source_column_id": "source", "source": "board"}, wantErr: "destination column is required"},
		{name: "missing source column", payload: map[string]any{"destination_column_id": "destination", "source": "board"}, wantErr: "expected source column is required"},
		{name: "missing provenance", payload: map[string]any{"destination_column_id": "destination", "expected_source_column_id": "source"}, wantErr: "source is required"},
		{name: "unknown field", payload: map[string]any{"destination_column_id": "destination", "expected_source_column_id": "source", "source": "board", "position": 1}, wantErr: "unknown task move field"},
		{name: "conflicting aliases", payload: map[string]any{"destination_column_id": "destination", "column_id": "other", "expected_source_column_id": "source", "source": "board"}, wantErr: "aliases must agree"},
		{name: "empty reason", payload: map[string]any{"destination_column_id": "destination", "expected_source_column_id": "source", "source": "board", "reason": "  "}, wantErr: "reason must not be empty"},
		{name: "oversized source", payload: map[string]any{"destination_column_id": "destination", "expected_source_column_id": "source", "source": strings.Repeat("x", maxTaskMoveHTTPSourceLength+1)}, wantErr: "source is too long"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := taskMoveRequest(t, http.MethodPost, "/api/v1/tasks/task/move", tc.payload, nil)
			_, err := decodeTaskMoveInput(req.Request)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("decode error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestTaskMoveRequiresWriteIfMatchAndIdempotency(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	actor, err := data.EnsureDisabledActor(ctx)
	if err != nil {
		t.Fatalf("ensure actor: %v", err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("MOVEHTTP"), Name: stringPtr("Move HTTP")}, actor.ID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	task, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("move validation")}, actor.ID)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	columns, err := data.ListColumns(ctx, project.ID)
	if err != nil {
		t.Fatalf("list columns: %v", err)
	}
	identity := auth.Identity{Actor: actor}

	missingVersion := taskMoveRequest(t, http.MethodPost, "/api/v1/tasks/"+task.ID+"/move", map[string]any{
		"destination_column_id": columns[1].ID, "expected_source_column_id": columns[0].ID, "source": "board",
	}, map[string]string{"Idempotency-Key": "missing-version"})
	server.taskMove(missingVersion, missingVersion.Request, identity, task.ID)
	if missingVersion.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match status = %d, body=%s", missingVersion.Code, missingVersion.Body.String())
	}

	missingKey := taskMoveRequest(t, http.MethodPost, "/api/v1/tasks/"+task.ID+"/move", map[string]any{
		"destination_column_id": columns[1].ID, "expected_source_column_id": columns[0].ID, "source": "board",
	}, map[string]string{"If-Match": `"v1"`})
	server.taskMove(missingKey, missingKey.Request, identity, task.ID)
	if missingKey.Code != http.StatusBadRequest || !strings.Contains(missingKey.Body.String(), "idempotency_required") {
		t.Fatalf("missing Idempotency-Key = %d %s", missingKey.Code, missingKey.Body.String())
	}

	readOnly := auth.Identity{Actor: actor, IsToken: true, Token: store.AuthToken{Scopes: map[string]bool{"tasks:read": true}}}
	noWrite := taskMoveRequest(t, http.MethodPost, "/api/v1/tasks/"+task.ID+"/move", map[string]any{
		"destination_column_id": columns[1].ID, "expected_source_column_id": columns[0].ID, "source": "board",
	}, map[string]string{"If-Match": `"v1"`, "Idempotency-Key": "read-only"})
	server.taskMove(noWrite, noWrite.Request, readOnly, task.ID)
	if noWrite.Code != http.StatusForbidden {
		t.Fatalf("missing tasks:write status = %d, body=%s", noWrite.Code, noWrite.Body.String())
	}
}

func TestTaskMoveIdempotencyReplaysRedactedSuccessAfterTaskDeletion(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	actor, err := data.EnsureDisabledActor(ctx)
	if err != nil {
		t.Fatalf("ensure actor: %v", err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("REPLAYMOVE"), Name: stringPtr("Replay move")}, actor.ID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	task, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("secret move title")}, actor.ID)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	columns, err := data.ListColumns(ctx, project.ID)
	if err != nil {
		t.Fatalf("list columns: %v", err)
	}
	agent, err := data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "move writer", ProjectIDs: []string{project.ID}}, actor.ID, "")
	if err != nil {
		t.Fatalf("create writer: %v", err)
	}
	_, plaintext, err := data.CreateTokenBy(ctx, agent.ID, actor.ID, "move", []string{"tasks:write"}, []string{project.ID}, nil)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	token, err := data.LookupToken(ctx, plaintext)
	if err != nil {
		t.Fatalf("lookup token: %v", err)
	}
	identity := auth.Identity{Actor: token.Actor, IsToken: true, Token: token}
	payload := map[string]any{
		"destination_column_id":     columns[1].ID,
		"expected_source_column_id": columns[0].ID,
		"source":                    "reconciler",
		"reason":                    "safe retry",
	}
	headers := map[string]string{"If-Match": `"v1"`, "Idempotency-Key": "move-retry"}
	first := taskMoveRequest(t, http.MethodPost, "/api/v1/tasks/"+task.ID+"/move", payload, headers)
	server.taskMove(first, first.Request, identity, task.ID)
	if first.Code != http.StatusOK {
		t.Fatalf("first move status = %d, body=%s", first.Code, first.Body.String())
	}
	if first.Header().Get("ETag") != `"v2"` {
		t.Fatalf("first move ETag = %q, want v2", first.Header().Get("ETag"))
	}
	if strings.Contains(first.Body.String(), "secret move title") || !strings.Contains(first.Body.String(), task.ID) {
		t.Fatalf("write-only move response leaked task data: %s", first.Body.String())
	}
	if err := data.DeleteTask(ctx, task.ID, 2, actor.ID); err != nil {
		t.Fatalf("delete moved task: %v", err)
	}
	replay := taskMoveRequest(t, http.MethodPost, "/api/v1/tasks/"+task.ID+"/move", payload, headers)
	server.taskMove(replay, replay.Request, identity, task.ID)
	if replay.Code != first.Code || replay.Body.String() != first.Body.String() || replay.Header().Get("ETag") != first.Header().Get("ETag") {
		t.Fatalf("move replay = %d %s etag=%q, want original %d %s etag=%q", replay.Code, replay.Body.String(), replay.Header().Get("ETag"), first.Code, first.Body.String(), first.Header().Get("ETag"))
	}
}

type taskMoveCall struct {
	*httptest.ResponseRecorder
	Request *http.Request
}

func taskMoveRequest(t *testing.T, method, target string, payload any, headers map[string]string) *taskMoveCall {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal move body: %v", err)
	}
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), requestBodyKey, body))
	req.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	return &taskMoveCall{ResponseRecorder: httptest.NewRecorder(), Request: req}
}
