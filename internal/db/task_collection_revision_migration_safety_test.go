package db

import (
	"context"
	"testing"
)

func TestTaskCollectionRevisionMigrationPreservesPopulatedProjectsAndRetainedWrites(t *testing.T) {
	ctx := context.Background()
	migrations, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) < 20 {
		t.Skip("migration 020 is not present")
	}
	database := newRawDatabase(t)
	applyMigrationPrefix(t, ctx, database, migrations, 19)
	populateProductionFixture(t, ctx, database, 19)
	if err := Migrate(ctx, database); err != nil {
		t.Fatalf("migrate populated pre-020 database: %v", err)
	}
	var revision int64
	if err := database.QueryRowContext(ctx, `SELECT task_collection_revision FROM projects WHERE id='project'`).Scan(&revision); err != nil {
		t.Fatalf("read migrated project revision: %v", err)
	}
	if revision != 0 {
		t.Fatalf("migrated project revision = %d, want zero", revision)
	}

	insertSnapshot := `INSERT INTO task_agent_work(task_id, operation_id, actor_id, state, phase, summary, next_action, checkpoint_refs, started_at, updated_at) VALUES ('task-1', 'retained-run', 'agent', 'working', '', 'Retained pulse', '', '[]', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`
	if _, err := database.ExecContext(ctx, insertSnapshot); err != nil {
		t.Fatalf("retained snapshot insert: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT task_collection_revision FROM projects WHERE id='project'`).Scan(&revision); err != nil {
		t.Fatalf("read revision after retained insert: %v", err)
	}
	if revision != 1 {
		t.Fatalf("revision after retained insert = %d, want 1", revision)
	}
	// A retained binary can issue a same-value timestamp update without knowing
	// about the new column. The trigger still advances the durable revision.
	if _, err := database.ExecContext(ctx, `UPDATE task_agent_work SET updated_at=updated_at WHERE task_id='task-1'`); err != nil {
		t.Fatalf("retained same-value snapshot update: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT task_collection_revision FROM projects WHERE id='project'`).Scan(&revision); err != nil {
		t.Fatalf("read revision after retained update: %v", err)
	}
	if revision != 2 {
		t.Fatalf("revision after retained update = %d, want 2", revision)
	}
	// Retained SQL can also move the single live snapshot between tasks. Both
	// affected projects must invalidate cursors, while an ordinary same-project
	// heartbeat above increments only once.
	if _, err := database.ExecContext(ctx, `INSERT INTO projects(id,key,slug,name,created_at,updated_at) VALUES ('project-other','OTHER','other','Other project','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert other retained project: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO columns(id,project_id,name,semantic_state,position,created_at,updated_at) VALUES ('column-other','project-other','Backlog','backlog',0,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert other retained column: %v", err)
	}
	var otherRevision int64
	if _, err := database.ExecContext(ctx, `INSERT INTO tasks(id,project_id,number,column_id,kind,title,created_at,updated_at) VALUES ('task-other','project-other',1,'column-other','task','Other task','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert other retained task: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT task_collection_revision FROM projects WHERE id='project-other'`).Scan(&otherRevision); err != nil {
		t.Fatalf("read destination revision after retained task insert: %v", err)
	}
	if otherRevision != 1 {
		t.Fatalf("revision after retained task insert = %d, want 1", otherRevision)
	}
	if _, err := database.ExecContext(ctx, `UPDATE tasks SET title='Other task edited', position=position+0.5 WHERE id='task-other'`); err != nil {
		t.Fatalf("retained task title/position update: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT task_collection_revision FROM projects WHERE id='project-other'`).Scan(&otherRevision); err != nil {
		t.Fatalf("read destination revision after retained task update: %v", err)
	}
	if otherRevision != 2 {
		t.Fatalf("revision after retained task update = %d, want 2", otherRevision)
	}
	if _, err := database.ExecContext(ctx, `UPDATE task_agent_work SET task_id='task-other' WHERE task_id='task-1'`); err != nil {
		t.Fatalf("move retained snapshot to another project: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT task_collection_revision FROM projects WHERE id='project'`).Scan(&revision); err != nil {
		t.Fatalf("read source revision after retained move: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT task_collection_revision FROM projects WHERE id='project-other'`).Scan(&otherRevision); err != nil {
		t.Fatalf("read destination revision after retained move: %v", err)
	}
	if revision != 3 || otherRevision != 3 {
		t.Fatalf("revisions after retained cross-project move = %d/%d, want 3/3", revision, otherRevision)
	}
	if _, err := database.ExecContext(ctx, `UPDATE task_agent_work SET task_id='task-1' WHERE task_id='task-other'`); err != nil {
		t.Fatalf("move retained snapshot back to original project: %v", err)
	}
	if _, err := database.ExecContext(ctx, `DELETE FROM task_agent_work WHERE task_id='task-1'`); err != nil {
		t.Fatalf("retained snapshot delete: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT task_collection_revision FROM projects WHERE id='project'`).Scan(&revision); err != nil {
		t.Fatalf("read revision after retained delete: %v", err)
	}
	if revision != 5 {
		t.Fatalf("revision after retained delete = %d, want 5", revision)
	}
	if _, err := database.ExecContext(ctx, `UPDATE tasks SET deleted_at='2026-01-02T00:00:00Z' WHERE id='task-other'`); err != nil {
		t.Fatalf("retained task soft-delete: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT task_collection_revision FROM projects WHERE id='project-other'`).Scan(&otherRevision); err != nil {
		t.Fatalf("read destination revision after retained task soft-delete: %v", err)
	}
	if otherRevision != 5 {
		t.Fatalf("revision after retained task soft-delete = %d, want 5", otherRevision)
	}
	if _, err := database.ExecContext(ctx, `DELETE FROM tasks WHERE id='task-other'`); err != nil {
		t.Fatalf("retained task hard-delete: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT task_collection_revision FROM projects WHERE id='project-other'`).Scan(&otherRevision); err != nil {
		t.Fatalf("read destination revision after retained task hard-delete: %v", err)
	}
	if otherRevision != 6 {
		t.Fatalf("revision after retained task hard-delete = %d, want 6", otherRevision)
	}
	// The task_agent_work foreign key cascades when a task is hard-deleted.
	// BEFORE DELETE must resolve OLD.task_id while the parent task still exists.
	cascadeSnapshot := `INSERT INTO task_agent_work(task_id, operation_id, actor_id, state, phase, summary, next_action, checkpoint_refs, started_at, updated_at) VALUES ('bug-1', 'retained-cascade', 'agent', 'working', '', 'Retained cascade pulse', '', '[]', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`
	if _, err := database.ExecContext(ctx, cascadeSnapshot); err != nil {
		t.Fatalf("retained cascade snapshot insert: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT task_collection_revision FROM projects WHERE id='project'`).Scan(&revision); err != nil {
		t.Fatalf("read revision after cascade snapshot insert: %v", err)
	}
	if revision != 6 {
		t.Fatalf("revision after cascade snapshot insert = %d, want 6", revision)
	}
	if _, err := database.ExecContext(ctx, `DELETE FROM tasks WHERE id='bug-1'`); err != nil {
		t.Fatalf("hard-delete task with retained snapshot: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT task_collection_revision FROM projects WHERE id='project'`).Scan(&revision); err != nil {
		t.Fatalf("read revision after cascade delete: %v", err)
	}
	if revision != 7 {
		t.Fatalf("revision after cascade delete = %d, want 7", revision)
	}
	if err := Migrate(ctx, database); err != nil {
		t.Fatalf("rerun migration: %v", err)
	}
	var applied int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version=20`).Scan(&applied); err != nil {
		t.Fatalf("count migration 020 rows: %v", err)
	}
	if applied != 1 {
		t.Fatalf("migration 020 applied rows after rerun = %d, want 1", applied)
	}
}

func TestTaskCollectionRevisionExhaustionRollsBackTaskMutation(t *testing.T) {
	ctx := context.Background()
	migrations, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) < 20 {
		t.Skip("migration 020 is not present")
	}
	database := newRawDatabase(t)
	applyMigrationPrefix(t, ctx, database, migrations, 19)
	populateProductionFixture(t, ctx, database, 19)
	if err := Migrate(ctx, database); err != nil {
		t.Fatalf("migrate populated pre-020 database: %v", err)
	}
	const maxInt64 int64 = 9223372036854775807
	if _, err := database.ExecContext(ctx, `UPDATE projects SET task_collection_revision=? WHERE id='project'`, maxInt64); err != nil {
		t.Fatalf("set maximum task collection revision: %v", err)
	}
	var beforeTitle string
	if err := database.QueryRowContext(ctx, `SELECT title FROM tasks WHERE id='task-1'`).Scan(&beforeTitle); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE tasks SET title='Overflow must roll back', updated_at='2026-01-02T00:00:00Z' WHERE id='task-1'`); err == nil {
		t.Fatal("task mutation unexpectedly succeeded at maximum task collection revision")
	}
	var afterTitle string
	var revision int64
	if err := database.QueryRowContext(ctx, `SELECT title, task_collection_revision FROM tasks JOIN projects ON projects.id=tasks.project_id WHERE tasks.id='task-1'`).Scan(&afterTitle, &revision); err != nil {
		t.Fatal(err)
	}
	if afterTitle != beforeTitle || revision != maxInt64 {
		t.Fatalf("overflow mutation changed data: title=%q revision=%d, want title=%q revision=%d", afterTitle, revision, beforeTitle, maxInt64)
	}
}
