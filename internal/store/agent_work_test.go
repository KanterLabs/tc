package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"roadmap/internal/db"
)

func TestPublishAgentWorkEnrichesTaskAndEmitsSafeAtomicEvents(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, _, task := createAgentWorkFixture(t, data, ctx, "PULSE")
	claimed, err := data.ClaimTask(ctx, task.ID, actor.ID, time.Hour, task.Version)
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	completed, total := 1, 2
	published, err := data.PublishAgentWork(ctx, task.ID, AgentWorkInput{
		OperationID:         "run-1",
		State:               "working",
		Phase:               "Implement store",
		Summary:             "The persistence path is in place.",
		NextAction:          "Run the focused tests.",
		CheckpointRefs:      []string{"schema", "publish"},
		CheckpointCompleted: &completed,
		CheckpointTotal:     &total,
	}, claimed.Version, actor.ID)
	if err != nil {
		t.Fatalf("publish agent work: %v", err)
	}
	if published.Version != claimed.Version+1 || published.AgentWork == nil {
		t.Fatalf("published task version/pulse = %d/%+v", published.Version, published.AgentWork)
	}
	work := published.AgentWork
	if work.OperationID != "run-1" || work.ActorID != actor.ID || work.State != "working" || work.Phase != "Implement store" || work.Summary == "" || work.NextAction == "" {
		t.Fatalf("published pulse = %+v", work)
	}
	if len(work.CheckpointRefs) != 2 || work.CheckpointCompleted == nil || *work.CheckpointCompleted != completed || work.CheckpointTotal == nil || *work.CheckpointTotal != total || work.Stale || work.ActionNeeded {
		t.Fatalf("published progress = %+v", work)
	}
	if published.CommentCount != 1 {
		t.Fatalf("comment count = %d, want 1", published.CommentCount)
	}
	var body string
	if err := database.QueryRowContext(ctx, `SELECT body FROM comments WHERE task_id=?`, task.ID).Scan(&body); err != nil {
		t.Fatalf("read progress comment: %v", err)
	}
	if body != "The persistence path is in place.\n\nNext: Run the focused tests." {
		t.Fatalf("comment body = %q", body)
	}
	rows, err := database.QueryContext(ctx, `SELECT type, payload FROM events WHERE task_id=? ORDER BY cursor`, task.ID)
	if err != nil {
		t.Fatalf("read task events: %v", err)
	}
	defer rows.Close()
	var eventTypes []string
	var progressed map[string]any
	for rows.Next() {
		var eventType, payload string
		if err := rows.Scan(&eventType, &payload); err != nil {
			t.Fatalf("scan task event: %v", err)
		}
		eventTypes = append(eventTypes, eventType)
		if eventType == "task.progressed" {
			if err := json.Unmarshal([]byte(payload), &progressed); err != nil {
				t.Fatalf("decode progress event: %v", err)
			}
		}
		if strings.Contains(payload, "persistence") || strings.Contains(payload, "focused") || strings.Contains(payload, "schema") || strings.Contains(payload, "publish") || strings.Contains(payload, "Implement store") {
			t.Fatalf("unsafe progress content in %s event: %s", eventType, payload)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read task events: %v", err)
	}
	if len(eventTypes) != 4 || eventTypes[2] != "comment.created" || eventTypes[3] != "task.progressed" {
		t.Fatalf("task event types = %v, want task.created/task.claimed/comment.created/task.progressed", eventTypes)
	}
	if progressed["version"] != float64(published.Version) || progressed["state"] != "working" || progressed["completed"] != float64(completed) || progressed["total"] != float64(total) {
		t.Fatalf("progress event payload = %#v", progressed)
	}
	for _, key := range []string{"operation_id", "operation_id_hash", "phase", "summary", "next_action", "checkpoint_refs"} {
		if _, ok := progressed[key]; ok {
			t.Fatalf("progress event exposes %q: %#v", key, progressed)
		}
	}
}

func TestPublishAgentWorkClaimVersionAndOperationGuards(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, _, task := createAgentWorkFixture(t, data, ctx, "GUARD")
	claimed, err := data.ClaimTask(ctx, task.ID, actor.ID, time.Hour, task.Version)
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	input := AgentWorkInput{OperationID: "run-guard", State: "working", Summary: "First pulse"}
	first, err := data.PublishAgentWork(ctx, task.ID, input, claimed.Version, actor.ID)
	if err != nil {
		t.Fatalf("publish first pulse: %v", err)
	}
	second, err := data.PublishAgentWork(ctx, task.ID, AgentWorkInput{OperationID: "run-guard", State: "verifying", Summary: "Second pulse"}, first.Version, actor.ID)
	if err != nil {
		t.Fatalf("publish second pulse: %v", err)
	}
	if second.AgentWork == nil || second.AgentWork.StartedAt != first.AgentWork.StartedAt || second.AgentWork.UpdatedAt == first.AgentWork.UpdatedAt {
		t.Fatalf("same-operation timestamps = first=%+v second=%+v", first.AgentWork, second.AgentWork)
	}
	other, err := data.CreateActor(ctx, Actor{Kind: "agent", Name: "Other agent"}, "")
	if err != nil {
		t.Fatalf("create other actor: %v", err)
	}
	released, err := data.ReleaseTask(ctx, task.ID, actor.ID, second.Version)
	if err != nil {
		t.Fatalf("release task: %v", err)
	}
	otherClaim, err := data.ClaimTask(ctx, task.ID, other.ID, time.Hour, released.Version)
	if err != nil {
		t.Fatalf("reclaim task: %v", err)
	}
	third, err := data.PublishAgentWork(ctx, task.ID, AgentWorkInput{OperationID: "run-guard", State: "working", Summary: "New claimant"}, otherClaim.Version, other.ID)
	if err != nil {
		t.Fatalf("publish from new claimant: %v", err)
	}
	if third.AgentWork == nil || third.AgentWork.StartedAt == second.AgentWork.StartedAt || third.AgentWork.ActorID != other.ID {
		t.Fatalf("new claimant operation timestamps/actor = %+v", third.AgentWork)
	}
	if _, err := data.PublishAgentWork(ctx, task.ID, input, first.Version, actor.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale version error = %v, want conflict", err)
	}
	if _, err := data.PublishAgentWork(ctx, task.ID, input, third.Version, actor.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong claimant error = %v, want forbidden", err)
	}
	if _, err := data.PublishAgentWork(ctx, task.ID, input, third.Version-1, other.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong expected version error = %v, want conflict", err)
	}
	if _, err := data.PublishAgentWork(ctx, task.ID, input, third.Version, actor.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong claimant after stale error = %v, want forbidden", err)
	}
	if _, err := data.PublishAgentWork(ctx, task.ID, input, third.Version, other.ID); err != nil {
		// The active claim is still valid, proving the failed attempts did not
		// advance the version or create a partial snapshot.
		t.Fatalf("active claimant publish after rejected attempts: %v", err)
	}
}

func TestPublishAgentWorkValidationAndLiveWorkFilters(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, project, first := createAgentWorkFixture(t, data, ctx, "LIVE")
	second, err := data.CreateTask(ctx, project.ID, TaskInput{Title: stringPtrForTest("Second")}, actor.ID)
	if err != nil {
		t.Fatalf("create second task: %v", err)
	}
	third, err := data.CreateTask(ctx, project.ID, TaskInput{Title: stringPtrForTest("Third")}, actor.ID)
	if err != nil {
		t.Fatalf("create third task: %v", err)
	}
	for _, task := range []Task{first, second, third} {
		claimed, claimErr := data.ClaimTask(ctx, task.ID, actor.ID, time.Hour, task.Version)
		if claimErr != nil {
			t.Fatalf("claim %s: %v", task.Title, claimErr)
		}
		if _, publishErr := data.PublishAgentWork(ctx, task.ID, AgentWorkInput{OperationID: task.Title, State: "working", Summary: task.Title}, claimed.Version, actor.ID); publishErr != nil {
			t.Fatalf("publish %s: %v", task.Title, publishErr)
		}
	}
	old := time.Now().UTC().Add(-AgentWorkStaleAfter - time.Second).Format(time.RFC3339Nano)
	if _, err := database.ExecContext(ctx, `UPDATE task_agent_work SET updated_at=?, state='working' WHERE task_id=?`, old, first.ID); err != nil {
		t.Fatalf("age first pulse: %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE task_agent_work SET state='waiting' WHERE task_id=?`, second.ID); err != nil {
		t.Fatalf("wait second pulse: %v", err)
	}
	live, more, err := data.ListMyWorkFiltered(ctx, actor.ID, []string{project.ID}, TaskFilter{LiveWork: true, Limit: 10})
	if err != nil {
		t.Fatalf("list live work: %v", err)
	}
	if more || len(live) != 3 || live[0].ID != second.ID || live[1].ID != first.ID {
		t.Fatalf("live work = %v, more=%v; want waiting then stale then newest", taskIDs(live), more)
	}
	if !live[0].AgentWork.ActionNeeded || !live[1].AgentWork.Stale || !live[1].AgentWork.ActionNeeded {
		t.Fatalf("live work action flags = first=%+v second=%+v", live[0].AgentWork, live[1].AgentWork)
	}
	archivedProject, err := data.CreateProject(ctx, ProjectInput{Key: stringPtrForTest("ARCHIVED"), Name: stringPtrForTest("Archived project")}, actor.ID)
	if err != nil {
		t.Fatalf("create archived project: %v", err)
	}
	archivedTask, err := data.CreateTask(ctx, archivedProject.ID, TaskInput{Title: stringPtrForTest("Archived pulse")}, actor.ID)
	if err != nil {
		t.Fatalf("create archived task: %v", err)
	}
	archivedClaim, err := data.ClaimTask(ctx, archivedTask.ID, actor.ID, time.Hour, archivedTask.Version)
	if err != nil {
		t.Fatalf("claim archived task: %v", err)
	}
	if _, err := data.PublishAgentWork(ctx, archivedTask.ID, AgentWorkInput{OperationID: "archived-pulse", State: "working", Summary: "Archived pulse"}, archivedClaim.Version, actor.ID); err != nil {
		t.Fatalf("publish archived task: %v", err)
	}
	archived := true
	if _, err := data.UpdateProject(ctx, archivedProject.ID, ProjectInput{Archived: &archived}, actor.ID); err != nil {
		t.Fatalf("archive project: %v", err)
	}
	unscopedLive, _, err := data.ListMyWorkFiltered(ctx, actor.ID, nil, TaskFilter{LiveWork: true, Limit: 10})
	if err != nil {
		t.Fatalf("list unscoped live work: %v", err)
	}
	if len(unscopedLive) != 3 {
		t.Fatalf("unscoped live work = %v, want only active-project tasks", taskIDs(unscopedLive))
	}
	for _, task := range unscopedLive {
		if task.ID == archivedTask.ID {
			t.Fatalf("unscoped live work leaked archived-project task %s", archivedTask.ID)
		}
	}
	filtered, _, err := data.ListTasks(ctx, project.ID, TaskFilter{AgentState: "stale", Limit: 10})
	if err != nil {
		t.Fatalf("filter stale tasks: %v", err)
	}
	if len(filtered) != 1 || filtered[0].ID != first.ID {
		t.Fatalf("stale tasks = %v", taskIDs(filtered))
	}
	if _, err := data.PublishAgentWork(ctx, first.ID, AgentWorkInput{OperationID: "bad space", State: "working", Summary: "bad"}, live[0].Version, actor.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid operation ID error = %v, want invalid", err)
	}
	if _, err := data.PublishAgentWork(ctx, first.ID, AgentWorkInput{OperationID: "bad-count", State: "working", Summary: "bad", CheckpointCompleted: intPtrForAgentWorkTest(1)}, live[0].Version, actor.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unpaired count error = %v, want invalid", err)
	}
}

func createAgentWorkFixture(t *testing.T, data *Store, ctx context.Context, key string) (Actor, Project, Task) {
	return createAgentWorkFixtureWithTitle(t, data, ctx, key, "First")
}

func createAgentWorkFixtureWithTitle(t *testing.T, data *Store, ctx context.Context, key, title string) (Actor, Project, Task) {
	t.Helper()
	actor, err := data.CreateActor(ctx, Actor{Kind: "agent", Name: "Agent " + key}, "")
	if err != nil {
		t.Fatalf("create actor: %v", err)
	}
	project, err := data.CreateProject(ctx, ProjectInput{Key: stringPtrForTest(key), Name: stringPtrForTest("Project " + key)}, actor.ID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	task, err := data.CreateTask(ctx, project.ID, TaskInput{Title: stringPtrForTest(title)}, actor.ID)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	return actor, project, task
}

func intPtrForAgentWorkTest(value int) *int { return &value }

func taskIDs(tasks []Task) []string {
	result := make([]string, 0, len(tasks))
	for _, task := range tasks {
		result = append(result, task.ID)
	}
	return result
}
