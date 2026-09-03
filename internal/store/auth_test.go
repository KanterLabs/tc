package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/KanterLabs/helm/internal/db"
)

func TestLookupTokenThrottlesLastUsedAt(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, err := data.CreateActor(ctx, Actor{Kind: "agent", Name: "auth throttle"}, "")
	if err != nil {
		t.Fatalf("create actor: %v", err)
	}
	token, raw, err := data.CreateToken(ctx, actor.ID, "test token", []string{"tasks:read"}, nil, nil)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	if _, err := database.ExecContext(ctx, `CREATE TABLE token_last_used_updates (token_id TEXT NOT NULL)`); err != nil {
		t.Fatalf("create update counter: %v", err)
	}
	if _, err := database.ExecContext(ctx, `CREATE TRIGGER token_last_used_update_counter AFTER UPDATE OF last_used_at ON tokens BEGIN INSERT INTO token_last_used_updates(token_id) VALUES (NEW.id); END`); err != nil {
		t.Fatalf("create update counter trigger: %v", err)
	}

	if _, err := data.LookupToken(ctx, raw); err != nil {
		t.Fatalf("first token lookup: %v", err)
	}
	first := tokenLastUsedAtForTest(t, database, token.ID)
	if !first.Valid {
		t.Fatal("first token lookup did not record last_used_at")
	}
	if got := tokenLastUsedUpdateCountForTest(t, database); got != 1 {
		t.Fatalf("last_used_at updates after first lookup = %d, want 1", got)
	}

	if _, err := data.LookupToken(ctx, raw); err != nil {
		t.Fatalf("second token lookup: %v", err)
	}
	second := tokenLastUsedAtForTest(t, database, token.ID)
	if second.String != first.String {
		t.Fatalf("last_used_at after lookup within throttle = %q, want unchanged %q", second.String, first.String)
	}
	if got := tokenLastUsedUpdateCountForTest(t, database); got != 1 {
		t.Fatalf("last_used_at updates after lookup within throttle = %d, want 1", got)
	}
}

func TestLookupTokenRefreshesOldLastUsedAt(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, err := data.CreateActor(ctx, Actor{Kind: "agent", Name: "old auth metadata"}, "")
	if err != nil {
		t.Fatalf("create actor: %v", err)
	}
	token, raw, err := data.CreateToken(ctx, actor.ID, "test token", []string{"tasks:read"}, nil, nil)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	oldTime := time.Now().UTC().Add(-(tokenLastUsedThrottle + time.Minute))
	oldValue := oldTime.Format(time.RFC3339Nano)
	if _, err := database.ExecContext(ctx, `UPDATE tokens SET last_used_at = ? WHERE id = ?`, oldValue, token.ID); err != nil {
		t.Fatalf("set old last_used_at: %v", err)
	}
	if _, err := database.ExecContext(ctx, `CREATE TABLE token_last_used_updates (token_id TEXT NOT NULL)`); err != nil {
		t.Fatalf("create update counter: %v", err)
	}
	if _, err := database.ExecContext(ctx, `CREATE TRIGGER token_last_used_update_counter AFTER UPDATE OF last_used_at ON tokens BEGIN INSERT INTO token_last_used_updates(token_id) VALUES (NEW.id); END`); err != nil {
		t.Fatalf("create update counter trigger: %v", err)
	}

	if _, err := data.LookupToken(ctx, raw); err != nil {
		t.Fatalf("token lookup with old metadata: %v", err)
	}
	got := tokenLastUsedAtForTest(t, database, token.ID)
	if !got.Valid {
		t.Fatal("token lookup removed last_used_at")
	}
	gotTime, err := time.Parse(time.RFC3339Nano, got.String)
	if err != nil {
		t.Fatalf("parse refreshed last_used_at %q: %v", got.String, err)
	}
	if !gotTime.After(oldTime) {
		t.Fatalf("refreshed last_used_at = %s, want after %s", gotTime, oldTime)
	}
	if got := tokenLastUsedUpdateCountForTest(t, database); got != 1 {
		t.Fatalf("last_used_at updates after old metadata lookup = %d, want 1", got)
	}
}

func TestActorForSessionThrottlesLastSeenAt(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, err := data.CreateActor(ctx, Actor{Kind: "human", Name: "session throttle"}, "")
	if err != nil {
		t.Fatalf("create actor: %v", err)
	}
	const raw = "session-secret"
	if err := data.CreateSession(ctx, actor.ID, raw, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if _, err := database.ExecContext(ctx, `CREATE TABLE session_last_seen_updates (session_hash TEXT NOT NULL)`); err != nil {
		t.Fatalf("create update counter: %v", err)
	}
	if _, err := database.ExecContext(ctx, `CREATE TRIGGER session_last_seen_update_counter AFTER UPDATE OF last_seen_at ON sessions BEGIN INSERT INTO session_last_seen_updates(session_hash) VALUES (NEW.token_hash); END`); err != nil {
		t.Fatalf("create update counter trigger: %v", err)
	}

	if _, err := data.ActorForSession(ctx, raw); err != nil {
		t.Fatalf("first session lookup: %v", err)
	}
	first := sessionLastSeenAtForTest(t, database, raw)
	if !first.Valid {
		t.Fatal("first session lookup did not preserve last_seen_at")
	}
	if got := sessionLastSeenUpdateCountForTest(t, database); got != 0 {
		t.Fatalf("last_seen_at updates after first lookup within throttle = %d, want 0", got)
	}

	if _, err := data.ActorForSession(ctx, raw); err != nil {
		t.Fatalf("second session lookup: %v", err)
	}
	second := sessionLastSeenAtForTest(t, database, raw)
	if second.String != first.String {
		t.Fatalf("last_seen_at after lookup within throttle = %q, want unchanged %q", second.String, first.String)
	}
	if got := sessionLastSeenUpdateCountForTest(t, database); got != 0 {
		t.Fatalf("last_seen_at updates after lookup within throttle = %d, want 0", got)
	}
}

func TestActorForSessionRefreshesOldLastSeenAt(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	data := New(database)
	actor, err := data.CreateActor(ctx, Actor{Kind: "human", Name: "old session metadata"}, "")
	if err != nil {
		t.Fatalf("create actor: %v", err)
	}
	const raw = "old-session-secret"
	if err := data.CreateSession(ctx, actor.ID, raw, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("create session: %v", err)
	}

	oldTime := time.Now().UTC().Add(-(tokenLastUsedThrottle + time.Minute))
	oldValue := oldTime.Format(time.RFC3339Nano)
	if _, err := database.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE token_hash = ?`, oldValue, HashSecret(raw)); err != nil {
		t.Fatalf("set old last_seen_at: %v", err)
	}
	if _, err := database.ExecContext(ctx, `CREATE TABLE session_last_seen_updates (session_hash TEXT NOT NULL)`); err != nil {
		t.Fatalf("create update counter: %v", err)
	}
	if _, err := database.ExecContext(ctx, `CREATE TRIGGER session_last_seen_update_counter AFTER UPDATE OF last_seen_at ON sessions BEGIN INSERT INTO session_last_seen_updates(session_hash) VALUES (NEW.token_hash); END`); err != nil {
		t.Fatalf("create update counter trigger: %v", err)
	}

	if _, err := data.ActorForSession(ctx, raw); err != nil {
		t.Fatalf("session lookup with old metadata: %v", err)
	}
	got := sessionLastSeenAtForTest(t, database, raw)
	if !got.Valid {
		t.Fatal("session lookup removed last_seen_at")
	}
	gotTime, err := time.Parse(time.RFC3339Nano, got.String)
	if err != nil {
		t.Fatalf("parse refreshed last_seen_at %q: %v", got.String, err)
	}
	if !gotTime.After(oldTime) {
		t.Fatalf("refreshed last_seen_at = %s, want after %s", gotTime, oldTime)
	}
	if got := sessionLastSeenUpdateCountForTest(t, database); got != 1 {
		t.Fatalf("last_seen_at updates after old metadata lookup = %d, want 1", got)
	}
}

func tokenLastUsedAtForTest(t *testing.T, database *sql.DB, tokenID string) sql.NullString {
	t.Helper()
	var lastUsed sql.NullString
	if err := database.QueryRow(`SELECT last_used_at FROM tokens WHERE id = ?`, tokenID).Scan(&lastUsed); err != nil {
		t.Fatalf("read last_used_at: %v", err)
	}
	return lastUsed
}

func tokenLastUsedUpdateCountForTest(t *testing.T, database *sql.DB) int {
	t.Helper()
	var count int
	if err := database.QueryRow(`SELECT COUNT(1) FROM token_last_used_updates`).Scan(&count); err != nil {
		t.Fatalf("read last_used_at update count: %v", err)
	}
	return count
}

func sessionLastSeenAtForTest(t *testing.T, database *sql.DB, raw string) sql.NullString {
	t.Helper()
	var lastSeen sql.NullString
	if err := database.QueryRow(`SELECT last_seen_at FROM sessions WHERE token_hash = ?`, HashSecret(raw)).Scan(&lastSeen); err != nil {
		t.Fatalf("read last_seen_at: %v", err)
	}
	return lastSeen
}

func sessionLastSeenUpdateCountForTest(t *testing.T, database *sql.DB) int {
	t.Helper()
	var count int
	if err := database.QueryRow(`SELECT COUNT(1) FROM session_last_seen_updates`).Scan(&count); err != nil {
		t.Fatalf("read last_seen_at update count: %v", err)
	}
	return count
}
