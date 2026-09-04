-- Task checklists are additive to the task and project rows.  Checklist
-- mutations advance the owning task version in the store, while this table
-- keeps item text and completion metadata out of the existing task record.
-- The defaults preserve every existing project and task during upgrade.
ALTER TABLE projects ADD COLUMN checklist_completion_policy TEXT NOT NULL DEFAULT 'warn'
    CHECK (checklist_completion_policy IN ('warn', 'require'));

CREATE TABLE IF NOT EXISTS task_checklist_items (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    text TEXT NOT NULL CHECK (length(trim(text)) > 0 AND length(text) <= 1000),
    position INTEGER NOT NULL CHECK (position >= 0),
    completed INTEGER NOT NULL DEFAULT 0 CHECK (completed IN (0, 1)),
    completed_at TEXT,
    completed_by TEXT REFERENCES actors(id) ON DELETE SET NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS task_checklist_items_task_position_idx
    ON task_checklist_items(task_id, position, id);

CREATE INDEX IF NOT EXISTS task_checklist_items_task_updated_idx
    ON task_checklist_items(task_id, updated_at DESC, id DESC);

-- Protect the cardinality limit for direct SQL callers and retained binaries.
-- The store enforces the same limit before opening its mutation transaction,
-- while this trigger closes the gap for older binaries writing the table.
CREATE TRIGGER IF NOT EXISTS task_checklist_items_limit_insert
BEFORE INSERT ON task_checklist_items
BEGIN
    SELECT RAISE(ABORT, 'checklist_limit_exceeded')
    WHERE (SELECT COUNT(1) FROM task_checklist_items WHERE task_id = NEW.task_id) >= 100;
END;

CREATE TRIGGER IF NOT EXISTS task_checklist_items_text_limit_insert
BEFORE INSERT ON task_checklist_items
BEGIN
    SELECT RAISE(ABORT, 'checklist_limit_exceeded')
    WHERE COALESCE((SELECT SUM(length(CAST(text AS BLOB))) FROM task_checklist_items WHERE task_id = NEW.task_id), 0) + length(CAST(NEW.text AS BLOB)) > 100000;
END;

CREATE TRIGGER IF NOT EXISTS task_checklist_items_text_limit_update
BEFORE UPDATE OF text ON task_checklist_items
BEGIN
    SELECT RAISE(ABORT, 'checklist_limit_exceeded')
    WHERE COALESCE((SELECT SUM(length(CAST(text AS BLOB))) FROM task_checklist_items WHERE task_id = OLD.task_id), 0) - length(CAST(OLD.text AS BLOB)) + length(CAST(NEW.text AS BLOB)) > 100000;
END;
