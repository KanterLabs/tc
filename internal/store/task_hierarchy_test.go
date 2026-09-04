package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestTaskHierarchyRoundTripRollupAndActivity(t *testing.T) {
	f := newDependencyFixture(t, "HIER")
	parent := f.task(t, "parent")
	child, err := f.store.CreateTask(f.ctx, f.project.ID, TaskInput{Title: dependencyStringPtr("child"), ParentTaskID: &parent.ID}, f.actor.ID)
	if err != nil {
		t.Fatalf("create child with parent: %v", err)
	}
	if child.ParentTaskID == nil || *child.ParentTaskID != parent.ID {
		t.Fatalf("created child parent = %#v, want %q", child.ParentTaskID, parent.ID)
	}
	loadedParent, err := f.store.GetTask(f.ctx, parent.ID)
	if err != nil {
		t.Fatalf("load parent: %v", err)
	}
	if loadedParent.HierarchySummary.ChildCount != 1 || loadedParent.HierarchySummary.CompletionPercent != 0 {
		t.Fatalf("parent hierarchy summary = %+v, want one open child", loadedParent.HierarchySummary)
	}
	graph, err := f.store.GetTaskHierarchy(f.ctx, child.ID)
	if err != nil {
		t.Fatalf("load child hierarchy: %v", err)
	}
	if graph.Parent == nil || graph.Parent.ID != parent.ID || len(graph.Ancestors) != 1 {
		t.Fatalf("child hierarchy = %+v, want parent and one ancestor", graph)
	}
	if len(graph.Children) != 0 || len(graph.Descendants) != 0 {
		t.Fatalf("leaf hierarchy collections = %+v, want empty", graph)
	}

	child, err = f.store.ClearTaskParent(f.ctx, child.ID, child.Version, f.actor.ID)
	if err != nil {
		t.Fatalf("clear parent: %v", err)
	}
	if child.ParentTaskID != nil || child.Version != 2 {
		t.Fatalf("cleared child = %+v, want nil parent and version 2", child)
	}
	if _, err := f.store.GetTaskHierarchy(f.ctx, child.ID); err != nil {
		t.Fatalf("reload unlinked child hierarchy: %v", err)
	}
	var linked, unlinked int
	if err := f.store.DB.QueryRowContext(f.ctx, `SELECT COUNT(*) FROM events WHERE type='task.parent_linked' AND task_id=?`, child.ID).Scan(&linked); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DB.QueryRowContext(f.ctx, `SELECT COUNT(*) FROM events WHERE type='task.parent_unlinked' AND task_id=?`, child.ID).Scan(&unlinked); err != nil {
		t.Fatal(err)
	}
	if linked != 1 || unlinked != 1 {
		t.Fatalf("hierarchy activity counts = linked %d unlinked %d, want one each", linked, unlinked)
	}
	var unlinkedParentKey string
	if err := f.store.DB.QueryRowContext(f.ctx, `SELECT json_extract(payload, '$.parent_key')
		FROM events
		WHERE type='task.parent_unlinked' AND task_id=?
		ORDER BY cursor DESC LIMIT 1`, child.ID).Scan(&unlinkedParentKey); err != nil {
		t.Fatalf("read unlinked hierarchy event: %v", err)
	}
	if unlinkedParentKey != parent.Key {
		t.Fatalf("unlinked hierarchy parent_key = %q, want %q", unlinkedParentKey, parent.Key)
	}
}

func TestTaskHierarchyRejectsCrossProjectCyclesAndUnsafeDelete(t *testing.T) {
	f := newDependencyFixture(t, "HINV")
	parent := f.task(t, "parent")
	child := f.task(t, "child")
	child, err := f.store.SetTaskParent(f.ctx, child.ID, parent.ID, child.Version, f.actor.ID)
	if err != nil {
		t.Fatalf("link child: %v", err)
	}
	if _, err := f.store.SetTaskParent(f.ctx, parent.ID, child.ID, parent.Version, f.actor.ID); !errors.Is(err, ErrHierarchyCycle) {
		t.Fatalf("cycle error = %v, want ErrHierarchyCycle", err)
	}
	if err := f.store.DeleteTask(f.ctx, parent.ID, parent.Version, f.actor.ID); !errors.Is(err, ErrHierarchyInUse) {
		t.Fatalf("unsafe delete error = %v, want ErrHierarchyInUse", err)
	}

	other, err := f.store.CreateProject(f.ctx, ProjectInput{Key: dependencyStringPtr("OTHERH"), Name: dependencyStringPtr("Other")}, f.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	otherTask, err := f.store.CreateTask(f.ctx, other.ID, TaskInput{Title: dependencyStringPtr("other")}, f.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SetTaskParent(f.ctx, child.ID, otherTask.ID, child.Version, f.actor.ID); !errors.Is(err, ErrHierarchyCrossProject) {
		t.Fatalf("cross-project error = %v, want ErrHierarchyCrossProject", err)
	}
	if _, err := f.store.SetTaskParent(f.ctx, child.ID, child.ID, child.Version, f.actor.ID); !errors.Is(err, ErrHierarchySelfReference) {
		t.Fatalf("self-parent error = %v, want ErrHierarchySelfReference", err)
	}
}

func TestTaskHierarchyDepthBound(t *testing.T) {
	f := newDependencyFixture(t, "HDEP")
	chain := make([]Task, 0, MaxTaskHierarchyDepth+2)
	for i := 0; i < MaxTaskHierarchyDepth+2; i++ {
		chain = append(chain, f.task(t, "chain"))
	}
	for i := 1; i < len(chain); i++ {
		updated, err := f.store.SetTaskParent(f.ctx, chain[i].ID, chain[i-1].ID, chain[i].Version, f.actor.ID)
		if i <= MaxTaskHierarchyDepth {
			if err != nil {
				t.Fatalf("link depth %d: %v", i, err)
			}
			chain[i] = updated
			continue
		}
		if !errors.Is(err, ErrHierarchyDepthExceeded) {
			t.Fatalf("depth %d error = %v, want ErrHierarchyDepthExceeded", i, err)
		}
	}
}

func TestTaskHierarchyRollupIncludesStatesBlockersAndAgentWork(t *testing.T) {
	f := newDependencyFixture(t, "HIERRL")
	parent := f.task(t, "rollup parent")

	liveChild := f.task(t, "waiting child")
	var err error
	liveChild, err = f.store.SetTaskParent(f.ctx, liveChild.ID, parent.ID, liveChild.Version, f.actor.ID)
	if err != nil {
		t.Fatalf("link waiting child: %v", err)
	}
	liveChild, err = f.store.ClaimTask(f.ctx, liveChild.ID, f.actor.ID, time.Hour, liveChild.Version)
	if err != nil {
		t.Fatalf("claim waiting child: %v", err)
	}
	liveChild, err = f.store.PublishAgentWork(f.ctx, liveChild.ID, AgentWorkInput{
		OperationID: "hierarchy/waiting",
		State:       "waiting",
		Summary:     "Waiting on review",
		NextAction:  "Review the child",
	}, liveChild.Version, f.actor.ID)
	if err != nil {
		t.Fatalf("publish waiting child: %v", err)
	}

	staleChild := f.task(t, "stale child")
	staleChild, err = f.store.SetTaskParent(f.ctx, staleChild.ID, parent.ID, staleChild.Version, f.actor.ID)
	if err != nil {
		t.Fatalf("link stale child: %v", err)
	}
	staleChild, err = f.store.ClaimTask(f.ctx, staleChild.ID, f.actor.ID, time.Hour, staleChild.Version)
	if err != nil {
		t.Fatalf("claim stale child: %v", err)
	}
	staleChild, err = f.store.PublishAgentWork(f.ctx, staleChild.ID, AgentWorkInput{
		OperationID: "hierarchy/stale",
		State:       "working",
		Summary:     "Needs a fresh pulse",
	}, staleChild.Version, f.actor.ID)
	if err != nil {
		t.Fatalf("publish stale child: %v", err)
	}
	oldPulse := time.Now().UTC().Add(-AgentWorkStaleAfter - time.Minute).Format(time.RFC3339Nano)
	if _, err := f.store.DB.ExecContext(f.ctx, `UPDATE task_agent_work SET updated_at=? WHERE task_id=?`, oldPulse, staleChild.ID); err != nil {
		t.Fatalf("age stale child pulse: %v", err)
	}

	blockedChild := f.task(t, "blocked child")
	blockedChild, err = f.store.SetTaskParent(f.ctx, blockedChild.ID, parent.ID, blockedChild.Version, f.actor.ID)
	if err != nil {
		t.Fatalf("link blocked child: %v", err)
	}
	blockedChild, err = f.store.BlockTask(f.ctx, blockedChild.ID, f.actor.ID, blockedChild.Version)
	if err != nil {
		t.Fatalf("block child: %v", err)
	}

	loaded, err := f.store.GetTask(f.ctx, parent.ID)
	if err != nil {
		t.Fatalf("load initial rollup: %v", err)
	}
	summary := loaded.HierarchySummary
	if summary.ChildCount != 3 || summary.CompletedChildCount != 0 || summary.CompletionPercent != 0 || summary.BlockedChildCount != 1 || summary.LiveAgentWorkCount != 2 || summary.ActionNeededCount != 2 || summary.StaleAgentWorkCount != 1 {
		t.Fatalf("initial hierarchy rollup = %+v, want three children, one blocked, two actionable pulses", summary)
	}
	if summary.StateCounts["blocked"] != 1 || summary.StateCounts["backlog"] != 2 {
		t.Fatalf("initial hierarchy state counts = %#v, want blocked=1/backlog=2", summary.StateCounts)
	}

	completed, err := f.store.CompleteTask(f.ctx, liveChild.ID, f.actor.ID, liveChild.Version)
	if err != nil {
		t.Fatalf("complete waiting child: %v", err)
	}
	loaded, err = f.store.GetTask(f.ctx, parent.ID)
	if err != nil {
		t.Fatalf("load completed rollup: %v", err)
	}
	summary = loaded.HierarchySummary
	if summary.ChildCount != 3 || summary.CompletedChildCount != 1 || summary.CompletionPercent < 33.33 || summary.CompletionPercent > 33.34 || summary.BlockedChildCount != 1 || summary.LiveAgentWorkCount != 1 || summary.ActionNeededCount != 1 || summary.StaleAgentWorkCount != 1 {
		t.Fatalf("completed hierarchy rollup = %+v, want completed/live/action counts updated", summary)
	}
	if summary.StateCounts["completed"] != 1 || summary.StateCounts["blocked"] != 1 {
		t.Fatalf("completed hierarchy state counts = %#v", summary.StateCounts)
	}
	if completed.AgentWork == nil || completed.AgentWork.ActionNeeded {
		t.Fatalf("completed child agent work = %+v, want retained non-actionable snapshot", completed.AgentWork)
	}
}

func TestTaskHierarchyConcurrentParentLinksHaveOneWinner(t *testing.T) {
	f := newDependencyFixture(t, "HIERRACE")
	parent := f.task(t, "race parent")
	child := f.task(t, "race child")
	start := make(chan struct{})
	var group sync.WaitGroup
	group.Add(2)
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			defer group.Done()
			<-start
			_, err := f.store.SetTaskParent(context.Background(), child.ID, parent.ID, child.Version, f.actor.ID)
			results <- err
		}()
	}
	close(start)
	group.Wait()
	close(results)
	var winners, conflicts int
	for err := range results {
		if err == nil {
			winners++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatalf("concurrent parent link error = %v, want stale-version conflict", err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("concurrent parent link outcomes = winners %d conflicts %d, want 1/1", winners, conflicts)
	}
	loaded, err := f.store.GetTask(f.ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ParentTaskID == nil || *loaded.ParentTaskID != parent.ID || loaded.Version != child.Version+1 {
		t.Fatalf("serialized child = %+v, want parent and one version increment", loaded)
	}
	var events int
	if err := f.store.DB.QueryRowContext(f.ctx, `SELECT COUNT(1) FROM events WHERE type='task.parent_linked' AND task_id=?`, child.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("concurrent parent link events = %d, want one", events)
	}
}

func TestTaskHierarchyFanoutLimitIsAtomic(t *testing.T) {
	f := newDependencyFixture(t, "HIERFAN")
	parent := f.task(t, "fanout parent")
	for index := 0; index < MaxTaskHierarchyChildren; index++ {
		child := f.task(t, "fanout child")
		if _, err := f.store.SetTaskParent(f.ctx, child.ID, parent.ID, child.Version, f.actor.ID); err != nil {
			t.Fatalf("link fanout child %d: %v", index, err)
		}
	}
	last := f.task(t, "fanout rejected")
	if _, err := f.store.SetTaskParent(f.ctx, last.ID, parent.ID, last.Version, f.actor.ID); !errors.Is(err, ErrHierarchyFanoutExceeded) || !errors.Is(err, ErrHierarchyLimitExceeded) {
		t.Fatalf("fanout rejection = %v, want fanout and generic hierarchy limit", err)
	}
	loaded, err := f.store.GetTask(f.ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.HierarchySummary.ChildCount != MaxTaskHierarchyChildren {
		t.Fatalf("fanout rollup child count = %d, want %d", loaded.HierarchySummary.ChildCount, MaxTaskHierarchyChildren)
	}
	if last.ParentTaskID != nil {
		t.Fatalf("rejected fanout child was mutated: %#v", last.ParentTaskID)
	}
}
