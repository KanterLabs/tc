-- TC-115 ordering metadata.  Existing task positions are already the
-- durable order key; this additive revision lets clients detect a reorder of
-- a neighboring card even when their task's own version is unchanged.
--
-- ALTER TABLE preserves every populated column/task row and the default keeps
-- retained binaries that do not mention this column fully compatible.
ALTER TABLE columns ADD COLUMN ordering_version INTEGER NOT NULL DEFAULT 1 CHECK (ordering_version > 0);

CREATE INDEX IF NOT EXISTS columns_ordering_revision_idx
    ON columns(project_id, id, ordering_version);

-- Keep the revision useful to retained binaries and lifecycle paths that
-- update task rows directly. Position/column membership changes are the
-- ordering boundary; deleted cards leave the visible order as well.
CREATE TRIGGER IF NOT EXISTS task_ordering_revision_on_insert
AFTER INSERT ON tasks
WHEN NEW.deleted_at IS NULL
BEGIN
    UPDATE columns
       SET ordering_version=ordering_version+1
     WHERE id=NEW.column_id AND ordering_version < 9223372036854775807;
END;

CREATE TRIGGER IF NOT EXISTS task_ordering_revision_on_update
AFTER UPDATE OF column_id, position, deleted_at ON tasks
WHEN OLD.deleted_at IS NULL AND (
        OLD.column_id <> NEW.column_id OR OLD.position <> NEW.position OR NEW.deleted_at IS NOT NULL
    )
BEGIN
    UPDATE columns
       SET ordering_version=ordering_version+1
     WHERE id=OLD.column_id AND ordering_version < 9223372036854775807;
    UPDATE columns
       SET ordering_version=ordering_version+1
     WHERE id=NEW.column_id AND NEW.deleted_at IS NULL AND NEW.column_id <> OLD.column_id AND ordering_version < 9223372036854775807;
END;

CREATE TRIGGER IF NOT EXISTS task_ordering_revision_on_restore
AFTER UPDATE OF deleted_at ON tasks
WHEN OLD.deleted_at IS NOT NULL AND NEW.deleted_at IS NULL
BEGIN
    UPDATE columns
       SET ordering_version=ordering_version+1
     WHERE id=NEW.column_id AND ordering_version < 9223372036854775807;
END;
