-- TC-13 mutable task cursors. Heartbeats intentionally do not emit activity
-- events or change task versions, but they can change stale/action-needed
-- membership. Keep an O(1), project-scoped monotonic revision for that
-- eventless mutation path.
--
-- The additive column defaults existing projects to zero. The triggers also
-- protect retained binaries that update task_agent_work without knowing about
-- this field, and fire for same-value timestamp writes so a successful
-- heartbeat cannot be hidden by SQLite's millisecond clock precision.
ALTER TABLE projects ADD COLUMN task_collection_revision INTEGER NOT NULL DEFAULT 0 CHECK (typeof(task_collection_revision) = 'integer' AND task_collection_revision >= 0);

-- Retained binaries predating agent work can still insert and update tasks
-- directly. Invalidate the project-scoped cursor revision for every task
-- write, including title, position, and soft-delete changes that may not emit
-- a newer event. Project moves invalidate both affected collections.
CREATE TRIGGER IF NOT EXISTS task_collection_revision_on_task_insert
AFTER INSERT ON tasks
BEGIN
    UPDATE projects
       SET task_collection_revision=task_collection_revision+1
     WHERE id=NEW.project_id;
END;

CREATE TRIGGER IF NOT EXISTS task_collection_revision_on_task_update
AFTER UPDATE ON tasks
BEGIN
    UPDATE projects
       SET task_collection_revision=task_collection_revision+1
     WHERE id=OLD.project_id;
    UPDATE projects
       SET task_collection_revision=task_collection_revision+1
     WHERE id=NEW.project_id AND NEW.project_id <> OLD.project_id;
END;

CREATE TRIGGER IF NOT EXISTS task_agent_work_revision_on_insert
AFTER INSERT ON task_agent_work
BEGIN
    UPDATE projects
       SET task_collection_revision=task_collection_revision+1
     WHERE id=(SELECT project_id FROM tasks WHERE id=NEW.task_id);
END;

CREATE TRIGGER IF NOT EXISTS task_agent_work_revision_on_update
AFTER UPDATE ON task_agent_work
BEGIN
    UPDATE projects
       SET task_collection_revision=task_collection_revision+1
     WHERE id=(SELECT project_id FROM tasks WHERE id=OLD.task_id);
    UPDATE projects
       SET task_collection_revision=task_collection_revision+1
     WHERE id=(SELECT project_id FROM tasks WHERE id=NEW.task_id)
       AND (SELECT project_id FROM tasks WHERE id=NEW.task_id)
           <> (SELECT project_id FROM tasks WHERE id=OLD.task_id);
END;

CREATE TRIGGER IF NOT EXISTS task_agent_work_revision_on_delete
BEFORE DELETE ON task_agent_work
BEGIN
    UPDATE projects
       SET task_collection_revision=task_collection_revision+1
     WHERE id=(SELECT project_id FROM tasks WHERE id=OLD.task_id);
END;

-- SQLite foreign-key cascade actions do not invoke child user triggers. Keep
-- hard task deletes observable as well, including the task_agent_work cascade;
-- the parent row is still available to resolve its project at this point.
CREATE TRIGGER IF NOT EXISTS task_collection_revision_on_task_delete
BEFORE DELETE ON tasks
BEGIN
    UPDATE projects
       SET task_collection_revision=task_collection_revision+1
     WHERE id=OLD.project_id;
END;
