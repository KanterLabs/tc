package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/KanterLabs/helm/internal/db"
)

func TestAuditRunAndFindingPreserveTaskStateAndDetectDrift(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, err := data.CreateActor(ctx, Actor{Kind: "human", Name: "Audit owner"}, "")
	if err != nil {
		t.Fatalf("create actor: %v", err)
	}
	project, err := data.CreateProject(ctx, ProjectInput{Key: stringPtrForTest("AUDIT"), Name: stringPtrForTest("Audit project")}, actor.ID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	columns, err := data.ListColumns(ctx, project.ID)
	if err != nil || len(columns) == 0 {
		t.Fatalf("list columns: %v", err)
	}
	task, err := data.CreateTask(ctx, project.ID, TaskInput{Title: stringPtrForTest("Snapshot me")}, actor.ID)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	beforeTask, err := data.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var beforeEvents, beforeComments int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(1) FROM events`).Scan(&beforeEvents); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(1) FROM comments`).Scan(&beforeComments); err != nil {
		t.Fatal(err)
	}

	run, err := data.CreateAuditRun(ctx, project.ID, actor.ID, AuditRunInput{Scope: "board", Status: "queued"})
	if err != nil {
		t.Fatalf("create audit run: %v", err)
	}
	if run.Status != "queued" || run.ProjectID != project.ID || run.ActorID != actor.ID || run.FindingCount != 0 || len(run.Findings) != 0 {
		t.Fatalf("created run = %+v", run)
	}
	findingInput := AuditFindingInput{
		TaskID:          task.ID,
		CapturedVersion: task.Version,
		SourceColumn:    columns[0].ID,
		Verdict:         "needs_attention",
		Confidence:      0.75,
		Reason:          "The task needs a review before work starts.",
		EvidenceRefs:    []string{"/api/v1/tasks/" + task.ID, "events:1"},
	}
	finding, err := data.AppendAuditFinding(ctx, run.ID, findingInput)
	if err != nil {
		t.Fatalf("append finding: %v", err)
	}
	if finding.Version != 1 || finding.ReviewState != "pending" || finding.ChangedSinceAudit || len(finding.EvidenceRefs) != 2 {
		t.Fatalf("appended finding = %+v", finding)
	}

	// A repeated append is idempotent and returns the original row, including
	// its immutable ID and timestamps.
	repeated, err := data.AppendAuditFinding(ctx, run.ID, findingInput)
	if err != nil {
		t.Fatalf("repeat finding: %v", err)
	}
	if repeated.ID != finding.ID || repeated.CreatedAt != finding.CreatedAt {
		t.Fatalf("repeat finding = %+v, original = %+v", repeated, finding)
	}
	changedInput := findingInput
	changedInput.Reason = "A different immutable explanation."
	if _, err := data.AppendAuditFinding(ctx, run.ID, changedInput); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed duplicate error = %v, want conflict", err)
	}

	// Audit writes do not emit events/comments or alter any task-side fields.
	afterAuditTask, err := data.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeTask, afterAuditTask) {
		t.Fatalf("task changed during audit writes: before=%+v after=%+v", beforeTask, afterAuditTask)
	}
	var afterEvents, afterComments int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(1) FROM events`).Scan(&afterEvents); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(1) FROM comments`).Scan(&afterComments); err != nil {
		t.Fatal(err)
	}
	if afterEvents != beforeEvents || afterComments != beforeComments {
		t.Fatalf("audit side effects: events %d->%d comments %d->%d", beforeEvents, afterEvents, beforeComments, afterComments)
	}

	updatedTask, err := data.UpdateTask(ctx, task.ID, TaskInput{Title: stringPtrForTest("Changed after audit")}, task.Version, actor.ID)
	if err != nil {
		t.Fatalf("update task after audit: %v", err)
	}
	if updatedTask.Version == finding.CapturedVersion {
		t.Fatal("task version did not advance")
	}
	if _, err := data.UpdateAuditFindingReview(ctx, finding.ID, AuditFindingReviewInput{ReviewState: "approved"}, finding.Version, actor.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("approve stale finding error = %v, want conflict", err)
	}
	unchangedFinding, err := data.GetAuditFinding(ctx, finding.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchangedFinding.ReviewState != "pending" || unchangedFinding.Version != finding.Version {
		t.Fatalf("stale approval changed finding: %+v", unchangedFinding)
	}
	read, err := data.GetAuditRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("get audit run: %v", err)
	}
	if len(read.Findings) != 0 || read.FindingCount != 1 {
		t.Fatalf("run detail must remain bounded: %+v", read)
	}
	findings, more, err := data.ListAuditFindings(ctx, run.ID, 50, 0)
	if err != nil {
		t.Fatalf("list audit findings: %v", err)
	}
	if more || len(findings) != 1 || !findings[0].ChangedSinceAudit {
		t.Fatalf("read findings = %+v more=%v, want one changed finding", findings, more)
	}
}

func TestAuditReviewVersioningFinalizationAndValidation(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, project, task, column := auditFixture(t, data, ctx, "REVIEW")
	run, err := data.CreateAuditRun(ctx, project.ID, actor.ID, AuditRunInput{Scope: "open_tasks"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	finding, err := data.AppendAuditFinding(ctx, run.ID, AuditFindingInput{
		TaskID:                      task.ID,
		CapturedVersion:             task.Version,
		SourceColumn:                column.ID,
		Verdict:                     "move_proposed",
		ProposedSemanticDestination: stringPtrForTest("ready"),
		Confidence:                  1,
		Reason:                      "Ready for the next lane.",
	})
	if err != nil {
		t.Fatalf("append finding: %v", err)
	}
	if _, err := data.UpdateAuditFindingReview(ctx, finding.ID, AuditFindingReviewInput{ReviewState: "approved"}, finding.Version, actor.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("approve before finalization error = %v, want conflict", err)
	}
	unchanged, err := data.GetAuditFinding(ctx, finding.ID)
	if err != nil || unchanged.ReviewState != "pending" || unchanged.Version != finding.Version {
		t.Fatalf("premature approval changed finding: %+v err=%v", unchanged, err)
	}
	complete, err := data.FinalizeAuditRun(ctx, run.ID, "partial", actor.ID)
	if err != nil {
		t.Fatalf("finalize run: %v", err)
	}
	if complete.Status != "partial" || complete.FinalizedAt == nil {
		t.Fatalf("finalized run = %+v", complete)
	}
	approved, err := data.UpdateAuditFindingReview(ctx, finding.ID, AuditFindingReviewInput{ReviewState: "approved"}, finding.Version, actor.ID)
	if err != nil {
		t.Fatalf("approve finding: %v", err)
	}
	if approved.Version != finding.Version+1 || approved.ReviewState != "approved" || approved.ProposedSemanticDestination == nil || *approved.ProposedSemanticDestination != "ready" {
		t.Fatalf("approved finding = %+v", approved)
	}
	if _, err := data.UpdateAuditFindingReview(ctx, finding.ID, AuditFindingReviewInput{ReviewState: "dismissed"}, finding.Version, actor.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale review error = %v, want conflict", err)
	}
	if _, err := data.UpdateAuditFindingReview(ctx, finding.ID, AuditFindingReviewInput{ReviewState: "pending", ProposedSemanticDestination: nil, ProposedSemanticDestinationSet: true}, approved.Version, actor.ID); err != nil {
		t.Fatalf("clear destination review: %v", err)
	}
	if _, err := data.UpdateAuditFindingReview(ctx, finding.ID, AuditFindingReviewInput{ReviewState: "invalid"}, approved.Version+1, actor.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid review state error = %v, want invalid", err)
	}

	repeated, err := data.FinalizeAuditRun(ctx, run.ID, "partial", actor.ID)
	if err != nil || repeated.Status != "partial" {
		t.Fatalf("repeat finalize = %+v err=%v", repeated, err)
	}
	if _, err := data.FinalizeAuditRun(ctx, run.ID, "failed", actor.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("different terminal status error = %v, want conflict", err)
	}
	if _, err := data.AppendAuditFinding(ctx, run.ID, AuditFindingInput{TaskID: task.ID, CapturedVersion: task.Version, SourceColumn: column.ID, Verdict: "correct", Confidence: 0.5, Reason: "late"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("append after finalize error = %v, want conflict", err)
	}
}

func TestAuditEvidenceAndSnapshotValidation(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, project, task, column := auditFixture(t, data, ctx, "VALIDATE")
	run, err := data.CreateAuditRun(ctx, project.ID, actor.ID, AuditRunInput{Scope: "board"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	base := AuditFindingInput{TaskID: task.ID, CapturedVersion: task.Version, SourceColumn: column.ID, Verdict: "correct", Confidence: 0.5, Reason: "Looks consistent."}
	invalidInputs := []AuditFindingInput{
		func() AuditFindingInput { v := base; v.SourceColumn = column.Name; return v }(),
		func() AuditFindingInput { v := base; v.Confidence = 1.1; return v }(),
		func() AuditFindingInput { v := base; v.EvidenceRefs = []string{"javascript:alert(1)"}; return v }(),
		func() AuditFindingInput { v := base; v.Verdict = "move_proposed"; return v }(),
		func() AuditFindingInput { v := base; v.ReviewState = "approved"; return v }(),
	}
	for i, input := range invalidInputs {
		if _, err := data.AppendAuditFinding(ctx, run.ID, input); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid input %d error = %v, want invalid", i, err)
		}
	}
	if _, err := data.AppendAuditFinding(ctx, run.ID, base); err != nil {
		t.Fatalf("valid finding after rejected inputs: %v", err)
	}
}

func TestAuditRunRetentionLimitRefusesWithoutDeletingHistory(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, project, _, _ := auditFixture(t, data, ctx, "RETAIN")

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin fixture transaction: %v", err)
	}
	for i := 0; i < maxAuditRunsPerProject; i++ {
		id := fmt.Sprintf("retained-audit-%04d", i)
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_runs(id, project_id, actor_id, scope, status, started_at, created_at, updated_at) VALUES (?, ?, ?, 'board', 'running', '2026-08-28T00:00:00Z', '2026-08-28T00:00:00Z', '2026-08-28T00:00:00Z')`, id, project.ID, actor.ID); err != nil {
			_ = tx.Rollback()
			t.Fatalf("insert retained audit %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit fixture transaction: %v", err)
	}

	if _, err := data.CreateAuditRun(ctx, project.ID, actor.ID, AuditRunInput{Scope: "board"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("create beyond retention ceiling error = %v, want invalid", err)
	}
	var count int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(1) FROM audit_runs WHERE project_id=?`, project.ID).Scan(&count); err != nil {
		t.Fatalf("count retained audits: %v", err)
	}
	if count != maxAuditRunsPerProject {
		t.Fatalf("retained audit count = %d, want %d", count, maxAuditRunsPerProject)
	}
	if _, err := data.GetAuditRun(ctx, "retained-audit-0000"); err != nil {
		t.Fatalf("oldest retained audit was deleted: %v", err)
	}
}

func auditFixture(t *testing.T, data *Store, ctx context.Context, key string) (Actor, Project, Task, Column) {
	t.Helper()
	actor, err := data.CreateActor(ctx, Actor{Kind: "human", Name: "Audit " + key}, "")
	if err != nil {
		t.Fatalf("create actor: %v", err)
	}
	project, err := data.CreateProject(ctx, ProjectInput{Key: stringPtrForTest(key), Name: stringPtrForTest("Project " + key)}, actor.ID)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	columns, err := data.ListColumns(ctx, project.ID)
	if err != nil || len(columns) == 0 {
		t.Fatalf("list columns: %v", err)
	}
	task, err := data.CreateTask(ctx, project.ID, TaskInput{Title: stringPtrForTest("Audit target")}, actor.ID)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	return actor, project, task, columns[0]
}
