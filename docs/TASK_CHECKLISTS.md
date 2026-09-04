# Task checklists

Task checklists keep measurable acceptance criteria attached to the task that
owns them. They are intentionally separate rows, so adding criteria does not
rewrite the task description or create board cards for every small step.

## API

The canonical endpoints are:

```text
GET    /api/v1/tasks/{task}/checklist
POST   /api/v1/tasks/{task}/checklist
PATCH  /api/v1/tasks/{task}/checklist
PATCH  /api/v1/tasks/{task}/checklist/{item}
DELETE /api/v1/tasks/{task}/checklist/{item}
```

`{task}` accepts the same opaque task ID or case-insensitive task key as the
task detail route. A checklist read requires `tasks:read`; writes require
`tasks:write`. The collection response is:

```json
{
  "task_id": "task_42",
  "version": 7,
  "data": [
    {
      "id": "check_1",
      "task_id": "task_42",
      "text": "Verify keyboard access",
      "position": 0,
      "completed": false,
      "completed_at": null,
      "completed_by": null,
      "created_at": "2026-09-03T10:00:00Z",
      "updated_at": "2026-09-03T10:00:00Z"
    }
  ],
  "summary": {
    "total": 1,
    "completed": 0,
    "open": 1,
    "percent": 0,
    "completion_policy": "warn",
    "warning": false
  }
}
```

Every checklist write is an optimistic-concurrency mutation. Clients send the
current task `ETag` as `If-Match: "vN"`; a stale version returns `409` with the
usual `stale_task` retry details. An `Idempotency-Key` is optional, but is
recommended for network retries and replays the exact original response.

Create accepts `text`, optional `completed`, and optional `position`. Item
patch accepts any non-empty subset of `text`, `completed`, and `position`.
Reorder replaces the complete order with `{ "item_ids": ["check_2",
"check_1"] }`; every existing item must appear exactly once. Successful
mutations advance the task version and return the updated task (or only
`id`/`version` to a write-only bearer token), with the new task ETag.

## Limits and policy

- A task has at most 100 checklist items.
- Item text is 1–1,000 Unicode characters after trimming.
- Combined checklist text is capped at 100,000 bytes per task.
- The normal API request body cap remains 2 MiB.

The project field `checklist_completion_policy` is `warn` by default. Under
`warn`, completing a task with open criteria succeeds and emits a completion
event containing the warning and open-item count; the completed task's summary
also sets `warning: true`. The same behavior applies when a task is moved
directly into a completed-semantic column, when a bug is resolved, or when a
column transition completes multiple tasks. Under `require`, each of those
completion paths is rejected with `409 checklist_incomplete` and
`details.open_items` until every item is checked. Rejected completion is
atomic: task and column versions, checklist state, and activity do not change.
Policy evaluation must stay inside the same write transaction after the
guarded mutation and before derived task/event writes; this ordering is a merge
invariant for the project/column administration changes.

## Activity and events

Checklist writes append one actor-attributed event with a server timestamp:

- `task.checklist_item_added`
- `task.checklist_item_updated`
- `task.checklist_item_removed`
- `task.checklist_reordered`

The events are available through `GET /api/v1/events` and are represented in
task/project timelines as `task_change` items. Bearer callers with
`events:read` but without `tasks:read` retain machine-readable item IDs,
positions, completion state, and versions; item text is redacted.

## UI behavior

Task cards show `completed/total` progress when criteria exist. Task details
provide an accessible Checklist panel with:

- a labeled add form (Enter submits);
- native keyboard-operable completion checkboxes;
- inline text editing (Enter saves);
- labeled up/down reorder controls;
- labeled remove controls;
- polite live announcements and explicit warning/error states.

All controls use the same ETag-aware API and refresh the task on a stale
version instead of silently overwriting another actor's change.
