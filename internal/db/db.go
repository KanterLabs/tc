// Package db owns SQLite connection setup and schema migrations.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Migrations is intentionally embedded in the binary so a deployment never
// depends on a writable source tree or a migration volume being mounted.
//
//go:embed migrations/*.sql
var Migrations embed.FS

// Keep the database materially below the 16 GiB root volume. The page cap is
// derived from the actual page size so an existing database created with a
// non-default page size receives the same byte ceiling. The WAL limit and
// autocheckpoint leave additional headroom for transient write-ahead frames.
const (
	sqliteTargetPageSize         int64 = 4096
	sqliteMaxDatabaseBytes       int64 = 512 << 20 // 512 MiB
	sqliteMaxWALBytes            int64 = 64 << 20  // 64 MiB
	sqliteWALAutocheckpointPages       = 1000
)

// Open opens a SQLite database with the pragmas required by the application.
// SQLite serializes writes, while WAL permits readers to continue during a
// write. The busy timeout is also present in the DSN so it applies to every
// pooled connection, not just the first one.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if strings.TrimSpace(path) == "" {
		path = "roadmap.db"
	}
	if path != ":memory:" && !strings.HasPrefix(path, "file:") {
		if err := ensureParent(path); err != nil {
			return nil, err
		}
		path = "file:" + path
	}
	if path == ":memory:" {
		// A named shared in-memory database survives across pooled connections.
		path = "file:roadmap-memory?mode=memory&cache=shared"
	}
	if !strings.Contains(path, "?") {
		path += "?"
	} else {
		path += "&"
	}
	path += fmt.Sprintf("_pragma=busy_timeout(5000)&_pragma=page_size(%d)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=journal_size_limit(%d)&_pragma=wal_autocheckpoint(%d)", sqliteTargetPageSize, sqliteMaxWALBytes, sqliteWALAutocheckpointPages)

	database, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// A small pool is enough for this single-process service and avoids a
	// thundering herd of writers. WAL still allows independent read queries.
	database.SetMaxOpenConns(8)
	database.SetMaxIdleConns(8)
	database.SetConnMaxLifetime(0)

	if err := database.PingContext(ctx); err != nil {
		database.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	// Keep these explicit as well as in the DSN. Some SQLite-compatible
	// drivers only apply URI pragmas when a connection is first created.
	for _, pragma := range []string{
		"PRAGMA busy_timeout = 5000",
		"PRAGMA foreign_keys = ON",
		fmt.Sprintf("PRAGMA page_size = %d", sqliteTargetPageSize),
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		fmt.Sprintf("PRAGMA journal_size_limit = %d", sqliteMaxWALBytes),
		fmt.Sprintf("PRAGMA wal_autocheckpoint = %d", sqliteWALAutocheckpointPages),
	} {
		if _, err := database.ExecContext(ctx, pragma); err != nil {
			database.Close()
			return nil, fmt.Errorf("sqlite %s: %w", pragma, err)
		}
	}
	if err := applySQLitePageCap(ctx, database); err != nil {
		database.Close()
		return nil, err
	}
	if err := Migrate(ctx, database); err != nil {
		database.Close()
		return nil, err
	}
	return database, nil
}

func applySQLitePageCap(ctx context.Context, database *sql.DB) error {
	var pageSize int64
	if err := database.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return fmt.Errorf("sqlite page size: %w", err)
	}
	if pageSize <= 0 {
		return fmt.Errorf("sqlite page size is invalid: %d", pageSize)
	}
	maxPages := sqliteMaxDatabaseBytes / pageSize
	if maxPages < 1 {
		maxPages = 1
	}
	// Assignment is intentionally a query: SQLite returns the effective value
	// and keeps an already-larger database readable when an operator upgrades
	// an old installation. SQLite cannot lower a cap below the current page
	// count; an operator must compact such a database separately.
	var effective int64
	if err := database.QueryRowContext(ctx, fmt.Sprintf("PRAGMA max_page_count = %d", maxPages)).Scan(&effective); err != nil {
		return fmt.Errorf("sqlite max page count: %w", err)
	}
	return nil
}

func ensureParent(path string) error {
	parent := filepath.Dir(path)
	if parent == "." || parent == "" {
		return nil
	}
	// MkdirAll is limited to the explicitly configured database parent.
	return os.MkdirAll(parent, 0o755)
}

// Migrate applies embedded migrations in lexical/version order. Each complete
// migration is atomic and can safely be retried after an interrupted startup.
func Migrate(ctx context.Context, database *sql.DB) error {
	if _, err := database.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}

	entries, err := fs.ReadDir(Migrations, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, err := migrationVersion(entry.Name())
		if err != nil {
			return err
		}
		var applied int
		if err := database.QueryRowContext(ctx, `SELECT COUNT(1) FROM schema_migrations WHERE version = ?`, version).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %d: %w", version, err)
		}
		if applied != 0 {
			continue
		}
		contents, err := fs.ReadFile(Migrations, "migrations/"+entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		tx, err := database.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, string(contents)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, version, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", version, err)
		}
	}
	return nil
}

func migrationVersion(name string) (int, error) {
	name = strings.TrimSuffix(name, filepath.Ext(name))
	underscore := strings.IndexByte(name, '_')
	if underscore <= 0 {
		return 0, fmt.Errorf("invalid migration filename %q", name)
	}
	version, err := strconv.Atoi(name[:underscore])
	if err != nil || version <= 0 {
		return 0, fmt.Errorf("invalid migration version %q", name)
	}
	return version, nil
}
