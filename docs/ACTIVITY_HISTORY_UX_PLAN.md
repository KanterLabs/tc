# Activity and task history UX plan

## Goal

Make Roadmap answer three questions without forcing a person to scan a large
backlog:

1. What are agents working on right now?
2. What needs my attention?
3. What happened on a task while I was away?

The board remains the inventory and prioritization surface. The Roadmap view
becomes the follow-along surface, and the task drawer becomes the durable
history for one task.

## Current foundation

Roadmap already has most of the live coordination primitives:

- Tasks expose the latest structured `agent_work` snapshot, including state,
  phase, summary, next action, checkpoint counts, and update time.
- Board cards and the task drawer render a live agent pulse.
- My Work has a cross-project Live view grouped by working, waiting,
  verifying, handoff, stale, and action-needed states.
- Mutations create append-only events, and comments are stored separately.
- The Roadmap summary API already returns ten recent activity events.
- The browser polls the event feed every 15 seconds and refreshes affected
  views and open tasks.

The continuity breaks in three places:

- The Roadmap page does not render the `recent_activity` data it receives and
  does not put live agent work near the workspace summary.
- Live Work is useful but is nested under My Work, which makes it feel like an
  assignment list instead of the default place to observe agents.
- The drawer renders all comments first and its session-cached events second.
  This is not one chronological history, older events are not reliably
  available, and agent progress history is reduced to generated comment text.

## Recommended experience

### 1. Roadmap becomes the follow-along dashboard

Add an **Agent work** section directly below the workspace pulse.

- Show compact counts for **Working**, **Needs you**, and **Stale**.
- Show at most five active rows, ordered by action-needed first and then most
  recently updated.
- Each row shows task key/title, project, agent, state, phase, checkpoint
  count, latest summary, and relative update time.
- Show assignee and active claimant as distinct concepts; resolve both to actor
  names instead of falling back to opaque IDs in the normal UI.
- A row opens the existing task drawer. **View all live work** opens the
  existing Live view under My Work.
- Empty state: "No agents are reporting work right now."

Add a **Recent activity** panel below it.

- Render the newest task activity across visible projects, or only the current
  project on a project Roadmap route.
- Default filters: **All**, **Agent updates**, **Comments**, and **Task changes**.
- Group adjacent low-value system changes on the same task when they occur in
  a short interval, but never group comments, waits, handoffs, blocks, or
  completions.
- Each item includes task identity, actor, human-readable action, useful
  context, and exact time on hover/focus.
- A task item opens the drawer at its Activity tab.
- Give the open task a stable URL such as `/p/{project}/tasks/{task-key}` so a
  dashboard item or historical update can be bookmarked and shared.
- Load more rather than allowing an unbounded dashboard feed.

This keeps a 50-task backlog quiet: only claimed/pulsing work and recent
changes rise into the dashboard.

### 2. Task drawer gets Details and Activity tabs

Keep the current live agent panel visible at the top, then add a compact
**Details | Activity** switch below the task title and actions.

The Activity tab contains one newest-first timeline:

- Agent progress update: state, phase, checkpoint delta, summary, and next
  action in one item.
- Human or agent comment: full body with an explicit Comment label.
- Task changes: claim, release, move, block, complete, reopen, and important
  field updates.
- Actor identity and human/agent styling on every item.
- Filters for **All**, **Agent**, **Comments**, and **Changes**.
- Cursor-based **Load older activity** pagination.
- New items merge into the open timeline after event polling without erasing a
  comment or task edit in progress.

Do not show a separate `comment.created` system row next to the comment. The
comment itself is the timeline item.

On desktop, preserve the side drawer and board context. On mobile, the same
tab control and timeline use the full-width drawer; no hover-only information
is required.

### 3. Durable timeline contract

Add a task-scoped read API rather than reconstructing history from the
browser's global event cache:

```text
GET /api/v1/tasks/{task}/timeline?before=<cursor>&limit=50&kind=<kind>
```

The response is newest-first and returns a stable discriminated item shape:

```json
{
  "id": "opaque-id",
  "cursor": 123,
  "kind": "agent_progress | comment | task_change",
  "task_id": "opaque-task-id",
  "actor": { "id": "opaque-actor-id", "kind": "agent", "name": "Codex" },
  "created_at": "2026-08-30T12:00:00Z",
  "progress": null,
  "comment": null,
  "change": null
}
```

The server owns de-duplication, actor enrichment, authorization, ordering, and
redaction. The web client should not join comments to events by timestamp.

Persist every structured progress snapshot in a new append-only
`task_agent_work_history` table while retaining `task_agent_work` as the
efficient latest-snapshot read model. The history row stores operation, actor,
state, phase, summary, next action, checkpoint references/counts, and creation
time. Progress publication inserts history, updates the latest snapshot,
creates its readable comment, and emits its event in the existing transaction.

Migration requirements:

- Add the history table and task/time index without changing or deleting
  existing task, comment, event, or snapshot rows.
- Treat existing comments/events as legacy history; do not invent rich
  progress fields during backfill.
- Prove migration against a populated database and retain compatibility with
  the currently retained rollback binary.
- Cover pagination stability, deleted actors/tasks, scoped authorization, and
  comment-event de-duplication.

## Delivery slices

### Slice 1 — Roadmap follow-along dashboard

Use the existing Roadmap recent events and Live Work APIs to surface Agent work
and Recent activity. This is the fastest visible improvement and requires no
schema change.

Acceptance criteria:

- Workspace and project Roadmap routes show the correctly scoped active-agent
  summary and recent activity.
- Action-needed work sorts before ordinary working updates.
- Activity filters and task deep-links work on desktop and mobile.
- Agent names, assignees, and active claimants remain distinguishable without
  first visiting Settings.
- Event polling updates the sections without a page reload.
- Existing board, My Work, and Roadmap metrics remain unchanged.

### Slice 2 — Durable task timeline API and progress history

Add append-only agent progress history and a task-scoped unified timeline.

Acceptance criteria:

- A task's comments, agent progress, and lifecycle changes are returned in one
  stable newest-first cursor sequence.
- Generated progress comments are represented once, not duplicated by their
  `comment.created` event.
- Old pages remain stable while new activity is written.
- Project-scoped tokens cannot read timelines outside their project.
- Populated-data migration, rollback compatibility, and API contract tests
  pass.

### Slice 3 — Task drawer activity experience

Replace the two-batch comment/event rendering with the timeline API and a
dedicated Activity tab.

Acceptance criteria:

- The current live pulse remains visible and historical progress updates are
  individually inspectable.
- Comments and changes appear in true chronological order with actor identity.
- Filtering, loading older items, posting a comment, and live insertion all
  work without duplicate entries.
- Keyboard focus, screen-reader labels, reduced-motion behavior, and mobile
  layout are covered by component and browser tests.

## Explicitly deferred

- Notifications, email, and push delivery.
- User-configurable task watching/subscriptions.
- WebSockets or server-sent events; bounded event polling is sufficient for
  this iteration.
- Editing or deleting activity history.
- A separate top-level Activity destination. Add one only if dashboard usage
  proves the bounded feed is insufficient.

## Success signals

- A person can identify all active, waiting, handoff, or stale agent tasks from
  Roadmap without opening the 50-card backlog.
- A person can understand the latest result and next action for an agent task
  in one click.
- Reopening a task days later shows a complete, ordered explanation of its
  comments, agent checkpoints, and lifecycle changes.
- No activity item is duplicated, silently omitted because of the browser's
  current session, or visible outside its project authorization scope.
