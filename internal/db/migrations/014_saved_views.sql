-- Saved views are named, shareable combinations of search filters and sort
-- fields.  The JSON columns intentionally keep this migration additive so a
-- retained binary can continue to read the existing task model while a newer
-- binary adds view state.
CREATE TABLE IF NOT EXISTS saved_views (
    id TEXT PRIMARY KEY,
    actor_id TEXT NOT NULL REFERENCES actors(id) ON DELETE CASCADE,
    name TEXT NOT NULL CHECK (length(trim(name)) > 0 AND length(name) <= 200),
    description TEXT NOT NULL DEFAULT '',
    filters TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(filters) AND json_type(filters) = 'object'),
    sort TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(sort) AND json_type(sort) = 'array'),
    shared INTEGER NOT NULL DEFAULT 0 CHECK (shared IN (0, 1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(actor_id, name)
);

CREATE INDEX IF NOT EXISTS saved_views_actor_updated_idx
    ON saved_views(actor_id, updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS saved_views_shared_updated_idx
    ON saved_views(shared, updated_at DESC, id DESC);

-- Search predicates commonly combine a project ceiling with state, priority,
-- ownership, or due-date filters.  These indexes keep those selective parts
-- indexable even though free-text q searches necessarily use a bounded scan.
CREATE INDEX IF NOT EXISTS tasks_search_scope_idx
    ON tasks(project_id, column_id, priority, due_at, updated_at, id);
CREATE INDEX IF NOT EXISTS task_labels_label_task_idx
    ON task_labels(label_id, task_id);
