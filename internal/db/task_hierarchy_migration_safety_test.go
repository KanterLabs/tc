package db

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	_ "modernc.org/sqlite"
)

// TC-114 adds one nullable self-reference to tasks.  This test keeps a
// production-shaped pre-017 database populated while upgrading, then proves
// that both retained (parent-unaware) writes and new hierarchy guards work on
// the migrated file.  A retained binary cannot mention the new column, so an
// omitted-column insert/update is the rollback-compatibility contract.
func TestTaskHierarchyMigrationPreservesPopulatedDataAndRetainedWrites(t *testing.T) {
	ctx := context.Background()
	migrations, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) < 17 {
		t.Skip("migration 017 is not present")
	}

	database := newRawDatabase(t)
	applyMigrationPrefix(t, ctx, database, migrations, 16)
	populateProductionFixture(t, ctx, database, 16)
	before := readStableFixture(t, ctx, database, 16)
	if err := Migrate(ctx, database); err != nil {
		t.Fatalf("migrate populated pre-017 database: %v", err)
	}
	after := readStableFixture(t, ctx, database, 17)
	if !reflect.DeepEqual(before.stable, after.stable) {
		t.Fatalf("stable fixture changed during hierarchy migration:\nbefore=%#v\nafter=%#v", before.stable, after.stable)
	}
	for _, table := range []string{"actors", "projects", "columns", "tasks", "comments", "events", "idempotency_keys"} {
		if got := after.counts[table]; got != before.counts[table] {
			t.Fatalf("%s count changed during hierarchy migration: before=%d after=%d", table, before.counts[table], got)
		}
	}

	var parentColumnCount int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('tasks') WHERE name='parent_task_id'`).Scan(&parentColumnCount); err != nil {
		t.Fatal(err)
	}
	if parentColumnCount != 1 {
		t.Fatalf("parent_task_id column count = %d, want 1", parentColumnCount)
	}
	var linkedRows int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE parent_task_id IS NOT NULL`).Scan(&linkedRows); err != nil {
		t.Fatal(err)
	}
	if linkedRows != 0 {
		t.Fatalf("legacy rows gained hierarchy links during migration: %d", linkedRows)
	}
	for _, index := range []string{"tasks_parent_idx"} {
		var count int
		if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("hierarchy index %q count = %d, want 1", index, count)
		}
	}
	for _, trigger := range []string{
		"task_hierarchy_validate_insert",
		"task_hierarchy_validate_parent_update",
		"task_hierarchy_guard_delete",
		"task_hierarchy_guard_project_update",
	} {
		var count int
		if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?`, trigger).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("hierarchy trigger %q count = %d, want 1", trigger, count)
		}
	}

	// This is the shape used by a pre-017 binary: it inserts and updates only
	// columns that existed before the additive migration.  The new nullable
	// column must remain untouched, not be reset by an old write.
	if _, err := database.ExecContext(ctx, `INSERT INTO tasks(id, project_id, number, column_id, kind, title, description, priority, position, version, created_at, updated_at) VALUES ('retained-task', 'project', 100, 'todo', 'task', 'Retained task', '', 'normal', 100, 1, '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z')`); err != nil {
		t.Fatalf("retained pre-017 insert: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO tasks(id, project_id, number, column_id, kind, title, description, priority, position, version, created_at, updated_at) VALUES ('hier-parent', 'project', 101, 'todo', 'task', 'Hierarchy parent', '', 'normal', 101, 1, '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z')`); err != nil {
		t.Fatalf("insert hierarchy parent: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO tasks(id, project_id, number, column_id, kind, title, description, priority, position, version, created_at, updated_at, parent_task_id) VALUES ('hier-child', 'project', 102, 'todo', 'task', 'Hierarchy child', '', 'normal', 102, 1, '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z', 'hier-parent')`); err != nil {
		t.Fatalf("insert hierarchy child: %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE tasks SET title='Retained edit', version=version+1, updated_at='2026-01-02T00:00:01Z' WHERE id='hier-child'`); err != nil {
		t.Fatalf("retained pre-017 update: %v", err)
	}
	var retainedParent sql.NullString
	var retainedTitle string
	if err := database.QueryRowContext(ctx, `SELECT title, parent_task_id FROM tasks WHERE id='hier-child'`).Scan(&retainedTitle, &retainedParent); err != nil {
		t.Fatal(err)
	}
	if retainedTitle != "Retained edit" || !retainedParent.Valid || retainedParent.String != "hier-parent" {
		t.Fatalf("retained write changed hierarchy state: title=%q parent=%q valid=%v", retainedTitle, retainedParent.String, retainedParent.Valid)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO tasks(id, project_id, number, column_id, kind, title, description, priority, position, version, created_at, updated_at, parent_task_id) VALUES ('direct-self', 'project', 103, 'todo', 'task', 'Direct self', '', 'normal', 103, 1, '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z', 'direct-self')`); err == nil {
		t.Fatal("direct SQL self-parent unexpectedly succeeded")
	}
	if _, err := database.ExecContext(ctx, `UPDATE tasks SET parent_task_id='hier-child' WHERE id='hier-parent'`); err == nil {
		t.Fatal("direct SQL cycle unexpectedly succeeded")
	}

	// Parent deletion is rejected before the soft-delete write; the child edge
	// and both task rows therefore remain intact after the failed statement.
	if _, err := database.ExecContext(ctx, `UPDATE tasks SET deleted_at='2026-01-02T00:00:02Z' WHERE id='hier-parent'`); err == nil {
		t.Fatal("parent with a live child was soft-deleted")
	}
	var parentDeleted sql.NullString
	if err := database.QueryRowContext(ctx, `SELECT deleted_at FROM tasks WHERE id='hier-parent'`).Scan(&parentDeleted); err != nil {
		t.Fatal(err)
	}
	if parentDeleted.Valid {
		t.Fatalf("rejected parent deletion left deleted_at=%q", parentDeleted.String)
	}
	if err := CheckIntegrity(ctx, database); err != nil {
		t.Fatalf("populated hierarchy schema integrity: %v", err)
	}
}
