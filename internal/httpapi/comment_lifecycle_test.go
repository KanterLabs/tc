package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/KanterLabs/helm/internal/store"
)

func TestCommentLifecycleHTTPUsesETagsAndRetainsAuditHistory(t *testing.T) {
	server, data, project, task := progressFixture(t)

	created := request(t, server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/comments", map[string]any{
		"body": "**initial**",
	}, map[string]string{"Idempotency-Key": "comment-create-1"})
	if created.Code != http.StatusCreated || created.Header().Get("ETag") != `"v1"` {
		t.Fatalf("create comment = %d etag=%q body=%s", created.Code, created.Header().Get("ETag"), created.Body.String())
	}
	var comment store.Comment
	if err := json.Unmarshal(created.Body.Bytes(), &comment); err != nil {
		t.Fatalf("decode created comment: %v", err)
	}
	if comment.Version != 1 || comment.Body != "**initial**" {
		t.Fatalf("created comment = %+v", comment)
	}
	read := request(t, server, http.MethodGet, "/api/v1/tasks/"+task.ID+"/comments/"+comment.ID, nil, nil)
	if read.Code != http.StatusOK || read.Header().Get("ETag") != `"v1"` || !strings.Contains(read.Body.String(), `"body":"**initial**"`) {
		t.Fatalf("read comment = %d etag=%q body=%s", read.Code, read.Header().Get("ETag"), read.Body.String())
	}

	editHeaders := map[string]string{
		"If-Match":        `"v1"`,
		"Idempotency-Key": "comment-edit-1",
	}
	edited := request(t, server, http.MethodPatch, "/api/v1/tasks/"+task.ID+"/comments/"+comment.ID, map[string]any{
		"body": "Edited with **Markdown**",
	}, editHeaders)
	if edited.Code != http.StatusOK || edited.Header().Get("ETag") != `"v2"` {
		t.Fatalf("edit comment = %d etag=%q body=%s", edited.Code, edited.Header().Get("ETag"), edited.Body.String())
	}
	var editedComment store.Comment
	if err := json.Unmarshal(edited.Body.Bytes(), &editedComment); err != nil {
		t.Fatalf("decode edited comment: %v", err)
	}
	if editedComment.Version != 2 || editedComment.Body != "Edited with **Markdown**" {
		t.Fatalf("edited comment = %+v", editedComment)
	}
	replay := request(t, server, http.MethodPatch, "/api/v1/tasks/"+task.ID+"/comments/"+comment.ID, map[string]any{
		"body": "Edited with **Markdown**",
	}, editHeaders)
	if replay.Code != edited.Code || replay.Body.String() != edited.Body.String() || replay.Header().Get("ETag") != edited.Header().Get("ETag") {
		t.Fatalf("comment edit replay differs: first=%d/%q/%s replay=%d/%q/%s", edited.Code, edited.Header().Get("ETag"), edited.Body.String(), replay.Code, replay.Header().Get("ETag"), replay.Body.String())
	}

	stale := request(t, server, http.MethodPatch, "/api/v1/tasks/"+task.ID+"/comments/"+comment.ID, map[string]any{
		"body": "stale overwrite",
	}, map[string]string{"If-Match": `"v1"`, "Idempotency-Key": "comment-edit-stale"})
	if stale.Code != http.StatusConflict || errorCode(t, stale) != "conflict" {
		t.Fatalf("stale comment edit = %d %s, want 409 conflict", stale.Code, stale.Body.String())
	}

	other, err := data.CreateAgent(context.Background(), store.Actor{Kind: "agent", Name: "Other comment actor", ProjectIDs: []string{project.ID}}, "actor-disabled-mode", "")
	if err != nil {
		t.Fatalf("create other actor: %v", err)
	}
	_, token, err := data.CreateTokenBy(context.Background(), other.ID, "actor-disabled-mode", "comment-write", []string{"tasks:write"}, []string{project.ID}, nil)
	if err != nil {
		t.Fatalf("create other actor token: %v", err)
	}
	unauthorized := request(t, server, http.MethodPatch, "/api/v1/tasks/"+task.ID+"/comments/"+comment.ID, map[string]any{
		"body": "unauthorized overwrite",
	}, map[string]string{
		"Authorization":   "Bearer " + token,
		"If-Match":        `"v2"`,
		"Idempotency-Key": "comment-edit-unauthorized",
	})
	if unauthorized.Code != http.StatusForbidden {
		t.Fatalf("unauthorized comment edit = %d %s, want 403", unauthorized.Code, unauthorized.Body.String())
	}

	deleted := request(t, server, http.MethodDelete, "/api/v1/tasks/"+task.ID+"/comments/"+comment.ID, nil, map[string]string{
		"If-Match":        `"v2"`,
		"Idempotency-Key": "comment-delete-1",
	})
	if deleted.Code != http.StatusNoContent || deleted.Header().Get("ETag") != `"v3"` || deleted.Body.Len() != 0 {
		t.Fatalf("delete comment = %d etag=%q body=%s", deleted.Code, deleted.Header().Get("ETag"), deleted.Body.String())
	}
	deleteReplay := request(t, server, http.MethodDelete, "/api/v1/tasks/"+task.ID+"/comments/"+comment.ID, nil, map[string]string{
		"If-Match":        `"v2"`,
		"Idempotency-Key": "comment-delete-1",
	})
	if deleteReplay.Code != deleted.Code || deleteReplay.Header().Get("ETag") != deleted.Header().Get("ETag") {
		t.Fatalf("comment delete replay differs: first=%d/%q replay=%d/%q", deleted.Code, deleted.Header().Get("ETag"), deleteReplay.Code, deleteReplay.Header().Get("ETag"))
	}

	active := request(t, server, http.MethodGet, "/api/v1/tasks/"+task.ID+"/comments", nil, nil)
	if active.Code != http.StatusOK || strings.Contains(active.Body.String(), comment.ID) || strings.Contains(active.Body.String(), "Edited with") {
		t.Fatalf("deleted comment leaked from active list: %d %s", active.Code, active.Body.String())
	}
	timeline := request(t, server, http.MethodGet, "/api/v1/tasks/"+task.ID+"/timeline?limit=100", nil, nil)
	if timeline.Code != http.StatusOK {
		t.Fatalf("comment lifecycle timeline = %d %s", timeline.Code, timeline.Body.String())
	}
	if strings.Contains(timeline.Body.String(), `"kind":"comment"`) || strings.Contains(timeline.Body.String(), "comment.created") {
		t.Fatalf("deleted comment or create event leaked from timeline: %s", timeline.Body.String())
	}
	if !strings.Contains(timeline.Body.String(), "comment.updated") || !strings.Contains(timeline.Body.String(), "comment.deleted") {
		t.Fatalf("comment lifecycle events missing from timeline: %s", timeline.Body.String())
	}

	var retainedBody string
	if err := data.DB.QueryRowContext(context.Background(), `SELECT body FROM comments WHERE id=?`, comment.ID).Scan(&retainedBody); err != nil {
		t.Fatalf("read retained comment body: %v", err)
	}
	if retainedBody != "Edited with **Markdown**" {
		t.Fatalf("retained comment body = %q", retainedBody)
	}
}
