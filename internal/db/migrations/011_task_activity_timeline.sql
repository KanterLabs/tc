-- Task activity history is an additive read model.  The existing
-- task_agent_work row remains the efficient latest-snapshot model while each
-- published pulse receives one immutable history row for the task timeline.
-- The optional links let timeline reads collapse the generated progress
-- comment and its comment.created event without guessing from timestamps.
CREATE TABLE IF NOT EXISTS task_agent_work_history (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    operation_id TEXT NOT NULL,
    actor_id TEXT NOT NULL REFERENCES actors(id) ON DELETE RESTRICT,
    state TEXT NOT NULL CHECK (state IN ('working', 'waiting', 'verifying', 'handoff')),
    phase TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL,
    next_action TEXT NOT NULL DEFAULT '',
    checkpoint_refs TEXT NOT NULL DEFAULT '[]',
    checkpoint_completed INTEGER,
    checkpoint_total INTEGER,
    started_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    generated_comment_id TEXT REFERENCES comments(id) ON DELETE SET NULL,
    progress_event_cursor INTEGER REFERENCES events(cursor) ON DELETE SET NULL,
    CHECK (json_valid(checkpoint_refs) AND json_type(checkpoint_refs) = 'array'),
    CHECK (
        (checkpoint_completed IS NULL AND checkpoint_total IS NULL)
        OR (
            checkpoint_completed IS NOT NULL AND checkpoint_total IS NOT NULL
            AND checkpoint_completed >= 0
            AND checkpoint_completed <= checkpoint_total
            AND checkpoint_total >= 1
            AND checkpoint_total <= 100
        )
    )
);

-- Keep each source's task-scoped activity scan on a covering ordering prefix.
-- The type/state columns are retained in the indexes so filtered pages do not
-- need to scan unrelated task activity as the history grows.
CREATE INDEX IF NOT EXISTS task_agent_work_history_task_time_idx
    ON task_agent_work_history(task_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS task_agent_work_history_task_type_time_idx
    ON task_agent_work_history(task_id, state, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS comments_task_timeline_time_idx
    ON comments(task_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS events_task_timeline_type_time_idx
    ON events(task_id, created_at DESC, type, cursor DESC);
