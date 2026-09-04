package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KanterLabs/helm/internal/db"
)

func portableTestString(value string) *string { return &value }

func TestPortableArchiveRoundTripPreservesPopulatedData(t *testing.T) {
	ctx := context.Background()
	sourceDB, err := db.Open(ctx, filepath.Join(t.TempDir(), "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sourceDB.Close()
	source := New(sourceDB)
	owner, err := source.CreateActor(ctx, Actor{Kind: "human", Name: "Source owner"}, "source-password-hash")
	if err != nil {
		t.Fatal(err)
	}
	worker, err := source.CreateActor(ctx, Actor{Kind: "agent", Name: "Source worker"}, "worker-secret")
	if err != nil {
		t.Fatal(err)
	}
	project, err := source.CreateProject(ctx, ProjectInput{Key: portableTestString("PORT"), Slug: portableTestString("portable-source"), Name: portableTestString("Portable source"), Description: portableTestString("A populated source project")}, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	columns, err := source.ListColumns(ctx, project.ID)
	if err != nil || len(columns) < 2 {
		t.Fatalf("source columns = %d, err=%v", len(columns), err)
	}
	description := "Preserve this task"
	due := "2026-12-31T00:00:00Z"
	first, err := source.CreateTask(ctx, project.ID, TaskInput{Title: portableTestString("First task"), Description: &description, Priority: portableTestString("high"), DueAt: &due, DueAtSet: true, ColumnID: &columns[1].ID, Position: float64Ptr(2)}, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	actual, expected := "It failed", "It succeeds"
	bugKind := "bug"
	bug, err := source.CreateTask(ctx, project.ID, TaskInput{Title: portableTestString("Typed bug"), Kind: &bugKind, Bug: &BugInput{ActualBehavior: &actual, ExpectedBehavior: &expected, ReproductionSteps: portableTestString("Open it"), Environment: portableTestString("CI"), AffectedVersion: portableTestString("1.2.3")}, ColumnID: &columns[0].ID}, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	label, err := source.CreateLabel(ctx, project.ID, LabelInput{Name: "important", Color: "#ef4444"}, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sourceDB.ExecContext(ctx, `INSERT INTO task_labels(task_id,label_id) VALUES (?,?)`, first.ID, label.ID); err != nil {
		t.Fatal(err)
	}
	comment, err := source.CreateComment(ctx, first.ID, worker.ID, "A durable comment")
	if err != nil {
		t.Fatal(err)
	}
	bug, err = source.AddTaskDependency(ctx, bug.ID, first.ID, bug.Version, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sourceDB.ExecContext(ctx, `INSERT INTO task_links(source_task_id,target_task_id,link_type,created_at) VALUES (?,?,?,?)`, first.ID, bug.ID, "relates", updatedTimestampForPortableTest()); err != nil {
		t.Fatal(err)
	}
	refs, _ := json.Marshal([]string{"checkpoint-a", "checkpoint-b"})
	started := "2026-09-01T10:00:00Z"
	updated := "2026-09-01T10:05:00Z"
	if _, err := sourceDB.ExecContext(ctx, `INSERT INTO task_agent_work(task_id,operation_id,actor_id,state,phase,summary,next_action,checkpoint_refs,checkpoint_completed,checkpoint_total,started_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, bug.ID, "portable-operation", worker.ID, "working", "build", "Preserve progress", "Review import", string(refs), 1, 2, started, updated); err != nil {
		t.Fatal(err)
	}
	if _, err := sourceDB.ExecContext(ctx, `INSERT INTO events(id,type,actor_id,project_id,task_id,payload,created_at) VALUES (?,?,?,?,?,?,?)`, "portable-event", "task.portable", worker.ID, project.ID, bug.ID, `{"safe":true}`, updated); err != nil {
		t.Fatal(err)
	}
	var sourceCursor int64
	if err := sourceDB.QueryRowContext(ctx, `SELECT cursor FROM events WHERE id=?`, "portable-event").Scan(&sourceCursor); err != nil {
		t.Fatal(err)
	}
	historyID := "portable-history"
	if _, err := sourceDB.ExecContext(ctx, `INSERT INTO task_agent_work_history(id,task_id,operation_id,actor_id,state,phase,summary,next_action,checkpoint_refs,checkpoint_completed,checkpoint_total,started_at,created_at,generated_comment_id,progress_event_cursor) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, historyID, bug.ID, "portable-operation", worker.ID, "working", "build", "Preserve progress", "Review import", string(refs), 1, 2, started, updated, comment.ID, sourceCursor); err != nil {
		t.Fatal(err)
	}

	archive, err := source.ExportPortable(ctx, []string{project.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.Projects) != 1 || len(archive.Columns) != len(columns) || len(archive.Tasks) != 2 || len(archive.Labels) != 1 || len(archive.Relationships.TaskLabels) != 1 || len(archive.Comments) != 1 || len(archive.Relationships.Dependencies) != 1 || len(archive.Relationships.TaskLinks) != 1 || len(archive.Activity.AgentWork) != 1 || len(archive.Activity.AgentWorkHistory) != 1 {
		t.Fatalf("archive counts projects=%d columns=%d tasks=%d labels=%d task_labels=%d comments=%d deps=%d links=%d work=%d history=%d", len(archive.Projects), len(archive.Columns), len(archive.Tasks), len(archive.Labels), len(archive.Relationships.TaskLabels), len(archive.Comments), len(archive.Relationships.Dependencies), len(archive.Relationships.TaskLinks), len(archive.Activity.AgentWork), len(archive.Activity.AgentWorkHistory))
	}
	var exportedBug *PortableBug
	for _, task := range archive.Tasks {
		if task.ID == bug.ID {
			exportedBug = task.Bug
		}
	}
	if exportedBug == nil || exportedBug.ActualBehavior != actual || exportedBug.ReporterID != owner.ID {
		t.Fatalf("exported bug = %+v", exportedBug)
	}
	encoded, _ := json.Marshal(archive)
	if strings.Contains(string(encoded), "source-password-hash") || strings.Contains(string(encoded), "worker-secret") {
		t.Fatalf("archive contains authentication material: %s", encoded)
	}

	destinationDB, err := db.Open(ctx, filepath.Join(t.TempDir(), "destination.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer destinationDB.Close()
	destination := New(destinationDB)
	importer, err := destination.CreateActor(ctx, Actor{Kind: "human", Name: "Importer"}, "importer-hash")
	if err != nil {
		t.Fatal(err)
	}
	report, err := destination.ImportPortable(ctx, archive, PortableImportOptions{ActorID: importer.ID})
	if err != nil {
		t.Fatalf("import archive: %v report=%+v", err, report)
	}
	if report.Counts.ProjectsCreated != 1 || report.Counts.TasksCreated != 2 || report.Counts.CommentsCreated != 1 || report.Counts.DependenciesCreated != 1 || report.Counts.LinksCreated != 1 || report.Counts.EventsCreated == 0 || len(report.Remaps) == 0 {
		t.Fatalf("import report = %+v", report)
	}
	importedBug, err := destination.GetTask(ctx, bug.ID)
	if err != nil {
		t.Fatal(err)
	}
	if importedBug.Title != bug.Title || importedBug.Description != bug.Description || importedBug.Version != bug.Version || importedBug.Bug == nil || importedBug.Bug.ActualBehavior != actual || importedBug.Bug.ReporterID != importer.ID {
		t.Fatalf("imported bug = %+v", importedBug)
	}
	var importedCommentActor, importedDependencyActor string
	if err := destinationDB.QueryRowContext(ctx, `SELECT actor_id FROM comments WHERE id=?`, comment.ID).Scan(&importedCommentActor); err != nil {
		t.Fatal(err)
	}
	if err := destinationDB.QueryRowContext(ctx, `SELECT COALESCE(created_by,'') FROM task_dependencies WHERE task_id=?`, bug.ID).Scan(&importedDependencyActor); err != nil {
		t.Fatal(err)
	}
	if importedCommentActor != importer.ID || importedDependencyActor != importer.ID {
		t.Fatalf("attribution comment=%q dependency=%q importer=%q", importedCommentActor, importedDependencyActor, importer.ID)
	}
	var importedLinks int
	if err := destinationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_links WHERE source_task_id=? AND target_task_id=? AND link_type=?`, first.ID, bug.ID, "relates").Scan(&importedLinks); err != nil {
		t.Fatal(err)
	}
	if importedLinks != 1 {
		t.Fatalf("imported task links = %d", importedLinks)
	}
	var historyCursor int64
	if err := destinationDB.QueryRowContext(ctx, `SELECT progress_event_cursor FROM task_agent_work_history WHERE id=?`, historyID).Scan(&historyCursor); err != nil {
		t.Fatal(err)
	}
	if historyCursor <= 0 {
		t.Fatalf("imported history cursor = %d", historyCursor)
	}

	secondReport, err := destination.ImportPortable(ctx, archive, PortableImportOptions{ActorID: importer.ID})
	if err != nil {
		t.Fatalf("retry archive: %v report=%+v", err, secondReport)
	}
	if secondReport.Counts.TasksCreated != 0 || secondReport.Counts.TasksSkipped != 2 || secondReport.Counts.HistoryCreated != 0 || secondReport.Counts.HistorySkipped != 1 {
		t.Fatalf("retry report = %+v", secondReport)
	}
	var taskCount, commentCount, historyCount int
	_ = destinationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE project_id=?`, project.ID).Scan(&taskCount)
	_ = destinationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM comments WHERE task_id=?`, first.ID).Scan(&commentCount)
	_ = destinationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_agent_work_history WHERE task_id=?`, bug.ID).Scan(&historyCount)
	if taskCount != 2 || commentCount != 1 || historyCount != 1 {
		t.Fatalf("retry duplicated data tasks=%d comments=%d history=%d", taskCount, commentCount, historyCount)
	}
}

func updatedTimestampForPortableTest() string { return "2026-09-01T10:05:00Z" }

func float64Ptr(value float64) *float64 { return &value }

type portableInterleavingSQL struct {
	portableSQL
	beforeTaskQuery func()
}

func (q *portableInterleavingSQL) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if q.beforeTaskQuery != nil && strings.Contains(query, "SELECT t.id,t.number") {
		callback := q.beforeTaskQuery
		q.beforeTaskQuery = nil
		callback()
	}
	return q.portableSQL.QueryContext(ctx, query, args...)
}

func TestPortableExportUsesSingleReadSnapshot(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "portable-export-snapshot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	data := New(database)
	actor, err := data.CreateActor(ctx, Actor{Kind: "human", Name: "Snapshot owner"}, "hash")
	if err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, ProjectInput{Key: portableTestString("SNAP"), Slug: portableTestString("snapshot"), Name: portableTestString("Snapshot")}, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	columns, err := data.ListColumns(ctx, project.ID)
	if err != nil || len(columns) == 0 {
		t.Fatalf("snapshot columns = %d, err=%v", len(columns), err)
	}
	baseTask, err := data.CreateTask(ctx, project.ID, TaskInput{Title: portableTestString("Before snapshot"), ColumnID: &columns[0].ID}, actor.ID)
	if err != nil {
		t.Fatal(err)
	}

	writerConn, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writerConn.Close()
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	newColumnID, newTaskID := "snapshot-new-column", "snapshot-new-task"
	var writerErr error
	query := &portableInterleavingSQL{portableSQL: tx}
	query.beforeTaskQuery = func() {
		writerTx, beginErr := writerConn.BeginTx(ctx, nil)
		if beginErr != nil {
			writerErr = beginErr
			return
		}
		defer writerTx.Rollback()
		if _, writerErr = writerTx.ExecContext(ctx, `INSERT INTO columns(id,project_id,name,semantic_state,position,created_at,updated_at) VALUES (?,?,?,?,?,?,?)`, newColumnID, project.ID, "Added concurrently", "backlog", 100, updatedTimestampForPortableTest(), updatedTimestampForPortableTest()); writerErr != nil {
			return
		}
		if _, writerErr = writerTx.ExecContext(ctx, `INSERT INTO tasks(id,project_id,number,column_id,kind,title,description,priority,position,version,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, newTaskID, project.ID, 100, newColumnID, "task", "Added concurrently", "", "normal", 0, 1, updatedTimestampForPortableTest(), updatedTimestampForPortableTest()); writerErr != nil {
			return
		}
		writerErr = writerTx.Commit()
	}
	archive, err := exportPortable(ctx, query, []string{project.ID})
	if err != nil {
		t.Fatalf("snapshot export: %v", err)
	}
	if writerErr != nil {
		t.Fatalf("interleaved writer: %v", writerErr)
	}
	if query.beforeTaskQuery != nil {
		t.Fatal("interleaved writer hook did not run before task query")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var currentColumns, currentTasks int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM columns WHERE project_id=?`, project.ID).Scan(&currentColumns); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE project_id=?`, project.ID).Scan(&currentTasks); err != nil {
		t.Fatal(err)
	}
	if currentColumns != len(columns)+1 || currentTasks != 2 {
		t.Fatalf("current rows columns=%d tasks=%d, want columns=%d tasks=2", currentColumns, currentTasks, len(columns)+1)
	}
	archiveColumnIDs := make(map[string]struct{}, len(archive.Columns))
	for _, column := range archive.Columns {
		archiveColumnIDs[column.ID] = struct{}{}
	}
	archiveTaskID := ""
	if len(archive.Tasks) > 0 {
		archiveTaskID = archive.Tasks[0].ID
	}
	if len(archive.Columns) != len(columns) || len(archive.Tasks) != 1 || archiveTaskID != baseTask.ID {
		t.Fatalf("snapshot archive columns=%d tasks=%d task=%q, want columns=%d tasks=1 task=%q", len(archive.Columns), len(archive.Tasks), archiveTaskID, len(columns), baseTask.ID)
	}
	for _, task := range archive.Tasks {
		if _, ok := archiveColumnIDs[task.ColumnID]; !ok {
			t.Fatalf("snapshot archive task %s references non-archived column %s", task.ID, task.ColumnID)
		}
	}
	for _, column := range archive.Columns {
		if column.ID == newColumnID {
			t.Fatalf("snapshot archive included concurrently-created column %s", newColumnID)
		}
	}
}

func TestPortableDryRunValidationAndConflictAreNonMutating(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "portable-validation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	data := New(database)
	actor, err := data.CreateActor(ctx, Actor{Kind: "human", Name: "Importer"}, "hash")
	if err != nil {
		t.Fatal(err)
	}
	timestamp := "2026-09-04T00:00:00Z"
	archive := PortableArchive{
		Format: PortableFormat, Version: PortableVersion, ExportedAt: timestamp,
		Projects: []PortableProject{{ID: "portable-project", Key: "PORT", Slug: "portable", Name: "Portable", Color: "#64748b", CreatedAt: timestamp, UpdatedAt: timestamp}},
		Columns:  []PortableColumn{{ID: "portable-column", ProjectID: "portable-project", Name: "Backlog", SemanticState: "backlog", CreatedAt: timestamp, UpdatedAt: timestamp}},
		Tasks:    []PortableTask{{ID: "portable-task", Number: 1, ProjectID: "portable-project", Kind: "task", ColumnID: "portable-column", Title: "Preview", Priority: "normal", Version: 1, CreatedAt: timestamp, UpdatedAt: timestamp}},
		Labels:   []PortableLabel{}, Comments: []PortableComment{},
		Relationships: PortableRelationships{TaskLabels: []PortableTaskLabel{}, Dependencies: []PortableDependency{}, TaskLinks: []PortableTaskLink{}},
		Activity:      PortableActivity{Events: []PortableEvent{}, AgentWork: []PortableAgentWork{}, AgentWorkHistory: []PortableAgentWorkHistory{}},
	}
	preview, err := data.ImportPortable(ctx, archive, PortableImportOptions{ActorID: actor.ID, DryRun: true})
	if err != nil {
		t.Fatalf("dry-run: %v report=%+v", err, preview)
	}
	if !preview.DryRun || preview.Counts.ProjectsCreated != 1 || preview.Counts.TasksCreated != 1 {
		t.Fatalf("dry-run report = %+v", preview)
	}
	var projects int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE id=?`, "portable-project").Scan(&projects); err != nil {
		t.Fatal(err)
	}
	if projects != 0 {
		t.Fatalf("dry-run wrote %d projects", projects)
	}
	invalidArchive := archive
	invalidArchive.Format = "unknown.format"
	if _, err := data.ImportPortable(ctx, invalidArchive, PortableImportOptions{ActorID: actor.ID}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid archive error = %v, want ErrInvalid", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects`).Scan(&projects); err != nil {
		t.Fatal(err)
	}
	if projects != 0 {
		t.Fatalf("invalid archive wrote %d projects", projects)
	}
	if _, err := data.ImportPortable(ctx, archive, PortableImportOptions{ActorID: actor.ID}); err != nil {
		t.Fatalf("valid import: %v", err)
	}
	conflictArchive := archive
	conflictArchive.Tasks = []PortableTask{}
	conflictArchive.Columns = []PortableColumn{}
	conflictArchive.Projects[0].Name = "Different project"
	if _, err := data.ImportPortable(ctx, conflictArchive, PortableImportOptions{ActorID: actor.ID, Conflict: "fail"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict error = %v, want ErrConflict", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects`).Scan(&projects); err != nil {
		t.Fatal(err)
	}
	if projects != 1 {
		t.Fatalf("conflict changed project count to %d", projects)
	}
}

func TestPortableImportInvalidatesMappedProjectTaskCursor(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "portable-cursor-revision.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	data := New(database)
	actor, err := data.CreateActor(ctx, Actor{Kind: "human", Name: "Importer"}, "hash")
	if err != nil {
		t.Fatal(err)
	}
	target, err := data.CreateProject(ctx, ProjectInput{Key: portableTestString("CURSOR"), Slug: portableTestString("cursor-target"), Name: portableTestString("Cursor target")}, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	columns, err := data.ListColumns(ctx, target.ID)
	if err != nil || len(columns) == 0 {
		t.Fatalf("target columns = %d, err=%v", len(columns), err)
	}
	for index := 0; index < 2; index++ {
		if _, err := data.CreateTask(ctx, target.ID, TaskInput{Title: portableTestString(fmt.Sprintf("Existing task %d", index+1)), ColumnID: &columns[0].ID}, actor.ID); err != nil {
			t.Fatalf("create existing task %d: %v", index, err)
		}
	}
	_, more, cursor, err := data.ListTasksCursor(ctx, target.ID, TaskFilter{Sort: TaskSortNumber, Limit: 1})
	if err != nil || !more || cursor == "" {
		t.Fatalf("first task page more=%t cursor=%q err=%v", more, cursor, err)
	}
	readStats := func() (int64, int, int) {
		t.Helper()
		var revision int64
		if err := database.QueryRowContext(ctx, `SELECT task_collection_revision FROM projects WHERE id=?`, target.ID).Scan(&revision); err != nil {
			t.Fatalf("read task collection revision: %v", err)
		}
		var tasks, events int
		if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE project_id=?`, target.ID).Scan(&tasks); err != nil {
			t.Fatalf("count target tasks: %v", err)
		}
		if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE project_id=?`, target.ID).Scan(&events); err != nil {
			t.Fatalf("count target events: %v", err)
		}
		return revision, tasks, events
	}
	revisionBefore, tasksBefore, eventsBefore := readStats()
	timestamp := updatedTimestampForPortableTest()
	archive := PortableArchive{
		Format: PortableFormat, Version: PortableVersion, ExportedAt: timestamp,
		Projects: []PortableProject{{ID: "cursor-source-project", Key: "CURSORSRC", Slug: "cursor-source", Name: "Cursor source", Color: "#64748b", CreatedAt: timestamp, UpdatedAt: timestamp}},
		Columns:  []PortableColumn{{ID: "cursor-source-column", ProjectID: "cursor-source-project", Name: "Imported backlog", SemanticState: "backlog", Position: 0, CreatedAt: timestamp, UpdatedAt: timestamp}},
		Tasks:    []PortableTask{{ID: "cursor-source-task", Number: 1, ProjectID: "cursor-source-project", Kind: "task", ColumnID: "cursor-source-column", Title: "Imported task", Priority: "normal", Version: 1, CreatedAt: timestamp, UpdatedAt: timestamp}},
		Labels:   []PortableLabel{}, Comments: []PortableComment{},
		Relationships: PortableRelationships{TaskLabels: []PortableTaskLabel{}, Dependencies: []PortableDependency{}, TaskLinks: []PortableTaskLink{}},
		Activity:      PortableActivity{Events: []PortableEvent{}, AgentWork: []PortableAgentWork{}, AgentWorkHistory: []PortableAgentWorkHistory{}},
	}
	preview, err := data.ImportPortable(ctx, archive, PortableImportOptions{ActorID: actor.ID, TargetProjectID: target.ID, DryRun: true})
	if err != nil {
		t.Fatalf("portable cursor dry-run: %v report=%+v", err, preview)
	}
	if !preview.DryRun || preview.Counts.ProjectsSkipped != 1 || preview.Counts.TasksCreated != 1 {
		t.Fatalf("portable cursor dry-run report = %+v", preview)
	}
	if revision, tasks, events := readStats(); revision != revisionBefore || tasks != tasksBefore || events != eventsBefore {
		t.Fatalf("dry-run changed target stats revision/tasks/events=%d/%d/%d, want %d/%d/%d", revision, tasks, events, revisionBefore, tasksBefore, eventsBefore)
	}

	invalidArchive := archive
	invalidArchive.Format = "invalid.format"
	invalidReport, err := data.ImportPortable(ctx, invalidArchive, PortableImportOptions{ActorID: actor.ID, TargetProjectID: target.ID})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid portable cursor import error=%v report=%+v, want ErrInvalid", err, invalidReport)
	}
	if revision, tasks, events := readStats(); revision != revisionBefore || tasks != tasksBefore || events != eventsBefore {
		t.Fatalf("validation failure changed target stats revision/tasks/events=%d/%d/%d, want %d/%d/%d", revision, tasks, events, revisionBefore, tasksBefore, eventsBefore)
	}

	report, err := data.ImportPortable(ctx, archive, PortableImportOptions{ActorID: actor.ID, TargetProjectID: target.ID})
	if err != nil {
		t.Fatalf("portable cursor import: %v report=%+v", err, report)
	}
	if report.Counts.ProjectsSkipped != 1 || report.Counts.ColumnsCreated != 1 || report.Counts.TasksCreated != 1 || !portableReportHasRemap(report, "project", "id") {
		t.Fatalf("portable cursor import report = %+v", report)
	}
	if revision, tasks, events := readStats(); revision <= revisionBefore || tasks != tasksBefore+1 || events != eventsBefore {
		t.Fatalf("import changed target stats revision/tasks/events=%d/%d/%d, want revision > %d, tasks=%d, events=%d", revision, tasks, events, revisionBefore, tasksBefore+1, eventsBefore)
	}
	_, _, _, err = data.ListTasksCursor(ctx, target.ID, TaskFilter{Sort: TaskSortNumber, CursorToken: cursor, Limit: 1})
	if !errors.Is(err, ErrTaskCollectionChanged) {
		t.Fatalf("portable import continuation error = %v, want ErrTaskCollectionChanged", err)
	}
	_, more, freshCursor, err := data.ListTasksCursor(ctx, target.ID, TaskFilter{Sort: TaskSortNumber, Limit: 1})
	if err != nil || !more || freshCursor == "" {
		t.Fatalf("fresh task page after portable import more=%t cursor=%q err=%v", more, freshCursor, err)
	}
	revisionAfterImport, tasksAfterImport, eventsAfterImport := readStats()
	retryReport, err := data.ImportPortable(ctx, archive, PortableImportOptions{ActorID: actor.ID, TargetProjectID: target.ID})
	if err != nil {
		t.Fatalf("identical portable retry: %v report=%+v", err, retryReport)
	}
	if retryReport.Counts.ProjectsCreated != 0 || retryReport.Counts.ColumnsCreated != 0 || retryReport.Counts.TasksCreated != 0 || retryReport.Counts.ProjectsSkipped != 1 || retryReport.Counts.ColumnsSkipped != 1 || retryReport.Counts.TasksSkipped != 1 {
		t.Fatalf("identical portable retry report = %+v", retryReport)
	}
	if revision, tasks, events := readStats(); revision != revisionAfterImport || tasks != tasksAfterImport || events != eventsAfterImport {
		t.Fatalf("identical portable retry changed target stats revision/tasks/events=%d/%d/%d, want %d/%d/%d", revision, tasks, events, revisionAfterImport, tasksAfterImport, eventsAfterImport)
	}
	if _, _, _, err := data.ListTasksCursor(ctx, target.ID, TaskFilter{Sort: TaskSortNumber, CursorToken: freshCursor, Limit: 1}); err != nil {
		t.Fatalf("identical portable retry invalidated fresh continuation: %v", err)
	}
}

func TestPortableConflictRemapsAreStableAcrossRetries(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "portable-remap-retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	data := New(database)
	actor, err := data.CreateActor(ctx, Actor{Kind: "human", Name: "Importer"}, "hash")
	if err != nil {
		t.Fatal(err)
	}
	timestamp := "2026-09-04T00:00:00Z"
	eventCursor := int64(1)
	commentID := "portable-remap-comment"
	checkpointCompleted, checkpointTotal := 0, 1
	// Occupy the first deterministic project remap candidate with unrelated
	// data. The retry below must still find the suffix selected after this
	// collision instead of creating a second copy on every attempt.
	occupiedProjectID := portableStableID("project", "portable-remap-project")
	if _, err := database.ExecContext(ctx, `INSERT INTO projects(id,key,slug,name,description,color,favorite,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?)`, occupiedProjectID, "OCCUPIED", "occupied", "Unrelated destination", "", "#64748b", 0, timestamp, timestamp); err != nil {
		t.Fatal(err)
	}
	archive := func(name, title, body, payload, summary string) PortableArchive {
		return PortableArchive{
			Format: PortableFormat, Version: PortableVersion, ExportedAt: timestamp,
			Source:        PortableSource{Product: "helm", API: "/api/v1"},
			Projects:      []PortableProject{{ID: "portable-remap-project", Key: "REMAP", Slug: "portable-remap", Name: name, Color: "#64748b", CreatedAt: timestamp, UpdatedAt: timestamp}},
			Columns:       []PortableColumn{{ID: "portable-remap-column", ProjectID: "portable-remap-project", Name: "Backlog", SemanticState: "backlog", Position: 0, CreatedAt: timestamp, UpdatedAt: timestamp}},
			Tasks:         []PortableTask{{ID: "portable-remap-task", Number: 1, ProjectID: "portable-remap-project", Kind: "task", ColumnID: "portable-remap-column", Title: title, Priority: "normal", Version: 1, CreatedAt: timestamp, UpdatedAt: timestamp}},
			Labels:        []PortableLabel{},
			Comments:      []PortableComment{{ID: commentID, TaskID: "portable-remap-task", ActorID: actor.ID, Body: body, CreatedAt: timestamp, UpdatedAt: timestamp}},
			Relationships: PortableRelationships{TaskLabels: []PortableTaskLabel{}, Dependencies: []PortableDependency{}, TaskLinks: []PortableTaskLink{}},
			Activity: PortableActivity{
				Events:           []PortableEvent{{Cursor: eventCursor, ID: "portable-remap-event", Type: "portable.test", ActorID: portableTestString(actor.ID), ProjectID: portableTestString("portable-remap-project"), TaskID: portableTestString("portable-remap-task"), Payload: json.RawMessage(payload), CreatedAt: timestamp}},
				AgentWork:        []PortableAgentWork{},
				AgentWorkHistory: []PortableAgentWorkHistory{{ID: "portable-remap-history", TaskID: "portable-remap-task", OperationID: "portable-remap-operation", ActorID: actor.ID, State: "working", Phase: "test", Summary: summary, CheckpointRefs: []string{"one"}, CheckpointCompleted: &checkpointCompleted, CheckpointTotal: &checkpointTotal, StartedAt: timestamp, CreatedAt: timestamp, GeneratedCommentID: portableTestString(commentID), ProgressEventCursor: &eventCursor}},
			},
		}
	}
	first := archive("First", "First task", "first comment", `{"v":1}`, "first summary")
	if report, err := data.ImportPortable(ctx, first, PortableImportOptions{ActorID: actor.ID}); err != nil {
		t.Fatalf("first import: %v report=%+v", err, report)
	}
	second := archive("Second", "Second task", "second comment", `{"v":2}`, "second summary")
	firstRemap, err := data.ImportPortable(ctx, second, PortableImportOptions{ActorID: actor.ID})
	if err != nil {
		t.Fatalf("first remapped import: %v report=%+v", err, firstRemap)
	}
	if firstRemap.Counts.ProjectsCreated != 1 || firstRemap.Counts.TasksCreated != 1 || firstRemap.Counts.CommentsCreated != 1 || firstRemap.Counts.EventsCreated != 1 || firstRemap.Counts.HistoryCreated != 1 {
		t.Fatalf("first remapped report = %+v", firstRemap)
	}
	secondRemap, err := data.ImportPortable(ctx, second, PortableImportOptions{ActorID: actor.ID})
	if err != nil {
		t.Fatalf("retry remapped import: %v report=%+v", err, secondRemap)
	}
	if secondRemap.Counts.ProjectsCreated != 0 || secondRemap.Counts.TasksCreated != 0 || secondRemap.Counts.CommentsCreated != 0 || secondRemap.Counts.EventsCreated != 0 || secondRemap.Counts.HistoryCreated != 0 {
		t.Fatalf("retry created duplicate records: %+v", secondRemap)
	}
	if secondRemap.Counts.ProjectsSkipped != 1 || secondRemap.Counts.TasksSkipped != 1 || secondRemap.Counts.CommentsSkipped != 1 || secondRemap.Counts.EventsSkipped != 1 || secondRemap.Counts.HistorySkipped != 1 {
		t.Fatalf("retry skip report = %+v", secondRemap)
	}
	var projectCount, taskCount, commentCount, eventCount, historyCount int
	for query, target := range map[string]*int{
		`SELECT COUNT(*) FROM projects`:                &projectCount,
		`SELECT COUNT(*) FROM tasks`:                   &taskCount,
		`SELECT COUNT(*) FROM comments`:                &commentCount,
		`SELECT COUNT(*) FROM events`:                  &eventCount,
		`SELECT COUNT(*) FROM task_agent_work_history`: &historyCount,
	} {
		if err := database.QueryRowContext(ctx, query).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if projectCount != 3 || taskCount != 2 || commentCount != 2 || eventCount != 2 || historyCount != 2 {
		t.Fatalf("retry duplicated records projects=%d tasks=%d comments=%d events=%d history=%d", projectCount, taskCount, commentCount, eventCount, historyCount)
	}
}

func TestPortableFieldOnlyRemapsReuseTheImportedSubtree(t *testing.T) {
	ctx := context.Background()
	timestamp := "2026-09-04T00:00:00Z"

	t.Run("project key and slug", func(t *testing.T) {
		database, err := db.Open(ctx, filepath.Join(t.TempDir(), "project-fields.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		data := New(database)
		actor, err := data.CreateActor(ctx, Actor{Kind: "human", Name: "Importer"}, "hash")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := data.CreateProject(ctx, ProjectInput{Key: portableTestString("FIELD"), Slug: portableTestString("field-slug"), Name: portableTestString("Existing")}, actor.ID); err != nil {
			t.Fatal(err)
		}
		archive := PortableArchive{
			Format: PortableFormat, Version: PortableVersion, ExportedAt: timestamp,
			Projects: []PortableProject{{ID: "field-project", Key: "FIELD", Slug: "field-slug", Name: "Imported", Color: "#64748b", CreatedAt: timestamp, UpdatedAt: timestamp}},
			Columns:  []PortableColumn{}, Tasks: []PortableTask{}, Labels: []PortableLabel{}, Comments: []PortableComment{},
			Relationships: PortableRelationships{TaskLabels: []PortableTaskLabel{}, Dependencies: []PortableDependency{}, TaskLinks: []PortableTaskLink{}},
			Activity:      PortableActivity{Events: []PortableEvent{}, AgentWork: []PortableAgentWork{}, AgentWorkHistory: []PortableAgentWorkHistory{}},
		}
		first, err := data.ImportPortable(ctx, archive, PortableImportOptions{ActorID: actor.ID})
		if err != nil {
			t.Fatalf("first import: %v report=%+v", err, first)
		}
		if first.Counts.ProjectsCreated != 1 || !portableReportHasRemap(first, "project", "key") || !portableReportHasRemap(first, "project", "slug") {
			t.Fatalf("first field remap report = %+v", first)
		}
		second, err := data.ImportPortable(ctx, archive, PortableImportOptions{ActorID: actor.ID})
		if err != nil {
			t.Fatalf("retry import: %v report=%+v", err, second)
		}
		if second.Counts.ProjectsCreated != 0 || second.Counts.ProjectsSkipped != 1 || !portableReportHasRemap(second, "project", "key") || !portableReportHasRemap(second, "project", "slug") {
			t.Fatalf("retry field remap report = %+v", second)
		}
		var projectCount int
		if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects`).Scan(&projectCount); err != nil {
			t.Fatal(err)
		}
		if projectCount != 2 {
			t.Fatalf("field-only project retry created a duplicate: count=%d", projectCount)
		}
	})

	t.Run("column position and task number", func(t *testing.T) {
		database, err := db.Open(ctx, filepath.Join(t.TempDir(), "child-fields.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		data := New(database)
		actor, err := data.CreateActor(ctx, Actor{Kind: "human", Name: "Importer"}, "hash")
		if err != nil {
			t.Fatal(err)
		}
		target, err := data.CreateProject(ctx, ProjectInput{Key: portableTestString("TARGET"), Name: portableTestString("Target")}, actor.ID)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := data.ListColumns(ctx, target.ID)
		if err != nil || len(columns) == 0 {
			t.Fatalf("target columns = %d, err=%v", len(columns), err)
		}
		if _, err := data.CreateTask(ctx, target.ID, TaskInput{Title: portableTestString("Existing number one"), ColumnID: &columns[0].ID}, actor.ID); err != nil {
			t.Fatal(err)
		}
		archive := PortableArchive{
			Format: PortableFormat, Version: PortableVersion, ExportedAt: timestamp,
			Projects: []PortableProject{{ID: "child-source-project", Key: "CHILD", Slug: "child-source", Name: "Child source", Color: "#64748b", CreatedAt: timestamp, UpdatedAt: timestamp}},
			Columns:  []PortableColumn{{ID: "child-source-column", ProjectID: "child-source-project", Name: "Imported column", SemanticState: "backlog", Position: 0, CreatedAt: timestamp, UpdatedAt: timestamp}},
			Tasks:    []PortableTask{{ID: "child-source-task", Number: 1, ProjectID: "child-source-project", Kind: "task", ColumnID: "child-source-column", Title: "Imported task", Priority: "normal", Version: 1, CreatedAt: timestamp, UpdatedAt: timestamp}},
			Labels:   []PortableLabel{}, Comments: []PortableComment{},
			Relationships: PortableRelationships{TaskLabels: []PortableTaskLabel{}, Dependencies: []PortableDependency{}, TaskLinks: []PortableTaskLink{}},
			Activity:      PortableActivity{Events: []PortableEvent{}, AgentWork: []PortableAgentWork{}, AgentWorkHistory: []PortableAgentWorkHistory{}},
		}
		first, err := data.ImportPortable(ctx, archive, PortableImportOptions{ActorID: actor.ID, TargetProjectID: target.ID})
		if err != nil {
			t.Fatalf("first child import: %v report=%+v", err, first)
		}
		if first.Counts.ColumnsCreated != 1 || first.Counts.TasksCreated != 1 || !portableReportHasRemap(first, "column", "position") || !portableReportHasRemap(first, "task", "number") {
			t.Fatalf("first child field remap report = %+v", first)
		}
		second, err := data.ImportPortable(ctx, archive, PortableImportOptions{ActorID: actor.ID, TargetProjectID: target.ID})
		if err != nil {
			t.Fatalf("retry child import: %v report=%+v", err, second)
		}
		if second.Counts.ColumnsCreated != 0 || second.Counts.ColumnsSkipped != 1 || second.Counts.TasksCreated != 0 || second.Counts.TasksSkipped != 1 || !portableReportHasRemap(second, "column", "position") || !portableReportHasRemap(second, "task", "number") {
			t.Fatalf("retry child field remap report = %+v", second)
		}
		var columnCount, taskCount int
		if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM columns WHERE project_id=?`, target.ID).Scan(&columnCount); err != nil {
			t.Fatal(err)
		}
		if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE project_id=?`, target.ID).Scan(&taskCount); err != nil {
			t.Fatal(err)
		}
		if columnCount != len(columns)+1 || taskCount != 2 {
			t.Fatalf("field-only child retry created duplicates: columns=%d tasks=%d", columnCount, taskCount)
		}
	})
}

func portableReportHasRemap(report PortableImportReport, entity, field string) bool {
	for _, remap := range report.Remaps {
		if remap.Entity == entity && remap.Field == field {
			return true
		}
	}
	return false
}

func portableReportHasIssue(report PortableImportReport, entity, field, message string) bool {
	for _, issue := range report.Errors {
		if issue.Entity == entity && issue.Field == field && issue.Message == message {
			return true
		}
	}
	return false
}

func TestPortableTaskLinkUniquenessIsPreflighted(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "portable-link-conflict.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	data := New(database)
	actor, err := data.CreateActor(ctx, Actor{Kind: "human", Name: "Importer"}, "hash")
	if err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, ProjectInput{Key: portableTestString("LINKS"), Name: portableTestString("Links")}, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	columns, err := data.ListColumns(ctx, project.ID)
	if err != nil || len(columns) == 0 {
		t.Fatalf("columns = %d, err=%v", len(columns), err)
	}
	createTask := func(title string) Task {
		t.Helper()
		task, createErr := data.CreateTask(ctx, project.ID, TaskInput{Title: portableTestString(title), ColumnID: &columns[0].ID}, actor.ID)
		if createErr != nil {
			t.Fatal(createErr)
		}
		return task
	}
	source, existingTarget, importedTarget := createTask("Source"), createTask("Existing target"), createTask("Imported target")
	createdAt := updatedTimestampForPortableTest()
	if _, err := database.ExecContext(ctx, `INSERT INTO task_links(source_task_id,target_task_id,link_type,created_at) VALUES (?,?,?,?)`, source.ID, existingTarget.ID, "relates", createdAt); err != nil {
		t.Fatal(err)
	}
	archive, err := data.ExportPortable(ctx, []string{project.ID})
	if err != nil {
		t.Fatal(err)
	}
	archive.Relationships.TaskLinks = []PortableTaskLink{{SourceTaskID: source.ID, TargetTaskID: importedTarget.ID, LinkType: "relates", CreatedAt: createdAt}}
	preview, err := data.ImportPortable(ctx, archive, PortableImportOptions{ActorID: actor.ID, TargetProjectID: project.ID, DryRun: true})
	if err != nil {
		t.Fatalf("link remap dry-run: %v report=%+v", err, preview)
	}
	if preview.Counts.LinksCreated != 0 || preview.Counts.LinksSkipped != 1 || !portableReportHasRemap(preview, "task_link", "target_task_id") {
		t.Fatalf("link remap preview = %+v", preview)
	}
	if _, err := data.ImportPortable(ctx, archive, PortableImportOptions{ActorID: actor.ID, TargetProjectID: project.ID}); err != nil {
		t.Fatalf("link remap import: %v", err)
	}
	var links int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_links WHERE source_task_id=?`, source.ID).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 1 {
		t.Fatalf("remap import changed destination link count to %d", links)
	}
	failPreview, err := data.ImportPortable(ctx, archive, PortableImportOptions{ActorID: actor.ID, TargetProjectID: project.ID, Conflict: portableConflictFail, DryRun: true})
	if !errors.Is(err, ErrConflict) || len(failPreview.Errors) == 0 || !strings.Contains(failPreview.Errors[0].Message, "already links") {
		t.Fatalf("link fail preview error=%v report=%+v", err, failPreview)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_links WHERE source_task_id=?`, source.ID).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 1 {
		t.Fatalf("link fail preview mutated destination link count to %d", links)
	}
}

func TestPortableDependencyLimitsArePreflightedWithoutMutation(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "portable-dependency-limit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	data := New(database)
	actor, err := data.CreateActor(ctx, Actor{Kind: "human", Name: "Importer"}, "hash")
	if err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, ProjectInput{Key: portableTestString("DEPLIMIT"), Name: portableTestString("Dependency limits")}, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	columns, err := data.ListColumns(ctx, project.ID)
	if err != nil || len(columns) == 0 {
		t.Fatalf("columns = %d, err=%v", len(columns), err)
	}
	dependent, err := data.CreateTask(ctx, project.ID, TaskInput{Title: portableTestString("Dependent"), ColumnID: &columns[0].ID}, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	prerequisites := make([]Task, 0, maxDirectTaskDependencies)
	for index := 0; index < maxDirectTaskDependencies; index++ {
		prerequisite, createErr := data.CreateTask(ctx, project.ID, TaskInput{Title: portableTestString(fmt.Sprintf("Prerequisite %d", index)), ColumnID: &columns[0].ID}, actor.ID)
		if createErr != nil {
			t.Fatal(createErr)
		}
		prerequisites = append(prerequisites, prerequisite)
		if _, err := database.ExecContext(ctx, `INSERT INTO task_dependencies(task_id,prerequisite_task_id,created_by,created_at) VALUES (?,?,?,?)`, dependent.ID, prerequisite.ID, actor.ID, updatedTimestampForPortableTest()); err != nil {
			t.Fatal(err)
		}
	}
	// The loop above intentionally exercises the trigger boundary; verify the
	// fixture really reached the limit before asking the importer to add one.
	var dependencyCount int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_dependencies WHERE task_id=?`, dependent.ID).Scan(&dependencyCount); err != nil {
		t.Fatal(err)
	}
	if dependencyCount != maxDirectTaskDependencies {
		t.Fatalf("dependency fixture count = %d", dependencyCount)
	}
	existingDependent, ok, err := portableExistingTask(database, dependent.ID)
	if err != nil || !ok {
		t.Fatalf("existing dependent = %+v, ok=%v, err=%v", existingDependent, ok, err)
	}
	timestamp := updatedTimestampForPortableTest()
	sourceProjectID := "dependency-source-project"
	existingDependent.ProjectID = sourceProjectID
	archive := PortableArchive{
		Format: PortableFormat, Version: PortableVersion, ExportedAt: timestamp,
		Projects: []PortableProject{{ID: sourceProjectID, Key: "DEPSOURCE", Slug: "dependency-source", Name: "Dependency source", Color: "#64748b", CreatedAt: timestamp, UpdatedAt: timestamp}},
		Columns:  []PortableColumn{{ID: columns[0].ID, ProjectID: sourceProjectID, Name: columns[0].Name, SemanticState: columns[0].SemanticState, Position: columns[0].Position, CreatedAt: columns[0].CreatedAt, UpdatedAt: columns[0].UpdatedAt}},
		Tasks:    []PortableTask{existingDependent, {ID: "portable-new-prerequisite", Number: 999, ProjectID: sourceProjectID, Kind: "task", ColumnID: columns[0].ID, Title: "New prerequisite", Priority: "normal", Version: 1, CreatedAt: timestamp, UpdatedAt: timestamp}},
		Labels:   []PortableLabel{}, Comments: []PortableComment{},
		Relationships: PortableRelationships{TaskLabels: []PortableTaskLabel{}, Dependencies: []PortableDependency{{TaskID: dependent.ID, PrerequisiteID: "portable-new-prerequisite", CreatedBy: portableTestString(actor.ID), CreatedAt: timestamp}}, TaskLinks: []PortableTaskLink{}},
		Activity:      PortableActivity{Events: []PortableEvent{}, AgentWork: []PortableAgentWork{}, AgentWorkHistory: []PortableAgentWorkHistory{}},
	}
	preview, err := data.ImportPortable(ctx, archive, PortableImportOptions{ActorID: actor.ID, TargetProjectID: project.ID, DryRun: true})
	if !errors.Is(err, ErrInvalid) || len(preview.Errors) == 0 || !strings.Contains(preview.Errors[0].Message, "direct prerequisites") {
		t.Fatalf("dependency limit dry-run error=%v report=%+v", err, preview)
	}
	var taskCount int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE project_id=?`, project.ID).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	beforeTaskCount := taskCount
	result, err := data.ImportPortable(ctx, archive, PortableImportOptions{ActorID: actor.ID, TargetProjectID: project.ID})
	if !errors.Is(err, ErrInvalid) || len(result.Errors) == 0 || !strings.Contains(result.Errors[0].Message, "direct prerequisites") {
		t.Fatalf("dependency limit import error=%v report=%+v", err, result)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE project_id=?`, project.ID).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if taskCount != beforeTaskCount {
		t.Fatalf("dependency limit import mutated task count from %d to %d", beforeTaskCount, taskCount)
	}
}

func TestPortableDependenciesPreflightDestinationGraphAndLifecycle(t *testing.T) {
	t.Run("destination cycle", func(t *testing.T) {
		ctx := context.Background()
		database, err := db.Open(ctx, filepath.Join(t.TempDir(), "portable-destination-cycle.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		data := New(database)
		actor, err := data.CreateActor(ctx, Actor{Kind: "human", Name: "Importer"}, "hash")
		if err != nil {
			t.Fatal(err)
		}
		project, err := data.CreateProject(ctx, ProjectInput{Key: portableTestString("CYCLE"), Name: portableTestString("Destination cycle")}, actor.ID)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := data.ListColumns(ctx, project.ID)
		if err != nil || len(columns) == 0 {
			t.Fatalf("columns = %d, err=%v", len(columns), err)
		}
		createTask := func(title string) Task {
			t.Helper()
			task, createErr := data.CreateTask(ctx, project.ID, TaskInput{Title: portableTestString(title), ColumnID: &columns[0].ID}, actor.ID)
			if createErr != nil {
				t.Fatal(createErr)
			}
			return task
		}
		a, b := createTask("A"), createTask("B")
		timestamp := updatedTimestampForPortableTest()
		if _, err := database.ExecContext(ctx, `INSERT INTO task_dependencies(task_id,prerequisite_task_id,created_by,created_at) VALUES (?,?,?,?)`, a.ID, b.ID, actor.ID, timestamp); err != nil {
			t.Fatal(err)
		}
		archive, err := data.ExportPortable(ctx, []string{project.ID})
		if err != nil {
			t.Fatal(err)
		}
		// Keep the destination A -> B edge out of the archive. Importing only
		// B -> A must be checked against the destination graph, not rejected by
		// the archive-local cycle validator first.
		archive.Relationships.Dependencies = []PortableDependency{{TaskID: b.ID, PrerequisiteID: a.ID, CreatedBy: portableTestString(actor.ID), CreatedAt: timestamp}}
		var beforeDependencies int
		if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_dependencies`).Scan(&beforeDependencies); err != nil {
			t.Fatal(err)
		}
		for _, options := range []PortableImportOptions{{ActorID: actor.ID, TargetProjectID: project.ID, DryRun: true}, {ActorID: actor.ID, TargetProjectID: project.ID}} {
			report, importErr := data.ImportPortable(ctx, archive, options)
			if !errors.Is(importErr, ErrConflict) || !portableReportHasIssue(report, "dependency", "prerequisite_task_id", "dependency would create a cycle in the destination graph") {
				t.Fatalf("destination cycle options=%+v error=%v report=%+v", options, importErr, report)
			}
			var dependencies int
			if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_dependencies`).Scan(&dependencies); err != nil {
				t.Fatal(err)
			}
			if dependencies != beforeDependencies {
				t.Fatalf("destination cycle options=%+v mutated dependencies from %d to %d", options, beforeDependencies, dependencies)
			}
		}
	})

	t.Run("active task with unmet prerequisite", func(t *testing.T) {
		ctx := context.Background()
		database, err := db.Open(ctx, filepath.Join(t.TempDir(), "portable-destination-unmet.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		data := New(database)
		actor, err := data.CreateActor(ctx, Actor{Kind: "human", Name: "Importer"}, "hash")
		if err != nil {
			t.Fatal(err)
		}
		project, err := data.CreateProject(ctx, ProjectInput{Key: portableTestString("UNMET"), Name: portableTestString("Destination unmet")}, actor.ID)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := data.ListColumns(ctx, project.ID)
		if err != nil || len(columns) < 3 {
			t.Fatalf("columns = %d, err=%v", len(columns), err)
		}
		var activeColumn Column
		for _, column := range columns {
			if column.SemanticState == "active" {
				activeColumn = column
				break
			}
		}
		if activeColumn.ID == "" {
			t.Fatal("active column was not created")
		}
		dependent, err := data.CreateTask(ctx, project.ID, TaskInput{Title: portableTestString("Active dependent"), ColumnID: &columns[0].ID}, actor.ID)
		if err != nil {
			t.Fatal(err)
		}
		prerequisite, err := data.CreateTask(ctx, project.ID, TaskInput{Title: portableTestString("Unfinished prerequisite"), ColumnID: &columns[0].ID}, actor.ID)
		if err != nil {
			t.Fatal(err)
		}
		timestamp := updatedTimestampForPortableTest()
		if _, err := database.ExecContext(ctx, `UPDATE tasks SET column_id=?,updated_at=? WHERE id=?`, activeColumn.ID, timestamp, dependent.ID); err != nil {
			t.Fatal(err)
		}
		archive, err := data.ExportPortable(ctx, []string{project.ID})
		if err != nil {
			t.Fatal(err)
		}
		archive.Relationships.Dependencies = []PortableDependency{{TaskID: dependent.ID, PrerequisiteID: prerequisite.ID, CreatedBy: portableTestString(actor.ID), CreatedAt: timestamp}}
		var beforeTasks, beforeDependencies int
		if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks`).Scan(&beforeTasks); err != nil {
			t.Fatal(err)
		}
		if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_dependencies`).Scan(&beforeDependencies); err != nil {
			t.Fatal(err)
		}
		for _, options := range []PortableImportOptions{{ActorID: actor.ID, TargetProjectID: project.ID, DryRun: true}, {ActorID: actor.ID, TargetProjectID: project.ID}} {
			report, importErr := data.ImportPortable(ctx, archive, options)
			if !errors.Is(importErr, ErrInvalid) || !portableReportHasIssue(report, "dependency", "task_id", "dependency prerequisites are not satisfied for this task state") {
				t.Fatalf("destination unmet options=%+v error=%v report=%+v", options, importErr, report)
			}
			var tasks, dependencies int
			if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks`).Scan(&tasks); err != nil {
				t.Fatal(err)
			}
			if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_dependencies`).Scan(&dependencies); err != nil {
				t.Fatal(err)
			}
			if tasks != beforeTasks || dependencies != beforeDependencies {
				t.Fatalf("destination unmet options=%+v mutated tasks/dependencies from %d/%d to %d/%d", options, beforeTasks, beforeDependencies, tasks, dependencies)
			}
		}
	})
}

func TestPortableAgentWorkHistoryConstraintsArePreflightedWithoutMutation(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "portable-history-constraint.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	data := New(database)
	actor, err := data.CreateActor(ctx, Actor{Kind: "human", Name: "Importer"}, "hash")
	if err != nil {
		t.Fatal(err)
	}
	project, err := data.CreateProject(ctx, ProjectInput{Key: portableTestString("HISTORY"), Name: portableTestString("History")}, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	columns, err := data.ListColumns(ctx, project.ID)
	if err != nil || len(columns) == 0 {
		t.Fatalf("columns = %d, err=%v", len(columns), err)
	}
	timestamp := updatedTimestampForPortableTest()
	completed, total := 2, 1
	archive := PortableArchive{
		Format: PortableFormat, Version: PortableVersion, ExportedAt: timestamp,
		Projects: []PortableProject{{ID: "history-source-project", Key: "HISTSOURCE", Slug: "history-source", Name: "History source", Color: "#64748b", CreatedAt: timestamp, UpdatedAt: timestamp}},
		Columns:  []PortableColumn{{ID: "history-source-column", ProjectID: "history-source-project", Name: "Backlog", SemanticState: "backlog", Position: 0, CreatedAt: timestamp, UpdatedAt: timestamp}},
		Tasks:    []PortableTask{{ID: "history-source-task", Number: 1, ProjectID: "history-source-project", Kind: "task", ColumnID: "history-source-column", Title: "History task", Priority: "normal", Version: 1, CreatedAt: timestamp, UpdatedAt: timestamp}},
		Labels:   []PortableLabel{}, Comments: []PortableComment{},
		Relationships: PortableRelationships{TaskLabels: []PortableTaskLabel{}, Dependencies: []PortableDependency{}, TaskLinks: []PortableTaskLink{}},
		Activity:      PortableActivity{Events: []PortableEvent{}, AgentWork: []PortableAgentWork{}, AgentWorkHistory: []PortableAgentWorkHistory{{ID: "history-invalid", TaskID: "history-source-task", OperationID: "history-operation", ActorID: actor.ID, State: "working", Phase: "test", Summary: "Invalid checkpoint", CheckpointRefs: []string{"checkpoint"}, CheckpointCompleted: &completed, CheckpointTotal: &total, StartedAt: timestamp, CreatedAt: timestamp}}},
	}
	var projects, tasks, histories int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects`).Scan(&projects); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks`).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_agent_work_history`).Scan(&histories); err != nil {
		t.Fatal(err)
	}
	preview, err := data.ImportPortable(ctx, archive, PortableImportOptions{ActorID: actor.ID, DryRun: true})
	if !errors.Is(err, ErrInvalid) || !portableReportHasIssue(preview, "agent_work_history", "checkpoint", "checkpoint progress must satisfy 0 <= completed <= total <= 100") {
		t.Fatalf("history constraint dry-run error=%v report=%+v", err, preview)
	}
	result, err := data.ImportPortable(ctx, archive, PortableImportOptions{ActorID: actor.ID})
	if !errors.Is(err, ErrInvalid) || !portableReportHasIssue(result, "agent_work_history", "checkpoint", "checkpoint progress must satisfy 0 <= completed <= total <= 100") {
		t.Fatalf("history constraint import error=%v report=%+v", err, result)
	}
	var afterProjects, afterTasks, afterHistories int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects`).Scan(&afterProjects); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks`).Scan(&afterTasks); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_agent_work_history`).Scan(&afterHistories); err != nil {
		t.Fatal(err)
	}
	if projects != afterProjects || tasks != afterTasks || histories != afterHistories {
		t.Fatalf("invalid history import mutated counts before=%d/%d/%d after=%d/%d/%d", projects, tasks, histories, afterProjects, afterTasks, afterHistories)
	}
}

func TestPortableAgentWorkValidationMatchesRuntimeConstraints(t *testing.T) {
	type mutation struct {
		name    string
		apply   func(*AgentWorkInput)
		field   string
		message string
	}
	mutations := []mutation{
		{name: "operation id regex", apply: func(input *AgentWorkInput) { input.OperationID = "invalid operation" }, field: "operation_id", message: "operation_id must be between 1 and 128 safe identifier characters"},
		{name: "phase length", apply: func(input *AgentWorkInput) { input.Phase = strings.Repeat("p", 121) }, field: "phase", message: "phase is too long"},
		{name: "next action length", apply: func(input *AgentWorkInput) { input.NextAction = strings.Repeat("n", 1001) }, field: "next_action", message: "next_action is too long"},
		{name: "empty checkpoint reference", apply: func(input *AgentWorkInput) { input.CheckpointRefs = []string{""} }, field: "checkpoint_refs", message: "checkpoint_refs items must be between 1 and 128 characters"},
		{name: "duplicate checkpoint reference", apply: func(input *AgentWorkInput) {
			input.CheckpointRefs, input.CheckpointCompleted, input.CheckpointTotal = []string{"checkpoint", "checkpoint"}, portableTestIntPointer(0), portableTestIntPointer(2)
		}, field: "checkpoint_refs", message: "checkpoint_refs must not contain duplicate items"},
		{name: "checkpoint reference count", apply: func(input *AgentWorkInput) { input.CheckpointRefs = make([]string, 101) }, field: "checkpoint_refs", message: "checkpoint_refs must contain at most 100 items"},
		{name: "checkpoint reference length", apply: func(input *AgentWorkInput) { input.CheckpointRefs = []string{strings.Repeat("r", 129)} }, field: "checkpoint_refs", message: "checkpoint_refs items must be between 1 and 128 characters"},
		{name: "checkpoint total consistency", apply: func(input *AgentWorkInput) {
			input.CheckpointCompleted, input.CheckpointTotal = portableTestIntPointer(0), portableTestIntPointer(2)
		}, field: "checkpoint_refs", message: "checkpoint_refs length must equal checkpoint_total"},
		{name: "checkpoint pair consistency", apply: func(input *AgentWorkInput) { input.CheckpointTotal = nil }, field: "checkpoint", message: "checkpoint_completed and checkpoint_total must be provided together"},
		{name: "state", apply: func(input *AgentWorkInput) { input.State = "paused" }, field: "state", message: "state must be working, waiting, verifying, or handoff"},
	}
	for _, recordKind := range []string{"agent work", "history"} {
		for _, testCase := range mutations {
			t.Run(recordKind+"/"+testCase.name, func(t *testing.T) {
				ctx := context.Background()
				database, err := db.Open(ctx, filepath.Join(t.TempDir(), "portable-agent-work-validation.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer database.Close()
				data := New(database)
				actor, err := data.CreateActor(ctx, Actor{Kind: "human", Name: "Importer"}, "hash")
				if err != nil {
					t.Fatal(err)
				}
				timestamp := updatedTimestampForPortableTest()
				completed, total := 0, 1
				input := AgentWorkInput{OperationID: "portable/activity", State: "working", Phase: "test", Summary: "Summary", NextAction: "Next", CheckpointRefs: []string{"checkpoint"}, CheckpointCompleted: &completed, CheckpointTotal: &total}
				testCase.apply(&input)
				archive := PortableArchive{
					Format: PortableFormat, Version: PortableVersion, ExportedAt: timestamp,
					Projects: []PortableProject{{ID: "activity-project", Key: "ACTIVITY", Slug: "activity", Name: "Activity", Color: "#64748b", CreatedAt: timestamp, UpdatedAt: timestamp}},
					Columns:  []PortableColumn{{ID: "activity-column", ProjectID: "activity-project", Name: "Backlog", SemanticState: "backlog", Position: 0, CreatedAt: timestamp, UpdatedAt: timestamp}},
					Tasks:    []PortableTask{{ID: "activity-task", Number: 1, ProjectID: "activity-project", Kind: "task", ColumnID: "activity-column", Title: "Activity task", Priority: "normal", Version: 1, CreatedAt: timestamp, UpdatedAt: timestamp}},
					Labels:   []PortableLabel{}, Comments: []PortableComment{},
					Relationships: PortableRelationships{TaskLabels: []PortableTaskLabel{}, Dependencies: []PortableDependency{}, TaskLinks: []PortableTaskLink{}},
					Activity:      PortableActivity{Events: []PortableEvent{}, AgentWork: []PortableAgentWork{}, AgentWorkHistory: []PortableAgentWorkHistory{}},
				}
				if recordKind == "agent work" {
					archive.Activity.AgentWork = []PortableAgentWork{{TaskID: inputTaskID, OperationID: input.OperationID, ActorID: actor.ID, State: input.State, Phase: input.Phase, Summary: input.Summary, NextAction: input.NextAction, CheckpointRefs: input.CheckpointRefs, CheckpointCompleted: input.CheckpointCompleted, CheckpointTotal: input.CheckpointTotal, StartedAt: timestamp, UpdatedAt: timestamp}}
				} else {
					archive.Activity.AgentWorkHistory = []PortableAgentWorkHistory{{ID: "activity-history", TaskID: inputTaskID, OperationID: input.OperationID, ActorID: actor.ID, State: input.State, Phase: input.Phase, Summary: input.Summary, NextAction: input.NextAction, CheckpointRefs: input.CheckpointRefs, CheckpointCompleted: input.CheckpointCompleted, CheckpointTotal: input.CheckpointTotal, StartedAt: timestamp, CreatedAt: timestamp}}
				}
				counts := func() (int, int, int, int) {
					var projects, tasks, work, history int
					if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects`).Scan(&projects); err != nil {
						t.Fatal(err)
					}
					if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks`).Scan(&tasks); err != nil {
						t.Fatal(err)
					}
					if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_agent_work`).Scan(&work); err != nil {
						t.Fatal(err)
					}
					if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_agent_work_history`).Scan(&history); err != nil {
						t.Fatal(err)
					}
					return projects, tasks, work, history
				}
				beforeProjects, beforeTasks, beforeWork, beforeHistory := counts()
				entity := "agent_work"
				if recordKind == "history" {
					entity = "agent_work_history"
				}
				preview, previewErr := data.ImportPortable(ctx, archive, PortableImportOptions{ActorID: actor.ID, DryRun: true})
				if !errors.Is(previewErr, ErrInvalid) || !portableReportHasIssue(preview, entity, testCase.field, testCase.message) {
					t.Fatalf("validation dry-run kind=%s error=%v report=%+v", recordKind, previewErr, preview)
				}
				result, resultErr := data.ImportPortable(ctx, archive, PortableImportOptions{ActorID: actor.ID})
				if !errors.Is(resultErr, ErrInvalid) || !portableReportHasIssue(result, entity, testCase.field, testCase.message) {
					t.Fatalf("validation import kind=%s error=%v report=%+v", recordKind, resultErr, result)
				}
				afterProjects, afterTasks, afterWork, afterHistory := counts()
				if beforeProjects != afterProjects || beforeTasks != afterTasks || beforeWork != afterWork || beforeHistory != afterHistory {
					t.Fatalf("invalid %s import mutated counts before=%d/%d/%d/%d after=%d/%d/%d/%d", recordKind, beforeProjects, beforeTasks, beforeWork, beforeHistory, afterProjects, afterTasks, afterWork, afterHistory)
				}
			})
		}
	}
}

const inputTaskID = "activity-task"

func portableTestIntPointer(value int) *int { return &value }
