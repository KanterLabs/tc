-- Comment lifecycle metadata is additive.  Existing comment rows remain
-- readable with version one and a live (NULL deleted_at) state; no historical
-- body or event row is rewritten.
ALTER TABLE comments ADD COLUMN version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE comments ADD COLUMN deleted_at TEXT;

-- Keep active-comment and task timeline reads bounded as comment history grows.
CREATE INDEX IF NOT EXISTS comments_task_lifecycle_time_idx
    ON comments(task_id, deleted_at, created_at DESC, id DESC);
