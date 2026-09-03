package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/KanterLabs/helm/internal/store"
)

func TestContractRejectsUnknownJSONFieldsAndInvalidPagination(t *testing.T) {
	server, _ := testServer(t, "disabled")

	unknown := request(t, server, http.MethodPost, "/api/v1/projects", map[string]any{
		"key":        "UNKNOWN",
		"name":       "Unknown field",
		"unexpected": true,
	}, nil)
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown project field status = %d, body=%s", unknown.Code, unknown.Body.String())
	}

	for _, target := range []string{
		"/api/v1/projects?limit=0",
		"/api/v1/projects?limit=not-a-number",
		"/api/v1/projects?cursor=not-a-cursor",
		"/api/v1/projects?cursor=-1",
	} {
		response := request(t, server, http.MethodGet, target, nil, nil)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid pagination %s status = %d, body=%s", target, response.Code, response.Body.String())
		}
	}
}

func TestContractOpenAPIDocumentIsNotCached(t *testing.T) {
	server, _ := testServer(t, "disabled")
	response := request(t, server, http.MethodGet, "/openapi.json", nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("openapi status = %d, body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("openapi Cache-Control = %q, want no-store", got)
	}
}

func TestAuthMutationsRejectIdempotencyKey(t *testing.T) {
	server, _ := testServer(t, "local")
	for _, path := range []string{"setup", "login", "logout"} {
		response := request(t, server, http.MethodPost, "/api/v1/auth/"+path, map[string]any{}, map[string]string{
			"Idempotency-Key": "auth-retry",
		})
		if response.Code != http.StatusBadRequest {
			t.Fatalf("auth %s idempotency status = %d, body=%s", path, response.Code, response.Body.String())
		}
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode auth %s error: %v", path, err)
		}
		if body.Error.Code != "idempotency_not_supported" {
			t.Fatalf("auth %s error code = %q", path, body.Error.Code)
		}
	}
}

func TestTaskMutationClaimConflictDetails(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatalf("ensure disabled actor: %v", err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("CONTRACT"), Name: stringPtr("Contract")}, "actor-disabled-mode")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	owner, err := data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "owner", ProjectIDs: []string{project.ID}}, "actor-disabled-mode", "")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	other, err := data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "other", ProjectIDs: []string{project.ID}}, "actor-disabled-mode", "")
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	_, otherToken, err := data.CreateTokenBy(ctx, other.ID, "actor-disabled-mode", "other", []string{"tasks:read", "tasks:write"}, []string{project.ID}, nil)
	if err != nil {
		t.Fatalf("create other token: %v", err)
	}

	task, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("patch claim")}, "actor-disabled-mode")
	if err != nil {
		t.Fatalf("create patch task: %v", err)
	}
	claimed, err := data.ClaimTask(ctx, task.ID, owner.ID, time.Hour, task.Version)
	if err != nil {
		t.Fatalf("claim patch task: %v", err)
	}
	patchResponse := request(t, server, http.MethodPatch, "/api/v1/tasks/"+task.ID, map[string]any{"title": "blocked"}, map[string]string{
		"Authorization": "Bearer " + otherToken,
		"If-Match":      `"v2"`,
	})
	assertTaskClaimConflictDetails(t, patchResponse, owner.ID, claimed.ClaimExpiresAt)

	deleteTask, err := data.CreateTask(ctx, project.ID, store.TaskInput{Title: stringPtr("delete claim")}, "actor-disabled-mode")
	if err != nil {
		t.Fatalf("create delete task: %v", err)
	}
	deleteClaim, err := data.ClaimTask(ctx, deleteTask.ID, owner.ID, time.Hour, deleteTask.Version)
	if err != nil {
		t.Fatalf("claim delete task: %v", err)
	}
	deleteResponse := request(t, server, http.MethodDelete, "/api/v1/tasks/"+deleteTask.ID, nil, map[string]string{
		"Authorization": "Bearer " + otherToken,
		"If-Match":      `"v2"`,
	})
	assertTaskClaimConflictDetails(t, deleteResponse, owner.ID, deleteClaim.ClaimExpiresAt)
}

func assertTaskClaimConflictDetails(t *testing.T, response interface {
	Result() *http.Response
}, claimedBy string, expiresAt *string) {
	t.Helper()
	result := response.Result()
	if result.StatusCode != http.StatusConflict {
		t.Fatalf("claim-conflict status = %d", result.StatusCode)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				ClaimedBy      string  `json:"claimed_by"`
				ClaimExpiresAt *string `json:"claim_expires_at"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.NewDecoder(result.Body).Decode(&body); err != nil {
		t.Fatalf("decode claim conflict: %v", err)
	}
	if body.Error.Code != "task_already_claimed" || body.Error.Details.ClaimedBy != claimedBy {
		t.Fatalf("claim conflict = %+v, want actor %q", body.Error, claimedBy)
	}
	if expiresAt == nil || body.Error.Details.ClaimExpiresAt == nil || !strings.EqualFold(*body.Error.Details.ClaimExpiresAt, *expiresAt) {
		t.Fatalf("claim expiry = %v, want %v", body.Error.Details.ClaimExpiresAt, expiresAt)
	}
}

func TestTaskCreatePositionContract(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatalf("ensure disabled actor: %v", err)
	}
	project, err := data.CreateProject(ctx, store.ProjectInput{Key: stringPtr("POSITION"), Name: stringPtr("Position")}, "actor-disabled-mode")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	for _, position := range []any{nil, float64(1000000000001)} {
		response := request(t, server, http.MethodPost, "/api/v1/projects/"+project.ID+"/tasks", map[string]any{
			"title":    "invalid position",
			"position": position,
		}, nil)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("position %#v status = %d, body=%s", position, response.Code, response.Body.String())
		}
	}
}
