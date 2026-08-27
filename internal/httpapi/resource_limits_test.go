package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"roadmap/internal/store"
)

func TestMutationRateLimiterUsesActorKeysAndStaysBounded(t *testing.T) {
	limiter := newMutationRateLimiter(1, 2, 2, time.Hour)
	now := time.Unix(100, 0)
	limiter.now = func() time.Time { return now }

	if allowed, _ := limiter.allow("agent-a"); !allowed {
		t.Fatal("first actor-a mutation was rejected")
	}
	if allowed, _ := limiter.allow("agent-a"); !allowed {
		t.Fatal("second actor-a mutation was rejected")
	}
	if allowed, retryAfter := limiter.allow("agent-a"); allowed || retryAfter <= 0 {
		t.Fatalf("third actor-a mutation = allowed=%v retry_after=%s, want rejection", allowed, retryAfter)
	}
	if allowed, _ := limiter.allow("agent-b"); !allowed {
		t.Fatal("actor-b should have an independent bucket")
	}
	// A full map cannot grow in response to new authenticated actor IDs. The
	// oldest entry is evicted to make room, and the bound remains absolute.
	if allowed, _ := limiter.allow("agent-c"); !allowed {
		t.Fatal("actor-c should be admitted after bounded eviction")
	}
	if got := len(limiter.entries); got > limiter.maxEntries {
		t.Fatalf("limiter entries = %d, max = %d", got, limiter.maxEntries)
	}
}

func TestBearerMutationLimitIsSharedAcrossTokensAndGETsRemainAvailable(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	agent, err := data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "budget agent"}, "actor-disabled-mode", "")
	if err != nil {
		t.Fatal(err)
	}
	_, tokenOne, err := data.CreateTokenBy(ctx, agent.ID, "actor-disabled-mode", "one", []string{"projects:read", "projects:write"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, tokenTwo, err := data.CreateTokenBy(ctx, agent.ID, "actor-disabled-mode", "two", []string{"projects:read", "projects:write"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < defaultAgentMutationBurst; i++ {
		response := request(t, server, http.MethodPost, "/api/v1/projects", map[string]any{
			"key":  fmt.Sprintf("RATE%02d", i),
			"name": fmt.Sprintf("Rate project %02d", i),
		}, map[string]string{"Authorization": "Bearer " + tokenOne, "Content-Type": "application/json"})
		if response.Code != http.StatusCreated {
			t.Fatalf("mutation %d status = %d, body=%s", i, response.Code, response.Body.String())
		}
	}

	// A new token owned by the same actor cannot reset the actor bucket.
	rejected := request(t, server, http.MethodPost, "/api/v1/projects", map[string]any{
		"key": "RATEBLOCKED", "name": "Must not be written",
	}, map[string]string{"Authorization": "Bearer " + tokenTwo, "Content-Type": "application/json"})
	if rejected.Code != http.StatusTooManyRequests || rejected.Header().Get("Retry-After") == "" {
		t.Fatalf("shared token rejection = %d Retry-After=%q body=%s", rejected.Code, rejected.Header().Get("Retry-After"), rejected.Body.String())
	}
	if _, err := data.GetProject(ctx, "RATEBLOCKED"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("rate-limited project lookup = %v, want not found", err)
	}

	read := request(t, server, http.MethodGet, "/api/v1/projects", nil, map[string]string{"Authorization": "Bearer " + tokenTwo})
	if read.Code == http.StatusTooManyRequests || read.Code != http.StatusOK {
		t.Fatalf("GET after mutation limit status = %d, body=%s", read.Code, read.Body.String())
	}

	// The disabled-mode development actor models the human owner and remains
	// unrestricted even while an agent actor is exhausted.
	human := request(t, server, http.MethodPost, "/api/v1/projects", map[string]any{
		"key": "HUMANOK", "name": "Owner mutation",
	}, nil)
	if human.Code != http.StatusCreated {
		t.Fatalf("human mutation after agent limit = %d, body=%s", human.Code, human.Body.String())
	}
}

func TestAgentMutationBudgetRejectsBeforeHandlerWrite(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	agent, err := data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "full budget"}, "actor-disabled-mode", "")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := data.CreateTokenBy(ctx, agent.ID, "actor-disabled-mode", "full", []string{"projects:write"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := data.DB.ExecContext(ctx, `INSERT INTO actor_resource_usage(actor_id, reserved_bytes, updated_at) VALUES (?, ?, ?)`, agent.ID, store.AgentMutationBudgetBytes, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}

	response := request(t, server, http.MethodPost, "/api/v1/projects", map[string]any{
		"key": "BUDGETBLOCKED", "name": "Must not be written",
	}, map[string]string{"Authorization": "Bearer " + token, "Content-Type": "application/json"})
	if response.Code != http.StatusInsufficientStorage {
		t.Fatalf("full-budget status = %d, body=%s", response.Code, response.Body.String())
	}
	if _, err := data.GetProject(ctx, "BUDGETBLOCKED"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("full-budget project lookup = %v, want not found", err)
	}
}

func TestIdempotentReplayRestoresLocationWithoutNewAdmission(t *testing.T) {
	server, data := testServer(t, "disabled")
	ctx := context.Background()
	if _, err := data.EnsureDisabledActor(ctx); err != nil {
		t.Fatal(err)
	}
	agent, err := data.CreateAgent(ctx, store.Actor{Kind: "agent", Name: "idempotent replay"}, "actor-disabled-mode", "")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := data.CreateTokenBy(ctx, agent.ID, "actor-disabled-mode", "replay", []string{"projects:write"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Exhaust the short-lived bucket after the first write. A replay must be
	// served from the idempotency record and therefore must not be rejected by
	// the limiter or charged against the persistent budget again.
	server.mutationLimiter = newMutationRateLimiter(1, 1, 2, time.Hour)
	server.agentRequestLimiter = newMutationRateLimiter(1, 2, 2, time.Hour)
	headers := map[string]string{
		"Authorization":   "Bearer " + token,
		"Content-Type":    "application/json",
		"Idempotency-Key": "project-replay-1",
	}
	payload := map[string]any{"key": "REPLAY", "name": "Replay project"}
	first := request(t, server, http.MethodPost, "/api/v1/projects", payload, headers)
	if first.Code != http.StatusCreated {
		t.Fatalf("first mutation status = %d, body=%s", first.Code, first.Body.String())
	}
	location := first.Header().Get("Location")
	if location == "" {
		t.Fatal("first mutation did not return Location")
	}
	used, err := data.AgentMutationUsage(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}

	replay := request(t, server, http.MethodPost, "/api/v1/projects", payload, headers)
	if replay.Code != first.Code || replay.Body.String() != first.Body.String() || replay.Header().Get("Location") != location {
		t.Fatalf("replay = status %d location %q body=%s, want status %d location %q body=%s", replay.Code, replay.Header().Get("Location"), replay.Body.String(), first.Code, location, first.Body.String())
	}
	usedAfter, err := data.AgentMutationUsage(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if usedAfter != used {
		t.Fatalf("replay budget usage = %d, first usage = %d", usedAfter, used)
	}

	// General bearer admission still protects the replay lookup itself. Once
	// its small bucket is exhausted, a repeated replay is rejected without
	// entering mutation admission or writing another project.
	limitedReplay := request(t, server, http.MethodPost, "/api/v1/projects", payload, headers)
	if limitedReplay.Code != http.StatusTooManyRequests {
		t.Fatalf("repeated replay status = %d, body=%s", limitedReplay.Code, limitedReplay.Body.String())
	}
	usedAfterLimitedReplay, err := data.AgentMutationUsage(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if usedAfterLimitedReplay != used {
		t.Fatalf("rate-limited replay budget usage = %d, first usage = %d", usedAfterLimitedReplay, used)
	}
	if _, err := data.GetProject(ctx, "REPLAY"); err != nil {
		t.Fatalf("replayed project lookup = %v", err)
	}
}
