package httpapi

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/KanterLabs/helm/internal/store"
)

// These deterministic fixtures are the offline quality gate. They exercise
// the same strict parser used in production without spending a user's quota
// or persisting representative prompts and project history.
func TestTaskDraftEvaluationFixtures(t *testing.T) {
	ref := func(key, state string) store.TaskContextReference {
		return store.TaskContextReference{ID: key, Key: key, Title: "Relevant work", Priority: "normal", SemanticState: state, Labels: []string{}, UpdatedAt: "2026-09-03T00:00:00Z"}
	}
	noisy := store.TaskDraftContext{CompletedTasks: []store.TaskContextReference{}, OpenTasks: []store.TaskContextReference{}}
	for i := 1; i <= 12; i++ {
		noisy.CompletedTasks = append(noisy.CompletedTasks, ref(fmt.Sprintf("NOISY-%d", i), "completed"))
		noisy.OpenTasks = append(noisy.OpenTasks, ref(fmt.Sprintf("NOISY-%d", i+12), "ready"))
	}
	fixtures := []struct {
		name     string
		context  store.TaskDraftContext
		output   string
		priority string
	}{
		{"empty project", store.TaskDraftContext{}, `{"title":"Establish the first workflow","description":"Create the initial project workflow.","acceptance_criteria":["The first workflow is documented"],"priority":"normal","rationale":"There is no prior work indicating urgency.","supporting_task_keys":[]}`, "normal"},
		{"new project", store.TaskDraftContext{OpenTasks: []store.TaskContextReference{ref("NEW-1", "ready")}}, `{"title":"Extend the ready workflow","description":"Build on the existing ready task.","acceptance_criteria":["The workflow has a verified next step"],"priority":"normal","rationale":"The cited ready task supplies nearby context.","supporting_task_keys":["NEW-1"]}`, "normal"},
		{"mature project", store.TaskDraftContext{CompletedTasks: []store.TaskContextReference{ref("MATURE-8", "completed")}, OpenTasks: []store.TaskContextReference{ref("MATURE-9", "active")}}, `{"title":"Unblock the active release","description":"Finish the dependency required by active release work.","acceptance_criteria":["The active release is no longer blocked"],"priority":"high","rationale":"Active related work makes this time-sensitive.","supporting_task_keys":["MATURE-8","MATURE-9"]}`, "high"},
		{"noisy project", noisy, `{"title":"Prioritize the relevant follow-up","description":"Use only the strongest related evidence.","acceptance_criteria":["The follow-up cites bounded project evidence"],"priority":"high","rationale":"The related ready task is a direct near-term input.","supporting_task_keys":["NOISY-24"]}`, "high"},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			started := time.Now()
			draft, err := decodeTaskDraftSuggestion(fixture.output, fixture.context)
			if err != nil {
				t.Fatalf("schema validity failed: %v", err)
			}
			if draft.Priority != fixture.priority {
				t.Fatalf("priority agreement = %q, want %q", draft.Priority, fixture.priority)
			}
			if time.Since(started) > 50*time.Millisecond {
				t.Fatalf("offline validation latency exceeded 50ms")
			}
		})
	}
}

func TestTaskDraftEvaluationRejectsIrrelevantCitationAndClassifiesCancellation(t *testing.T) {
	contextPack := store.TaskDraftContext{OpenTasks: []store.TaskContextReference{{Key: "SAFE-1"}}}
	output := `{"title":"Draft","description":"Description","acceptance_criteria":["Outcome"],"priority":"normal","rationale":"Reason","supporting_task_keys":["INVENTED-9"]}`
	if _, err := decodeTaskDraftSuggestion(output, contextPack); err == nil {
		t.Fatal("irrelevant citation passed the relevance gate")
	}
	if got := classifyCodexDraftError(context.Canceled); got != "canceled" {
		t.Fatalf("cancellation classification = %q", got)
	}
	if got := classifyCodexDraftError(context.DeadlineExceeded); got != "timed_out" {
		t.Fatalf("timeout classification = %q", got)
	}
}
