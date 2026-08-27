CREATE TABLE IF NOT EXISTS project_counters (
    project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    next_number INTEGER NOT NULL CHECK (next_number > 0)
);
INSERT OR IGNORE INTO project_counters(project_id, next_number)
SELECT project_id, COALESCE(MAX(number), 0) + 1 FROM tasks GROUP BY project_id;
