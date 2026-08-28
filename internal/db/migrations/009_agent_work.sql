-- Agent work is a single live snapshot per task. Keeping the pulse separate
-- from tasks preserves the existing task description contract while allowing
-- agents to publish progress (and advance the task version) independently.
CREATE TABLE IF NOT EXISTS task_agent_work (
    task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
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
    updated_at TEXT NOT NULL,
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

CREATE INDEX IF NOT EXISTS task_agent_work_state_idx
    ON task_agent_work(state, updated_at, task_id);
CREATE INDEX IF NOT EXISTS task_agent_work_updated_idx
    ON task_agent_work(updated_at DESC, task_id);
