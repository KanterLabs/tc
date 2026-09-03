# Card dependencies feature plan

## Goal

Let a task declare that one or more other tasks must finish first. Helm
should make the order visible, prevent work from starting out of sequence, and
preserve the existing board, claim, audit, and optimistic-concurrency rules.

Recommended branch name: `feature/card-dependencies`.

## Helm delivery

TC-9 is the existing high-level feature card. Keep it as the umbrella intent
and deliver it through these unclaimed implementation slices:

1. **TC-102 — Persist task dependency graph and summaries.**
2. **TC-103 — Expose task dependency API and activity.**
3. **TC-104 — Enforce prerequisite ordering across task lifecycles.**
4. **TC-105 — Manage dependencies in the task drawer.**
5. **TC-106 — Surface dependency readiness on boards and validate release.**

Implement TC-102 first. TC-103 and TC-104 may follow from the stable store
contract, TC-105 follows the HTTP contract, and TC-106 is the integration and
release gate. Do not claim the later slices until their prerequisites are
merged or their interfaces are fixed.

Before TC-102 is claimed, reconcile TC-9's current "across allowed projects"
criterion with this plan's same-project-only scope. The recommended v1 choice
is to amend TC-9 to same-project relationships and keep cross-project work
explicitly deferred. If cross-project support remains binding, stop and redesign
the authorization, leakage, deletion, cycle, search, and navigation contracts;
the five slices below are not sufficient for that larger boundary.

## Product decisions

- A task can have zero or more direct prerequisites and can block zero or more
  dependents.
- Dependencies are allowed only between live tasks in the same project. This
  keeps authorization, task-key resolution, and project deletion predictable.
- The graph must remain acyclic. Self-links, duplicate links, and any edge that
  closes a direct or transitive cycle are rejected.
- A prerequisite is satisfied only while its task is in a `completed` semantic
  column (`completed_at` is non-null). A task with at least one unfinished
  prerequisite is **dependency-blocked**.
- Dependency-blocked is derived coordination state, not a board column. Adding
  or completing a dependency never moves cards automatically and never writes
  a synthetic block reason.
- Hard ordering applies at the start/finish boundaries: a dependency-blocked
  task cannot be claimed, moved into an `active` or `completed` semantic
  column, completed, or resolved. Existing backlog/ready/blocked cards may be
  linked freely.
- Adding an unfinished prerequisite to an already active, completed, or
  actively claimed task is rejected. Adding a satisfied prerequisite is
  allowed.
- A completed prerequisite may be reopened only when none of its unfinished
  dependents is active or actively claimed. Completed dependents are historical
  work and do not prevent a prerequisite from reopening.
- A prerequisite with live dependents cannot be deleted until those links are
  removed. Deleting a dependent removes its outgoing links in the same
  transaction. This avoids dangling links under the existing soft-delete model.
- Dependency changes are first-class task activity with their own actor and
  event; they are not encoded in task descriptions or comments.
- A task is limited to 200 direct prerequisites and 200 direct dependents.
  Additions that would exceed either bound fail with
  `dependency_limit_exceeded`; relation reads are never unbounded.
- A prerequisite completion/reopen publishes bounded invalidation for each
  direct dependent so polling refreshes derived summaries in open views even
  though the dependent task's editable version did not change.

## User experience

### Board cards

- Show a compact dependency badge only when a task participates in the graph:
  `2 prerequisites`, `1 remaining`, or `Blocks 3`.
- Use an amber lock treatment for an unmet prerequisite and a neutral/check
  treatment when all prerequisites are satisfied. Do not reuse the red/manual
  Blocked-column treatment.
- Add a board filter with `All`, `Dependency blocked`, and `Dependency ready`.
  Text search continues to match task content rather than expanding into
  linked-card titles.
- Disable claim and complete actions when blocked, with an accessible reason
  such as “Finish TC-42 and TC-57 first.” The server remains authoritative.

### Task drawer

Add a **Dependencies** section to Details with two lists:

- **Waiting on** — direct prerequisites, each showing key, title, completion
  state, and a link that opens that task in the existing drawer route.
- **Blocking** — direct dependents with the same compact status treatment.

An `Add dependency` combobox searches the current project's existing task-list
endpoint, excludes the current task and already-linked tasks, and displays
completion state before selection. Adding and removing remain separate actions
from “Save changes,” so each has its own pending/error state and fresh ETag.

Keyboard and screen-reader requirements:

- The combobox follows the ARIA combobox/listbox pattern and works without
  pointer input.
- Status is conveyed by text and icon, not color alone.
- Add/remove success is announced through the existing live region; failures
  keep focus on the initiating control.
- On mobile the two lists stack, linked rows retain at least a 44px target, and
  no information is hover-only.

## Storage and domain model

Add an append-only migration `012_task_dependencies.sql` with a dedicated
table rather than extending `task_links`. The existing
`task_links_source_type_idx` permits only one link per source/type for bug
duplicate resolution, while a dependency must support many prerequisites.

```sql
CREATE TABLE task_dependencies (
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    prerequisite_task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    created_by TEXT REFERENCES actors(id) ON DELETE SET NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (task_id, prerequisite_task_id),
    CHECK (task_id <> prerequisite_task_id)
);

CREATE INDEX task_dependencies_prerequisite_idx
    ON task_dependencies(prerequisite_task_id, task_id);
```

No existing rows require backfill. Retained binaries ignore the new table, so
binary-only rollback keeps the pre-012 task read/write shape intact. Ignoring
the table is not sufficient behavioral compatibility, however: migration 012
must also add fail-closed database guards for the claim/start/complete/reopen/
delete boundaries. This prevents a retained pre-012 binary from violating live
dependency rows while allowing unrelated legacy task edits to continue.

Add store types for a compact task reference, a dependency collection, and a
summary embedded in task responses:

```json
{
  "dependency_summary": {
    "prerequisite_count": 2,
    "unmet_prerequisite_count": 1,
    "dependent_count": 3,
    "blocked": true
  }
}
```

The collection endpoint returns expanded direct relations; task-list reads
return only the summary. Populate summaries in batches for collection reads so
the board does not add two queries per card on top of the current enrichment
path.

Cycle validation runs inside the same SQLite write transaction as insertion.
A recursive CTE starts at the proposed prerequisite and follows its
prerequisites; reaching the dependent task rejects the edge. SQLite's serialized
writes make two concurrent graph mutations observe a deterministic order only
after the writer lock is acquired. The current generic transaction helper uses
a deferred transaction, so dependency mutations need a tested immediate-writer
path (or equivalent) before performing the cycle read.

## API contract

### Read relations

`GET /api/v1/tasks/{task}/dependencies`

- Requires `tasks:read` and normal project-ceiling authorization.
- Accepts an opaque ID or case-insensitive task key through the existing task
  reference resolver.
- Returns `{ prerequisites: [...], dependents: [...] }`, with each relation
  containing `id`, `key`, `title`, `completed_at`, and `satisfied`.
- Returns no more than the documented 200 direct rows in either direction; the
  mutation limit guarantees a complete bounded response.
- Returns the current task ETag so the next mutation can use `If-Match`.

### Add a prerequisite

`POST /api/v1/tasks/{task}/dependencies`

```json
{ "prerequisite": "TC-42" }
```

- Requires `tasks:write`, the source task's exact `If-Match`, and an
  explicitly non-empty `Idempotency-Key`; the handler rejects a missing key
  rather than relying on the generic optional idempotency wrapper.
- Resolves and validates both tasks in the transaction, inserts the edge,
  increments only the dependent task's version, and emits
  `task.dependency_added` with safe task IDs/keys.
- Returns the updated task, dependency summary, and new ETag.

### Remove a prerequisite

`DELETE /api/v1/tasks/{task}/dependencies/{prerequisite}`

- Uses the same scope, `If-Match`, idempotency, claim-ownership, and
  administrator-override rules as other task writes.
- Removes exactly one edge, increments the dependent task version, emits
  `task.dependency_removed`, and returns the updated task/new ETag.

### Errors

Use stable codes with actionable details:

- `dependency_self_reference` — source and prerequisite are the same task.
- `dependency_cross_project` — tasks do not share a project.
- `dependency_already_exists` — the direct edge exists.
- `dependency_limit_exceeded` — either direct relationship bound is full.
- `dependency_cycle` — include a bounded task-key path proving the cycle.
- `unmet_dependencies` — return a bounded list of unmet task references when a
  guarded lifecycle action is refused.
- `dependency_in_use` — deletion/reopen would invalidate started dependent
  work; return the blocking dependent references.

Conflicts caused by a stale source version remain `409 stale_task`; callers
with write-only/claim-only bearer tokens receive the current redacted shape
already used elsewhere.

Add typed server mappings for every dependency error. Scope-aware redaction
must also cover the new bounded relation/path details; inaccessible task keys
or titles cannot leak through a conflict envelope.

Add `dependency=blocked|ready` to project task-list and My Work filters.
`ready` means “has dependencies and all are satisfied”; tasks with no
dependencies match neither value.

## Lifecycle integration

Centralize graph checks in store transaction helpers and call them from every
path that can cross a guarded boundary:

- task claim;
- generic task patch when the destination column is active/completed;
- generic complete;
- ordinary task reopen, including a completed task moved into the manual
  Blocked column;
- bug triage/resolve/reopen;
- any raw task move that later permits lifecycle destinations;
- column semantic-state edits that would make contained tasks active/completed
  or reopen a prerequisite in bulk;
- task soft deletion.

Do not rely on HTTP handlers for invariants; direct store callers and future
endpoints must get the same behavior. Checks must occur after the transaction
has acquired the writer lock, alongside the task version/claim predicate, so a
concurrent completion or edge edit cannot create time-of-check/time-of-use
drift.

The existing task timeline maps the new events to readable changes:

- “Shane added TC-42 as a prerequisite.”
- “Codex removed dependency on TC-42.”

## Delivery slices

### 1. Dependency persistence and read model

- Add migration 012, store types, graph queries, batched summaries, and
  add/remove operations.
- Prove self-link, duplicate, same-project, cycle, soft-delete, and concurrent
  insert behavior in store tests.
- Extend populated migration and retained-binary rollback tests.

Acceptance criteria:

- Multiple direct prerequisites and dependents persist and round-trip.
- Direct and transitive cycles cannot be committed under concurrent writers.
- Existing populated data, bug duplicate links, task reads, and retained
  pre-012 writes remain intact.
- A retained pre-012 binary can still edit unrelated tasks but cannot bypass
  dependency claim/start/complete/reopen/delete invariants.
- Collection summary loading is bounded and does not regress into per-task
  dependency queries.

### 2. HTTP contract and activity

- Add read/add/remove routes, filters, OpenAPI/docs, events, timeline labels,
  authorization, optimistic versioning, idempotent replay, and the bounded
  dependent-invalidation contract consumed by polling clients.

Acceptance criteria:

- Human and bearer callers see the documented full/redacted responses.
- Add/remove mutations honor task claims, stale ETags, exact same-key replay,
  project ceilings, and reduced responses for write-only identities.
- Dependency changes appear once in events and the task timeline.
- Polling clients understand the dependent-invalidation contract without
  incrementing the dependent's editable version solely for derived state.
- `openapi.yaml`, generated JSON, and checked-in API documentation agree.

### 3. Lifecycle ordering enforcement

- Apply centralized prerequisite checks to all task/bug/column lifecycle paths
  listed above.
- Protect reopen and soft-delete paths from invalidating work that has already
  started.
- Publish bounded dependent invalidation transactionally when completion or
  reopen changes derived readiness.

Acceptance criteria:

- Every start/finish path rejects unmet prerequisites atomically with stable
  details; no rejected call changes task, claim, graph, comment, or event state.
- Completing a prerequisite immediately makes its dependents eligible.
- Completion/reopen refreshes every affected dependent view through the stable
  invalidation contract.
- Reopen, delete, admin claim override, stale ETag, and concurrent transition
  behavior are covered by store and contract tests.
- Lifecycle dependency invariants cannot be bypassed by direct store callers.

### 4. Drawer dependency management

- Add TypeScript models/API methods and a focused dependency section component.
- Implement project-local search, add/remove, linked-task navigation, loading,
  empty, stale-version, and authorization/error states.

Acceptance criteria:

- A user can add and remove multiple prerequisites without losing unsaved
  drawer edits.
- Both relationship directions and live completion state are understandable on
  desktop and mobile.
- Keyboard, focus, live-region, and touch-target behavior pass component tests.
- An ETag conflict refreshes authoritative task/dependency state without
  silently retrying a different graph mutation.

### 5. Board visibility and end-to-end hardening

- Add card badges, action explanations, dependency filters, state merging, and
  browser coverage for the ordered-work journey.
- Run migration, Go, frontend unit, production build, and Playwright suites.

Acceptance criteria:

- A dependency-blocked card is recognizable and filterable without opening it.
- Claim, active move, complete, bug resolve, prerequisite completion, and
  linked-task navigation behave consistently across refresh/poll cycles.
- No dependency mutation leaks a task outside its project scope.
- Production migration uses a verified pre-upgrade backup; a populated copy is
  migrated and checked for task/link counts and referential integrity before
  release.

## Verification matrix

- **Graph:** none, one, many, fan-in, fan-out, direct cycle, long cycle,
  duplicate, self-link, and concurrent opposing inserts.
- **Lifecycle:** claim, renew, patch into active/completed, complete, block,
  resolve/reopen bug, reclassify column, delete source, and delete prerequisite.
- **Concurrency:** stale source ETag, edge changed between reads, prerequisite
  completed/reopened while another writer claims the dependent, and exact
  idempotent replay after deletion. Opposing edge inserts must prove the writer
  lock is acquired before either cycle read.
- **Authorization:** human, administrator override, read-only token, write-only
  token, claim-only token, permitted project, and cross-project reference.
- **UI:** keyboard combobox, add/remove focus restoration, server conflict,
  polling merge, mobile drawer, screen-reader names, reduced motion, and empty
  states.
- **Upgrade:** pre-012 populated database, row-count/integrity comparison,
  current binary writes, retained-binary writes after upgrade, backup
  verification, retained-binary attempts to bypass dependency invariants, and
  no automatic restore.

## Explicitly deferred

- Cross-project dependencies.
- Optional/advisory dependency types, finish-to-finish scheduling, lead/lag
  time, dates, estimates, and Gantt/critical-path visualization.
- Automatic card movement, automatic claim release, or notifications when a
  prerequisite completes.
- Bulk graph editing and transitive-closure materialization.
- Import/export compatibility with Trello or other project-management APIs.
