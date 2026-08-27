package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"roadmap/internal/db"
)

func TestBugStoreLifecycleAndFilters(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, err := data.CreateActor(ctx, Actor{Kind: "human", Name: "Reporter"}, "")
	if err != nil {
		t.Fatalf("create reporter: %v", err)
	}
	assignee, err := data.CreateActor(ctx, Actor{Kind: "agent", Name: "Triage agent"}, "")
	if err != nil {
		t.Fatalf("create assignee: %v", err)
	}
	project, err := data.CreateProject(ctx, ProjectInput{Key: stringPtrForTest("BUG"), Name: stringPtrForTest("Bugs")}, actor.ID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	actual, expected, steps, environment, version := "Button crashes", "Button submits", "1. Open page", "linux", "1.2.3"
	bug, err := data.CreateTask(ctx, project.ID, TaskInput{
		Title: stringPtrForTest("Crash on submit"),
		Kind:  stringPtrForTest("bug"),
		Bug: &BugInput{
			ActualBehavior: &actual, ExpectedBehavior: &expected, ReproductionSteps: &steps,
			Environment: &environment, AffectedVersion: &version,
		},
	}, actor.ID)
	if err != nil {
		t.Fatalf("create bug: %v", err)
	}
	if bug.Kind != bugKind || bug.Bug == nil || bug.Bug.ReporterID != actor.ID || bug.Bug.ActualBehavior != actual {
		t.Fatalf("created bug = %+v", bug)
	}
	if bug.Version != 1 || bug.Key != "BUG-1" {
		t.Fatalf("created version/key = %d/%q", bug.Version, bug.Key)
	}

	severity := "s2"
	updatedActual := "Button crashes after submit"
	updated, err := data.UpdateTask(ctx, bug.ID, TaskInput{Bug: &BugInput{ActualBehavior: &updatedActual, Severity: &severity}}, bug.Version, actor.ID)
	if err != nil {
		t.Fatalf("update bug details: %v", err)
	}
	if updated.Version != 2 || updated.Bug == nil || updated.Bug.ActualBehavior != updatedActual || updated.Bug.Severity == nil || *updated.Bug.Severity != severity {
		t.Fatalf("updated bug = %+v", updated)
	}
	filtered, more, err := data.ListTasks(ctx, project.ID, TaskFilter{Kind: "bug", Severity: "s2", Reporter: actor.ID, Query: "crashes after", Limit: 10})
	if err != nil {
		t.Fatalf("filter bugs: %v", err)
	}
	if more || len(filtered) != 1 || filtered[0].ID != bug.ID {
		t.Fatalf("filtered bugs = %d/%+v, more=%v", len(filtered), filtered, more)
	}

	activeColumn, err := data.StateColumn(ctx, project.ID, "active")
	if err != nil {
		t.Fatalf("active column: %v", err)
	}
	triaged, err := data.TriageBug(ctx, bug.ID, TriageBugInput{Severity: stringPtrForTest("s1"), SeveritySet: true, Priority: stringPtrForTest("high"), Assignee: &assignee.ID, AssigneeSet: true, ColumnID: &activeColumn.ID}, updated.Version, actor.ID)
	if err != nil {
		t.Fatalf("triage bug: %v", err)
	}
	if triaged.Version != 3 || triaged.Bug == nil || triaged.Priority != "high" || triaged.Assignee == nil || *triaged.Assignee != assignee.ID || triaged.ColumnID != activeColumn.ID || triaged.Bug.Severity == nil || *triaged.Bug.Severity != "s1" {
		t.Fatalf("triaged bug = %+v", triaged)
	}

	resolved, err := data.ResolveBug(ctx, bug.ID, ResolveBugInput{Resolution: "fixed", Note: "Verified in a follow-up build"}, triaged.Version, actor.ID)
	if err != nil {
		t.Fatalf("resolve bug: %v", err)
	}
	if resolved.Version != 4 || resolved.Bug == nil || resolved.Bug.Resolution == nil || *resolved.Bug.Resolution != "fixed" || resolved.Bug.ResolvedBy == nil || *resolved.Bug.ResolvedBy != actor.ID || resolved.CompletedAt == nil || resolved.ClaimedBy != nil || resolved.CommentCount != 1 {
		t.Fatalf("resolved bug = %+v", resolved)
	}
	var payload string
	if err := database.QueryRowContext(ctx, `SELECT payload FROM events WHERE task_id=? ORDER BY cursor DESC LIMIT 1`, bug.ID).Scan(&payload); err != nil {
		t.Fatalf("read resolve event: %v", err)
	}
	if payload == "" || containsJSONString(payload, updatedActual) {
		t.Fatalf("resolve event contains bug text: %s", payload)
	}

	reopened, err := data.ReopenBug(ctx, bug.ID, "The crash still reproduces", resolved.Version, actor.ID)
	if err != nil {
		t.Fatalf("reopen bug: %v", err)
	}
	if reopened.Version != 5 || reopened.Bug == nil || reopened.Bug.Resolution != nil || reopened.Bug.ResolvedBy != nil || reopened.Bug.ResolvedAt != nil || reopened.Bug.DuplicateOf != nil || reopened.CompletedAt != nil || reopened.CommentCount != 2 {
		t.Fatalf("reopened bug = %+v", reopened)
	}
	backlog, err := data.StateColumn(ctx, project.ID, "backlog")
	if err != nil {
		t.Fatalf("backlog column: %v", err)
	}
	if reopened.ColumnID != backlog.ID {
		t.Fatalf("reopened column = %q, want %q", reopened.ColumnID, backlog.ID)
	}
}

func TestBugDuplicateCycleAndClaimOverride(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	owner, err := data.CreateActor(ctx, Actor{Kind: "human", Name: "Owner"}, "")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	other, err := data.CreateActor(ctx, Actor{Kind: "agent", Name: "Other"}, "")
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	project, err := data.CreateProject(ctx, ProjectInput{Key: stringPtrForTest("DUP"), Name: stringPtrForTest("Duplicate bugs")}, owner.ID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	create := func(title string) Task {
		actual := title + " actual"
		result, createErr := data.CreateTask(ctx, project.ID, TaskInput{Title: stringPtrForTest(title), Kind: stringPtrForTest("bug"), Bug: &BugInput{ActualBehavior: &actual}}, owner.ID)
		if createErr != nil {
			t.Fatalf("create %s: %v", title, createErr)
		}
		return result
	}
	first, second, third := create("First"), create("Second"), create("Third")
	firstClaimed, err := data.ClaimTask(ctx, first.ID, other.ID, 0, first.Version)
	if err != nil {
		t.Fatalf("claim first: %v", err)
	}
	if _, err := data.TriageBug(ctx, first.ID, TriageBugInput{Severity: stringPtrForTest("s2"), SeveritySet: true}, firstClaimed.Version, owner.ID); !errors.Is(err, ErrClaimUnavailable) {
		t.Fatalf("triage with another actor claim error = %v, want ErrClaimUnavailable", err)
	}
	triaged, err := data.TriageBugWithClaimOverride(ctx, first.ID, TriageBugInput{Severity: stringPtrForTest("s2"), SeveritySet: true}, firstClaimed.Version, owner.ID, true)
	if err != nil {
		t.Fatalf("triage with override: %v", err)
	}
	if triaged.Version != firstClaimed.Version+1 {
		t.Fatalf("override version = %d, want %d", triaged.Version, firstClaimed.Version+1)
	}

	dupSecond, err := data.ResolveBugWithClaimOverride(ctx, first.ID, ResolveBugInput{Resolution: "duplicate", DuplicateOf: &second.ID}, triaged.Version, owner.ID, true)
	if err != nil {
		t.Fatalf("resolve duplicate: %v", err)
	}
	if dupSecond.Bug == nil || dupSecond.Bug.DuplicateOf == nil || *dupSecond.Bug.DuplicateOf != second.ID {
		t.Fatalf("duplicate bug = %+v", dupSecond.Bug)
	}
	secondDup, err := data.ResolveBug(ctx, second.ID, ResolveBugInput{Resolution: "duplicate", DuplicateOf: &third.ID}, second.Version, owner.ID)
	if err != nil {
		t.Fatalf("resolve second duplicate: %v", err)
	}
	if _, err := data.ResolveBug(ctx, third.ID, ResolveBugInput{Resolution: "duplicate", DuplicateOf: &first.ID}, third.Version, owner.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate cycle error = %v, want ErrInvalid", err)
	}
	var links int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(1) FROM task_links WHERE link_type='duplicate'`).Scan(&links); err != nil {
		t.Fatalf("count duplicate links: %v", err)
	}
	if links != 2 {
		t.Fatalf("duplicate links = %d, want 2", links)
	}
	_ = secondDup
}

func TestIssueListingProjectAllowListAndMigrationDefault(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, err := data.CreateActor(ctx, Actor{Kind: "human", Name: "Actor"}, "")
	if err != nil {
		t.Fatalf("create actor: %v", err)
	}
	firstProject, err := data.CreateProject(ctx, ProjectInput{Key: stringPtrForTest("ONE"), Name: stringPtrForTest("One")}, actor.ID)
	if err != nil {
		t.Fatalf("create first project: %v", err)
	}
	secondProject, err := data.CreateProject(ctx, ProjectInput{Key: stringPtrForTest("TWO"), Name: stringPtrForTest("Two")}, actor.ID)
	if err != nil {
		t.Fatalf("create second project: %v", err)
	}
	for _, project := range []*Project{&firstProject, &secondProject} {
		actual := "details"
		if _, err := data.CreateTask(ctx, project.ID, TaskInput{Title: stringPtrForTest("Issue"), Kind: stringPtrForTest("bug"), Bug: &BugInput{ActualBehavior: &actual}}, actor.ID); err != nil {
			t.Fatalf("create issue: %v", err)
		}
	}
	if _, err := data.CreateTask(ctx, firstProject.ID, TaskInput{Title: stringPtrForTest("Normal task")}, actor.ID); err != nil {
		t.Fatalf("create normal task: %v", err)
	}
	var kind string
	if err := database.QueryRowContext(ctx, `SELECT kind FROM tasks WHERE kind='task' LIMIT 1`).Scan(&kind); err != nil {
		t.Fatalf("read migration task kind: %v", err)
	}
	if kind != "task" {
		t.Fatalf("migration kind = %q", kind)
	}
	issues, more, err := data.ListIssuesWithExtra(ctx, TaskFilter{ProjectIDs: []string{secondProject.ID}, Limit: 1})
	if err != nil {
		t.Fatalf("list scoped issues: %v", err)
	}
	if more || len(issues) != 1 || issues[0].ProjectID != secondProject.ID {
		t.Fatalf("scoped issues = %d/%+v, more=%v", len(issues), issues, more)
	}
	issues, more, err = data.ListIssuesWithExtra(ctx, TaskFilter{ProjectIDs: []string{}, Limit: 1})
	if err != nil {
		t.Fatalf("list empty scoped issues: %v", err)
	}
	if more || len(issues) != 0 {
		t.Fatalf("empty scoped issues = %d/%+v, more=%v", len(issues), issues, more)
	}
}

func containsJSONString(raw, value string) bool {
	var decoded any
	if json.Unmarshal([]byte(raw), &decoded) != nil {
		return false
	}
	return stringsContainValue(decoded, value)
}

func stringsContainValue(value any, wanted string) bool {
	switch typed := value.(type) {
	case string:
		return typed == wanted
	case []any:
		for _, item := range typed {
			if stringsContainValue(item, wanted) {
				return true
			}
		}
	case map[string]any:
		for _, item := range typed {
			if stringsContainValue(item, wanted) {
				return true
			}
		}
	}
	return false
}
