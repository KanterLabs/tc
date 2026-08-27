CREATE TABLE IF NOT EXISTS actor_projects (
    actor_id TEXT NOT NULL REFERENCES actors(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    PRIMARY KEY(actor_id, project_id)
);
CREATE INDEX IF NOT EXISTS actor_projects_project_idx ON actor_projects(project_id, actor_id);
