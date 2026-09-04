package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/KanterLabs/helm/internal/db"
)

// BenchmarkListSearchTasksPage keeps the performance claim tied to the same
// SQL path used by the HTTP endpoint. It models a ten-project workspace with
// 3,000 live tasks, a five-project bearer ceiling, and a 50-row page. Rows are
// inserted directly so setup cost and task mutation events do not pollute the
// measured read path.
func BenchmarkListSearchTasksPage(b *testing.B) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		b.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, err := data.CreateActor(ctx, Actor{Kind: "human", Name: "Search benchmark"}, "")
	if err != nil {
		b.Fatalf("create actor: %v", err)
	}

	projects := make([]Project, 0, 10)
	columns := make([]Column, 0, 10)
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("SB%02d", i)
		project, projectErr := data.CreateProject(ctx, ProjectInput{Key: &key, Name: stringPtrForTest(fmt.Sprintf("Search benchmark %d", i))}, actor.ID)
		if projectErr != nil {
			b.Fatalf("create project %d: %v", i, projectErr)
		}
		column, columnErr := data.StateColumn(ctx, project.ID, "ready")
		if columnErr != nil {
			b.Fatalf("find ready column %d: %v", i, columnErr)
		}
		projects = append(projects, project)
		columns = append(columns, column)
	}

	timestamp := now()
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		b.Fatalf("begin task fixture: %v", err)
	}
	for i := 0; i < 3000; i++ {
		projectIndex := i % len(projects)
		title := fmt.Sprintf("Unrelated benchmark task %d", i)
		if i%3 == 0 {
			title = fmt.Sprintf("Search needle candidate %d", i)
		}
		_, insertErr := tx.ExecContext(ctx, `INSERT INTO tasks(id, project_id, number, kind, column_id, title, description, priority, position, version, created_at, updated_at) VALUES (?, ?, ?, 'task', ?, ?, '', 'normal', ?, 1, ?, ?)`, fmt.Sprintf("search-benchmark-%04d", i), projects[projectIndex].ID, i+1, columns[projectIndex].ID, title, float64(i), timestamp, timestamp)
		if insertErr != nil {
			_ = tx.Rollback()
			b.Fatalf("insert task %d: %v", i, insertErr)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatalf("commit task fixture: %v", err)
	}

	allowedProjects := make([]string, 0, 5)
	for _, project := range projects[:5] {
		allowedProjects = append(allowedProjects, project.ID)
	}
	filter := SearchFilter{Query: "needle", ProjectIDs: allowedProjects, Limit: 50}
	if tasks, _, warmupErr := data.ListSearchTasksWithExtra(ctx, filter); warmupErr != nil || len(tasks) != 50 {
		b.Fatalf("warmup search returned %d tasks, error=%v", len(tasks), warmupErr)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tasks, _, searchErr := data.ListSearchTasksWithExtra(ctx, filter)
		if searchErr != nil {
			b.Fatalf("search page: %v", searchErr)
		}
		if len(tasks) != 50 {
			b.Fatalf("search page returned %d tasks, want 50", len(tasks))
		}
	}
}
