# Portable export and import

Helm's portability API moves project data between installations without
replacing the destination database. It is intentionally separate from the
SQLite backup/restore tools used for disaster recovery.

## Format

`GET /api/v1/projects/{project}/export` (or `GET /api/v1/export?project=...`)
returns a JSON attachment with:

```json
{
  "format": "helm.portable",
  "version": 1,
  "exported_at": "2026-09-04T12:00:00Z",
  "source": {"product": "helm", "api": "/api/v1"},
  "projects": [], "columns": [], "tasks": [], "labels": [], "actors": [],
  "relationships": {
    "task_labels": [], "dependencies": [], "task_links": []
  },
  "activity": {
    "events": [], "agent_work": [], "agent_work_history": []
  },
  "comments": []
}
```

The archive includes live projects, columns, tasks, typed bug details, labels,
comments, task-label edges, dependencies, links, project-scoped events, and
agent-work snapshots/history. IDs, task numbers, versions, timestamps, and
relationships are explicit fields. Deleted tasks are not exported for
resurrection; use an SQLite backup when the goal is exact disaster recovery.
Actor entries contain safe display metadata only. Password hashes, tokens,
sessions, Codex credentials, and other authentication material are never
included.

The format is versioned. Importers must reject an unknown `format` or
`version`; a future format can be introduced alongside v1 rather than silently
changing the meaning of an existing archive.

## Validate, preview, and import

POST the archive directly or wrap it in an import request:

```sh
curl -b cookies.txt -H 'Content-Type: application/json' \
  -X POST 'https://helm.example/api/v1/import?dry_run=true&conflict=remap' \
  --data-binary @helm-operations-portable-v1.json
```

The browser uses the equivalent wrapper form, which also supports importing a
one-project archive into an existing project:

```json
{
  "archive": {"format": "helm.portable", "version": 1, "projects": []},
  "dry_run": true,
  "conflict": "remap",
  "target_project": "destination-project-id"
}
```

`dry_run` performs archive and destination validation without writing. The
report includes counts, warnings, validation errors, and `remaps`. The default
`conflict=remap` keeps a source ID where it is free; when it is occupied by
different data, it derives a deterministic destination ID (and reports the
mapping). Project keys, slugs, column positions, and task numbers are remapped
similarly. `conflict=fail` aborts on an incompatible stable-ID conflict.
When an `Idempotency-Key` is supplied, the canonical import query options
(`dry_run`, `conflict`, and `target_project`) are part of the request identity,
so reusing a key with different import behavior returns an idempotency conflict.

Imports validate all records and relationships before the first insert. The
actual operation then runs as one transaction and only inserts records: it
does not update, delete, restore, or replace destination rows. Repeating an
archive is safe: equal stable records are skipped, while any attribution to
the importing actor or destination remap is reported. Event cursors are
instance-local SQLite sequence values, so imported events receive destination
cursors and the report warns when a cursor changes; activity IDs remain stable.

Source actor IDs that are absent on the destination are attributed to the
authenticated importing actor and reported as actor remaps. This preserves
the audit trail without creating an unverified identity or copying credentials.

The endpoints require `projects:read`, `tasks:read`, and `events:read` for
export, and `projects:write` plus `tasks:write` for import. Project-scoped
tokens can export/import only the projects in their allow-list. A project-scoped
import must select an existing allowed destination with the project route (or
the `target_project` wrapper field); global and Trello imports reject scoped
tokens because they could otherwise create a project outside the allow-list. A
project route (`.../projects/{project}/import`) selects the destination project
and never overwrites its metadata.

## Trello adapter

`POST /api/v1/import/trello` accepts a Trello board export/API object. Pass
`target_project={id|key|slug}` when a project-scoped token should import into an
existing allowed project; without it, an unscoped caller receives a new project.
The adapter is isolated in `internal/portable`, converts lists, cards, labels, due
dates, and comment actions into the Helm v1 envelope, and then delegates to the
same validated transactional importer. Unsupported Trello members,
memberships, checklists, attachments, custom fields, Power-Ups/plugins,
non-comment actions, card assignees, closed flags, list/card positions,
`idLabels`, `dueComplete`, board preferences, and unknown fields are returned as
explicit warnings. Cards in a completed-named list receive a completion
timestamp and cannot be claimed. Unsupported values are not silently dropped
or mistaken for Helm records.

## Boards and recovery

Helm v1 still has one board per project. `GET
/api/v1/projects/{project}/boards` exposes a permission-checked virtual board
descriptor so navigation can adopt a board seam without changing existing
`/p/{project-slug}` URLs or project permissions. Multiple boards per project
remain a deliberate deferred migration decision; no speculative schema change
is made by portable import.

Portable import/export is not backup/restore. Use the documented backup helper
and an explicit restore operation for disaster recovery, including deleted
records and the exact SQLite state. Application upgrades remain additive and
rollback-compatible; a failed upgrade never restores an older database or
discards accepted writes.
