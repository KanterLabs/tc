package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	_ "modernc.org/sqlite"
)

type contextExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func TestTaskDependencyMigrationPreservesPopulatedDataAndConstraints(t *testing.T) {
	ctx := context.Background()
	migrations, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) < 12 {
		t.Skip("migration 012 is not present")
	}

	database := newRawDatabase(t)
	applyMigrationPrefix(t, ctx, database, migrations, 11)
	populateProductionFixture(t, ctx, database, 11)
	before := readStableFixture(t, ctx, database, 11)
	if err := Migrate(ctx, database); err != nil {
		t.Fatalf("migrate populated pre-012 database: %v", err)
	}
	after := readStableFixture(t, ctx, database, 12)
	if !reflect.DeepEqual(before.stable, after.stable) {
		t.Fatalf("stable fixture changed during migration:\nbefore=%#v\nafter=%#v", before.stable, after.stable)
	}
	for _, table := range []string{"actors", "projects", "columns", "tasks", "task_links", "comments", "events", "idempotency_keys"} {
		if got := after.counts[table]; got != before.counts[table] {
			t.Fatalf("%s count changed during migration: before=%d after=%d", table, before.counts[table], got)
		}
	}
	var dependencyCount int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_dependencies`).Scan(&dependencyCount); err != nil {
		t.Fatal(err)
	}
	if dependencyCount != 0 {
		t.Fatalf("new dependency rows after additive migration = %d, want 0", dependencyCount)
	}
	var reverseIndexCount int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='task_dependencies_prerequisite_idx'`).Scan(&reverseIndexCount); err != nil {
		t.Fatal(err)
	}
	if reverseIndexCount != 1 {
		t.Fatalf("reverse dependency index count = %d, want 1", reverseIndexCount)
	}
	for _, trigger := range []string{
		"task_dependencies_validate_insert",
		"task_dependencies_guard_task_update",
		"task_dependencies_guard_column_update",
	} {
		var triggerCount int
		if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?`, trigger).Scan(&triggerCount); err != nil {
			t.Fatal(err)
		}
		if triggerCount != 1 {
			t.Fatalf("dependency trigger %q count = %d, want 1", trigger, triggerCount)
		}
	}

	type foreignKey struct {
		table    string
		from     string
		to       string
		onUpdate string
		onDelete string
	}
	foreignKeys := make([]foreignKey, 0, 3)
	rows, err := database.QueryContext(ctx, `PRAGMA foreign_key_list('task_dependencies')`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, sequence int
		var key foreignKey
		var match string
		if err := rows.Scan(&id, &sequence, &key.table, &key.from, &key.to, &key.onUpdate, &key.onDelete, &match); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		foreignKeys = append(foreignKeys, key)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if len(foreignKeys) != 3 {
		t.Fatalf("task dependency foreign keys = %#v, want 3 entries", foreignKeys)
	}
	wantForeignKeys := []foreignKey{
		{table: "tasks", from: "task_id", to: "id", onUpdate: "NO ACTION", onDelete: "CASCADE"},
		{table: "tasks", from: "prerequisite_task_id", to: "id", onUpdate: "NO ACTION", onDelete: "CASCADE"},
		{table: "actors", from: "created_by", to: "id", onUpdate: "NO ACTION", onDelete: "SET NULL"},
	}
	for _, want := range wantForeignKeys {
		found := false
		for _, got := range foreignKeys {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("foreign key %#v missing from %#v", want, foreignKeys)
		}
	}

	// task-1 starts with a fixture claim; release it before creating a valid
	// dependency so insertion exercises the graph guard rather than claim
	// policy.  No pre-existing data is changed by migration itself.
	if _, err := database.ExecContext(ctx, `UPDATE tasks SET claimed_by=NULL, claim_expires_at=NULL WHERE id='task-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO task_dependencies(task_id, prerequisite_task_id, created_by, created_at) VALUES ('task-1', 'bug-1', 'owner', '2026-01-02T00:00:00Z')`); err != nil {
		t.Fatalf("insert valid dependency: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO projects(id, key, slug, name, created_at, updated_at) VALUES ('cross-project', 'CROSS', 'cross-project', 'Cross project', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO columns(id, project_id, name, semantic_state, position, created_at, updated_at) VALUES ('cross-todo', 'cross-project', 'Todo', 'backlog', 1, '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO tasks(id, project_id, number, column_id, title, created_at, updated_at) VALUES ('cross-task', 'cross-project', 1, 'cross-todo', 'Cross project task', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO tasks(id, project_id, number, column_id, title, created_at, updated_at) VALUES ('deleted-task', 'project', 50, 'todo', 'Deleted task', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z'), ('fk-task-a', 'project', 51, 'todo', 'Foreign key source', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z'), ('fk-task-b', 'project', 52, 'todo', 'Foreign key prerequisite', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE tasks SET deleted_at='2026-01-02T00:00:00Z' WHERE id='deleted-task'`); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_dependencies`).Scan(&dependencyCount); err != nil {
		t.Fatal(err)
	}
	if dependencyCount != 1 {
		t.Fatalf("dependency row count = %d, want 1", dependencyCount)
	}

	// Each failure is a single statement; the row count assertion after every
	// rejection proves that the trigger cannot leave a partial edge behind.
	invalidInserts := []struct {
		name  string
		query string
		args  []any
	}{
		{
			name:  "self reference",
			query: `INSERT INTO task_dependencies(task_id, prerequisite_task_id, created_by, created_at) VALUES ('fk-task-a', 'fk-task-a', 'owner', '2026-01-02T00:00:00Z')`,
		},
		{
			name:  "cross project",
			query: `INSERT INTO task_dependencies(task_id, prerequisite_task_id, created_by, created_at) VALUES ('task-1', 'cross-task', 'owner', '2026-01-02T00:00:00Z')`,
		},
		{
			name:  "deleted task",
			query: `INSERT INTO task_dependencies(task_id, prerequisite_task_id, created_by, created_at) VALUES ('task-1', 'deleted-task', 'owner', '2026-01-02T00:00:00Z')`,
		},
		{
			name:  "actor foreign key",
			query: `INSERT INTO task_dependencies(task_id, prerequisite_task_id, created_by, created_at) VALUES ('fk-task-a', 'fk-task-b', 'missing-actor', '2026-01-02T00:00:00Z')`,
		},
	}
	for _, test := range invalidInserts {
		t.Run(test.name, func(t *testing.T) {
			if _, err := database.ExecContext(ctx, test.query, test.args...); err == nil {
				t.Fatal("invalid dependency insert unexpectedly succeeded")
			}
			if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_dependencies`).Scan(&dependencyCount); err != nil {
				t.Fatal(err)
			}
			if dependencyCount != 1 {
				t.Fatalf("dependency rows after rejected insert = %d, want 1", dependencyCount)
			}
		})
	}
	if err := CheckIntegrity(ctx, database); err != nil {
		t.Fatalf("populated dependency schema integrity: %v", err)
	}
}

func TestTaskDependencyInsertionLimitsAndCycles(t *testing.T) {
	ctx := context.Background()
	database := newRawDatabase(t)
	migrations, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) < 12 {
		t.Skip("migration 012 is not present")
	}
	applyMigrationPrefix(t, ctx, database, migrations, len(migrations))
	populateProductionFixture(t, ctx, database, len(migrations))
	if _, err := database.ExecContext(ctx, `UPDATE tasks SET claimed_by=NULL, claim_expires_at=NULL WHERE id='task-1'`); err != nil {
		t.Fatal(err)
	}
	insertTask := func(exec contextExecer, id string, number int) error {
		_, err := exec.ExecContext(ctx, `INSERT INTO tasks(id, project_id, number, column_id, kind, title, description, priority, position, version, created_at, updated_at) VALUES (?, 'project', ?, 'todo', 'task', ?, '', 'normal', ?, 1, '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z')`, id, number, id, number)
		return err
	}
	insertDependency := func(exec contextExecer, taskID, prerequisiteID string) error {
		_, err := exec.ExecContext(ctx, `INSERT INTO task_dependencies(task_id, prerequisite_task_id, created_by, created_at) VALUES (?, ?, 'owner', '2026-01-02T00:00:00Z')`, taskID, prerequisiteID)
		return err
	}

	firstLimitTx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertTask(firstLimitTx, "limit-source", 1000); err != nil {
		firstLimitTx.Rollback()
		t.Fatalf("insert limit source: %v", err)
	}
	for index := 0; index < 201; index++ {
		prerequisiteID := fmt.Sprintf("limit-prerequisite-%03d", index)
		if err := insertTask(firstLimitTx, prerequisiteID, 1100+index); err != nil {
			firstLimitTx.Rollback()
			t.Fatalf("insert %s: %v", prerequisiteID, err)
		}
		if index < 200 {
			if err := insertDependency(firstLimitTx, "limit-source", prerequisiteID); err != nil {
				firstLimitTx.Rollback()
				t.Fatalf("insert prerequisite %d: %v", index, err)
			}
		} else if err := insertDependency(firstLimitTx, "limit-source", prerequisiteID); err == nil {
			firstLimitTx.Rollback()
			t.Fatal("201st prerequisite unexpectedly succeeded")
		}
	}
	if err := firstLimitTx.Commit(); err != nil {
		t.Fatalf("commit prerequisite limit fixture: %v", err)
	}
	var count int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_dependencies WHERE task_id='limit-source'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 200 {
		t.Fatalf("prerequisites after limit rejection = %d, want 200", count)
	}

	secondLimitTx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertTask(secondLimitTx, "limit-prerequisite", 1400); err != nil {
		secondLimitTx.Rollback()
		t.Fatalf("insert limit prerequisite: %v", err)
	}
	for index := 0; index < 201; index++ {
		dependentID := fmt.Sprintf("limit-dependent-%03d", index)
		if err := insertTask(secondLimitTx, dependentID, 1500+index); err != nil {
			secondLimitTx.Rollback()
			t.Fatalf("insert %s: %v", dependentID, err)
		}
		if index < 200 {
			if err := insertDependency(secondLimitTx, dependentID, "limit-prerequisite"); err != nil {
				secondLimitTx.Rollback()
				t.Fatalf("insert dependent %d: %v", index, err)
			}
		} else if err := insertDependency(secondLimitTx, dependentID, "limit-prerequisite"); err == nil {
			secondLimitTx.Rollback()
			t.Fatal("201st dependent unexpectedly succeeded")
		}
	}
	if err := secondLimitTx.Commit(); err != nil {
		t.Fatalf("commit dependent limit fixture: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_dependencies WHERE prerequisite_task_id='limit-prerequisite'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 200 {
		t.Fatalf("dependents after limit rejection = %d, want 200", count)
	}

	cycleTx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for index, id := range []string{"cycle-a", "cycle-b", "cycle-c"} {
		if err := insertTask(cycleTx, id, 1800+index); err != nil {
			cycleTx.Rollback()
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	if err := insertDependency(cycleTx, "cycle-a", "cycle-b"); err != nil {
		cycleTx.Rollback()
		t.Fatal(err)
	}
	if err := insertDependency(cycleTx, "cycle-b", "cycle-c"); err != nil {
		cycleTx.Rollback()
		t.Fatal(err)
	}
	if err := insertDependency(cycleTx, "cycle-c", "cycle-a"); err == nil {
		cycleTx.Rollback()
		t.Fatal("transitive dependency cycle unexpectedly succeeded")
	}
	if err := cycleTx.Commit(); err != nil {
		t.Fatalf("commit cycle fixture: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_dependencies WHERE task_id LIKE 'cycle-%'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("cycle rows after rejected edge = %d, want 2", count)
	}
	if err := CheckIntegrity(ctx, database); err != nil {
		t.Fatalf("dependency limit/cycle integrity: %v", err)
	}
}

func TestRetainedPre012BinaryCannotBypassDependencyGuards(t *testing.T) {
	ctx := context.Background()
	migrations, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) < 12 {
		t.Skip("migration 012 is not present")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "roadmap.db")
	database, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	applyMigrationPrefix(t, ctx, database, migrations, 11)
	populateProductionFixture(t, ctx, database, 11)
	if err := Migrate(ctx, database); err != nil {
		t.Fatalf("upgrade pre-012 database: %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE tasks SET claimed_by=NULL, claim_expires_at=NULL WHERE id='task-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO columns(id, project_id, name, semantic_state, position, created_at, updated_at) VALUES ('done', 'project', 'Done', 'completed', 3, '2026-01-02T00:00:02Z', '2026-01-02T00:00:02Z')`); err != nil {
		t.Fatal(err)
	}
	insertTask := func(id string, number int) {
		t.Helper()
		if _, err := database.ExecContext(ctx, `INSERT INTO tasks(id, project_id, number, column_id, kind, title, description, priority, position, version, created_at, updated_at) VALUES (?, 'project', ?, 'todo', 'task', ?, '', 'normal', ?, 1, '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z')`, id, number, id, number); err != nil {
			t.Fatalf("insert retained fixture task %s: %v", id, err)
		}
	}
	insertTask("bulk-dependent", 100)
	insertTask("bulk-prerequisite", 101)
	if _, err := database.ExecContext(ctx, `INSERT INTO task_dependencies(task_id, prerequisite_task_id, created_by, created_at) VALUES ('task-1', 'bug-1', 'owner', '2026-01-02T00:00:00Z'), ('bulk-dependent', 'bulk-prerequisite', 'owner', '2026-01-02T00:00:00Z')`); err != nil {
		t.Fatalf("insert retained dependency fixtures: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	workerPath := buildRetainedDependencyBinary(t)
	worker := exec.Command(workerPath, path)
	if output, err := worker.CombinedOutput(); err != nil {
		t.Fatalf("retained dependency binary: %v\n%s", err, output)
	}

	retained, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer retained.Close()
	var title, columnID string
	var version int
	if err := retained.QueryRowContext(ctx, `SELECT title, column_id, version FROM tasks WHERE id='task-1'`).Scan(&title, &columnID, &version); err != nil {
		t.Fatal(err)
	}
	if title != "Retained dependency edit" || columnID != "doing" || version != 5 {
		t.Fatalf("retained task state = %q/%q/%d, want edited active version 5", title, columnID, version)
	}
	var bugColumn, bugCompleted sql.NullString
	if err := retained.QueryRowContext(ctx, `SELECT column_id, completed_at FROM tasks WHERE id='bug-1'`).Scan(&bugColumn, &bugCompleted); err != nil {
		t.Fatal(err)
	}
	if bugColumn.String != "done" || !bugCompleted.Valid {
		t.Fatalf("retained prerequisite state = %q/%q, want completed in done", bugColumn.String, bugCompleted.String)
	}
	var bulkState string
	if err := retained.QueryRowContext(ctx, `SELECT semantic_state FROM columns WHERE id='todo'`).Scan(&bulkState); err != nil {
		t.Fatal(err)
	}
	if bulkState != "backlog" {
		t.Fatalf("blocked bulk completion changed todo semantic state to %q", bulkState)
	}
	var bulkCompleted int
	if err := retained.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE id IN ('bulk-dependent', 'bulk-prerequisite') AND completed_at IS NOT NULL`).Scan(&bulkCompleted); err != nil {
		t.Fatal(err)
	}
	if bulkCompleted != 0 {
		t.Fatalf("blocked bulk completion wrote %d completion timestamps", bulkCompleted)
	}
	var dependencyCount int
	if err := retained.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_dependencies`).Scan(&dependencyCount); err != nil {
		t.Fatal(err)
	}
	if dependencyCount != 2 {
		t.Fatalf("retained binary changed dependency row count to %d, want 2", dependencyCount)
	}
	if err := CheckIntegrity(ctx, retained); err != nil {
		t.Fatalf("retained dependency schema integrity: %v", err)
	}
}

const retainedDependencyBinarySource = `package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

func main() {
	if len(os.Args) != 2 {
		fail("usage: retained-dependency DATABASE")
	}
	database, err := sql.Open("sqlite", "file:"+os.Args[1]+"?_pragma=foreign_keys(1)")
	if err != nil {
		fail("open database: %v", err)
	}
	defer database.Close()

	// A pre-012 edit that does not touch lifecycle columns remains valid.
	if _, err := database.Exec("UPDATE tasks SET title='Retained dependency edit', version=version+1, updated_at='2026-01-03T00:00:00Z' WHERE id='task-1' AND version=3"); err != nil {
		fail("unrelated retained edit: %v", err)
	}

	// Each guarded statement changes multiple columns so a successful partial
	// write would be visible in the final assertions in the Go test.
	reject(database, "claim", "UPDATE tasks SET claimed_by='agent', claim_expires_at='2999-01-01T00:00:00Z', version=version+1, updated_at='2026-01-03T00:00:01Z' WHERE id='task-1' AND version=4")
	reject(database, "start", "UPDATE tasks SET column_id='doing', version=version+1, updated_at='2026-01-03T00:00:02Z' WHERE id='task-1' AND version=4")
	reject(database, "complete", "UPDATE tasks SET column_id='done', completed_at='2026-01-03T00:00:03Z', version=version+1, updated_at='2026-01-03T00:00:03Z' WHERE id='task-1' AND version=4")
	reject(database, "delete prerequisite", "UPDATE tasks SET deleted_at='2026-01-03T00:00:04Z', version=version+1, updated_at='2026-01-03T00:00:04Z' WHERE id='bug-1' AND version=2")

	// Completing the prerequisite makes the normal retained task transition
	// valid.  It then gives the reopen and bulk-column guards a live dependent
	// that has already started work.
	if _, err := database.Exec("UPDATE tasks SET column_id='done', completed_at='2026-01-03T00:00:05Z', version=version+1, updated_at='2026-01-03T00:00:05Z' WHERE id='bug-1' AND version=2"); err != nil {
		fail("complete prerequisite: %v", err)
	}
	if _, err := database.Exec("UPDATE tasks SET column_id='doing', claimed_by='agent', claim_expires_at='2999-01-01T00:00:00Z', version=version+1, updated_at='2026-01-03T00:00:06Z' WHERE id='task-1' AND version=4"); err != nil {
		fail("start retained dependent: %v", err)
	}
	reject(database, "reopen prerequisite", "UPDATE tasks SET column_id='todo', completed_at=NULL, version=version+1, updated_at='2026-01-03T00:00:07Z' WHERE id='bug-1' AND version=3")
	reject(database, "bulk reopen", "UPDATE columns SET semantic_state='backlog', updated_at='2026-01-03T00:00:08Z' WHERE id='done' AND semantic_state='completed'")
	reject(database, "bulk start", "UPDATE columns SET semantic_state='active', updated_at='2026-01-03T00:00:09Z' WHERE id='todo' AND semantic_state='backlog'")
	reject(database, "bulk completion", "UPDATE columns SET semantic_state='completed', updated_at='2026-01-03T00:00:10Z' WHERE id='todo' AND semantic_state='backlog'")
}

func reject(database *sql.DB, name, statement string) {
	if _, err := database.Exec(statement); err == nil {
		fail("%s unexpectedly succeeded", name)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\\n", args...)
	os.Exit(1)
}
`

func buildRetainedDependencyBinary(t *testing.T) string {
	t.Helper()
	moduleRoot := findModuleRoot(t)
	sourceDir := t.TempDir()
	sourcePath := filepath.Join(sourceDir, "main.go")
	binaryPath := filepath.Join(sourceDir, "roadmap-retained-dependency")
	if err := os.WriteFile(sourcePath, []byte(retainedDependencyBinarySource), 0o600); err != nil {
		t.Fatalf("write retained dependency source: %v", err)
	}
	build := exec.Command("go", "build", "-mod=readonly", "-o", binaryPath, sourcePath)
	build.Dir = moduleRoot
	build.Env = os.Environ()
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build retained dependency binary: %v\n%s", err, output)
	}
	if info, err := os.Stat(binaryPath); err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		t.Fatalf("retained dependency binary is not executable: %v", err)
	}
	return binaryPath
}
