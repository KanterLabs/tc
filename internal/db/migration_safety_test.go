package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestEmbeddedSchemaIdentityIsStable(t *testing.T) {
	version, digest, err := EmbeddedSchema()
	if err != nil {
		t.Fatalf("embedded schema: %v", err)
	}
	if version < 10 {
		t.Fatalf("latest schema version = %d, want at least 10", version)
	}
	if len(digest) != 64 {
		t.Fatalf("migration digest length = %d, want 64", len(digest))
	}
	versionAgain, digestAgain, err := EmbeddedSchema()
	if err != nil {
		t.Fatalf("embedded schema second read: %v", err)
	}
	if versionAgain != version || digestAgain != digest {
		t.Fatalf("embedded schema changed between reads: (%d, %s), (%d, %s)", version, digest, versionAgain, digestAgain)
	}
	if got, err := LatestEmbeddedSchemaVersion(); err != nil || got != version {
		t.Fatalf("latest embedded version = %d, %v; want %d", got, err, version)
	}
	if got, err := EmbeddedMigrationDigest(); err != nil || got != digest {
		t.Fatalf("embedded migration digest = %s, %v; want %s", got, err, digest)
	}
}

func TestMigrationVersionValidation(t *testing.T) {
	for _, name := range []string{"migration.sql", "0_bad.sql", "x_bad.sql", "01.sql"} {
		if _, err := migrationVersion(name); err == nil {
			t.Errorf("migrationVersion(%q) unexpectedly succeeded", name)
		}
	}
	for _, test := range []struct {
		name string
		want int
	}{
		{name: "001_init.sql", want: 1},
		{name: "009_additive_change.sql", want: 9},
		{name: "010_board_audits.sql", want: 10},
	} {
		if got, err := migrationVersion(test.name); err != nil || got != test.want {
			t.Errorf("migrationVersion(%q) = %d, %v; want %d", test.name, got, err, test.want)
		}
	}
}

func TestProductionShapedPrefixesMigrateWithoutChangingStableData(t *testing.T) {
	ctx := context.Background()
	migrations, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	latest, _, err := EmbeddedSchema()
	if err != nil {
		t.Fatal(err)
	}
	for prefix := 1; prefix <= latest; prefix++ {
		prefix := prefix
		t.Run(fmt.Sprintf("prefix_%03d", prefix), func(t *testing.T) {
			database := newRawDatabase(t)
			applyMigrationPrefix(t, ctx, database, migrations, prefix)
			populateProductionFixture(t, ctx, database, prefix)
			before := readStableFixture(t, ctx, database, prefix)
			beforeAudits := readAuditFixture(t, ctx, database)
			if err := Migrate(ctx, database); err != nil {
				database.Close()
				t.Fatalf("migrate prefix %d: %v", prefix, err)
			}
			after := readStableFixture(t, ctx, database, latest)
			if !reflect.DeepEqual(before.stable, after.stable) {
				t.Fatalf("stable fixture changed during migration:\nbefore=%#v\nafter=%#v", before.stable, after.stable)
			}
			if got := after.counts["actors"]; got != 2 {
				t.Fatalf("actor count = %d, want 2", got)
			}
			if got := after.counts["tasks"]; got != 2 {
				t.Fatalf("task count = %d, want 2", got)
			}
			if got := after.counts["comments"]; got != 1 {
				t.Fatalf("comment count = %d, want 1", got)
			}
			if got := after.counts["events"]; got != 1 {
				t.Fatalf("event count = %d, want 1", got)
			}
			afterAudits := readAuditFixture(t, ctx, database)
			if afterAudits.runs == "<absent>" || afterAudits.findings == "<absent>" {
				t.Fatalf("migration %d did not create audit tables: %#v", prefix, afterAudits)
			}
			if prefix >= 10 && !reflect.DeepEqual(beforeAudits, afterAudits) {
				t.Fatalf("audit fixture changed during migration %d:\nbefore=%#v\nafter=%#v", prefix, beforeAudits, afterAudits)
			}
			inspection, err := InspectSchema(ctx, database)
			if err != nil {
				t.Fatalf("inspect migrated schema: %v", err)
			}
			if inspection.SchemaVersion != latest || len(inspection.PendingVersions) != 0 || len(inspection.UnknownVersions) != 0 {
				t.Fatalf("migrated schema inspection = %#v", inspection)
			}
			if err := CheckIntegrity(ctx, database); err != nil {
				t.Fatalf("migrated schema integrity: %v", err)
			}
			assertMigratedAdditiveValues(t, ctx, database, prefix)
			appliedBefore := after.counts["schema_migrations"]
			if err := Migrate(ctx, database); err != nil {
				t.Fatalf("rerun migration: %v", err)
			}
			afterRerun := readStableFixture(t, ctx, database, latest)
			afterRerunAudits := readAuditFixture(t, ctx, database)
			if afterRerun.counts["schema_migrations"] != appliedBefore || !reflect.DeepEqual(after.stable, afterRerun.stable) || !reflect.DeepEqual(afterAudits, afterRerunAudits) {
				t.Fatalf("rerun changed migration/data state: before=%#v after=%#v", after, afterRerun)
			}
		})
	}
}

func TestInspectSchemaReportsLatestPendingAndUnknownVersions(t *testing.T) {
	ctx := context.Background()
	latest, _, err := EmbeddedSchema()
	if err != nil {
		t.Fatal(err)
	}
	if latest < 10 {
		t.Fatalf("latest schema version = %d, want schema 10", latest)
	}

	t.Run("latest", func(t *testing.T) {
		database := newRawDatabase(t)
		if err := Migrate(ctx, database); err != nil {
			t.Fatalf("migrate latest schema: %v", err)
		}
		inspection, err := InspectSchema(ctx, database)
		if err != nil {
			t.Fatal(err)
		}
		if inspection.SchemaVersion != latest || !reflect.DeepEqual(inspection.AppliedVersions, versionsThrough(latest)) || len(inspection.PendingVersions) != 0 || len(inspection.UnknownVersions) != 0 {
			t.Fatalf("latest schema inspection = %#v", inspection)
		}
	})

	t.Run("pending", func(t *testing.T) {
		database := newRawDatabase(t)
		migrations, err := embeddedMigrations()
		if err != nil {
			t.Fatal(err)
		}
		applyMigrationPrefix(t, ctx, database, migrations, latest-1)
		inspection, err := InspectSchema(ctx, database)
		if err != nil {
			t.Fatal(err)
		}
		if inspection.SchemaVersion != latest-1 || !reflect.DeepEqual(inspection.AppliedVersions, versionsThrough(latest-1)) || !reflect.DeepEqual(inspection.PendingVersions, []int{latest}) || len(inspection.UnknownVersions) != 0 {
			t.Fatalf("pending schema inspection = %#v", inspection)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		database := newRawDatabase(t)
		migrations, err := embeddedMigrations()
		if err != nil {
			t.Fatal(err)
		}
		applyMigrationPrefix(t, ctx, database, migrations, latest)
		const unknownVersion = 999
		if _, err := database.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, 'future')`, unknownVersion); err != nil {
			t.Fatal(err)
		}
		inspection, err := InspectSchema(ctx, database)
		if err != nil {
			t.Fatal(err)
		}
		if inspection.SchemaVersion != unknownVersion || !reflect.DeepEqual(inspection.AppliedVersions, append(versionsThrough(latest), unknownVersion)) || len(inspection.PendingVersions) != 0 || !reflect.DeepEqual(inspection.UnknownVersions, []int{unknownVersion}) {
			t.Fatalf("unknown schema inspection = %#v", inspection)
		}
	})
}

func versionsThrough(latest int) []int {
	versions := make([]int, latest)
	for index := range versions {
		versions[index] = index + 1
	}
	return versions
}

func TestMigrateLeavesUnknownNewerVersionForRollbackCompatibility(t *testing.T) {
	ctx := context.Background()
	database := newRawDatabase(t)
	if _, err := database.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL); INSERT INTO schema_migrations(version, applied_at) VALUES (999, 'future');`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, database); err != nil {
		t.Fatalf("migrate database with unknown newer version: %v", err)
	}
	var ownTables int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='actors'`).Scan(&ownTables); err != nil {
		t.Fatal(err)
	}
	if ownTables != 0 {
		t.Fatalf("retained migration created local tables in a newer-schema database: %d", ownTables)
	}
	inspection, err := InspectSchema(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if !containsInt(inspection.UnknownVersions, 999) {
		t.Fatalf("unknown versions = %v, want 999", inspection.UnknownVersions)
	}
}

func TestPre009QueriesAndPostUpgradeWritesSurviveBinaryOnlyRollback(t *testing.T) {
	ctx := context.Background()
	migrations, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) < 9 {
		t.Skip("migration 009 is not present")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "roadmap.db")
	database, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	applyMigrationPrefix(t, ctx, database, migrations, 8)
	populateProductionFixture(t, ctx, database, 8)
	if err := Migrate(ctx, database); err != nil {
		t.Fatalf("upgrade through migration 009: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO task_agent_work(task_id, operation_id, actor_id, state, phase, summary, next_action, checkpoint_refs, checkpoint_completed, checkpoint_total, started_at, updated_at) VALUES ('task-1', 'run-1', 'agent', 'working', 'Implement', 'Upgrade safely', 'Run checks', '["migration"]', 1, 2, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	// This is the pre-009 task read/write shape used by retained binaries. It
	// intentionally does not mention task_agent_work or any newer columns.
	if _, err := database.ExecContext(ctx, `UPDATE tasks SET title='Updated by retained binary', description='retained write', priority='normal', column_id='todo', position=1.75, assignee_id=NULL, due_at=NULL, version=version+1, updated_at='2026-01-02T00:00:00Z' WHERE id='task-1' AND version=3`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen without db.Open/Migrate, standing in for the retained binary's
	// binary-only rollback. New-schema rows must remain untouched while old
	// task query/write patterns continue to work.
	retained, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer retained.Close()
	var title, operationID, state string
	if err := retained.QueryRowContext(ctx, `SELECT title FROM tasks WHERE id='task-1'`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "Updated by retained binary" {
		t.Fatalf("retained task title = %q", title)
	}
	if err := retained.QueryRowContext(ctx, `SELECT operation_id, state FROM task_agent_work WHERE task_id='task-1'`).Scan(&operationID, &state); err != nil {
		t.Fatal(err)
	}
	if operationID != "run-1" || state != "working" {
		t.Fatalf("post-upgrade work row = %s/%s", operationID, state)
	}
	if _, err := retained.ExecContext(ctx, `UPDATE tasks SET title='Rollback write', version=version+1, updated_at='2026-01-03T00:00:00Z' WHERE id='task-1' AND version=4`); err != nil {
		t.Fatal(err)
	}
	if err := retained.QueryRowContext(ctx, `SELECT operation_id FROM task_agent_work WHERE task_id='task-1'`).Scan(&operationID); err != nil {
		t.Fatal(err)
	}
	if operationID != "run-1" {
		t.Fatalf("task_agent_work changed during retained write: %q", operationID)
	}
}

func TestRetainedBinaryProcessAgainstSchema10(t *testing.T) {
	ctx := context.Background()
	migrations, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) < 10 {
		t.Skip("migration 010 is not present")
	}
	latest, _, err := EmbeddedSchema()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "roadmap.db")
	database, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	applyMigrationPrefix(t, ctx, database, migrations, 8)
	populateProductionFixture(t, ctx, database, 8)
	if err := Migrate(ctx, database); err != nil {
		t.Fatalf("upgrade to schema %d: %v", latest, err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO task_agent_work(task_id, operation_id, actor_id, state, phase, summary, next_action, checkpoint_refs, started_at, updated_at) VALUES ('task-1', 'retained-run', 'agent', 'working', 'old binary', 'Existing pulse', 'Keep going', '[]', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	populateAuditFixture(t, ctx, database)
	beforeAudits := readAuditFixture(t, ctx, database)
	inspection, err := InspectSchema(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.SchemaVersion != latest || len(inspection.PendingVersions) != 0 || len(inspection.UnknownVersions) != 0 {
		t.Fatalf("upgraded schema inspection = %#v", inspection)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	// Build a standalone executable that represents the retained pre-009
	// binary's database boundary. It deliberately has no dependency on this
	// package's migration runner and performs only the old task query/write.
	workerPath := buildRetainedBinary(t)
	worker := exec.Command(workerPath, path)
	if output, err := worker.CombinedOutput(); err != nil {
		t.Fatalf("retained binary process: %v\n%s", err, output)
	}
	retained, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer retained.Close()
	var title, operationID string
	if err := retained.QueryRowContext(ctx, `SELECT title FROM tasks WHERE id='task-1'`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "Retained process write" {
		t.Fatalf("retained process task write = %q", title)
	}
	if err := retained.QueryRowContext(ctx, `SELECT operation_id FROM task_agent_work WHERE task_id='task-1'`).Scan(&operationID); err != nil {
		t.Fatal(err)
	}
	if operationID != "retained-run" {
		t.Fatalf("post-upgrade agent work row changed by retained process: %q", operationID)
	}
	afterAudits := readAuditFixture(t, ctx, retained)
	if !reflect.DeepEqual(beforeAudits, afterAudits) {
		t.Fatalf("retained process changed audit rows:\nbefore=%#v\nafter=%#v", beforeAudits, afterAudits)
	}
	inspection, err = InspectSchema(ctx, retained)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.SchemaVersion != latest || len(inspection.PendingVersions) != 0 || len(inspection.UnknownVersions) != 0 {
		t.Fatalf("retained process changed schema inspection = %#v", inspection)
	}
	if err := CheckIntegrity(ctx, retained); err != nil {
		t.Fatalf("retained process integrity: %v", err)
	}
}

func TestSchema10AuditDDLIntegrityAndConstraintRejections(t *testing.T) {
	ctx := context.Background()
	migrations, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) < 10 {
		t.Skip("migration 010 is not present")
	}
	database := newRawDatabase(t)
	applyMigrationPrefix(t, ctx, database, migrations, len(migrations))
	populateProductionFixture(t, ctx, database, 10)

	for _, table := range []string{"audit_runs", "audit_findings"} {
		var count int
		if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("audit table %q count = %d, want 1", table, count)
		}
	}
	for _, index := range []string{
		"audit_runs_project_started_idx",
		"audit_runs_actor_started_idx",
		"audit_findings_audit_created_idx",
		"audit_findings_task_idx",
		"audit_findings_verdict_idx",
	} {
		var count int
		if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("audit index %q count = %d, want 1", index, count)
		}
	}
	if err := CheckIntegrity(ctx, database); err != nil {
		t.Fatalf("populated audit schema integrity: %v", err)
	}

	if _, err := database.ExecContext(ctx, `INSERT INTO audit_runs(id, project_id, actor_id, scope, started_at, created_at, updated_at) VALUES ('audit-default', 'project', 'owner', 'default values', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z')`); err != nil {
		t.Fatalf("insert audit run with defaults: %v", err)
	}
	var status string
	if err := database.QueryRowContext(ctx, `SELECT status FROM audit_runs WHERE id='audit-default'`).Scan(&status); err != nil || status != "running" {
		t.Fatalf("audit run default status = %q, %v; want running", status, err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO audit_findings(id, audit_id, task_id, captured_version, source_column, verdict, confidence, reason, created_at, updated_at) VALUES ('finding-default', 'audit-default', 'task-1', 3, 'todo', 'correct', 1, 'No issue', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z')`); err != nil {
		t.Fatalf("insert audit finding with defaults: %v", err)
	}
	var evidenceRefs, reviewState string
	var findingVersion int
	if err := database.QueryRowContext(ctx, `SELECT evidence_refs, review_state, version FROM audit_findings WHERE id='finding-default'`).Scan(&evidenceRefs, &reviewState, &findingVersion); err != nil {
		t.Fatal(err)
	}
	if evidenceRefs != "[]" || reviewState != "pending" || findingVersion != 1 {
		t.Fatalf("audit finding defaults = %q/%q/%d; want []/pending/1", evidenceRefs, reviewState, findingVersion)
	}

	auditRunCases := []struct {
		name  string
		query string
		args  []any
	}{
		{
			name:  "project foreign key",
			query: `INSERT INTO audit_runs(id, project_id, actor_id, scope, started_at, created_at, updated_at) VALUES (?, 'missing-project', 'owner', 'scope', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z')`,
			args:  []any{"audit-bad-project"},
		},
		{
			name:  "actor foreign key",
			query: `INSERT INTO audit_runs(id, project_id, actor_id, scope, started_at, created_at, updated_at) VALUES (?, 'project', 'missing-actor', 'scope', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z')`,
			args:  []any{"audit-bad-actor"},
		},
		{
			name:  "empty scope",
			query: `INSERT INTO audit_runs(id, project_id, actor_id, scope, started_at, created_at, updated_at) VALUES (?, 'project', 'owner', ?, '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z')`,
			args:  []any{"audit-bad-scope-empty", "  "},
		},
		{
			name:  "scope too long",
			query: `INSERT INTO audit_runs(id, project_id, actor_id, scope, started_at, created_at, updated_at) VALUES (?, 'project', 'owner', ?, '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z')`,
			args:  []any{"audit-bad-scope-long", strings.Repeat("s", 201)},
		},
		{
			name:  "status enum",
			query: `INSERT INTO audit_runs(id, project_id, actor_id, scope, status, started_at, created_at, updated_at) VALUES (?, 'project', 'owner', 'scope', 'unknown', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z')`,
			args:  []any{"audit-bad-status"},
		},
		{
			name:  "finalized running run",
			query: `INSERT INTO audit_runs(id, project_id, actor_id, scope, finalized_at, started_at, created_at, updated_at) VALUES (?, 'project', 'owner', 'scope', '2026-01-02T01:00:00Z', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z')`,
			args:  []any{"audit-bad-finalized"},
		},
		{
			name:  "terminal run missing finalized timestamp",
			query: `INSERT INTO audit_runs(id, project_id, actor_id, scope, status, started_at, created_at, updated_at) VALUES (?, 'project', 'owner', 'scope', 'complete', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z')`,
			args:  []any{"audit-bad-terminal-timestamp"},
		},
	}
	for _, test := range auditRunCases {
		t.Run("audit_runs/"+test.name, func(t *testing.T) {
			if _, err := database.ExecContext(ctx, test.query, test.args...); err == nil {
				t.Fatalf("invalid audit run unexpectedly succeeded")
			}
		})
	}

	longSourceColumn := strings.Repeat("c", 201)
	longReason := strings.Repeat("r", 2001)
	longReviewState := strings.Repeat("v", 33)
	auditFindingCases := []struct {
		name  string
		query string
		args  []any
	}{
		{
			name:  "audit foreign key",
			query: validAuditFindingInsert,
			args:  []any{"finding-bad-audit", "missing-audit", "task-1", 3, "todo", "correct", nil, 0.5, "reason", "[]", "pending", 1},
		},
		{
			name:  "task foreign key",
			query: validAuditFindingInsert,
			args:  []any{"finding-bad-task", "audit-1", "missing-task", 3, "todo", "correct", nil, 0.5, "reason", "[]", "pending", 1},
		},
		{
			name:  "captured version positive",
			query: validAuditFindingInsert,
			args:  []any{"finding-bad-captured-version", "audit-1", "task-1", 0, "todo", "correct", nil, 0.5, "reason", "[]", "pending", 1},
		},
		{
			name:  "source column nonempty",
			query: validAuditFindingInsert,
			args:  []any{"finding-bad-source-empty", "audit-1", "task-1", 3, " ", "correct", nil, 0.5, "reason", "[]", "pending", 1},
		},
		{
			name:  "source column length",
			query: validAuditFindingInsert,
			args:  []any{"finding-bad-source-long", "audit-1", "task-1", 3, longSourceColumn, "correct", nil, 0.5, "reason", "[]", "pending", 1},
		},
		{
			name:  "verdict enum",
			query: validAuditFindingInsert,
			args:  []any{"finding-bad-verdict", "audit-1", "task-1", 3, "todo", "wrong", nil, 0.5, "reason", "[]", "pending", 1},
		},
		{
			name:  "destination enum",
			query: validAuditFindingInsert,
			args:  []any{"finding-bad-destination", "audit-1", "task-1", 3, "todo", "move_proposed", "invalid", 0.5, "reason", "[]", "pending", 1},
		},
		{
			name:  "confidence lower bound",
			query: validAuditFindingInsert,
			args:  []any{"finding-bad-confidence-low", "audit-1", "task-1", 3, "todo", "correct", nil, -0.01, "reason", "[]", "pending", 1},
		},
		{
			name:  "confidence upper bound",
			query: validAuditFindingInsert,
			args:  []any{"finding-bad-confidence-high", "audit-1", "task-1", 3, "todo", "correct", nil, 1.01, "reason", "[]", "pending", 1},
		},
		{
			name:  "reason nonempty",
			query: validAuditFindingInsert,
			args:  []any{"finding-bad-reason-empty", "audit-1", "task-1", 3, "todo", "correct", nil, 0.5, " ", "[]", "pending", 1},
		},
		{
			name:  "reason length",
			query: validAuditFindingInsert,
			args:  []any{"finding-bad-reason-long", "audit-1", "task-1", 3, "todo", "correct", nil, 0.5, longReason, "[]", "pending", 1},
		},
		{
			name:  "evidence refs JSON",
			query: validAuditFindingInsert,
			args:  []any{"finding-bad-evidence-json", "audit-1", "task-1", 3, "todo", "correct", nil, 0.5, "reason", "not-json", "pending", 1},
		},
		{
			name:  "evidence refs array",
			query: validAuditFindingInsert,
			args:  []any{"finding-bad-evidence-object", "audit-1", "task-1", 3, "todo", "correct", nil, 0.5, "reason", "{}", "pending", 1},
		},
		{
			name:  "review state nonempty",
			query: validAuditFindingInsert,
			args:  []any{"finding-bad-review-empty", "audit-1", "task-1", 3, "todo", "correct", nil, 0.5, "reason", "[]", " ", 1},
		},
		{
			name:  "review state length",
			query: validAuditFindingInsert,
			args:  []any{"finding-bad-review-long", "audit-1", "task-1", 3, "todo", "correct", nil, 0.5, "reason", "[]", longReviewState, 1},
		},
		{
			name:  "review state enum",
			query: validAuditFindingInsert,
			args:  []any{"finding-bad-review-enum", "audit-1", "task-1", 3, "todo", "correct", nil, 0.5, "reason", "[]", "reviewed", 1},
		},
		{
			name:  "finding version positive",
			query: validAuditFindingInsert,
			args:  []any{"finding-bad-version", "audit-1", "task-1", 3, "todo", "correct", nil, 0.5, "reason", "[]", "pending", 0},
		},
		{
			name:  "audit task unique",
			query: validAuditFindingInsert,
			args:  []any{"finding-duplicate", "audit-1", "task-1", 3, "todo", "correct", nil, 0.5, "reason", "[]", "pending", 1},
		},
	}
	for _, test := range auditFindingCases {
		t.Run("audit_findings/"+test.name, func(t *testing.T) {
			if _, err := database.ExecContext(ctx, test.query, test.args...); err == nil {
				t.Fatalf("invalid audit finding unexpectedly succeeded")
			}
		})
	}

	if _, err := database.ExecContext(ctx, `DELETE FROM audit_runs WHERE id='audit-default'`); err != nil {
		t.Fatalf("delete audit run for cascade test: %v", err)
	}
	var findings int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_findings WHERE audit_id='audit-default'`).Scan(&findings); err != nil {
		t.Fatal(err)
	}
	if findings != 0 {
		t.Fatalf("audit finding cascade count = %d, want 0", findings)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO actors(id, kind, name, email, password_hash, admin, created_at, updated_at) VALUES ('audit-actor', 'agent', 'Audit actor', 'audit-actor@example.test', 'hash', 0, '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO audit_runs(id, project_id, actor_id, scope, started_at, created_at, updated_at) VALUES ('audit-restrict-actor', 'project', 'audit-actor', 'scope', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `DELETE FROM actors WHERE id='audit-actor'`); err == nil {
		t.Fatal("actor referenced by audit run was deleted")
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO tasks(id, project_id, number, column_id, kind, title, description, priority, position, version, created_at, updated_at) VALUES ('audit-task', 'project', 99, 'todo', 'task', 'Audit task', '', 'normal', 99, 1, '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, validAuditFindingInsert, "finding-restrict-task", "audit-1", "audit-task", 1, "todo", "correct", nil, 1.0, "retained", "[]", "pending", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `DELETE FROM tasks WHERE id='audit-task'`); err == nil {
		t.Fatal("task referenced by audit finding was deleted")
	}
	if err := CheckIntegrity(ctx, database); err != nil {
		t.Fatalf("audit schema integrity after constraint checks: %v", err)
	}
}

const validAuditFindingInsert = `INSERT INTO audit_findings(id, audit_id, task_id, captured_version, source_column, verdict, proposed_semantic_destination, confidence, reason, evidence_refs, review_state, version, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z')`

const retainedBinarySource = `package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

func main() {
	if len(os.Args) != 2 {
		fail("usage: retained-roadmap DATABASE")
	}
	database, err := sql.Open("sqlite", "file:"+os.Args[1]+"?_pragma=foreign_keys(1)")
	if err != nil {
		fail("open database: %v", err)
	}
	defer database.Close()
	var title string
	if err := database.QueryRow("SELECT id, number, project_id, column_id, title, description, priority, position, version FROM tasks WHERE id='task-1'").Scan(new(string), new(int), new(string), new(string), &title, new(string), new(string), new(float64), new(int)); err != nil {
		fail("read pre-009 task shape: %v", err)
	}
	if title == "" {
		fail("retained read returned an empty task title")
	}
	result, err := database.Exec("UPDATE tasks SET title='Retained process write', version=version+1, updated_at='2026-01-04T00:00:00Z' WHERE id='task-1' AND version=3")
	if err != nil {
		fail("write pre-009 task shape: %v", err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		fail("retained write affected %d rows: %v", changed, err)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\\n", args...)
	os.Exit(1)
}
`

func buildRetainedBinary(t *testing.T) string {
	t.Helper()
	moduleRoot := findModuleRoot(t)
	sourceDir := t.TempDir()
	sourcePath := filepath.Join(sourceDir, "main.go")
	binaryPath := filepath.Join(sourceDir, "roadmap-retained")
	if err := os.WriteFile(sourcePath, []byte(retainedBinarySource), 0o600); err != nil {
		t.Fatalf("write retained binary source: %v", err)
	}
	build := exec.Command("go", "build", "-mod=readonly", "-o", binaryPath, sourcePath)
	build.Dir = moduleRoot
	// The normal package test has already resolved this module's dependencies;
	// building from the same module avoids any git checkout or network lookup
	// for historical application sources.
	build.Env = os.Environ()
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build retained binary: %v\n%s", err, output)
	}
	if info, err := os.Stat(binaryPath); err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		t.Fatalf("retained binary is not executable: %v", err)
	}
	return binaryPath
}

func findModuleRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get test working directory: %v", err)
	}
	for {
		if info, statErr := os.Stat(filepath.Join(directory, "go.mod")); statErr == nil && !info.IsDir() {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("could not find module root")
		}
		directory = parent
	}
}

func TestPreflightCopiesAndCleansPrivateDatabase(t *testing.T) {
	ctx := context.Background()
	migrations, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "candidate.db")
	database, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	applyMigrationPrefix(t, ctx, database, migrations, 1)
	populateProductionFixture(t, ctx, database, 1)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := fileBytes(path)
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := Preflight(ctx, path)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	latest, _, _ := EmbeddedSchema()
	if inspection.SchemaVersion != latest {
		t.Fatalf("preflight schema version = %d, want %d", inspection.SchemaVersion, latest)
	}
	after, err := fileBytes(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("preflight modified its source database")
	}
	if matches, err := filepath.Glob(filepath.Join(dir, ".roadmap-preflight-*")); err != nil || len(matches) != 0 {
		t.Fatalf("preflight temporary files remain: %v", matches)
	}
}

func TestFailedMigrationRollsBackAndCanRetry(t *testing.T) {
	ctx := context.Background()
	migrations, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	database := newRawDatabase(t)
	applyMigrationPrefix(t, ctx, database, migrations, 1)
	// Migration 002 is intentionally made to fail after its CREATE IF NOT
	// EXISTS statement is bypassed. Its INSERT must not leave version 2 behind.
	if _, err := database.ExecContext(ctx, `DROP TABLE auth_setup; CREATE TABLE auth_setup (id INTEGER PRIMARY KEY);`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, database); err == nil {
		t.Fatal("Migrate unexpectedly succeeded with malformed auth_setup")
	}
	var applied int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version=2`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 0 {
		t.Fatalf("failed migration recorded version 2 = %d", applied)
	}
	if _, err := database.ExecContext(ctx, `DROP TABLE auth_setup`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, database); err != nil {
		t.Fatalf("retry after failed migration: %v", err)
	}
	if err := CheckIntegrity(ctx, database); err != nil {
		t.Fatalf("retry integrity: %v", err)
	}
}

type stableFixture struct {
	stable map[string]string
	counts map[string]int
}

type auditFixture struct {
	runs     string
	findings string
}

func newRawDatabase(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "roadmap.db")
	database, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Ping(); err != nil {
		database.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func applyMigrationPrefix(t *testing.T, ctx context.Context, database *sql.DB, migrations []embeddedMigration, prefix int) {
	t.Helper()
	for _, migration := range migrations[:prefix] {
		tx, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, string(migration.contents)); err != nil {
			tx.Rollback()
			t.Fatalf("apply prefix migration %d: %v", migration.version, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, 'fixture')`, migration.version); err != nil {
			tx.Rollback()
			t.Fatalf("record prefix migration %d: %v", migration.version, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
}

func populateProductionFixture(t *testing.T, ctx context.Context, database *sql.DB, prefix int) {
	t.Helper()
	exec := func(query string, args ...any) {
		if _, err := database.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("populate fixture: %v\n%s", err, query)
		}
	}
	exec(`INSERT INTO actors(id, kind, name, email, password_hash, admin, created_at, updated_at) VALUES ('owner', 'human', 'Owner', 'owner@example.test', 'hash-owner', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	exec(`INSERT INTO actors(id, kind, name, email, password_hash, admin, created_at, updated_at) VALUES ('agent', 'agent', 'Agent', 'agent@example.test', 'hash-agent', 0, '2026-01-01T00:00:01Z', '2026-01-01T00:00:01Z')`)
	if prefix >= 5 {
		exec(`UPDATE actors SET description=CASE id WHEN 'owner' THEN 'owner description' ELSE 'agent description' END`)
	}
	exec(`INSERT INTO projects(id, key, slug, name, description, color, favorite, created_at, updated_at) VALUES ('project', 'ROAD', 'roadmap', 'Roadmap', 'Project', '#123456', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	exec(`INSERT INTO columns(id, project_id, name, semantic_state, position, created_at, updated_at) VALUES ('todo', 'project', 'Todo', 'backlog', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	exec(`INSERT INTO columns(id, project_id, name, semantic_state, position, created_at, updated_at) VALUES ('doing', 'project', 'Doing', 'active', 2, '2026-01-01T00:00:01Z', '2026-01-01T00:00:01Z')`)
	if prefix >= 8 {
		exec(`INSERT INTO tasks(id, project_id, number, column_id, kind, title, description, priority, position, claimed_by, claim_expires_at, version, created_at, updated_at) VALUES ('task-1', 'project', 1, 'todo', 'task', 'Task one', 'Keep this', 'high', 1.5, 'agent', '2027-01-01T00:00:00Z', 3, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
		exec(`INSERT INTO tasks(id, project_id, number, column_id, kind, title, description, priority, position, version, created_at, updated_at) VALUES ('bug-1', 'project', 2, 'doing', 'bug', 'Bug one', 'Bug details', 'urgent', 2.5, 2, '2026-01-01T00:00:01Z', '2026-01-01T00:00:01Z')`)
	} else {
		exec(`INSERT INTO tasks(id, project_id, number, column_id, title, description, priority, position, claimed_by, claim_expires_at, version, created_at, updated_at) VALUES ('task-1', 'project', 1, 'todo', 'Task one', 'Keep this', 'high', 1.5, 'agent', '2027-01-01T00:00:00Z', 3, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
		exec(`INSERT INTO tasks(id, project_id, number, column_id, title, description, priority, position, version, created_at, updated_at) VALUES ('bug-1', 'project', 2, 'doing', 'Bug one', 'Bug details', 'urgent', 2.5, 2, '2026-01-01T00:00:01Z', '2026-01-01T00:00:01Z')`)
	}
	if prefix >= 8 {
		exec(`INSERT INTO bug_details(task_id, reporter_id, severity, actual_behavior) VALUES ('bug-1', 'owner', 's2', 'It fails')`)
	}
	exec(`INSERT INTO labels(id, project_id, name, color, created_at, updated_at) VALUES ('label-1', 'project', 'Important', '#ff0000', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	exec(`INSERT INTO task_labels(task_id, label_id) VALUES ('task-1', 'label-1')`)
	exec(`INSERT INTO comments(id, task_id, actor_id, body, created_at, updated_at) VALUES ('comment-1', 'task-1', 'owner', 'A stable comment', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	exec(`INSERT INTO events(id, type, actor_id, project_id, task_id, payload, created_at) VALUES ('event-1', 'task.created', 'owner', 'project', 'task-1', '{"stable":true}', '2026-01-01T00:00:00Z')`)
	exec(`INSERT INTO idempotency_keys(actor_id, key, method, path, request_hash, status, response_body, created_at) VALUES ('owner', 'fixture-key', 'POST', '/api/v1/tasks', 'request-hash', 201, '{"id":"task-1"}', '2026-01-01T00:00:00Z')`)
	if prefix >= 4 {
		exec(`INSERT INTO actor_projects(actor_id, project_id) VALUES ('agent', 'project')`)
	}
	if prefix >= 6 {
		exec(`INSERT INTO actor_resource_usage(actor_id, reserved_bytes, updated_at) VALUES ('agent', 42, '2026-01-01T00:00:00Z')`)
	}
	if prefix >= 7 {
		exec(`UPDATE idempotency_keys SET response_location='/api/v1/tasks/task-1' WHERE actor_id='owner' AND key='fixture-key'`)
	}
	if prefix >= 3 {
		exec(`INSERT OR REPLACE INTO project_counters(project_id, next_number) VALUES ('project', 3)`)
	}
	if prefix >= 10 {
		populateAuditFixture(t, ctx, database)
	}
	if prefix >= 15 {
		exec(`INSERT INTO task_checklist_items(id, task_id, text, position, completed, created_at, updated_at) VALUES ('checklist-1', 'task-1', 'Preserve this acceptance criterion', 0, 0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	}
}

func populateAuditFixture(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	if _, err := database.ExecContext(ctx, `INSERT INTO audit_runs(id, project_id, actor_id, scope, status, started_at, finalized_at, created_at, updated_at) VALUES ('audit-1', 'project', 'owner', 'board', 'complete', '2026-01-01T00:00:00Z', '2026-01-01T00:05:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:05:00Z')`); err != nil {
		t.Fatalf("populate audit run: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO audit_findings(id, audit_id, task_id, captured_version, source_column, verdict, proposed_semantic_destination, confidence, reason, evidence_refs, review_state, version, created_at, updated_at) VALUES ('finding-1', 'audit-1', 'task-1', 3, 'todo', 'move_proposed', 'active', 0.87, 'Task belongs in active work', '["event-1"]', 'pending', 2, '2026-01-01T00:01:00Z', '2026-01-01T00:01:00Z')`); err != nil {
		t.Fatalf("populate audit finding: %v", err)
	}
}

func assertMigratedAdditiveValues(t *testing.T, ctx context.Context, database *sql.DB, prefix int) {
	t.Helper()
	var value string
	if err := database.QueryRowContext(ctx, `SELECT next_number FROM project_counters WHERE project_id='project'`).Scan(&value); err != nil || value != "3" {
		t.Fatalf("project counter = %q, %v; want 3", value, err)
	}
	if prefix >= 4 {
		if err := database.QueryRowContext(ctx, `SELECT actor_id || ':' || project_id FROM actor_projects WHERE actor_id='agent' AND project_id='project'`).Scan(&value); err != nil || value != "agent:project" {
			t.Fatalf("actor project grant = %q, %v; want agent:project", value, err)
		}
	}
	if prefix >= 6 {
		if err := database.QueryRowContext(ctx, `SELECT actor_id || ':' || reserved_bytes FROM actor_resource_usage WHERE actor_id='agent'`).Scan(&value); err != nil || value != "agent:42" {
			t.Fatalf("actor resource usage = %q, %v; want agent:42", value, err)
		}
	}
	if prefix >= 7 {
		if err := database.QueryRowContext(ctx, `SELECT COALESCE(response_location,'') FROM idempotency_keys WHERE actor_id='owner' AND key='fixture-key'`).Scan(&value); err != nil || value != "/api/v1/tasks/task-1" {
			t.Fatalf("idempotency response location = %q, %v; want task location", value, err)
		}
	}
	if err := database.QueryRowContext(ctx, `SELECT kind FROM tasks WHERE id='task-1'`).Scan(&value); err != nil || value != "task" {
		t.Fatalf("task kind = %q, %v; want task", value, err)
	}
	if prefix >= 8 {
		if err := database.QueryRowContext(ctx, `SELECT actual_behavior FROM bug_details WHERE task_id='bug-1'`).Scan(&value); err != nil || value != "It fails" {
			t.Fatalf("bug details = %q, %v; want fixture value", value, err)
		}
	} else {
		var bugs int
		if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM bug_details WHERE task_id='bug-1'`).Scan(&bugs); err != nil || bugs != 0 {
			t.Fatalf("pre-008 bug details = %d, %v; want none", bugs, err)
		}
	}
	var checklistCount int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_checklist_items WHERE task_id='task-1'`).Scan(&checklistCount); err != nil {
		t.Fatalf("checklist table after migration: %v", err)
	}
	if prefix >= 15 {
		if checklistCount != 1 {
			t.Fatalf("checklist rows after retained-data migration = %d, want 1", checklistCount)
		}
		if err := database.QueryRowContext(ctx, `SELECT text FROM task_checklist_items WHERE id='checklist-1'`).Scan(&value); err != nil || value != "Preserve this acceptance criterion" {
			t.Fatalf("checklist retained value = %q, %v", value, err)
		}
	} else if checklistCount != 0 {
		t.Fatalf("new checklist table unexpectedly populated during migration = %d", checklistCount)
	}
	if err := database.QueryRowContext(ctx, `SELECT checklist_completion_policy FROM projects WHERE id='project'`).Scan(&value); err != nil || value != "warn" {
		t.Fatalf("checklist completion policy after migration = %q, %v; want warn", value, err)
	}
	var collectionRevision int64
	wantCollectionRevision := int64(0)
	if prefix >= 20 {
		// When the fixture is populated after migration 020, its two retained
		// task inserts are intentionally observed by the compatibility triggers.
		wantCollectionRevision = 2
	}
	if err := database.QueryRowContext(ctx, `SELECT task_collection_revision FROM projects WHERE id='project'`).Scan(&collectionRevision); err != nil || collectionRevision != wantCollectionRevision {
		t.Fatalf("task collection revision after migration = %d, %v; want %d", collectionRevision, err, wantCollectionRevision)
	}
	var commentVersion int64
	if err := database.QueryRowContext(ctx, `SELECT version FROM comments WHERE id='comment-1'`).Scan(&commentVersion); err != nil || commentVersion != 1 {
		t.Fatalf("comment lifecycle version = %d, %v; want default 1", commentVersion, err)
	}
	var deletedAt sql.NullString
	if err := database.QueryRowContext(ctx, `SELECT deleted_at FROM comments WHERE id='comment-1'`).Scan(&deletedAt); err != nil || deletedAt.Valid {
		t.Fatalf("comment lifecycle tombstone = %v, %v; want live NULL", deletedAt, err)
	}
}

func readStableFixture(t *testing.T, ctx context.Context, database *sql.DB, schemaVersion int) stableFixture {
	t.Helper()
	stable := map[string]string{}
	queries := map[string]string{
		"actors":      `SELECT group_concat(id || ':' || kind || ':' || name || ':' || COALESCE(email,'') || ':' || COALESCE(password_hash,''), '|') FROM (SELECT id, kind, name, email, password_hash FROM actors ORDER BY id)`,
		"projects":    `SELECT id || ':' || key || ':' || slug || ':' || name FROM projects ORDER BY id`,
		"columns":     `SELECT group_concat(id || ':' || project_id || ':' || position, '|') FROM (SELECT id, project_id, position FROM columns ORDER BY position, id)`,
		"tasks":       `SELECT group_concat(id || ':' || number || ':' || project_id || ':' || column_id || ':' || position || ':' || COALESCE(claimed_by,''), '|') FROM (SELECT id, number, project_id, column_id, position, claimed_by FROM tasks ORDER BY number, id)`,
		"comments":    `SELECT id || ':' || task_id || ':' || actor_id || ':' || body FROM comments ORDER BY id`,
		"events":      `SELECT id || ':' || type || ':' || COALESCE(actor_id,'') || ':' || COALESCE(task_id,'') || ':' || payload FROM events ORDER BY cursor`,
		"idempotency": `SELECT actor_id || ':' || key || ':' || response_body FROM idempotency_keys ORDER BY actor_id, key`,
	}
	counts := map[string]int{}
	for name, query := range queries {
		var value sql.NullString
		if err := database.QueryRowContext(ctx, query).Scan(&value); err != nil {
			// Tables introduced after this prefix are intentionally absent until
			// Migrate creates them; they are represented as empty fixture values.
			stable[name] = "<absent>"
			continue
		}
		if value.Valid {
			stable[name] = value.String
		} else {
			stable[name] = ""
		}
	}
	for _, table := range []string{"actors", "projects", "columns", "tasks", "labels", "task_labels", "comments", "events", "idempotency_keys", "schema_migrations"} {
		var count int
		if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err == nil {
			counts[table] = count
		}
	}
	// `schemaVersion` is intentionally consumed so callers can make the
	// prefix/latest distinction explicit at the call site.
	_ = schemaVersion
	return stableFixture{stable: stable, counts: counts}
}

func readAuditFixture(t *testing.T, ctx context.Context, database *sql.DB) auditFixture {
	t.Helper()
	fixture := auditFixture{}
	queries := map[string]*string{
		"runs":     &fixture.runs,
		"findings": &fixture.findings,
	}
	statements := map[string]string{
		"runs":     `SELECT group_concat(value, '|') FROM (SELECT id || ':' || project_id || ':' || actor_id || ':' || scope || ':' || status || ':' || started_at || ':' || COALESCE(finalized_at, '') || ':' || created_at || ':' || updated_at AS value FROM audit_runs ORDER BY id)`,
		"findings": `SELECT group_concat(value, '|') FROM (SELECT id || ':' || audit_id || ':' || task_id || ':' || captured_version || ':' || source_column || ':' || verdict || ':' || COALESCE(proposed_semantic_destination, '') || ':' || confidence || ':' || reason || ':' || evidence_refs || ':' || review_state || ':' || version || ':' || created_at || ':' || updated_at AS value FROM audit_findings ORDER BY id)`,
	}
	for name, destination := range queries {
		var value sql.NullString
		if err := database.QueryRowContext(ctx, statements[name]).Scan(&value); err != nil {
			*destination = "<absent>"
			continue
		}
		if value.Valid {
			*destination = value.String
		}
	}
	return fixture
}

func containsInt(values []int, wanted int) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func fileBytes(path string) ([]byte, error) {
	return os.ReadFile(path)
}
