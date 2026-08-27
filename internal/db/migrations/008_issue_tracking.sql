-- Typed issue tracking builds on tasks so existing task IDs, keys, claims,
-- comments, labels, deleted rows, and optimistic versions remain unchanged.
-- SQLite appends the column with its default for every pre-existing row.
ALTER TABLE tasks ADD COLUMN kind TEXT NOT NULL DEFAULT 'task'
    CHECK (kind IN ('task', 'bug'));

CREATE INDEX IF NOT EXISTS tasks_kind_idx ON tasks(project_id, kind, updated_at, id);

-- Bug-only metadata is kept in a 1:1 table. Optional text is represented as
-- an empty string for stable JSON output; severity and lifecycle fields remain
-- nullable until the corresponding workflow action occurs.
CREATE TABLE IF NOT EXISTS bug_details (
    task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
    reporter_id TEXT NOT NULL REFERENCES actors(id) ON DELETE RESTRICT,
    severity TEXT CHECK (severity IS NULL OR severity IN ('s1', 's2', 's3', 's4')),
    actual_behavior TEXT NOT NULL CHECK (length(trim(actual_behavior)) > 0),
    expected_behavior TEXT NOT NULL DEFAULT '',
    reproduction_steps TEXT NOT NULL DEFAULT '',
    environment TEXT NOT NULL DEFAULT '',
    affected_version TEXT NOT NULL DEFAULT '',
    resolution TEXT CHECK (resolution IS NULL OR resolution IN ('fixed', 'duplicate', 'not_planned', 'cannot_reproduce', 'works_as_designed')),
    resolved_by TEXT REFERENCES actors(id) ON DELETE SET NULL,
    resolved_at TEXT,
    duplicate_of TEXT REFERENCES tasks(id) ON DELETE SET NULL
);
CREATE INDEX IF NOT EXISTS bug_details_severity_idx ON bug_details(severity, task_id);
CREATE INDEX IF NOT EXISTS bug_details_reporter_idx ON bug_details(reporter_id, task_id);
CREATE INDEX IF NOT EXISTS bug_details_resolution_idx ON bug_details(resolution, task_id);

-- A generic directed link table makes duplicate-of validation extensible and
-- lets a recursive query reject cycles before the bug detail is updated.
CREATE TABLE IF NOT EXISTS task_links (
    source_task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    target_task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    link_type TEXT NOT NULL CHECK (length(trim(link_type)) > 0),
    created_at TEXT NOT NULL,
    PRIMARY KEY(source_task_id, target_task_id, link_type),
    CHECK (source_task_id <> target_task_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS task_links_source_type_idx
    ON task_links(source_task_id, link_type);
CREATE INDEX IF NOT EXISTS task_links_target_type_idx
    ON task_links(target_task_id, link_type);
