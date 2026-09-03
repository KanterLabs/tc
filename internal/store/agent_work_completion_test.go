package store

import (
	"context"
	"reflect"
	"testing"

	"github.com/KanterLabs/helm/internal/db"
)

func TestCompletedAgentWorkRetainsSnapshotAndRestoresOnReopen(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, project, _ := createAgentWorkFixture(t, data, ctx, "COMPLETEWORK")

	assignee := actor.ID
	task, err := data.CreateTask(ctx, project.ID, TaskInput{
		Title:       stringPtrForTest("Retain completed work"),
		Assignee:    &assignee,
		AssigneeSet: true,
	}, actor.ID)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	claimed, err := data.ClaimTask(ctx, task.ID, actor.ID, 0, task.Version)
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	completed, total := 2, 3
	published, err := data.PublishAgentWork(ctx, task.ID, AgentWorkInput{
		OperationID:         "completion/retain",
		State:               "waiting",
		Phase:               "waiting-for-review",
		Summary:             "The implementation is ready for review.",
		NextAction:          "Review the retained snapshot.",
		CheckpointRefs:      []string{"implementation", "review", "reopen"},
		CheckpointCompleted: &completed,
		CheckpointTotal:     &total,
	}, claimed.Version, actor.ID)
	if err != nil {
		t.Fatalf("publish work: %v", err)
	}
	oldUpdatedAt := "2020-01-01T00:00:00Z"
	if _, err := database.ExecContext(ctx, `UPDATE task_agent_work SET updated_at=? WHERE task_id=?`, oldUpdatedAt, task.ID); err != nil {
		t.Fatalf("age snapshot: %v", err)
	}
	aged, err := data.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("read aged task: %v", err)
	}
	if aged.AgentWork == nil || !aged.AgentWork.Stale || !aged.AgentWork.ActionNeeded {
		t.Fatalf("aged work = %+v, want stale and action_needed", aged.AgentWork)
	}

	completedTask, err := data.CompleteTaskWithClaim(ctx, task.ID, actor.ID, published.Version)
	if err != nil {
		t.Fatalf("complete task: %v", err)
	}
	if completedTask.CompletedAt == nil || completedTask.AgentWork == nil {
		t.Fatalf("completed task = %+v, want completion and retained work", completedTask)
	}
	if completedTask.AgentWork.Stale || completedTask.AgentWork.ActionNeeded {
		t.Fatalf("completed work flags = %+v, want stale=false/action_needed=false", completedTask.AgentWork)
	}
	assertAgentWorkSnapshotPreserved(t, aged.AgentWork, completedTask.AgentWork)
	var storedUpdatedAt string
	if err := database.QueryRowContext(ctx, `SELECT updated_at FROM task_agent_work WHERE task_id=?`, task.ID).Scan(&storedUpdatedAt); err != nil {
		t.Fatalf("read retained snapshot timestamp: %v", err)
	}
	if storedUpdatedAt != oldUpdatedAt {
		t.Fatalf("completion changed retained snapshot timestamp to %q", storedUpdatedAt)
	}

	backlog, err := data.StateColumn(ctx, project.ID, "backlog")
	if err != nil {
		t.Fatalf("get backlog column: %v", err)
	}
	reopened, err := data.UpdateTask(ctx, task.ID, TaskInput{ColumnID: &backlog.ID}, completedTask.Version, actor.ID)
	if err != nil {
		t.Fatalf("reopen task: %v", err)
	}
	if reopened.CompletedAt != nil || reopened.AgentWork == nil {
		t.Fatalf("reopened task = %+v, want unfinished retained work", reopened)
	}
	if !reopened.AgentWork.Stale || !reopened.AgentWork.ActionNeeded {
		t.Fatalf("reopened work flags = %+v, want stale/action_needed restored", reopened.AgentWork)
	}
	assertAgentWorkSnapshotPreserved(t, completedTask.AgentWork, reopened.AgentWork)
}

func TestCompletedAgentWorkIsExcludedFromLivenessFilters(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, project, _ := createAgentWorkFixture(t, data, ctx, "COMPLETEFILTERS")

	assignee := actor.ID
	task, err := data.CreateTask(ctx, project.ID, TaskInput{
		Title:       stringPtrForTest("Completed assigned task"),
		Assignee:    &assignee,
		AssigneeSet: true,
	}, actor.ID)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	claimed, err := data.ClaimTask(ctx, task.ID, actor.ID, 0, task.Version)
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	published, err := data.PublishAgentWork(ctx, task.ID, AgentWorkInput{
		OperationID: "completion/filter-task",
		State:       "waiting",
		Summary:     "Waiting task snapshot.",
	}, claimed.Version, actor.ID)
	if err != nil {
		t.Fatalf("publish task work: %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE task_agent_work SET updated_at=? WHERE task_id=?`, "2020-01-01T00:00:00Z", task.ID); err != nil {
		t.Fatalf("age task snapshot: %v", err)
	}
	completedTask, err := data.CompleteTaskWithClaim(ctx, task.ID, actor.ID, published.Version)
	if err != nil {
		t.Fatalf("complete assigned task: %v", err)
	}

	actual := "The completed bug still reproduces."
	bug, err := data.CreateTask(ctx, project.ID, TaskInput{
		Title:       stringPtrForTest("Completed issue"),
		Kind:        stringPtrForTest("bug"),
		Assignee:    &assignee,
		AssigneeSet: true,
		Bug:         &BugInput{ActualBehavior: &actual},
	}, actor.ID)
	if err != nil {
		t.Fatalf("create bug: %v", err)
	}
	bugClaimed, err := data.ClaimTask(ctx, bug.ID, actor.ID, 0, bug.Version)
	if err != nil {
		t.Fatalf("claim bug: %v", err)
	}
	bugPublished, err := data.PublishAgentWork(ctx, bug.ID, AgentWorkInput{
		OperationID: "completion/filter-bug",
		State:       "handoff",
		Summary:     "Handing the issue off.",
	}, bugClaimed.Version, actor.ID)
	if err != nil {
		t.Fatalf("publish bug work: %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE task_agent_work SET updated_at=? WHERE task_id=?`, "2020-01-01T00:00:00Z", bug.ID); err != nil {
		t.Fatalf("age bug snapshot: %v", err)
	}
	completedBug, err := data.ResolveBug(ctx, bug.ID, ResolveBugInput{Resolution: "fixed"}, bugPublished.Version, actor.ID)
	if err != nil {
		t.Fatalf("resolve bug: %v", err)
	}
	if completedBug.CompletedAt == nil || completedBug.AgentWork == nil || completedBug.AgentWork.Stale || completedBug.AgentWork.ActionNeeded {
		t.Fatalf("completed bug = %+v, want retained inactive work", completedBug)
	}

	for _, state := range []string{"waiting", "stale", "missing"} {
		tasks, _, err := data.ListTasks(ctx, project.ID, TaskFilter{AgentState: state, Limit: 50})
		if err != nil {
			t.Fatalf("list project tasks by %s: %v", state, err)
		}
		assertTaskAbsent(t, tasks, completedTask.ID, "project tasks agent_state="+state)

		issues, _, err := data.ListIssues(ctx, TaskFilter{AgentState: state, Limit: 50})
		if err != nil {
			t.Fatalf("list issues by %s: %v", state, err)
		}
		assertTaskAbsent(t, issues, completedBug.ID, "issues agent_state="+state)

		assigned, _, err := data.ListMyWorkFiltered(ctx, actor.ID, []string{project.ID}, TaskFilter{AgentState: state, Limit: 50})
		if err != nil {
			t.Fatalf("list assigned work by %s: %v", state, err)
		}
		assertTaskAbsent(t, assigned, completedTask.ID, "assigned work agent_state="+state)
	}

	for name, filter := range map[string]TaskFilter{
		"project tasks": {ActionNeeded: true, Limit: 50},
		"issues":        {ActionNeeded: true, Limit: 50},
		"assigned work": {ActionNeeded: true, Limit: 50},
	} {
		var tasks []Task
		switch name {
		case "project tasks":
			tasks, _, err = data.ListTasks(ctx, project.ID, filter)
		case "issues":
			tasks, _, err = data.ListIssues(ctx, filter)
		case "assigned work":
			tasks, _, err = data.ListMyWorkFiltered(ctx, actor.ID, []string{project.ID}, filter)
		}
		if err != nil {
			t.Fatalf("list %s action-needed work: %v", name, err)
		}
		assertTaskAbsent(t, tasks, completedTask.ID, name+" action_needed=true")
		assertTaskAbsent(t, tasks, completedBug.ID, name+" action_needed=true")
	}
}

func assertAgentWorkSnapshotPreserved(t *testing.T, before, after *AgentWork) {
	t.Helper()
	if before == nil || after == nil {
		t.Fatalf("snapshot presence changed: before=%+v after=%+v", before, after)
	}
	beforeCopy, afterCopy := *before, *after
	// These are read-time liveness signals and intentionally change at task
	// completion/reopen; all durable snapshot content must remain identical.
	beforeCopy.Stale, beforeCopy.ActionNeeded = afterCopy.Stale, afterCopy.ActionNeeded
	if !reflect.DeepEqual(beforeCopy, afterCopy) {
		t.Fatalf("snapshot content changed: before=%+v after=%+v", before, after)
	}
}

func assertTaskAbsent(t *testing.T, tasks []Task, id, collection string) {
	t.Helper()
	for _, task := range tasks {
		if task.ID == id {
			t.Fatalf("%s included completed task %s", collection, id)
		}
	}
}
