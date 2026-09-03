-- Task dependencies are a bounded, same-project directed graph.  The table
-- is deliberately separate from task_links: one task may have many
-- prerequisites while task_links retains its one-link-per-source/type
-- duplicate-resolution contract.
CREATE TABLE IF NOT EXISTS task_dependencies (
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    prerequisite_task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    created_by TEXT REFERENCES actors(id) ON DELETE SET NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (task_id, prerequisite_task_id),
    CHECK (task_id <> prerequisite_task_id)
);

CREATE INDEX IF NOT EXISTS task_dependencies_prerequisite_idx
    ON task_dependencies(prerequisite_task_id, task_id);

-- This trigger protects direct SQL callers as well as the dependency store
-- implementation.  The graph-size checks count existing rows because the
-- proposed row is not visible to a BEFORE trigger.  The recursive check is
-- intentionally in the same write transaction as the insert, so a direct
-- caller cannot commit a self or transitive cycle behind the store checks.
CREATE TRIGGER IF NOT EXISTS task_dependencies_validate_insert
BEFORE INSERT ON task_dependencies
BEGIN
    SELECT RAISE(ABORT, 'dependency_cross_project_or_not_live')
    WHERE NOT EXISTS (
        SELECT 1
        FROM tasks dependent
        JOIN tasks prerequisite ON prerequisite.id = NEW.prerequisite_task_id
        WHERE dependent.id = NEW.task_id
          AND dependent.project_id = prerequisite.project_id
          AND dependent.deleted_at IS NULL
          AND prerequisite.deleted_at IS NULL
    );

    SELECT RAISE(ABORT, 'dependency_limit_exceeded_prerequisites')
    WHERE (
        SELECT COUNT(1)
        FROM task_dependencies
        WHERE task_id = NEW.task_id
    ) >= 200;

    SELECT RAISE(ABORT, 'dependency_limit_exceeded_dependents')
    WHERE (
        SELECT COUNT(1)
        FROM task_dependencies
        WHERE prerequisite_task_id = NEW.prerequisite_task_id
    ) >= 200;

    SELECT RAISE(ABORT, 'dependency_cycle')
    WHERE EXISTS (
        WITH RECURSIVE ancestors(task_id) AS (
            SELECT NEW.prerequisite_task_id
            UNION
            SELECT dependency.prerequisite_task_id
            FROM task_dependencies dependency
            JOIN ancestors ancestor ON ancestor.task_id = dependency.task_id
        )
        SELECT 1
        FROM ancestors
        WHERE task_id = NEW.task_id
    );

    -- Adding an unfinished prerequisite to a task that has already started
    -- (or is being completed) would invalidate work that the old binary can
    -- no longer reason about.  Include NEW in the proposed prerequisite set;
    -- BEFORE INSERT triggers cannot see the row in task_dependencies yet.
    SELECT RAISE(ABORT, 'unmet_dependencies')
    WHERE EXISTS (
        SELECT 1
        FROM tasks dependent
        JOIN columns dependent_column
          ON dependent_column.id = dependent.column_id
         AND dependent_column.project_id = dependent.project_id
        WHERE dependent.id = NEW.task_id
          AND dependent.deleted_at IS NULL
          AND (
              dependent_column.semantic_state IN ('active', 'completed')
              OR (
                  dependent.claimed_by IS NOT NULL
                  AND dependent.claim_expires_at IS NOT NULL
                  AND julianday(dependent.claim_expires_at) > julianday('now')
              )
          )
          AND EXISTS (
              WITH proposed_prerequisites(prerequisite_task_id) AS (
                  SELECT NEW.prerequisite_task_id
                  UNION ALL
                  SELECT dependency.prerequisite_task_id
                  FROM task_dependencies dependency
                  WHERE dependency.task_id = NEW.task_id
              )
              SELECT 1
              FROM proposed_prerequisites proposed
              LEFT JOIN tasks prerequisite
                ON prerequisite.id = proposed.prerequisite_task_id
              LEFT JOIN columns prerequisite_column
                ON prerequisite_column.id = prerequisite.column_id
               AND prerequisite_column.project_id = prerequisite.project_id
              WHERE prerequisite.id IS NULL
                 OR prerequisite.deleted_at IS NOT NULL
                 OR prerequisite.project_id <> dependent.project_id
                 OR prerequisite_column.id IS NULL
                 OR prerequisite_column.semantic_state <> 'completed'
                 OR prerequisite.completed_at IS NULL
          )
    );
END;

-- A retained pre-012 binary still writes the original tasks row directly.
-- Keep dependency invariants at that boundary rather than relying solely on
-- newer store code.  A task deletion removes its own links after checking
-- that no live dependent would be left pointing at a deleted prerequisite.
CREATE TRIGGER IF NOT EXISTS task_dependencies_guard_task_update
BEFORE UPDATE OF column_id, claimed_by, claim_expires_at, completed_at, deleted_at
ON tasks
BEGIN
    SELECT RAISE(ABORT, 'dependency_in_use')
    WHERE OLD.deleted_at IS NULL
      AND NEW.deleted_at IS NOT NULL
      AND EXISTS (
          SELECT 1
          FROM task_dependencies dependency
          JOIN tasks dependent ON dependent.id = dependency.task_id
          WHERE dependency.prerequisite_task_id = OLD.id
            AND dependent.deleted_at IS NULL
      );

    -- Claim, start, and complete all require every live prerequisite to be
    -- in a completed column with a completion timestamp.  The state change
    -- test intentionally uses the pre-update column state: column semantic
    -- state is changed before its bulk task update, and the column trigger
    -- below validates that operation as a set.
    SELECT RAISE(ABORT, 'unmet_dependencies')
    WHERE NEW.deleted_at IS NULL
      AND (
          (
              NEW.claimed_by IS NOT NULL
              AND (
                  OLD.claimed_by IS NULL
                  OR OLD.claimed_by IS NOT NEW.claimed_by
                  OR OLD.claim_expires_at IS NOT NEW.claim_expires_at
              )
          )
          OR (
              COALESCE((
                  SELECT destination.semantic_state
                  FROM columns destination
                  WHERE destination.id = NEW.column_id
                    AND destination.project_id = NEW.project_id
              ), '') = 'active'
              AND COALESCE((
                  SELECT source.semantic_state
                  FROM columns source
                  WHERE source.id = OLD.column_id
                    AND source.project_id = OLD.project_id
              ), '') <> 'active'
          )
          OR (
              COALESCE((
                  SELECT destination.semantic_state
                  FROM columns destination
                  WHERE destination.id = NEW.column_id
                    AND destination.project_id = NEW.project_id
              ), '') = 'completed'
              AND (
                  COALESCE((
                      SELECT source.semantic_state
                      FROM columns source
                      WHERE source.id = OLD.column_id
                        AND source.project_id = OLD.project_id
                  ), '') <> 'completed'
                  OR (
                      OLD.completed_at IS NULL
                      AND NEW.completed_at IS NOT NULL
                  )
              )
          )
      )
      AND EXISTS (
          SELECT 1
          FROM task_dependencies dependency
          LEFT JOIN tasks prerequisite
            ON prerequisite.id = dependency.prerequisite_task_id
          LEFT JOIN columns prerequisite_column
            ON prerequisite_column.id = prerequisite.column_id
           AND prerequisite_column.project_id = prerequisite.project_id
          WHERE dependency.task_id = NEW.id
            AND (
                prerequisite.id IS NULL
                OR prerequisite.deleted_at IS NOT NULL
                OR prerequisite.project_id <> NEW.project_id
                OR prerequisite_column.id IS NULL
                OR prerequisite_column.semantic_state <> 'completed'
                OR prerequisite.completed_at IS NULL
            )
      );

    -- Leaving a completed state is a reopen.  An unfinished dependent that
    -- is active or actively claimed has already started work against this
    -- prerequisite, so the old binary must fail atomically.
    SELECT RAISE(ABORT, 'dependency_in_use')
    WHERE OLD.deleted_at IS NULL
      AND NEW.deleted_at IS NULL
      AND COALESCE((
          SELECT source.semantic_state
          FROM columns source
          WHERE source.id = OLD.column_id
            AND source.project_id = OLD.project_id
      ), '') = 'completed'
      AND (
          COALESCE((
              SELECT destination.semantic_state
              FROM columns destination
              WHERE destination.id = NEW.column_id
                AND destination.project_id = NEW.project_id
          ), '') <> 'completed'
          OR NEW.completed_at IS NULL
      )
      AND EXISTS (
          SELECT 1
          FROM task_dependencies dependency
          JOIN tasks dependent ON dependent.id = dependency.task_id
          LEFT JOIN columns dependent_column
            ON dependent_column.id = dependent.column_id
           AND dependent_column.project_id = dependent.project_id
          WHERE dependency.prerequisite_task_id = OLD.id
            AND dependent.deleted_at IS NULL
            AND (
                COALESCE(dependent_column.semantic_state, '') = 'active'
                OR (
                    dependent.claimed_by IS NOT NULL
                    AND dependent.claim_expires_at IS NOT NULL
                    AND julianday(dependent.claim_expires_at) > julianday('now')
                )
            )
            AND (
                COALESCE(dependent_column.semantic_state, '') <> 'completed'
                OR dependent.completed_at IS NULL
            )
      );

    -- Deleting a dependent is allowed, but its edges must not remain as
    -- references to a non-live task.  Incoming edges are also removed when
    -- all dependents are already non-live; the check above rejects any live
    -- dependent before this cleanup executes.
    DELETE FROM task_dependencies
    WHERE OLD.deleted_at IS NULL
      AND NEW.deleted_at IS NOT NULL
      AND (task_id = OLD.id OR prerequisite_task_id = OLD.id);
END;

-- UpdateColumn changes a column first and then updates every contained task.
-- Validate the dependency graph before that bulk state change.  In
-- particular, an unfinished prerequisite in the same column is not treated as
-- atomically satisfied: a blocked task cannot cross the finish boundary just
-- because SQLite will update all rows in one statement.
CREATE TRIGGER IF NOT EXISTS task_dependencies_guard_column_update
BEFORE UPDATE OF semantic_state ON columns
WHEN OLD.semantic_state IS NOT NEW.semantic_state
BEGIN
    SELECT RAISE(ABORT, 'unmet_dependencies')
    WHERE NEW.semantic_state IN ('active', 'completed')
      AND EXISTS (
          SELECT 1
          FROM tasks dependent
          WHERE dependent.column_id = NEW.id
            AND dependent.project_id = NEW.project_id
            AND dependent.deleted_at IS NULL
            AND EXISTS (
                SELECT 1
                FROM task_dependencies dependency
                LEFT JOIN tasks prerequisite
                  ON prerequisite.id = dependency.prerequisite_task_id
                LEFT JOIN columns prerequisite_column
                  ON prerequisite_column.id = prerequisite.column_id
                 AND prerequisite_column.project_id = prerequisite.project_id
                WHERE dependency.task_id = dependent.id
                  AND (
                      prerequisite.id IS NULL
                      OR prerequisite.deleted_at IS NOT NULL
                      OR prerequisite.project_id <> dependent.project_id
                      OR prerequisite_column.id IS NULL
                      OR COALESCE(prerequisite_column.semantic_state, '') <> 'completed'
                      OR prerequisite.completed_at IS NULL
                  )
            )
      );

    SELECT RAISE(ABORT, 'dependency_in_use')
    WHERE OLD.semantic_state = 'completed'
      AND NEW.semantic_state <> 'completed'
      AND EXISTS (
          SELECT 1
          FROM tasks prerequisite
          JOIN task_dependencies dependency
            ON dependency.prerequisite_task_id = prerequisite.id
          JOIN tasks dependent
            ON dependent.id = dependency.task_id
          LEFT JOIN columns dependent_column
            ON dependent_column.id = dependent.column_id
           AND dependent_column.project_id = dependent.project_id
          WHERE prerequisite.column_id = NEW.id
            AND prerequisite.project_id = NEW.project_id
            AND prerequisite.deleted_at IS NULL
            AND dependent.deleted_at IS NULL
            AND (
                (
                    CASE
                        WHEN dependent.column_id = NEW.id
                         AND dependent.project_id = NEW.project_id
                            THEN NEW.semantic_state
                        ELSE COALESCE(dependent_column.semantic_state, '')
                    END
                ) = 'active'
                OR (
                    dependent.claimed_by IS NOT NULL
                    AND dependent.claim_expires_at IS NOT NULL
                    AND julianday(dependent.claim_expires_at) > julianday('now')
                )
            )
            AND (
                (
                    CASE
                        WHEN dependent.column_id = NEW.id
                         AND dependent.project_id = NEW.project_id
                         AND OLD.semantic_state = 'completed'
                         AND NEW.semantic_state <> 'completed'
                            THEN NULL
                        ELSE dependent.completed_at
                    END
                ) IS NULL
                OR (
                    CASE
                        WHEN dependent.column_id = NEW.id
                         AND dependent.project_id = NEW.project_id
                            THEN NEW.semantic_state
                        ELSE COALESCE(dependent_column.semantic_state, '')
                    END
                ) <> 'completed'
            )
      );
END;
