package store

import (
	"errors"
	"testing"
)

func TestProjectColumnAdministrationPreservesMappingsAndRehomesTasks(t *testing.T) {
	f := newDependencyFixture(t, "ADMIN")
	columns, err := f.store.ListColumns(f.ctx, f.project.ID)
	if err != nil {
		t.Fatalf("list default columns: %v", err)
	}
	if len(columns) != 5 {
		t.Fatalf("default columns = %d, want 5", len(columns))
	}

	name, state, position := "Intake", "backlog", 1
	created, err := f.store.CreateColumn(f.ctx, f.project.ID, ColumnInput{Name: &name, SemanticState: &state, Position: &position}, f.actor.ID)
	if err != nil {
		t.Fatalf("create column: %v", err)
	}
	columns, err = f.store.ListColumns(f.ctx, f.project.ID)
	if err != nil {
		t.Fatalf("list reordered columns: %v", err)
	}
	if len(columns) != 6 || columns[1].ID != created.ID || columns[1].Position != 1 {
		t.Fatalf("created column order = %+v, want Intake at position 1", columns)
	}
	for index, column := range columns {
		if column.Position != index {
			t.Fatalf("column %s position = %d, want %d", column.ID, column.Position, index)
		}
	}
	renamed, renamedState, renamedPosition := "Intake triage", "backlog", 0
	updated, err := f.store.UpdateColumnWithVersion(f.ctx, created.ID, ColumnInput{Name: &renamed, SemanticState: &renamedState, Position: &renamedPosition}, f.actor.ID, true, versionPtrForAdmin(created.Version))
	if err != nil {
		t.Fatalf("rename/reorder column: %v", err)
	}
	if updated.Name != renamed || updated.Position != renamedPosition || updated.Version != created.Version+1 {
		t.Fatalf("updated column = %+v, want renamed position %d version %d", updated, renamedPosition, created.Version+1)
	}
	created = updated

	task, err := f.store.CreateTask(f.ctx, f.project.ID, TaskInput{Title: dependencyStringPtr("move on archive"), ColumnID: &created.ID}, f.actor.ID)
	if err != nil {
		t.Fatalf("create task in extra column: %v", err)
	}
	archived, err := f.store.UpdateColumnWithVersion(f.ctx, created.ID, ColumnInput{Archived: boolPtrForAdmin(true)}, f.actor.ID, true, versionPtrForAdmin(created.Version))
	if err != nil {
		t.Fatalf("archive extra column: %v", err)
	}
	if archived.ArchivedAt == nil || archived.Version != created.Version+1 {
		t.Fatalf("archived column = %+v, want archived and incremented", archived)
	}
	rehomed, err := f.store.GetTask(f.ctx, task.ID)
	if err != nil {
		t.Fatalf("reload rehomed task: %v", err)
	}
	if rehomed.ColumnID == created.ID || rehomed.Version != task.Version+1 {
		t.Fatalf("rehome = column %s version %d, want another column and version %d", rehomed.ColumnID, rehomed.Version, task.Version+1)
	}
	fallback, err := f.store.StateColumn(f.ctx, f.project.ID, "backlog")
	if err != nil {
		t.Fatalf("read backlog fallback: %v", err)
	}
	if rehomed.ColumnID != fallback.ID {
		t.Fatalf("rehome destination = %s, want backlog column %s", rehomed.ColumnID, fallback.ID)
	}
	live, err := f.store.ListColumns(f.ctx, f.project.ID)
	if err != nil {
		t.Fatalf("list live columns: %v", err)
	}
	if len(live) != 5 {
		t.Fatalf("live columns = %d, want 5", len(live))
	}
	all, err := f.store.ListColumnsIncludingArchived(f.ctx, f.project.ID)
	if err != nil {
		t.Fatalf("list all columns: %v", err)
	}
	if len(all) != 6 || all[len(all)-1].ID != created.ID {
		t.Fatalf("all columns = %+v, want archived column after live columns", all)
	}

	restored, err := f.store.UpdateColumnWithVersion(f.ctx, created.ID, ColumnInput{Archived: boolPtrForAdmin(false)}, f.actor.ID, true, versionPtrForAdmin(archived.Version))
	if err != nil {
		t.Fatalf("restore column: %v", err)
	}
	if restored.ArchivedAt != nil {
		t.Fatalf("restored column still archived: %+v", restored)
	}
	live, err = f.store.ListColumns(f.ctx, f.project.ID)
	if err != nil {
		t.Fatalf("list restored columns: %v", err)
	}
	for index, column := range live {
		if column.Position != index {
			t.Fatalf("restored column %s position = %d, want %d", column.ID, column.Position, index)
		}
	}
}

func TestVersionedColumnAdministrationRetainsSoleSemanticMapping(t *testing.T) {
	f := newDependencyFixture(t, "ADMINMAP")
	backlog, err := f.store.StateColumn(f.ctx, f.project.ID, "backlog")
	if err != nil {
		t.Fatalf("read backlog: %v", err)
	}
	state := "ready"
	_, err = f.store.UpdateColumnWithVersion(f.ctx, backlog.ID, ColumnInput{SemanticState: &state}, f.actor.ID, true, versionPtrForAdmin(backlog.Version))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("sole mapping change error = %v, want ErrConflict", err)
	}
	_, err = f.store.UpdateColumnWithVersion(f.ctx, backlog.ID, ColumnInput{Archived: boolPtrForAdmin(true)}, f.actor.ID, true, versionPtrForAdmin(backlog.Version))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("sole mapping archive error = %v, want ErrConflict", err)
	}
	unchanged, err := f.store.GetColumn(f.ctx, backlog.ID)
	if err != nil {
		t.Fatalf("reload sole mapping: %v", err)
	}
	if unchanged.ArchivedAt != nil || unchanged.Version != backlog.Version {
		t.Fatalf("rejected sole mapping archive changed column = %+v, want version %d and live", unchanged, backlog.Version)
	}
}

func TestVersionedProjectAdministrationPreservesStableReference(t *testing.T) {
	f := newDependencyFixture(t, "ADMINPROJECT")
	first, err := f.store.GetProject(f.ctx, f.project.Slug)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	name := "Renamed project"
	updated, err := f.store.UpdateProjectWithVersion(f.ctx, first.ID, ProjectInput{Name: &name}, f.actor.ID, versionPtrForAdmin(first.Version))
	if err != nil {
		t.Fatalf("rename project: %v", err)
	}
	if updated.Name != name || updated.Slug != first.Slug || updated.Key != first.Key || updated.Version != first.Version+1 {
		t.Fatalf("renamed project = %+v, want stable key/slug and incremented version", updated)
	}
	_, err = f.store.UpdateProjectWithVersion(f.ctx, first.ID, ProjectInput{Description: dependencyStringPtr("stale")}, f.actor.ID, versionPtrForAdmin(first.Version))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale project update error = %v, want ErrConflict", err)
	}

	archived, err := f.store.UpdateProjectWithVersion(f.ctx, first.ID, ProjectInput{Archived: boolPtrForAdmin(true)}, f.actor.ID, versionPtrForAdmin(updated.Version))
	if err != nil {
		t.Fatalf("archive project: %v", err)
	}
	if archived.ArchivedAt == nil {
		t.Fatalf("archived project = %+v, want archived", archived)
	}
	restored, err := f.store.UpdateProjectWithVersion(f.ctx, first.Slug, ProjectInput{Archived: boolPtrForAdmin(false)}, f.actor.ID, versionPtrForAdmin(archived.Version))
	if err != nil {
		t.Fatalf("restore project by slug: %v", err)
	}
	if restored.ArchivedAt != nil || restored.Slug != first.Slug || restored.Key != first.Key {
		t.Fatalf("restored project = %+v, want stable references", restored)
	}
}

func boolPtrForAdmin(value bool) *bool { return &value }

func versionPtrForAdmin(value int64) *int64 { return &value }
