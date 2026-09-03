package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/KanterLabs/helm/internal/store"
)

func TestTaskHeartbeatRefreshesOnlyAgentWorkAndKeepsTaskETag(t *testing.T) {
	fixture := newAgentWorkHTTPFixture(t, "HEARTBEATAPI")
	ctx := context.Background()
	task := fixture.createTask(t, "heartbeat API", "")
	claimed, err := fixture.data.ClaimTask(ctx, task.ID, fixture.actor.ID, time.Hour, task.Version)
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	published := publishAgentWorkHTTP(t, fixture, claimed, "working", "heartbeat API snapshot", nil)
	oldUpdatedAt := "2026-01-01T00:00:00Z"
	if _, err := fixture.data.DB.ExecContext(ctx, `UPDATE task_agent_work SET updated_at=? WHERE task_id=?`, oldUpdatedAt, task.ID); err != nil {
		t.Fatalf("age snapshot: %v", err)
	}
	before, err := fixture.data.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("read before heartbeat: %v", err)
	}
	commentsBefore, eventsBefore, idempotencyBefore := heartbeatDurableCounts(t, fixture, task.ID)

	response := request(t, fixture.server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/heartbeat", map[string]any{
		"operation_id": "http-hardening/" + task.ID,
	}, map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusOK || response.Header().Get("ETag") != fmt.Sprintf(`"v%d"`, published.Version) {
		t.Fatalf("heartbeat = %d etag=%q body=%s, want 200 and unchanged v%d", response.Code, response.Header().Get("ETag"), response.Body.String(), published.Version)
	}
	var updated store.Task
	if err := json.Unmarshal(response.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode heartbeat response: %v", err)
	}
	if updated.Version != before.Version || updated.UpdatedAt != before.UpdatedAt {
		t.Fatalf("heartbeat changed task metadata: before v%d/%q after v%d/%q", before.Version, before.UpdatedAt, updated.Version, updated.UpdatedAt)
	}
	if updated.AgentWork == nil || updated.AgentWork.UpdatedAt == oldUpdatedAt || updated.AgentWork.Summary != before.AgentWork.Summary || updated.AgentWork.OperationID != before.AgentWork.OperationID {
		t.Fatalf("heartbeat response work = %+v, want only refreshed timestamp", updated.AgentWork)
	}
	if updated.AgentWork.Phase != before.AgentWork.Phase || updated.AgentWork.NextAction != before.AgentWork.NextAction || updated.AgentWork.State != before.AgentWork.State || len(updated.AgentWork.CheckpointRefs) != len(before.AgentWork.CheckpointRefs) {
		t.Fatalf("heartbeat changed snapshot content: before=%+v after=%+v", before.AgentWork, updated.AgentWork)
	}
	commentsAfter, eventsAfter, idempotencyAfter := heartbeatDurableCounts(t, fixture, task.ID)
	if commentsAfter != commentsBefore || eventsAfter != eventsBefore || idempotencyAfter != idempotencyBefore {
		t.Fatalf("heartbeat added durable noise: comments %d/%d events %d/%d idempotency %d/%d", commentsBefore, commentsAfter, eventsBefore, eventsAfter, idempotencyBefore, idempotencyAfter)
	}
}

func TestTaskHeartbeatBearerRedactionAndBudgetNeutrality(t *testing.T) {
	fixture := newAgentWorkHTTPFixture(t, "HEARTBEATAGENT")
	ctx := context.Background()
	agent, err := fixture.data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "heartbeat agent", ProjectIDs: []string{fixture.project.ID}}, fixture.actor.ID, "")
	if err != nil {
		t.Fatalf("create heartbeat agent: %v", err)
	}
	_, token, err := fixture.data.CreateTokenBy(ctx, agent.ID, fixture.actor.ID, "heartbeat", []string{"tasks:claim"}, []string{fixture.project.ID}, nil)
	if err != nil {
		t.Fatalf("create heartbeat token: %v", err)
	}
	task := fixture.createTask(t, "claim-only heartbeat", "")
	claimed, err := fixture.data.ClaimTask(ctx, task.ID, agent.ID, time.Hour, task.Version)
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	published, err := fixture.data.PublishAgentWork(ctx, task.ID, store.AgentWorkInput{OperationID: "heartbeat/agent", State: "working", Summary: "Agent heartbeat"}, claimed.Version, agent.ID)
	if err != nil {
		t.Fatalf("publish agent work: %v", err)
	}
	usageBefore, err := fixture.data.AgentMutationUsage(ctx, agent.ID)
	if err != nil {
		t.Fatalf("read budget before heartbeat: %v", err)
	}
	_, _, idempotencyBefore := heartbeatDurableCounts(t, fixture, task.ID)
	response := request(t, fixture.server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/heartbeat", map[string]any{"operation_id": "heartbeat/agent"}, map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	})
	if response.Code != http.StatusOK || response.Header().Get("ETag") != fmt.Sprintf(`"v%d"`, published.Version) {
		t.Fatalf("claim-only heartbeat = %d etag=%q body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	if want := fmt.Sprintf(`{"id":%q,"version":%d}`, task.ID, published.Version); response.Body.String() != want {
		t.Fatalf("claim-only heartbeat body = %s, want exact reduced response %s", response.Body.String(), want)
	}
	usageAfter, err := fixture.data.AgentMutationUsage(ctx, agent.ID)
	if err != nil {
		t.Fatalf("read budget after heartbeat: %v", err)
	}
	if usageAfter != usageBefore {
		t.Fatalf("heartbeat reserved mutation budget: before=%d after=%d", usageBefore, usageAfter)
	}
	_, _, idempotencyAfter := heartbeatDurableCounts(t, fixture, task.ID)
	if idempotencyAfter != idempotencyBefore {
		t.Fatalf("heartbeat persisted idempotency row: before=%d after=%d", idempotencyBefore, idempotencyAfter)
	}
}

func TestTaskHeartbeatRejectsInvalidRequestsAndUnsupportedIdempotency(t *testing.T) {
	fixture := newAgentWorkHTTPFixture(t, "HEARTBEATVALID")
	task := fixture.createTask(t, "heartbeat validation", "")
	cases := []struct {
		name    string
		payload any
		header  map[string]string
	}{
		{name: "missing operation", payload: map[string]any{}, header: map[string]string{"Content-Type": "application/json"}},
		{name: "null operation", payload: map[string]any{"operation_id": nil}, header: map[string]string{"Content-Type": "application/json"}},
		{name: "empty operation", payload: map[string]any{"operation_id": "  "}, header: map[string]string{"Content-Type": "application/json"}},
		{name: "unknown field", payload: map[string]any{"operation_id": "heartbeat/valid", "state": "working"}, header: map[string]string{"Content-Type": "application/json"}},
		{name: "idempotency unsupported", payload: map[string]any{"operation_id": "heartbeat/valid"}, header: map[string]string{"Content-Type": "application/json", "Idempotency-Key": "heartbeat-key"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			response := request(t, fixture.server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/heartbeat", test.payload, test.header)
			wantCode := "invalid_request"
			if test.name == "idempotency unsupported" {
				wantCode = "idempotency_not_supported"
			}
			if response.Code != http.StatusBadRequest || errorCode(t, response) != wantCode {
				t.Fatalf("heartbeat %s = %d %s, want 400 %s", test.name, response.Code, response.Body.String(), wantCode)
			}
		})
	}
}

func TestTaskHeartbeatRateLimitDoesNotConsumeBudget(t *testing.T) {
	fixture := newAgentWorkHTTPFixture(t, "HEARTBEATRATE")
	ctx := context.Background()
	agent, err := fixture.data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "rate limited heartbeat", ProjectIDs: []string{fixture.project.ID}}, fixture.actor.ID, "")
	if err != nil {
		t.Fatalf("create heartbeat agent: %v", err)
	}
	_, token, err := fixture.data.CreateTokenBy(ctx, agent.ID, fixture.actor.ID, "heartbeat rate", []string{"tasks:claim"}, []string{fixture.project.ID}, nil)
	if err != nil {
		t.Fatalf("create heartbeat token: %v", err)
	}
	task := fixture.createTask(t, "rate limited heartbeat", "")
	claimed, err := fixture.data.ClaimTask(ctx, task.ID, agent.ID, time.Hour, task.Version)
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	if _, err := fixture.data.PublishAgentWork(ctx, task.ID, store.AgentWorkInput{OperationID: "heartbeat/rate", State: "working", Summary: "Rate limited heartbeat"}, claimed.Version, agent.ID); err != nil {
		t.Fatalf("publish agent work: %v", err)
	}
	fixture.server.mutationLimiter = newMutationRateLimiter(1, 1, 2, time.Hour)
	headers := map[string]string{"Authorization": "Bearer " + token, "Content-Type": "application/json"}
	first := request(t, fixture.server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/heartbeat", map[string]any{"operation_id": "heartbeat/rate"}, headers)
	if first.Code != http.StatusOK {
		t.Fatalf("first heartbeat = %d %s", first.Code, first.Body.String())
	}
	usageBefore, err := fixture.data.AgentMutationUsage(ctx, agent.ID)
	if err != nil {
		t.Fatalf("read budget before limited heartbeat: %v", err)
	}
	second := request(t, fixture.server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/heartbeat", map[string]any{"operation_id": "heartbeat/rate"}, headers)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("limited heartbeat = %d %s, want 429", second.Code, second.Body.String())
	}
	usageAfter, err := fixture.data.AgentMutationUsage(ctx, agent.ID)
	if err != nil {
		t.Fatalf("read budget after limited heartbeat: %v", err)
	}
	if usageAfter != usageBefore {
		t.Fatalf("limited heartbeat changed budget: before=%d after=%d", usageBefore, usageAfter)
	}
}

func heartbeatDurableCounts(t *testing.T, fixture agentWorkHTTPFixture, taskID string) (comments, events, idempotency int) {
	t.Helper()
	ctx := context.Background()
	for query, target := range map[string]*int{
		`SELECT COUNT(1) FROM comments WHERE task_id=?`: &comments,
		`SELECT COUNT(1) FROM events WHERE task_id=?`:   &events,
	} {
		if err := fixture.data.DB.QueryRowContext(ctx, query, taskID).Scan(target); err != nil {
			t.Fatalf("count heartbeat durable rows (%s): %v", query, err)
		}
	}
	if err := fixture.data.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM idempotency_keys`).Scan(&idempotency); err != nil {
		t.Fatalf("count heartbeat idempotency rows: %v", err)
	}
	return comments, events, idempotency
}

func TestTaskHeartbeatMissingSnapshotAndClaimFailuresRemainQuiet(t *testing.T) {
	fixture := newAgentWorkHTTPFixture(t, "HEARTBEATFAIL")
	ctx := context.Background()
	task := fixture.createTask(t, "heartbeat missing snapshot", "")
	claimed, err := fixture.data.ClaimTask(ctx, task.ID, fixture.actor.ID, time.Hour, task.Version)
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	before, err := fixture.data.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("read task before missing snapshot heartbeat: %v", err)
	}
	response := request(t, fixture.server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/heartbeat", map[string]any{"operation_id": "heartbeat/missing"}, map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusForbidden || errorCode(t, response) != "forbidden" {
		t.Fatalf("missing snapshot heartbeat = %d %s, want 403 forbidden", response.Code, response.Body.String())
	}
	after, err := fixture.data.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("read task after missing snapshot heartbeat: %v", err)
	}
	if after.Version != before.Version || after.UpdatedAt != before.UpdatedAt || after.AgentWork != nil || after.CommentCount != before.CommentCount {
		t.Fatalf("missing snapshot rejection mutated task: before=%+v after=%+v", before, after)
	}

	if _, err := fixture.data.DB.ExecContext(ctx, `UPDATE tasks SET claim_expires_at=? WHERE id=?`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), task.ID); err != nil {
		t.Fatalf("expire claim: %v", err)
	}
	if _, err := fixture.data.PublishAgentWork(ctx, task.ID, store.AgentWorkInput{OperationID: "heartbeat/missing", State: "working", Summary: "Late snapshot"}, claimed.Version, fixture.actor.ID); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("expired fixture publish = %v, want forbidden", err)
	}
	// The endpoint still fails closed when a caller presents an operation ID
	// that is absent, regardless of the now-expired claim.
	response = request(t, fixture.server, http.MethodPost, "/api/v1/tasks/"+task.ID+"/heartbeat", map[string]any{"operation_id": "heartbeat/missing"}, map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusForbidden {
		t.Fatalf("expired missing-snapshot heartbeat = %d %s, want 403", response.Code, response.Body.String())
	}
}
