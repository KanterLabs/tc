-- Project/column administration is additive so retained binaries can still
-- read and write the pre-administration fields during rollback. Versions are
-- resource-local validators for optimistic UI edits; archived columns remain
-- addressable for restore while the partial position index keeps active board
-- positions contiguous.
ALTER TABLE projects ADD COLUMN version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE columns ADD COLUMN archived_at TEXT;
ALTER TABLE columns ADD COLUMN version INTEGER NOT NULL DEFAULT 1;

DROP INDEX IF EXISTS columns_project_position_unique;
CREATE UNIQUE INDEX IF NOT EXISTS columns_project_position_unique
    ON columns(project_id, position) WHERE archived_at IS NULL;
CREATE INDEX IF NOT EXISTS columns_project_archived_idx ON columns(project_id, archived_at, position, id);
