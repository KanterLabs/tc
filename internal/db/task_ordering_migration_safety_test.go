package db

import (
	"context"
	"testing"
)

func TestTaskOrderingMigrationPreservesPopulatedPositionsAndRetainedQueries(t *testing.T) {
	ctx := context.Background()
	migrations, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) < 19 {
		t.Skip("migration 019 is not present")
	}
	database := newRawDatabase(t)
	applyMigrationPrefix(t, ctx, database, migrations, 18)
	populateProductionFixture(t, ctx, database, 12)
	var beforePositions string
	if err := database.QueryRowContext(ctx, `SELECT group_concat(id || ':' || position, '|') FROM (SELECT id, position FROM tasks ORDER BY number, id)`).Scan(&beforePositions); err != nil {
		t.Fatal(err)
	}
	var beforeColumns int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM columns`).Scan(&beforeColumns); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, database); err != nil {
		t.Fatalf("migrate populated pre-019 database: %v", err)
	}
	var afterPositions string
	if err := database.QueryRowContext(ctx, `SELECT group_concat(id || ':' || position, '|') FROM (SELECT id, position FROM tasks ORDER BY number, id)`).Scan(&afterPositions); err != nil {
		t.Fatal(err)
	}
	if afterPositions != beforePositions {
		t.Fatalf("task positions changed during additive migration: before=%q after=%q", beforePositions, afterPositions)
	}
	var afterColumns, defaultRevisionCount int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM columns`).Scan(&afterColumns); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM columns WHERE ordering_version=1`).Scan(&defaultRevisionCount); err != nil {
		t.Fatal(err)
	}
	if afterColumns != beforeColumns || defaultRevisionCount != beforeColumns {
		t.Fatalf("column migration state = count %d/%d, default revisions %d/%d", afterColumns, beforeColumns, defaultRevisionCount, beforeColumns)
	}
	var indexCount int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='columns_ordering_revision_idx'`).Scan(&indexCount); err != nil {
		t.Fatal(err)
	}
	if indexCount != 1 {
		t.Fatalf("ordering revision index count = %d, want 1", indexCount)
	}
	for _, trigger := range []string{"task_ordering_revision_on_insert", "task_ordering_revision_on_update", "task_ordering_revision_on_restore"} {
		var triggerCount int
		if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?`, trigger).Scan(&triggerCount); err != nil {
			t.Fatal(err)
		}
		if triggerCount != 1 {
			t.Fatalf("ordering trigger %q count = %d, want 1", trigger, triggerCount)
		}
	}

	// A retained binary still uses the pre-019 column projection and task
	// write shape. The additive field must not make those queries fail.
	var id, projectID, name, semanticState, createdAt, updatedAt string
	var position int
	if err := database.QueryRowContext(ctx, `SELECT id, project_id, name, semantic_state, position, created_at, updated_at FROM columns WHERE id='todo'`).Scan(&id, &projectID, &name, &semanticState, &position, &createdAt, &updatedAt); err != nil {
		t.Fatalf("retained column query: %v", err)
	}
	if id != "todo" || projectID != "project" || semanticState != "backlog" {
		t.Fatalf("retained column snapshot = %s/%s/%s", id, projectID, semanticState)
	}
	if _, err := database.ExecContext(ctx, `UPDATE tasks SET position=position+0.25, version=version+1, updated_at='2026-01-03T00:00:00Z' WHERE id='task-1' AND version=3`); err != nil {
		t.Fatalf("retained task write: %v", err)
	}
	var taskPosition float64
	if err := database.QueryRowContext(ctx, `SELECT position FROM tasks WHERE id='task-1'`).Scan(&taskPosition); err != nil {
		t.Fatal(err)
	}
	if taskPosition != 1.75 {
		t.Fatalf("retained task position = %v, want 1.75", taskPosition)
	}
	if err := Migrate(ctx, database); err != nil {
		t.Fatalf("rerun migration: %v", err)
	}
	var applied int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version=19`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("migration 019 applied rows after rerun = %d, want 1", applied)
	}
}
