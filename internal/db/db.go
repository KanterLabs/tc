// Package db owns SQLite connection setup and schema migrations.
package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"fmt"
	"io"
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

// SchemaInspection describes the migration state observed in a database and
// the migration set embedded in this binary. Unknown versions are reported,
// but are deliberately not treated as an error: retained binaries must be
// able to open a database upgraded by a newer release for rollback/recovery.
// The explicit Preflight operation below is the fail-closed path for a
// candidate database.
type SchemaInspection struct {
	// SchemaVersion is the greatest version recorded in schema_migrations. A
	// database with no migration table or rows has version zero.
	SchemaVersion int
	// AppliedVersions is sorted numerically and includes versions unknown to
	// this binary.
	AppliedVersions []int
	// EmbeddedSchemaVersion and MigrationDigest describe this binary's
	// embedded migration set.
	EmbeddedSchemaVersion int
	MigrationDigest       string
	// PendingVersions are embedded migrations not recorded in the database.
	PendingVersions []int
	// UnknownVersions are recorded in the database but absent from this
	// binary's embedded migration set.
	UnknownVersions []int
}

type embeddedMigration struct {
	name     string
	version  int
	contents []byte
}

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
		path = "file:helm-memory?mode=memory&cache=shared"
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

// embeddedMigrations returns the complete embedded migration set in numeric
// order. It validates the set before any database mutation so a malformed
// release cannot create bookkeeping state and then fail halfway through
// startup. Duplicate versions are rejected even when their filenames differ.
func embeddedMigrations() ([]embeddedMigration, error) {
	entries, err := fs.ReadDir(Migrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	migrations := make([]embeddedMigration, 0, len(entries))
	versions := make(map[int]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, err := migrationVersion(entry.Name())
		if err != nil {
			return nil, err
		}
		if prior, exists := versions[version]; exists {
			return nil, fmt.Errorf("duplicate embedded migration version %d (%s and %s)", version, prior, entry.Name())
		}
		contents, err := fs.ReadFile(Migrations, "migrations/"+entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		versions[version] = entry.Name()
		migrations = append(migrations, embeddedMigration{name: entry.Name(), version: version, contents: contents})
	}
	if len(migrations) == 0 {
		return nil, fmt.Errorf("no embedded SQL migrations found")
	}
	sort.Slice(migrations, func(i, j int) bool {
		if migrations[i].version == migrations[j].version {
			return migrations[i].name < migrations[j].name
		}
		return migrations[i].version < migrations[j].version
	})
	for index, migration := range migrations {
		expected := index + 1
		if migration.version != expected {
			return nil, fmt.Errorf("embedded migration versions must be contiguous from 1 (found %d, want %d)", migration.version, expected)
		}
	}
	return migrations, nil
}

// EmbeddedSchema returns the latest version and deterministic SHA-256 digest
// of this binary's embedded migration set. The digest includes each numeric
// version, filename, byte length, and exact SQL bytes in numeric order, so a
// changed migration cannot silently share an old identity.
func EmbeddedSchema() (int, string, error) {
	migrations, err := embeddedMigrations()
	if err != nil {
		return 0, "", err
	}
	hash := sha256.New()
	for _, migration := range migrations {
		// Length-prefixing prevents concatenation ambiguities while retaining a
		// simple, reproducible representation suitable for sidecar metadata.
		if _, err := fmt.Fprintf(hash, "%d\n%d\n%s\n", migration.version, len(migration.contents), migration.name); err != nil {
			return 0, "", fmt.Errorf("hash migration %s: %w", migration.name, err)
		}
		if _, err := hash.Write(migration.contents); err != nil {
			return 0, "", fmt.Errorf("hash migration %s: %w", migration.name, err)
		}
		if _, err := hash.Write([]byte{'\n'}); err != nil {
			return 0, "", fmt.Errorf("hash migration %s: %w", migration.name, err)
		}
	}
	return migrations[len(migrations)-1].version, fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// LatestEmbeddedSchemaVersion exposes the latest valid version in the
// embedded migration set.
func LatestEmbeddedSchemaVersion() (int, error) {
	version, _, err := EmbeddedSchema()
	return version, err
}

// EmbeddedMigrationDigest exposes the deterministic migration-set digest.
func EmbeddedMigrationDigest() (string, error) {
	_, digest, err := EmbeddedSchema()
	return digest, err
}

// Migrate applies embedded migrations in version order. Each complete
// migration is atomic and can safely be retried after an interrupted startup.
// An unknown newer database version is intentionally left in place so a
// retained older binary can still start for rollback/recovery; schema-preflight
// is the explicit fail-closed candidate check.
func Migrate(ctx context.Context, database *sql.DB) error {
	migrations, err := embeddedMigrations()
	if err != nil {
		return err
	}
	if _, err := database.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	if err := validateAppliedMigrationRows(ctx, database); err != nil {
		return err
	}
	var highestApplied int64
	if err := database.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&highestApplied); err != nil {
		return fmt.Errorf("inspect latest applied migration: %w", err)
	}
	// A retained binary must not try to fill in its own missing bookkeeping or
	// objects when a newer release has already recorded a schema version. The
	// newer release owns that format; leaving the database untouched is the
	// rollback-compatible behavior.
	if highestApplied > int64(migrations[len(migrations)-1].version) {
		return nil
	}
	for _, migration := range migrations {
		var applied int
		if err := database.QueryRowContext(ctx, `SELECT COUNT(1) FROM schema_migrations WHERE version = ?`, migration.version).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %d: %w", migration.version, err)
		}
		if applied != 0 {
			continue
		}
		tx, err := database.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", migration.version, err)
		}
		if _, err := tx.ExecContext(ctx, string(migration.contents)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", migration.name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, migration.version, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %d: %w", migration.version, err)
		}
		if err := tx.Commit(); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("commit migration %d: %w", migration.version, err)
		}
	}
	return nil
}

func validateAppliedMigrationRows(ctx context.Context, database *sql.DB) error {
	rows, err := database.QueryContext(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return fmt.Errorf("inspect applied migrations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			return fmt.Errorf("inspect applied migration: %w", err)
		}
		if version <= 0 || version > int64(^uint(0)>>1) {
			return fmt.Errorf("invalid applied migration version %d", version)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect applied migrations: %w", err)
	}
	return nil
}

// InspectSchema reads migration bookkeeping without changing the database.
// It is safe to call against a database created by a newer release; such
// versions are surfaced through UnknownVersions instead of being rejected.
func InspectSchema(ctx context.Context, database *sql.DB) (SchemaInspection, error) {
	latest, digest, err := EmbeddedSchema()
	if err != nil {
		return SchemaInspection{}, err
	}
	inspection := SchemaInspection{
		EmbeddedSchemaVersion: latest,
		MigrationDigest:       digest,
		AppliedVersions:       []int{},
		PendingVersions:       []int{},
		UnknownVersions:       []int{},
	}

	var migrationTable int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&migrationTable); err != nil {
		return SchemaInspection{}, fmt.Errorf("inspect migration table: %w", err)
	}
	if migrationTable == 0 {
		for version := range embeddedVersionSet() {
			inspection.PendingVersions = append(inspection.PendingVersions, version)
		}
		sort.Ints(inspection.PendingVersions)
		return inspection, nil
	}

	rows, err := database.QueryContext(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return SchemaInspection{}, fmt.Errorf("read applied migrations: %w", err)
	}
	defer rows.Close()
	embedded := embeddedVersionSet()
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			return SchemaInspection{}, fmt.Errorf("read applied migration: %w", err)
		}
		if version <= 0 || version > int64(^uint(0)>>1) {
			return SchemaInspection{}, fmt.Errorf("invalid applied migration version %d", version)
		}
		v := int(version)
		inspection.AppliedVersions = append(inspection.AppliedVersions, v)
		if v > inspection.SchemaVersion {
			inspection.SchemaVersion = v
		}
		if !embedded[v] {
			inspection.UnknownVersions = append(inspection.UnknownVersions, v)
		}
	}
	if err := rows.Err(); err != nil {
		return SchemaInspection{}, fmt.Errorf("read applied migrations: %w", err)
	}
	for version := range embedded {
		if !containsVersion(inspection.AppliedVersions, version) {
			inspection.PendingVersions = append(inspection.PendingVersions, version)
		}
	}
	sort.Ints(inspection.PendingVersions)
	sort.Ints(inspection.UnknownVersions)
	return inspection, nil
}

func embeddedVersionSet() map[int]bool {
	migrations, err := embeddedMigrations()
	if err != nil {
		return map[int]bool{}
	}
	versions := make(map[int]bool, len(migrations))
	for _, migration := range migrations {
		versions[migration.version] = true
	}
	return versions
}

func containsVersion(versions []int, wanted int) bool {
	for _, version := range versions {
		if version == wanted {
			return true
		}
	}
	return false
}

// IntegrityCheck runs both SQLite consistency checks. A foreign-key check is
// intentionally separate from integrity_check because SQLite's latter does
// not guarantee that all relational references are valid.
func IntegrityCheck(ctx context.Context, database *sql.DB) error {
	rows, err := database.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return fmt.Errorf("sqlite integrity check: %w", err)
	}
	defer rows.Close()
	seen := false
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return fmt.Errorf("sqlite integrity check: %w", err)
		}
		seen = true
		if result != "ok" {
			return fmt.Errorf("sqlite integrity check failed: %s", result)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sqlite integrity check: %w", err)
	}
	if !seen {
		return fmt.Errorf("sqlite integrity check returned no result")
	}
	return nil
}

func ForeignKeyCheck(ctx context.Context, database *sql.DB) error {
	rows, err := database.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("sqlite foreign key check: %w", err)
	}
	defer rows.Close()
	violations := 0
	for rows.Next() {
		// The pragma currently returns four columns. Scanning into []any keeps
		// this check tolerant of SQLite-compatible drivers adding a detail while
		// still requiring that every row be consumed.
		columns, err := rows.Columns()
		if err != nil {
			return fmt.Errorf("sqlite foreign key check: %w", err)
		}
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return fmt.Errorf("sqlite foreign key check: %w", err)
		}
		violations++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sqlite foreign key check: %w", err)
	}
	if violations != 0 {
		return fmt.Errorf("sqlite foreign key check found %d violation(s)", violations)
	}
	return nil
}

// CheckIntegrity is a convenient combined consistency check for callers that
// need the same predicate as backup/restore helpers.
func CheckIntegrity(ctx context.Context, database *sql.DB) error {
	if err := IntegrityCheck(ctx, database); err != nil {
		return err
	}
	return ForeignKeyCheck(ctx, database)
}

// Preflight copies sourcePath into a private temporary database, migrates and
// validates only that copy, and removes it before returning. It never opens
// the supplied path through the writable application setup, so production
// data cannot be migrated by this operation. The source must be a standalone
// SQLite file; callers should use helm-backup's online backup first when a
// live database has a WAL sidecar.
func Preflight(ctx context.Context, sourcePath string) (SchemaInspection, error) {
	absolute, err := preflightSourcePath(sourcePath)
	if err != nil {
		return SchemaInspection{}, err
	}
	source, err := os.Open(absolute)
	if err != nil {
		return SchemaInspection{}, fmt.Errorf("open preflight source: %w", err)
	}
	defer source.Close()
	sourceInfo, err := source.Stat()
	if err != nil {
		return SchemaInspection{}, fmt.Errorf("inspect preflight source: %w", err)
	}
	pathInfo, err := os.Stat(absolute)
	if err != nil || !os.SameFile(sourceInfo, pathInfo) {
		return SchemaInspection{}, fmt.Errorf("preflight source changed while opening")
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		journalPath := absolute + suffix
		if info, statErr := os.Lstat(journalPath); statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 || info.Size() != 0 {
				return SchemaInspection{}, fmt.Errorf("preflight source has a live SQLite journal")
			}
		} else if !os.IsNotExist(statErr) {
			return SchemaInspection{}, fmt.Errorf("inspect preflight journal: %w", statErr)
		}
	}

	temporary, err := os.CreateTemp(filepath.Dir(absolute), ".helm-preflight-*")
	if err != nil {
		return SchemaInspection{}, fmt.Errorf("create preflight copy: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
		_ = os.Remove(temporaryPath + "-wal")
		_ = os.Remove(temporaryPath + "-shm")
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return SchemaInspection{}, fmt.Errorf("secure preflight copy: %w", err)
	}
	if _, err := io.Copy(temporary, source); err != nil {
		return SchemaInspection{}, fmt.Errorf("copy preflight source: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return SchemaInspection{}, fmt.Errorf("sync preflight copy: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return SchemaInspection{}, fmt.Errorf("close preflight copy: %w", err)
	}
	pathInfoAfter, err := os.Stat(absolute)
	if err != nil || !os.SameFile(sourceInfo, pathInfoAfter) {
		return SchemaInspection{}, fmt.Errorf("preflight source changed during copy")
	}
	if pathInfoAfter.Size() != sourceInfo.Size() || pathInfoAfter.ModTime() != sourceInfo.ModTime() {
		return SchemaInspection{}, fmt.Errorf("preflight source contents changed during copy")
	}

	database, err := Open(ctx, temporaryPath)
	if err != nil {
		return SchemaInspection{}, fmt.Errorf("migrate preflight copy: %w", err)
	}
	inspection, inspectErr := InspectSchema(ctx, database)
	if inspectErr == nil {
		inspectErr = CheckIntegrity(ctx, database)
	}
	closeErr := database.Close()
	if inspectErr != nil {
		return SchemaInspection{}, inspectErr
	}
	if closeErr != nil {
		return SchemaInspection{}, fmt.Errorf("close preflight database: %w", closeErr)
	}
	if len(inspection.UnknownVersions) != 0 {
		return SchemaInspection{}, fmt.Errorf("database contains schema newer than this binary")
	}
	if inspection.SchemaVersion != inspection.EmbeddedSchemaVersion || len(inspection.PendingVersions) != 0 {
		return SchemaInspection{}, fmt.Errorf("database schema is not at embedded version")
	}
	return inspection, nil
}

// MigrateCandidate upgrades one already-staged database path in place and
// validates the result. It is intentionally separate from Preflight: restore
// uses it only after copying a retained backup into a private, disposable
// staging directory. Callers must never pass the live production database.
func MigrateCandidate(ctx context.Context, candidatePath string) (SchemaInspection, error) {
	absolute, err := preflightSourcePath(candidatePath)
	if err != nil {
		return SchemaInspection{}, err
	}
	database, err := Open(ctx, absolute)
	if err != nil {
		return SchemaInspection{}, fmt.Errorf("migrate candidate: %w", err)
	}
	inspection, inspectErr := InspectSchema(ctx, database)
	if inspectErr == nil {
		inspectErr = CheckIntegrity(ctx, database)
	}
	closeErr := database.Close()
	if inspectErr != nil {
		return SchemaInspection{}, inspectErr
	}
	if closeErr != nil {
		return SchemaInspection{}, fmt.Errorf("close candidate database: %w", closeErr)
	}
	if len(inspection.UnknownVersions) != 0 {
		return SchemaInspection{}, fmt.Errorf("candidate database contains schema newer than this binary")
	}
	if inspection.SchemaVersion != inspection.EmbeddedSchemaVersion || len(inspection.PendingVersions) != 0 {
		return SchemaInspection{}, fmt.Errorf("candidate database schema is not at embedded version")
	}
	return inspection, nil
}

func preflightSourcePath(sourcePath string) (string, error) {
	if strings.TrimSpace(sourcePath) == "" || sourcePath == ":memory:" || strings.HasPrefix(sourcePath, "file:") {
		return "", fmt.Errorf("preflight source must be a filesystem path")
	}
	absolute, err := filepath.Abs(sourcePath)
	if err != nil {
		return "", fmt.Errorf("resolve preflight source: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", fmt.Errorf("inspect preflight source: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("preflight source must be a regular file")
	}
	return absolute, nil
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
