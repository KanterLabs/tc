-- Persistent per-agent mutation reservations survive process restarts. An
-- operator can reset one actor's allowance with:
--   DELETE FROM actor_resource_usage WHERE actor_id = '<actor id>';
CREATE TABLE IF NOT EXISTS actor_resource_usage (
    actor_id TEXT PRIMARY KEY REFERENCES actors(id) ON DELETE CASCADE,
    reserved_bytes INTEGER NOT NULL CHECK (reserved_bytes >= 0),
    updated_at TEXT NOT NULL
);
