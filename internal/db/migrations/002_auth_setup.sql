-- The setup singleton was added after the initial schema shipped. Keeping it
-- as an idempotent migration also upgrades existing installations safely.
CREATE TABLE IF NOT EXISTS auth_setup (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    completed_at TEXT NOT NULL
);
INSERT OR IGNORE INTO auth_setup(id, completed_at)
SELECT 1, MIN(created_at) FROM actors WHERE kind = 'human' HAVING COUNT(1) > 0;
