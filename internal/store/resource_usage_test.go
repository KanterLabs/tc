package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/KanterLabs/helm/internal/db"
)

func TestReserveAgentMutationIsAtomicAtBudgetBoundary(t *testing.T) {
	database, err := db.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	ctx := context.Background()
	agent, err := data.CreateActor(ctx, Actor{Kind: "agent", Name: "concurrent budget"}, "")
	if err != nil {
		t.Fatal(err)
	}
	starting := AgentMutationBudgetBytes - 2*AgentMutationOverheadBytes
	if _, err := database.ExecContext(ctx, `INSERT INTO actor_resource_usage(actor_id, reserved_bytes, updated_at) VALUES (?, ?, ?)`, agent.ID, starting, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}

	const attempts = 32
	results := make(chan error, attempts)
	var group sync.WaitGroup
	for i := 0; i < attempts; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			results <- data.ReserveAgentMutation(ctx, agent.ID, 0)
		}()
	}
	group.Wait()
	close(results)

	successes := 0
	for reservationErr := range results {
		if reservationErr == nil {
			successes++
			continue
		}
		if !errors.Is(reservationErr, ErrResourceLimit) {
			t.Fatalf("concurrent reservation error = %v, want resource limit or success", reservationErr)
		}
	}
	if successes != 2 {
		t.Fatalf("successful reservations = %d, want exactly 2", successes)
	}
	used, err := data.AgentMutationUsage(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if used != AgentMutationBudgetBytes {
		t.Fatalf("usage = %d, budget = %d", used, AgentMutationBudgetBytes)
	}
}

func TestResetAgentMutationUsageAllowsOperatorRecovery(t *testing.T) {
	database, err := db.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	ctx := context.Background()
	agent, err := data.CreateActor(ctx, Actor{Kind: "agent", Name: "reset budget"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := data.ReserveAgentMutation(ctx, agent.ID, 0); err != nil {
		t.Fatal(err)
	}
	if used, err := data.AgentMutationUsage(ctx, agent.ID); err != nil || used == 0 {
		t.Fatalf("usage before reset = %d, err=%v", used, err)
	}
	if err := data.ResetAgentMutationUsage(ctx, agent.ID); err != nil {
		t.Fatal(err)
	}
	if used, err := data.AgentMutationUsage(ctx, agent.ID); err != nil || used != 0 {
		t.Fatalf("usage after reset = %d, err=%v", used, err)
	}
}

func TestReserveAgentMutationChargesBodyAndFixedOverhead(t *testing.T) {
	database, err := db.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	ctx := context.Background()
	agent, err := data.CreateActor(ctx, Actor{Kind: "agent", Name: "body budget"}, "")
	if err != nil {
		t.Fatal(err)
	}
	const bodyBytes = 1234
	if err := data.ReserveAgentMutation(ctx, agent.ID, bodyBytes); err != nil {
		t.Fatal(err)
	}
	used, err := data.AgentMutationUsage(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := AgentMutationOverheadBytes + bodyBytes
	if used != want {
		t.Fatalf("usage = %d, want fixed overhead plus body = %d", used, want)
	}
}
