-- Board audits are append-only snapshots.  This migration deliberately adds
-- only new tables and indexes: retained binaries can continue to read and
-- write the pre-audit task/claim/comment/event shape during rollback.
CREATE TABLE IF NOT EXISTS audit_runs (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    actor_id TEXT NOT NULL REFERENCES actors(id) ON DELETE RESTRICT,
    scope TEXT NOT NULL CHECK (length(trim(scope)) > 0 AND length(scope) <= 200),
    status TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('queued', 'running', 'complete', 'partial', 'failed')),
    started_at TEXT NOT NULL,
    finalized_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK (
        (status IN ('queued', 'running') AND finalized_at IS NULL)
        OR (status IN ('complete', 'partial', 'failed') AND finalized_at IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS audit_runs_project_started_idx
    ON audit_runs(project_id, started_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS audit_runs_actor_started_idx
    ON audit_runs(actor_id, started_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS audit_findings (
    id TEXT PRIMARY KEY,
    audit_id TEXT NOT NULL REFERENCES audit_runs(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
    captured_version INTEGER NOT NULL CHECK (captured_version > 0),
    source_column TEXT NOT NULL CHECK (length(trim(source_column)) > 0 AND length(source_column) <= 200),
    verdict TEXT NOT NULL CHECK (verdict IN ('correct', 'needs_attention', 'move_proposed')),
    proposed_semantic_destination TEXT CHECK (
        proposed_semantic_destination IS NULL
        OR proposed_semantic_destination IN ('backlog', 'ready', 'active', 'blocked', 'completed')
    ),
    confidence REAL NOT NULL CHECK (confidence >= 0 AND confidence <= 1),
    reason TEXT NOT NULL CHECK (length(trim(reason)) > 0 AND length(reason) <= 2000),
    evidence_refs TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(evidence_refs) AND json_type(evidence_refs) = 'array'),
    review_state TEXT NOT NULL DEFAULT 'pending' CHECK (review_state IN ('pending', 'approved', 'dismissed')),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(audit_id, task_id)
);

CREATE INDEX IF NOT EXISTS audit_findings_audit_created_idx
    ON audit_findings(audit_id, created_at, id);
CREATE INDEX IF NOT EXISTS audit_findings_task_idx
    ON audit_findings(task_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS audit_findings_verdict_idx
    ON audit_findings(audit_id, verdict, review_state, id);
