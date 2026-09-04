-- Task hierarchy is a single, project-local parent edge.  It is deliberately
-- additive so retained binaries can continue to read and write the original
-- task columns while newer binaries use parent_task_id when present.
ALTER TABLE tasks ADD COLUMN parent_task_id TEXT REFERENCES tasks(id) ON DELETE RESTRICT;

CREATE INDEX IF NOT EXISTS tasks_parent_idx
    ON tasks(parent_task_id, project_id, deleted_at, number, id);

-- Protect direct SQL writers as well as the store boundary.  The proposed
-- parent row is not visible to a BEFORE trigger, so the fan-out count uses the
-- rows that already exist and the recursive CTE is bounded at one level past
-- the public depth limit.  A malformed legacy cycle therefore fails closed
-- instead of consuming unbounded work.
CREATE TRIGGER IF NOT EXISTS task_hierarchy_validate_insert
BEFORE INSERT ON tasks
WHEN NEW.parent_task_id IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'hierarchy_self_reference')
    WHERE NEW.id = NEW.parent_task_id;

    SELECT RAISE(ABORT, 'hierarchy_cross_project_or_not_live')
    WHERE NOT EXISTS (
        SELECT 1
        FROM tasks parent
        WHERE parent.id = NEW.parent_task_id
          AND parent.project_id = NEW.project_id
          AND parent.deleted_at IS NULL
    );

    SELECT RAISE(ABORT, 'hierarchy_fanout_exceeded')
    WHERE (
        SELECT COUNT(1)
        FROM tasks child
        WHERE child.parent_task_id = NEW.parent_task_id
          AND child.project_id = NEW.project_id
          AND child.deleted_at IS NULL
    ) >= 200;

    SELECT RAISE(ABORT, 'hierarchy_depth_exceeded')
    WHERE EXISTS (
        WITH RECURSIVE parent_chain(id, depth) AS (
            SELECT NEW.parent_task_id, 1
            UNION ALL
            SELECT parent.parent_task_id, parent_chain.depth + 1
            FROM tasks parent
            JOIN parent_chain ON parent.id = parent_chain.id
            WHERE parent.parent_task_id IS NOT NULL
              AND parent_chain.depth < 22
        )
        SELECT 1
        FROM parent_chain
        WHERE depth > 20 OR id = NEW.id
    );
END;

CREATE TRIGGER IF NOT EXISTS task_hierarchy_validate_parent_update
BEFORE UPDATE OF parent_task_id ON tasks
WHEN NEW.parent_task_id IS NOT OLD.parent_task_id
BEGIN
    SELECT RAISE(ABORT, 'hierarchy_self_reference')
    WHERE NEW.parent_task_id IS NOT NULL
      AND NEW.id = NEW.parent_task_id;

    SELECT RAISE(ABORT, 'hierarchy_cross_project_or_not_live')
    WHERE NEW.parent_task_id IS NOT NULL
      AND NOT EXISTS (
          SELECT 1
          FROM tasks parent
          WHERE parent.id = NEW.parent_task_id
            AND parent.project_id = NEW.project_id
            AND parent.deleted_at IS NULL
      );

    SELECT RAISE(ABORT, 'hierarchy_fanout_exceeded')
    WHERE NEW.parent_task_id IS NOT NULL
      AND (
          SELECT COUNT(1)
          FROM tasks child
          WHERE child.parent_task_id = NEW.parent_task_id
            AND child.project_id = NEW.project_id
            AND child.deleted_at IS NULL
            AND child.id <> NEW.id
      ) >= 200;

    SELECT RAISE(ABORT, 'hierarchy_depth_exceeded')
    WHERE NEW.parent_task_id IS NOT NULL
      AND EXISTS (
          WITH RECURSIVE parent_chain(id, depth) AS (
              SELECT NEW.parent_task_id, 1
              UNION ALL
              SELECT parent.parent_task_id, parent_chain.depth + 1
              FROM tasks parent
              JOIN parent_chain ON parent.id = parent_chain.id
              WHERE parent.parent_task_id IS NOT NULL
                AND parent_chain.depth < 22
          ),
          descendants(id, depth) AS (
              SELECT NEW.id, 0
              UNION ALL
              SELECT child.id, descendants.depth + 1
              FROM tasks child
              JOIN descendants ON child.parent_task_id = descendants.id
              WHERE child.deleted_at IS NULL
                AND descendants.depth < 22
          )
          SELECT 1
          FROM parent_chain, descendants
          WHERE parent_chain.id = NEW.id
             OR parent_chain.depth + descendants.depth > 20
      );
END;

-- A parent cannot be soft-deleted while a live child still points at it.
-- Physical DELETEs are additionally rejected by the self foreign key.  Child
-- deletion remains safe and leaves the historical row's parent pointer intact
-- for rollback/recovery inspection while all live reads hide that row.
CREATE TRIGGER IF NOT EXISTS task_hierarchy_guard_delete
BEFORE UPDATE OF deleted_at ON tasks
WHEN OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'hierarchy_parent_in_use')
    WHERE EXISTS (
        SELECT 1
        FROM tasks child
        WHERE child.parent_task_id = OLD.id
          AND child.project_id = OLD.project_id
          AND child.deleted_at IS NULL
    );
END;

-- project_id is not an application-editable field, but retaining the invariant
-- at the database edge prevents a direct maintenance writer from moving a
-- linked task across project boundaries.
CREATE TRIGGER IF NOT EXISTS task_hierarchy_guard_project_update
BEFORE UPDATE OF project_id ON tasks
WHEN NEW.project_id IS NOT OLD.project_id
BEGIN
    SELECT RAISE(ABORT, 'hierarchy_cross_project_or_not_live')
    WHERE NEW.parent_task_id IS NOT NULL
      AND NOT EXISTS (
          SELECT 1
          FROM tasks parent
          WHERE parent.id = NEW.parent_task_id
            AND parent.project_id = NEW.project_id
            AND parent.deleted_at IS NULL
      );

    SELECT RAISE(ABORT, 'hierarchy_cross_project_or_not_live')
    WHERE EXISTS (
        SELECT 1
        FROM tasks child
        WHERE child.parent_task_id = NEW.id
          AND child.project_id <> NEW.project_id
          AND child.deleted_at IS NULL
    );
END;
