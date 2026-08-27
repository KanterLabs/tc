package db

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpenAppliesSQLitePageAndWALCaps(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "roadmap.db")
	database, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	var pageSize, maxPages, walLimit, autocheckpoint int64
	for _, query := range []struct {
		name string
		dest *int64
		sql  string
	}{
		{"page_size", &pageSize, "PRAGMA page_size"},
		{"max_page_count", &maxPages, "PRAGMA max_page_count"},
		{"journal_size_limit", &walLimit, "PRAGMA journal_size_limit"},
		{"wal_autocheckpoint", &autocheckpoint, "PRAGMA wal_autocheckpoint"},
	} {
		if err := database.QueryRowContext(context.Background(), query.sql).Scan(query.dest); err != nil {
			t.Fatalf("query %s: %v", query.name, err)
		}
	}
	if pageSize <= 0 {
		t.Fatalf("page size = %d", pageSize)
	}
	expectedPages := sqliteMaxDatabaseBytes / pageSize
	if expectedPages < 1 {
		expectedPages = 1
	}
	if maxPages != expectedPages {
		t.Fatalf("max page count = %d, want %d for page size %d", maxPages, expectedPages, pageSize)
	}
	if maxPages*pageSize > sqliteMaxDatabaseBytes {
		t.Fatalf("database cap = %d bytes, exceeds %d", maxPages*pageSize, sqliteMaxDatabaseBytes)
	}
	if walLimit != sqliteMaxWALBytes {
		t.Fatalf("WAL journal size limit = %d, want %d", walLimit, sqliteMaxWALBytes)
	}
	if autocheckpoint != sqliteWALAutocheckpointPages {
		t.Fatalf("WAL autocheckpoint = %d, want %d", autocheckpoint, sqliteWALAutocheckpointPages)
	}
}
