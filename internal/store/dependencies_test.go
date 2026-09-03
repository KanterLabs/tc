package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"roadmap/internal/db"
)

type dependencyFixture struct {
	ctx     context.Context
	store   *Store
	actor   Actor
	project Project
}

func newDependencyFixture(t *testing.T, key string) dependencyFixture {
	t.Helper()
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "dependencies.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	store := New(database)
	actor, err := store.CreateActor(ctx, Actor{Kind: "human", Name: "Dependency tester"}, "")
	if err != nil {
		t.Fatalf("create actor: %v", err)
	}
	project, err := store.CreateProject(ctx, ProjectInput{Key: dependencyStringPtr(key), Name: dependencyStringPtr("Dependencies")}, actor.ID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return dependencyFixture{ctx: ctx, store: store, actor: actor, project: project}
}

func dependencyStringPtr(value string) *string { return &value }

func (f dependencyFixture) task(t *testing.T, title string) Task {
	t.Helper()
	task, err := f.store.CreateTask(f.ctx, f.project.ID, TaskInput{Title: dependencyStringPtr(title)}, f.actor.ID)
	if err != nil {
		t.Fatalf("create task %q: %v", title, err)
	}
	return task
}

func (f dependencyFixture) add(t *testing.T, dependent, prerequisite Task) Task {
	t.Helper()
	updated, err := f.store.AddTaskDependency(f.ctx, dependent.Key, prerequisite.Key, dependent.Version, f.actor.ID)
	if err != nil {
		t.Fatalf("add dependency %s -> %s: %v", dependent.Key, prerequisite.Key, err)
	}
	return updated
}

func TestTaskDependenciesRoundTripSummaryAndVersion(t *testing.T) {
	f := newDependencyFixture(t, "DEP")
	first := f.task(t, "first prerequisite")
	second := f.task(t, "second prerequisite")
	dependent := f.task(t, "dependent")

	updated := f.add(t, dependent, first)
	if updated.Version != dependent.Version+1 {
		t.Fatalf("dependent version after add = %d, want %d", updated.Version, dependent.Version+1)
	}
	if first.Version != 1 || second.Version != 1 {
		t.Fatalf("prerequisite versions changed: first=%d second=%d", first.Version, second.Version)
	}
	updated = f.add(t, updated, second)
	if updated.Version != dependent.Version+2 {
		t.Fatalf("dependent version after second add = %d, want %d", updated.Version, dependent.Version+2)
	}

	graph, err := f.store.GetTaskDependencies(f.ctx, dependent.ID)
	if err != nil {
		t.Fatalf("read dependency graph: %v", err)
	}
	if len(graph.Prerequisites) != 2 || len(graph.Dependents) != 0 {
		t.Fatalf("graph = %+v, want two prerequisites and no dependents", graph)
	}
	if graph.Prerequisites[0].Key != first.Key || graph.Prerequisites[1].Key != second.Key {
		t.Fatalf("prerequisites = %+v, want stable task-number order", graph.Prerequisites)
	}
	firstGraph, err := f.store.GetTaskDependencies(f.ctx, first.ID)
	if err != nil {
		t.Fatalf("read prerequisite graph: %v", err)
	}
	if len(firstGraph.Dependents) != 1 || firstGraph.Dependents[0].ID != dependent.ID {
		t.Fatalf("first prerequisite dependents = %+v", firstGraph.Dependents)
	}

	loaded, err := f.store.GetTask(f.ctx, dependent.ID)
	if err != nil {
		t.Fatalf("load dependent: %v", err)
	}
	if loaded.DependencySummary != (DependencySummary{PrerequisiteCount: 2, UnmetPrerequisiteCount: 2, DependentCount: 0, Blocked: true}) {
		t.Fatalf("initial summary = %+v", loaded.DependencySummary)
	}
	completedFirst, err := f.store.CompleteTask(f.ctx, first.ID, f.actor.ID, first.Version)
	if err != nil {
		t.Fatalf("complete first prerequisite: %v", err)
	}
	loaded, err = f.store.GetTask(f.ctx, dependent.ID)
	if err != nil {
		t.Fatalf("reload dependent: %v", err)
	}
	if loaded.DependencySummary.UnmetPrerequisiteCount != 1 || !loaded.DependencySummary.Blocked {
		t.Fatalf("summary after first completion = %+v", loaded.DependencySummary)
	}
	fresh, readErr := f.store.GetTaskDependencies(f.ctx, dependent.ID)
	if readErr != nil {
		t.Fatalf("read fresh graph: %v", readErr)
	}
	if !fresh.Prerequisites[0].Satisfied || fresh.Prerequisites[0].CompletedAt == nil {
		t.Fatalf("completed prerequisite relation = %+v", fresh.Prerequisites[0])
	}
	completedSecond, err := f.store.CompleteTask(f.ctx, second.ID, f.actor.ID, second.Version)
	if err != nil {
		t.Fatalf("complete second prerequisite: %v", err)
	}
	loaded, err = f.store.GetTask(f.ctx, dependent.ID)
	if err != nil {
		t.Fatalf("reload ready dependent: %v", err)
	}
	if loaded.DependencySummary != (DependencySummary{PrerequisiteCount: 2, UnmetPrerequisiteCount: 0, DependentCount: 0, Blocked: false}) {
		t.Fatalf("ready summary = %+v", loaded.DependencySummary)
	}

	removed, err := f.store.RemoveTaskDependency(f.ctx, dependent.Key, first.Key, loaded.Version, f.actor.ID)
	if err != nil {
		t.Fatalf("remove dependency: %v", err)
	}
	if removed.Version != loaded.Version+1 {
		t.Fatalf("dependent version after remove = %d, want %d", removed.Version, loaded.Version+1)
	}
	if completedFirst.Version != first.Version+1 || completedSecond.Version != second.Version+1 {
		t.Fatalf("prerequisite completion versions = first=%d second=%d", completedFirst.Version, completedSecond.Version)
	}
	graph, err = f.store.GetTaskDependencies(f.ctx, dependent.ID)
	if err != nil || len(graph.Prerequisites) != 1 || graph.Prerequisites[0].ID != second.ID {
		t.Fatalf("graph after remove = %+v, err=%v", graph, err)
	}
}

func TestTaskDependenciesRejectValidationFailures(t *testing.T) {
	f := newDependencyFixture(t, "VALID")
	dependent := f.task(t, "dependent")
	prerequisite := f.task(t, "prerequisite")

	if _, err := f.store.AddTaskDependency(f.ctx, dependent.ID, dependent.ID, dependent.Version, f.actor.ID); !errors.Is(err, ErrDependencySelfReference) {
		t.Fatalf("self dependency error = %v, want ErrDependencySelfReference", err)
	}
	updated := f.add(t, dependent, prerequisite)
	if _, err := f.store.AddTaskDependency(f.ctx, dependent.ID, prerequisite.ID, updated.Version, f.actor.ID); !errors.Is(err, ErrDependencyAlreadyExists) {
		t.Fatalf("duplicate dependency error = %v, want ErrDependencyAlreadyExists", err)
	}

	other, err := f.store.CreateProject(f.ctx, ProjectInput{Key: dependencyStringPtr("OTHER"), Name: dependencyStringPtr("Other")}, f.actor.ID)
	if err != nil {
		t.Fatalf("create other project: %v", err)
	}
	otherTask, err := f.store.CreateTask(f.ctx, other.ID, TaskInput{Title: dependencyStringPtr("other task")}, f.actor.ID)
	if err != nil {
		t.Fatalf("create other task: %v", err)
	}
	if _, err := f.store.AddTaskDependency(f.ctx, dependent.ID, otherTask.ID, updated.Version, f.actor.ID); !errors.Is(err, ErrDependencyCrossProject) {
		t.Fatalf("cross-project error = %v, want ErrDependencyCrossProject", err)
	}

	deleted := f.task(t, "deleted prerequisite")
	if err := f.store.DeleteTask(f.ctx, deleted.ID, deleted.Version, f.actor.ID); err != nil {
		t.Fatalf("delete prerequisite: %v", err)
	}
	if _, err := f.store.AddTaskDependency(f.ctx, dependent.ID, deleted.ID, updated.Version, f.actor.ID); !errors.Is(err, ErrDependencyNotFound) || !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted prerequisite error = %v, want dependency and generic not found", err)
	}

	activeColumn, err := f.store.StateColumn(f.ctx, f.project.ID, "active")
	if err != nil {
		t.Fatalf("read active column: %v", err)
	}
	active := f.task(t, "already active")
	active, err = f.store.UpdateTask(f.ctx, active.ID, TaskInput{ColumnID: &activeColumn.ID}, active.Version, f.actor.ID)
	if err != nil {
		t.Fatalf("move task to active: %v", err)
	}
	unfinished := f.task(t, "unfinished prerequisite")
	if _, err := f.store.AddTaskDependency(f.ctx, active.ID, unfinished.ID, active.Version, f.actor.ID); !errors.Is(err, ErrUnmetDependencies) {
		t.Fatalf("active task unmet dependency error = %v, want ErrUnmetDependencies", err)
	}
}

func TestTaskDependenciesRejectDirectAndTransitiveCycles(t *testing.T) {
	f := newDependencyFixture(t, "CYCLE")
	a := f.task(t, "a")
	b := f.task(t, "b")
	c := f.task(t, "c")
	b = f.add(t, b, c)
	a = f.add(t, a, b)

	if _, err := f.store.AddTaskDependency(f.ctx, c.ID, a.ID, c.Version, f.actor.ID); !errors.Is(err, ErrDependencyCycle) {
		t.Fatalf("transitive cycle error = %v, want ErrDependencyCycle", err)
	} else if typed, ok := err.(*Error); !ok || typed.Details == nil {
		t.Fatalf("cycle error details = %#v, want bounded path details", err)
	}
	if _, err := f.store.AddTaskDependency(f.ctx, b.ID, a.ID, b.Version, f.actor.ID); !errors.Is(err, ErrDependencyCycle) {
		t.Fatalf("direct cycle error = %v, want ErrDependencyCycle", err)
	}
	graph, err := f.store.GetTaskDependencies(f.ctx, c.ID)
	if err != nil {
		t.Fatalf("read cycle source graph: %v", err)
	}
	if len(graph.Prerequisites) != 0 {
		t.Fatalf("rejected cycle changed graph = %+v", graph)
	}
}

func TestTaskDependenciesSerializeOpposingCycleWrites(t *testing.T) {
	f := newDependencyFixture(t, "RACE")
	a := f.task(t, "a")
	b := f.task(t, "b")
	start := make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		<-start
		_, err := f.store.AddTaskDependency(f.ctx, a.ID, b.ID, a.Version, f.actor.ID)
		results <- err
	}()
	go func() {
		defer group.Done()
		<-start
		_, err := f.store.AddTaskDependency(f.ctx, b.ID, a.ID, b.Version, f.actor.ID)
		results <- err
	}()
	close(start)
	done := make(chan struct{})
	go func() {
		group.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("opposing dependency writes did not complete")
	}
	var success, cycle int
	for range 2 {
		err := <-results
		if err == nil {
			success++
		} else if errors.Is(err, ErrDependencyCycle) {
			cycle++
		} else {
			t.Fatalf("opposing write error = %v, want one cycle rejection", err)
		}
	}
	if success != 1 || cycle != 1 {
		t.Fatalf("opposing writes success/cycle = %d/%d, want 1/1", success, cycle)
	}
}

func TestTaskDependenciesSummariesAndListFilter(t *testing.T) {
	f := newDependencyFixture(t, "FILTER")
	prerequisite := f.task(t, "prerequisite")
	blocked := f.task(t, "blocked")
	ready := f.task(t, "ready")
	blocked = f.add(t, blocked, prerequisite)
	ready = f.add(t, ready, prerequisite)

	all, more, err := f.store.ListTasks(f.ctx, f.project.ID, TaskFilter{Limit: 20})
	if err != nil || more {
		t.Fatalf("list all tasks: %v more=%v", err, more)
	}
	byID := make(map[string]Task, len(all))
	for _, task := range all {
		byID[task.ID] = task
	}
	if byID[blocked.ID].DependencySummary != (DependencySummary{PrerequisiteCount: 1, UnmetPrerequisiteCount: 1, Blocked: true}) {
		t.Fatalf("blocked list summary = %+v", byID[blocked.ID].DependencySummary)
	}
	if byID[prerequisite.ID].DependencySummary.DependentCount != 2 {
		t.Fatalf("prerequisite list summary = %+v", byID[prerequisite.ID].DependencySummary)
	}
	blockedPage, _, err := f.store.ListTasks(f.ctx, f.project.ID, TaskFilter{Dependency: "blocked", Limit: 20})
	if err != nil {
		t.Fatalf("list blocked tasks: %v", err)
	}
	if len(blockedPage) != 2 || blockedPage[0].ID != blocked.ID || blockedPage[1].ID != ready.ID {
		t.Fatalf("blocked filter = %+v, want both blocked tasks", blockedPage)
	}
	completed, err := f.store.CompleteTask(f.ctx, prerequisite.ID, f.actor.ID, prerequisite.Version)
	if err != nil {
		t.Fatalf("complete prerequisite: %v", err)
	}
	readyPage, _, err := f.store.ListTasks(f.ctx, f.project.ID, TaskFilter{Dependency: "ready", Limit: 20})
	if err != nil {
		t.Fatalf("list ready tasks: %v", err)
	}
	if len(readyPage) != 2 || readyPage[0].ID != blocked.ID || readyPage[1].ID != ready.ID {
		t.Fatalf("ready filter = %+v, want both dependency-ready tasks", readyPage)
	}
	if completed.Version != prerequisite.Version+1 || blocked.Version != 2 || ready.Version != 2 {
		t.Fatalf("version behavior prerequisite=%d blocked=%d ready=%d", completed.Version, blocked.Version, ready.Version)
	}
}

func TestTaskDependenciesSoftDeleteLifecycle(t *testing.T) {
	f := newDependencyFixture(t, "DELETE")
	prerequisite := f.task(t, "outgoing prerequisite")
	dependent := f.task(t, "dependent to delete")
	dependent = f.add(t, dependent, prerequisite)

	if err := f.store.DeleteTask(f.ctx, dependent.ID, dependent.Version, f.actor.ID); err != nil {
		t.Fatalf("delete dependent: %v", err)
	}
	if _, err := f.store.GetTask(f.ctx, dependent.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted dependent lookup error = %v, want ErrNotFound", err)
	}
	prerequisite, err := f.store.GetTask(f.ctx, prerequisite.ID)
	if err != nil {
		t.Fatalf("reload prerequisite after dependent delete: %v", err)
	}
	if prerequisite.DependencySummary != (DependencySummary{}) {
		t.Fatalf("prerequisite summary after dependent delete = %+v, want zero counts", prerequisite.DependencySummary)
	}
	prerequisiteGraph, err := f.store.GetTaskDependencies(f.ctx, prerequisite.ID)
	if err != nil {
		t.Fatalf("read prerequisite graph after dependent delete: %v", err)
	}
	if len(prerequisiteGraph.Dependents) != 0 {
		t.Fatalf("prerequisite graph after dependent delete = %+v, want no dependents", prerequisiteGraph)
	}

	livePrerequisite := f.task(t, "protected prerequisite")
	liveDependent := f.task(t, "live dependent")
	liveDependent = f.add(t, liveDependent, livePrerequisite)
	beforePrerequisite := livePrerequisite
	beforeDependent := liveDependent
	if err := f.store.DeleteTask(f.ctx, livePrerequisite.ID, livePrerequisite.Version, f.actor.ID); err == nil {
		t.Fatal("delete prerequisite succeeded with a live dependent")
	} else if !errors.Is(err, ErrDependencyInUse) {
		t.Fatalf("delete prerequisite error = %v, want ErrDependencyInUse", err)
	}
	afterPrerequisite, err := f.store.GetTask(f.ctx, livePrerequisite.ID)
	if err != nil {
		t.Fatalf("reload protected prerequisite after rejected delete: %v", err)
	}
	afterDependent, err := f.store.GetTask(f.ctx, liveDependent.ID)
	if err != nil {
		t.Fatalf("reload live dependent after rejected prerequisite delete: %v", err)
	}
	if afterPrerequisite.Version != beforePrerequisite.Version || afterPrerequisite.CompletedAt != beforePrerequisite.CompletedAt {
		t.Fatalf("rejected prerequisite delete changed task: before=%+v after=%+v", beforePrerequisite, afterPrerequisite)
	}
	if afterDependent.Version != beforeDependent.Version {
		t.Fatalf("rejected prerequisite delete changed dependent version: before=%d after=%d", beforeDependent.Version, afterDependent.Version)
	}
	var deletedAt sql.NullString
	if err := f.store.DB.QueryRowContext(f.ctx, `SELECT deleted_at FROM tasks WHERE id=?`, livePrerequisite.ID).Scan(&deletedAt); err != nil {
		t.Fatalf("read protected prerequisite deleted_at: %v", err)
	}
	if deletedAt.Valid {
		t.Fatalf("rejected prerequisite delete set deleted_at=%q", deletedAt.String)
	}
	graph, err := f.store.GetTaskDependencies(f.ctx, livePrerequisite.ID)
	if err != nil {
		t.Fatalf("read protected prerequisite graph: %v", err)
	}
	if len(graph.Dependents) != 1 || graph.Dependents[0].ID != liveDependent.ID {
		t.Fatalf("protected prerequisite graph after rejected delete = %+v, want live dependent", graph)
	}
}

func TestTaskDependenciesEnforceDirectBounds(t *testing.T) {
	f := newDependencyFixture(t, "BOUNDS")
	dependent := f.task(t, "many prerequisites")
	for i := 0; i < maxDirectTaskDependencies; i++ {
		prerequisite := f.task(t, "prerequisite")
		dependent = f.add(t, dependent, prerequisite)
	}
	overflowPrerequisite := f.task(t, "overflow prerequisite")
	before := dependent
	if _, err := f.store.AddTaskDependency(f.ctx, dependent.ID, overflowPrerequisite.ID, dependent.Version, f.actor.ID); !errors.Is(err, ErrDependencyLimitExceeded) {
		t.Fatalf("prerequisite overflow error = %v, want ErrDependencyLimitExceeded", err)
	}
	after, err := f.store.GetTask(f.ctx, dependent.ID)
	if err != nil {
		t.Fatalf("read prerequisite-bound task: %v", err)
	}
	if after.Version != before.Version || after.DependencySummary.PrerequisiteCount != maxDirectTaskDependencies {
		t.Fatalf("prerequisite overflow mutated task: before=%+v after=%+v", before.DependencySummary, after.DependencySummary)
	}

	prerequisite := f.task(t, "many dependents")
	for i := 0; i < maxDirectTaskDependencies; i++ {
		dependent := f.task(t, "dependent")
		if _, err := f.store.AddTaskDependency(f.ctx, dependent.ID, prerequisite.ID, dependent.Version, f.actor.ID); err != nil {
			t.Fatalf("add dependent %d: %v", i, err)
		}
	}
	overflowDependent := f.task(t, "overflow dependent")
	if _, err := f.store.AddTaskDependency(f.ctx, overflowDependent.ID, prerequisite.ID, overflowDependent.Version, f.actor.ID); !errors.Is(err, ErrDependencyLimitExceeded) {
		t.Fatalf("dependent overflow error = %v, want ErrDependencyLimitExceeded", err)
	}
	loaded, err := f.store.GetTask(f.ctx, prerequisite.ID)
	if err != nil {
		t.Fatalf("read dependent-bound task: %v", err)
	}
	if loaded.DependencySummary.DependentCount != maxDirectTaskDependencies {
		t.Fatalf("dependent overflow mutated graph: summary=%+v", loaded.DependencySummary)
	}
}
