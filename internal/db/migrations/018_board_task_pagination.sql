-- Board collection reads use per-column keyset cursors. These additive
-- indexes keep the common filtered scans bounded without changing any task
-- rows or existing identifiers, and remain safe for retained binaries that
-- do not know about this migration.
CREATE INDEX IF NOT EXISTS tasks_project_column_order_idx
    ON tasks(project_id, column_id, position, number, id)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS tasks_project_created_order_idx
    ON tasks(project_id, created_at, number, id)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS tasks_project_updated_order_idx
    ON tasks(project_id, updated_at, number, id)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS tasks_project_number_order_idx
    ON tasks(project_id, number, id)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS tasks_project_priority_order_idx
    ON tasks(project_id, priority, number, id)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS tasks_project_title_order_idx
    ON tasks(project_id, lower(title), number, id)
    WHERE deleted_at IS NULL;
