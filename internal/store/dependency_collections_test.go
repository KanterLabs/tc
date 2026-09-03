package store

import (
	"errors"
	"testing"
	"time"
)

func assignDependencyFixtureTask(t *testing.T, fixture dependencyFixture, task Task) Task {
	t.Helper()
	assignee := fixture.actor.ID
	updated, err := fixture.store.UpdateTask(fixture.ctx, task.ID, TaskInput{
		Assignee:    &assignee,
		AssigneeSet: true,
	}, task.Version, fixture.actor.ID)
	if err != nil {
		t.Fatalf("assign %s: %v", task.Key, err)
	}
	return updated
}

func TestMyWorkDependencyFiltersAndSummaries(t *testing.T) {
	fixture := newDependencyFixture(t, "MYDEP")
	unfinished := fixture.task(t, "unfinished prerequisite")
	completed := fixture.task(t, "completed prerequisite")
	blocked := assignDependencyFixtureTask(t, fixture, fixture.task(t, "blocked work"))
	ready := assignDependencyFixtureTask(t, fixture, fixture.task(t, "ready work"))
	plain := assignDependencyFixtureTask(t, fixture, fixture.task(t, "plain work"))

	var err error
	completed, err = fixture.store.CompleteTask(fixture.ctx, completed.ID, fixture.actor.ID, completed.Version)
	if err != nil {
		t.Fatalf("complete prerequisite: %v", err)
	}
	blocked = fixture.add(t, blocked, unfinished)
	ready = fixture.add(t, ready, completed)

	blockedPage, more, err := fixture.store.ListMyWorkFiltered(
		fixture.ctx,
		fixture.actor.ID,
		[]string{fixture.project.ID},
		TaskFilter{Dependency: "blocked", Limit: 20},
	)
	if err != nil {
		t.Fatalf("list dependency-blocked my work: %v", err)
	}
	if more || len(blockedPage) != 1 || blockedPage[0].ID != blocked.ID {
		t.Fatalf("blocked my work = %+v, more=%v", blockedPage, more)
	}
	if blockedPage[0].DependencySummary != (DependencySummary{
		PrerequisiteCount: 1, UnmetPrerequisiteCount: 1, Blocked: true,
	}) {
		t.Fatalf("blocked my-work summary = %+v", blockedPage[0].DependencySummary)
	}

	readyPage, more, err := fixture.store.ListMyWorkFiltered(
		fixture.ctx,
		fixture.actor.ID,
		[]string{fixture.project.ID},
		TaskFilter{Dependency: "ready", Limit: 20},
	)
	if err != nil {
		t.Fatalf("list dependency-ready my work: %v", err)
	}
	if more || len(readyPage) != 1 || readyPage[0].ID != ready.ID {
		t.Fatalf("ready my work = %+v, more=%v", readyPage, more)
	}
	if readyPage[0].DependencySummary != (DependencySummary{PrerequisiteCount: 1}) {
		t.Fatalf("ready my-work summary = %+v", readyPage[0].DependencySummary)
	}
	for _, task := range readyPage {
		if task.ID == plain.ID {
			t.Fatal("task without dependencies matched dependency-ready filter")
		}
	}

	_, _, err = fixture.store.ListMyWorkFiltered(
		fixture.ctx,
		fixture.actor.ID,
		[]string{fixture.project.ID},
		TaskFilter{Dependency: "unknown", Limit: 20},
	)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid dependency filter error = %v, want ErrInvalid", err)
	}
}

func TestRoadmapUpcomingIncludesDependencySummary(t *testing.T) {
	fixture := newDependencyFixture(t, "DUEDEP")
	prerequisite := fixture.task(t, "due prerequisite")
	dependent := fixture.task(t, "upcoming dependent")
	dueAt := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339Nano)
	dependent, err := fixture.store.UpdateTask(fixture.ctx, dependent.ID, TaskInput{
		DueAt:    &dueAt,
		DueAtSet: true,
	}, dependent.Version, fixture.actor.ID)
	if err != nil {
		t.Fatalf("set due date: %v", err)
	}
	dependent = fixture.add(t, dependent, prerequisite)

	view, err := fixture.store.Roadmap(fixture.ctx, fixture.project.ID)
	if err != nil {
		t.Fatalf("read roadmap: %v", err)
	}
	if len(view.Upcoming) != 1 || view.Upcoming[0].ID != dependent.ID {
		t.Fatalf("upcoming tasks = %+v", view.Upcoming)
	}
	if view.Upcoming[0].DependencySummary != (DependencySummary{
		PrerequisiteCount: 1, UnmetPrerequisiteCount: 1, Blocked: true,
	}) {
		t.Fatalf("upcoming dependency summary = %+v", view.Upcoming[0].DependencySummary)
	}
}

func TestTaskListRejectsUnknownDependencyFilter(t *testing.T) {
	fixture := newDependencyFixture(t, "BADDEP")
	fixture.task(t, "ordinary task")

	_, _, err := fixture.store.ListTasks(
		fixture.ctx,
		fixture.project.ID,
		TaskFilter{Dependency: "unknown", Limit: 20},
	)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid dependency filter error = %v, want ErrInvalid", err)
	}
}
