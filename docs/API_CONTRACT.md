# Roadmap v1 API contract

The browser and external agents use the same JSON API under `/api/v1`.
Single-resource responses return the resource directly. Collection responses
are `{ "data": [...], "next_cursor": "..." }`. Every error uses the same
envelope:

```json
{"error":{"code":"machine_code","message":"Human-readable summary","details":{}}}
```

IDs are opaque strings and timestamps are UTC RFC 3339 values. Optional JSON
fields are omitted when they have no value unless a route explicitly documents
`null`.

## Discovery, headers, and security

- `GET /api/v1` is public discovery and returns `{ "name": "roadmap", "version": "v1" }`, plus `revision` when configured.
- `GET /openapi.json` returns the OpenAPI 3.1 contract and is served with
  `Cache-Control: no-store`, like the JSON API responses.
- Every response includes `X-Request-ID`. Deployments with a release SHA also include `X-Roadmap-Revision`.
- API responses are `Cache-Control: no-store`. Clients may send `X-Request-ID` (up to 128 characters); otherwise the server generates one.
- Cookie- and Cloudflare-authenticated mutations require an `Origin` exactly equal to the configured public origin. Missing or different origins return `403` (`csrf_origin`). Bearer-token requests are exempt from this Origin check. `GET`, `HEAD`, and `OPTIONS` do not require Origin.
- Public mutation routes (`setup`, `login`, and `logout`) use the same exact-Origin rule and reject `Idempotency-Key` with `400` (`idempotency_not_supported`).
- Humans use the local session cookie or verified Cloudflare Access identity. Agents use scoped `Authorization: Bearer` tokens. Missing credentials return `401`; valid credentials without the required permission or scope return `403`.

## Authentication

- `GET /api/v1/auth/status`
- `POST /api/v1/auth/setup`
- `POST /api/v1/auth/login`
- `POST /api/v1/auth/logout`
- `GET /api/v1/auth/me`

The server supports `local`, `cloudflare`, and development-only `disabled`
authentication modes. Cloudflare identity comes from the protected Tunnel
`Cf-Access-Jwt-Assertion` header. The server verifies its RS256 signature,
issuer, expiration, and configured UI/API application audience before using the
signed email claim; generic proxy identity headers are not trusted.

Setup accepts required `email` and `password`, with optional `name`; when name
is omitted it defaults to the email's local part. Setup is single-use. Login
accepts email and password. Logout is safe to call without an active session.
Missing, explicit-null, mistyped, malformed, or schema-invalid setup/login
fields are `400` (`invalid_request` or `invalid_json`). Only a syntactically
valid request whose credentials are not accepted is `401` (`invalid_credentials`);
CSRF-origin failures are `403`. Unknown JSON fields are rejected with `400`.

Actors always contain `id`, `kind`, `name`, `admin`, `created_at`, and
`updated_at`. Human actors may include `email`; agent email is unsupported.
Actors may include `description`, `project_ids`, and agent token metadata. Token
metadata never includes plaintext token values.

## Projects, columns, and labels

- `GET|POST /api/v1/projects`
- `GET|PATCH /api/v1/projects/{project}`
- `GET|POST /api/v1/projects/{project}/columns`
- `GET /api/v1/projects/{project}/timeline`
- `GET /api/v1/columns/{column}`
- `PATCH /api/v1/columns/{column}`
- `GET|POST /api/v1/projects/{project}/labels`
- `DELETE /api/v1/labels/{label}`

A project contains `id`, uppercase `key`, stable `slug`, `name`,
`description`, `color`, `favorite`, timestamps, and optional aggregate counts.
A supplied slug is normalized to lowercase, must be URL-safe, and is limited to
64 characters; an omitted slug is generated. Project color defaults to `#64748b`.

Creating a project atomically creates ordered Backlog, Ready, In progress,
Blocked, and Done columns. A column contains `id`, `project_id`, `name`,
`semantic_state`, `position`, and timestamps. An empty object or a body with
no recognized field in a project or column PATCH returns `400`; null is not a
clear operation for their non-null fields. Explicit null, wrong types, empty
strings, invalid formats, and values outside declared min/max constraints are
rejected for non-nullable request properties; array items reject null,
wrong-type, empty, and duplicate values. A human administrator may explicitly
override an active task claim when changing a column's semantic state; bearer
tokens and ordinary human actors cannot.

Project and column mutation responses are scope-aware. Humans and bearer tokens
with `projects:read` receive the full object. A bearer token with
`projects:write` but without `projects:read` receives only `{ "id": "..." }`
for project mutations, or `{ "id": "...", "project_id": "..." }` for column
mutations. Idempotent retries replay that same reduced body; direct GET routes
still require the corresponding read scope. If a write-only bearer attempts a
semantic-state change blocked by an active task claim, the `409`
`task_already_claimed` response has an empty `details` object; human callers
and bearer tokens with `tasks:read` retain diagnostic claim details. Human
administrators may explicitly override this active-claim restriction, while
bearer tokens and ordinary human actors cannot.

Labels contain `id`, `project_id`, `name`, `color`, and timestamps. Label color
defaults to `#94a3b8` when omitted or null.

## Tasks, comments, and claims

- `GET|POST /api/v1/projects/{project}/tasks`
- `GET /api/v1/issues`
- `GET|PATCH|DELETE /api/v1/tasks/{task}`
- `GET|POST /api/v1/tasks/{task}/comments`
- `GET /api/v1/tasks/{task}/timeline`
- `POST /api/v1/tasks/{task}/claim`
- `POST /api/v1/tasks/{task}/progress`
- `POST /api/v1/tasks/{task}/renew`
- `POST /api/v1/tasks/{task}/release`
- `POST /api/v1/tasks/{task}/complete`
- `POST /api/v1/tasks/{task}/block`
- `POST /api/v1/tasks/{task}/triage`
- `POST /api/v1/tasks/{task}/resolve`
- `POST /api/v1/tasks/{task}/reopen`

Task references accept an opaque task ID or a project-local key such as
`OPS-42`; key matching is case-insensitive. A task contains `id`, `number`,
`key`, `project_id`, `kind`, non-null `column_id`, `title`, Markdown
`description`, `priority`, `position`, `version`, timestamps, `labels`, and
`comment_count`. It may also contain the latest agent progress snapshot in
`agent_work`; this field is omitted until an agent publishes progress. `kind`
is `task` or `bug`; bug tasks also contain the optional nested `bug` details
described below. The assignee, current claimant, claim expiry, due date, and
completion timestamp are omitted when unset. Task responses may also include a
bounded `dependency_summary` with `prerequisite_count`,
`unmet_prerequisite_count`, `dependent_count`, and `blocked`; the counts cover
only live, same-project direct relations and each count is capped at 200.

Task PATCH requires at least one recognized field. `{}` and unknown-only bodies
return `400`. `column_id`/`column`, `position`, and other non-null fields
reject JSON null. `description`, `assignee`/`assignee_id`, `due_at`, and
`labels`/`label_ids` accept null to clear their value. The `column` names are
aliases for `column_id`; `labels` and `label_ids` are aliases. Task responses
include a strong `ETag` in the exact form `"vN"`. Position is non-negative and
is limited to `1e12`.

Successful task mutations return the full Task for humans and bearer tokens
with `tasks:read`. Bearer tokens without `tasks:read` receive exactly
`{ "id": "...", "version": N }`, with the strong ETag still set to `"vN"`.
This reduced form applies to task creation, PATCH, claim, renew, release,
complete, block, progress, triage, resolve, and reopen, and idempotent retries
replay the already-reduced body.
Direct task GET and task collections still require `tasks:read`.

`GET /api/v1/projects/{project}/timeline` returns the same typed, newest-first
timeline items as the task route, merged across every non-deleted task in the
selected project. It requires `tasks:read` and honors project-scoped bearer
tokens. Use the opaque `next_cursor` as `before` for stable keyset pagination;
`kind` optionally restricts results to `agent_progress`, `comment`, or
`task_change`. Generated progress comments and their `comment.created` and
`task.progressed` events are represented once by the corresponding structured
progress item. Legacy comments and events remain visible with their original
typed payloads, and actor enrichment includes only actor ID, kind, and name.

PATCH, DELETE, and task action requests require an exact quoted `If-Match: "vN"`
value. Missing If-Match returns `428`; an unquoted, weak, malformed, or
non-positive value returns `400`; a stale version returns `409` (`stale_task`)
with the current task in `error.details.current`. PATCH and DELETE also return
`409` (`task_already_claimed`) when another actor holds an active claim; humans
and tokens with `tasks:read` may receive `claimed_by` and `claim_expires_at`.
Human administrators may explicitly override an active claim for PATCH, DELETE,
complete, and block; bearer tokens and ordinary human actors cannot. For tokens
without `tasks:read`, stale or claim conflict details never include the task
title, description, or other read-protected fields: `details.current` is
reduced to the task `id` and `version` needed for retry. Humans and tokens with
`tasks:read` retain the full current task. The runtime does not use `412` for
this contract.

Claims default to a 1,800-second lease. Explicit `lease_seconds` (or the
compatibility alias `duration_seconds`) must be an integer from 30 through
604,800 seconds, inclusive. Unclaimed or expired tasks may be claimed; only
the current owner may renew or release an active lease. A non-owner cannot
complete or block a task with an active claim (`403`), except for a human
administrator explicitly overriding that claim.

### Task dependencies

The dependency graph is a direct-edge graph scoped to one live project. The
routes are:

- `GET /api/v1/tasks/{task}/dependencies`
- `POST /api/v1/tasks/{task}/dependencies`
- `DELETE /api/v1/tasks/{task}/dependencies/{prerequisite}`

`{task}` and `{prerequisite}` accept an opaque task ID or a case-insensitive
project-local key such as `OPS-42`. Reads require `tasks:read`; bearer tokens
may only read tasks in their project access ceiling. The GET response is:

```json
{
  "prerequisites": [
    {
      "id": "task_41",
      "key": "OPS-41",
      "title": "Define event cursor contract",
      "completed_at": "2026-08-27T10:15:00Z",
      "satisfied": true
    }
  ],
  "dependents": []
}
```

Both arrays contain direct, live, same-project relations only and are capped
at 200 entries. Deleted tasks and dangling historical edges are omitted. Each
relation has `id`, `key`, `title`, `completed_at` (or JSON `null`), and
`satisfied`; a relation is satisfied only when its task is complete in the
completed semantic column. The GET response includes the referenced task's
strong `ETag: "vN"`.

Adding and removing an edge require `tasks:write`, the exact current dependent
task ETag in `If-Match: "vN"`, and an explicitly non-empty,
non-whitespace `Idempotency-Key`. The POST body is exactly:

```json
{"prerequisite":"OPS-41"}
```

The DELETE path identifies the prerequisite. Both mutations enforce the
caller's project access ceiling, the same-project rule, 200 direct
prerequisite and 200 direct dependent limits, and cycle prevention. A task
with a live claim may be changed only by its claim owner; a human administrator
may explicitly override another actor's claim. A successful mutation advances
only the dependent task version, returns the updated Task and its new strong
ETag (or `{ "id": "...", "version": N }` for a write-only bearer without
`tasks:read`), and emits the corresponding dependency event. Idempotent
retries replay the original body and ETag before mutable task lookup.

Lifecycle ordering is enforced inside the store transaction for every entry
point: claim, generic task PATCH or move into `active`/`completed`, complete,
ordinary task reopen or `completed`-to-`blocked` transition, bug triage,
resolve, or reopen, semantic column changes (including bulk column updates),
and dependency insertion into already active, completed, or actively claimed
work.
Every direct prerequisite must be live and satisfied before a task crosses a
start/finish boundary. Reopening a completed prerequisite is rejected while
an unfinished dependent is active or actively claimed, and deleting a
prerequisite is rejected while it has a live dependent. Deleting a dependent
cleans up its own graph edges atomically. Rejected lifecycle requests do not
change the task, claim, graph, comments, or events.

The stable dependency error codes are:

- `dependency_self_reference`: the task was supplied as its own prerequisite.
- `dependency_cross_project`: the two visible tasks are not in the same
  project. References outside a bearer token's project ceiling are masked as
  `not_found` (or `forbidden` for a directly requested task) rather than
  disclosing another project.
- `dependency_already_exists`: the requested direct edge already exists.
- `dependency_limit_exceeded`: a 200 direct-prerequisite or direct-dependent
  limit would be exceeded.
- `dependency_cycle`: the edge would create a direct or transitive cycle.
- `dependency_not_found`: the referenced task or edge does not exist as a live
  relation.
- `unmet_dependencies`: a lifecycle transition attempted to activate or
  complete a task while a prerequisite remained unsatisfied.
- `dependency_in_use`: deleting a prerequisite task would leave a live
  dependent edge; remove the edge first.

These codes appear in the normal error envelope. Validation-style dependency
errors use `400`; graph, stale-task, active-claim, and idempotency conflicts
use `409`; missing `If-Match` uses `428` with `if_match_required`; malformed
preconditions use `400`. Reusing a key under the same authenticated principal
for another method, path, or payload returns `409` with
`idempotency_key_reused`; different principals (and bearer credentials) have
isolated key namespaces. A missing or blank required key returns `400` with
`idempotency_required`. Error details
are scope-aware: callers without `tasks:read` never receive task titles,
descriptions, or other read-protected fields. Conflict details are reduced to
bounded safe metadata. Write-only dependency callers receive no relation IDs,
keys, titles, completion timestamps, satisfaction flags, or cycle paths; only
non-sensitive fields such as direction, count, or limit may remain. Callers
with `tasks:read` may receive bounded IDs/keys and cycle paths, but never more
than the 200-edge graph limits. Stale task conflicts retain only the dependent
task `id` and `version` for write-only retry callers.

### Board audits and guarded moves

Board audits are durable, read-first snapshots. The routes are:

- `GET|POST /api/v1/projects/{project}/audits`
- `GET /api/v1/audits/{audit}`
- `GET|POST /api/v1/audits/{audit}/findings`
- `POST /api/v1/audits/{audit}/finalize`
- `PATCH /api/v1/audit-findings/{finding}`
- `POST /api/v1/tasks/{task}/move`

Audit reads (`GET` on the project run collection, run summary, or findings)
require `tasks:read`. Audit writes (creating a run, appending a finding,
finalizing a run, or reviewing a finding) require `tasks:write`. These are the
existing task scopes; audits do not introduce a separate permission scope.
Project-scoped bearer tokens may access only runs in their permitted project.
Collections use the normal cursor pagination (`limit` defaults to 50 and is
capped at 200).

Creating a run requires `scope` and accepts optional `status` (`queued` or
`running`, default `running`). A run uses exactly these lifecycle states:
`queued`, `running`, `complete`, `partial`, and `failed`. Finalization requires
one terminal `status`: `complete`, `partial`, or `failed`; timestamps and all
lifecycle transitions are server-owned. A run response contains
`id`, `project_id`, `actor_id`, `scope`, `status`, `started_at`, optional
`finalized_at`, `created_at`, `updated_at`, `finding_count`, and `findings`.
The run detail endpoint is intentionally a summary: `findings` is always an
empty array there. Fetch findings through the paginated findings endpoint.
Each project retains at most 1,000 audit runs and each run at most 10,000
findings. Reaching a ceiling rejects new data; the server never silently
deletes older audits.

Appending a finding requires the following JSON fields:

```json
{
  "task_id": "task_42",
  "captured_version": 7,
  "source_column": "col_backlog",
  "verdict": "move_proposed",
  "proposed_semantic_destination": "ready",
  "confidence": 0.9,
  "reason": "Move to Ready after triage.",
  "evidence_refs": ["/api/v1/tasks/task_42"]
}
```

`source_column_id` is accepted as an alias for `source_column`; if both are
sent they must match. `verdict` is `correct`, `needs_attention`, or
`move_proposed`. A `move_proposed` finding requires
`proposed_semantic_destination`; other verdicts must omit it. The destination
is one of the five semantic states (`backlog`, `ready`, `active`, `blocked`,
or `completed`). `confidence` is from 0 through 1, `reason` is 1–2,000
characters, and `evidence_refs` contains at most 100 unique safe identifiers
(each at most 512 characters; query strings, fragments, executable URLs, and
data URLs are rejected). New findings always begin with `review_state` set to
`pending`; approval or dismissal is accepted only through the versioned review
endpoint. A returned finding contains its immutable snapshot fields plus
`version` (starting at 1), timestamps, and the read-time boolean
`changed_since_audit`, which is true when the task version or source column no
longer matches the captured snapshot. Repeating the same finding for the same
task is safe; changing its snapshot returns `409`.

`PATCH /api/v1/audit-findings/{finding}` requires the finding's strong quoted
`If-Match: "vN"` and a body containing `review_state`. It may also contain
`proposed_semantic_destination`; omission preserves the existing value and
explicit `null` clears it. Only review metadata changes. The response carries
the incremented finding `ETag`; stale versions return `409` with the normal
conflict envelope. `Idempotency-Key` is supported on all audit writes and
should be sent for retries.
Approval is accepted only after the agent finalizes the run as `complete` or
`partial`. Approving a queued/running/failed run, or a finding whose task
version or source column has already changed, returns `409`; the finding
remains pending.

Audit reads, finding appends, finalization, and finding review never move or
otherwise mutate a task. A client should show the captured source,
destination, evidence, confidence, and current drift state in a read-only
preview, then require an explicit confirmation before calling the separate
task-move endpoint. Approving a finding is not an apply operation.

`POST /api/v1/tasks/{task}/move` is the only audit reconciliation apply step.
It requires `tasks:write`, `If-Match` for the current task version, and a
non-empty `Idempotency-Key`. Its canonical request shape is:

```json
{
  "destination_column_id": "col_ready",
  "expected_source_column_id": "col_backlog",
  "source": "board_audit",
  "reason": "Approved board-audit recommendation."
}
```

The destination and expected-source fields also accept the compatibility
aliases `destination_column`, `to_column_id`, `to_column`, `column_id`,
`column`, and `source_column_id`, `from_column_id`, `expected_column_id`,
`source_column`, `from_column`, respectively; multiple aliases must agree.
`source` is required and capped at 200 characters; `reason` is optional and
capped at 10,000 characters. `position` is not accepted: the server computes
the destination position atomically. Only columns with semantic state
`backlog` or `ready` are valid destinations. The expected source column,
current task version, unfinished state, and claim state are guarded in the
same transaction. Any live claim—including the caller's own—returns `409`
`task_already_claimed`; stale version or source returns `409` (`stale_task` or
`conflict`) and does not move the task. A successful move increments the task
version, returns the normal full Task or `{ "id": "...", "version": N }`
write-only response, sets the strong task `ETag`, and emits a `task.moved`
event containing the from/to columns, old/new position, resulting version,
actor, source, and reason. An idempotent retry replays the original response.

### Live agent work

`POST /api/v1/tasks/{task}/progress` publishes the current live-work snapshot
for a task. The caller must be the actor that owns an unexpired active claim;
bearer tokens also require the `tasks:claim` scope and must remain inside their
project ceiling. The task must not already be complete. The request requires
the exact quoted current task ETag in `If-Match: "vN"`; missing or malformed
preconditions use the normal `428` or `400` responses, and a stale ETag returns
`409` with the current task. Clients should also send an `Idempotency-Key` for
safe retries. A successful publish advances the task version and returns the
normal full-or-reduced Task mutation response with its new strong ETag. Retries
with the same key replay the original response.

The JSON body is a complete replacement snapshot. Required, non-null fields
are `operation_id`, `state`, and `summary`:

```json
{
  "operation_id": "deploy-42",
  "state": "working",
  "summary": "Validated the migration against the copied database.",
  "phase": "Preflight",
  "next_action": "Run the release smoke test",
  "checkpoint_refs": ["backup", "preflight", "smoke"],
  "checkpoint_completed": 2,
  "checkpoint_total": 3
}
```

`state` is one of `working`, `waiting`, `verifying`, or `handoff`; it describes
agent coordination and is independent of the task's board-column state.
`phase` and `next_action` are optional strings. `checkpoint_refs` is an
optional list of unique reference strings. `checkpoint_completed` and
`checkpoint_total` are optional but must be supplied together; counts satisfy
`0 <= checkpoint_completed <= checkpoint_total <= 100`, and a non-empty refs
list must contain exactly `checkpoint_total` entries. Omitting optional fields
clears their prior snapshot values; explicit JSON `null` is not accepted.
`operation_id` is a stable safe identifier for the current operation. Reusing
it preserves `started_at`; changing it starts a new operation. The server
sets `actor_id` and the `started_at`/`updated_at` timestamps; the timestamps
are UTC RFC 3339 values and are never accepted from the client.

Every Task response that includes a snapshot embeds `agent_work` with
`operation_id`, `actor_id`, `state`, `phase`, `summary`, `next_action`,
`checkpoint_refs`, optional paired checkpoint counts, `started_at`,
`updated_at`, `stale`, and `action_needed`. `stale` is computed at read time
when `updated_at` is at least 15 minutes old, using the server clock and an
inclusive boundary. It is deterministic and is not a task failure: the server
does not mark the task failed or release its claim merely because a pulse is
stale. `action_needed` is true when the state is `waiting` or `handoff`, or
when the pulse is stale. These are response/filter signals, not additional
writeable states. Completion preserves the latest snapshot as historical
context while forcing both liveness flags to false. Completed tasks are
excluded from every `agent_state` and `action_needed=true` result; reopening a
task restores ordinary read-time classification of the retained snapshot.

Publishing is atomic. Alongside the snapshot, the server appends a
`task.progressed` event whose payload is bounded safe metadata (`version`,
`state`, `completed`, and `total`), and a `comment.created` event for the
generated narrative comment. The comment body contains the required summary
and, when supplied, `Next: {next_action}`; free-form narrative is therefore
kept out of the structured event payload. Ordinary comments remain compatible:
`POST /api/v1/tasks/{task}/comments` still creates an independent comment and
does not require an agent claim, progress fields, or the progress endpoint.

Comments return `id`, `task_id`, `actor_id`, `body`, `created_at`, and
`updated_at`. On create, `body` and compatibility field `text` are accepted;
when both are non-empty, `body` wins, otherwise a non-empty `text` is used.
Both empty returns `400`.

### Durable task activity timeline

`GET /api/v1/tasks/{task}/timeline` requires `tasks:read` and returns the
task's durable activity as `{ "data": [...], "next_cursor": "" }`. The page
is newest-first and accepts `limit` (default 50, capped at 200), an optional
`kind` filter (`agent_progress`, `comment`, or `task_change`), and an opaque
`before` cursor from a prior item or `next_cursor`. Cursors are keyset
boundaries rather than offsets, so newer writes do not shift an older page;
malformed, empty, repeated, or overlong `before` values return `400`.

Each item has `id`, its stable opaque `cursor`, `kind`, `task_id`, an enriched
`actor` (`id`, `kind`, and `name`, or `null` when the actor no longer exists),
`created_at`, and exactly one typed payload. `agent_progress` uses `progress`
with the persisted operation, state, phase, summary, next action, checkpoint
references/counts, and start time. `comment` carries the canonical Comment
object. `task_change` carries the original event ID/type/payload. The other
two payload fields are JSON `null`.

Progress publication appends one immutable structured history row while
retaining `task_agent_work` as the latest snapshot. Its generated narrative
comment and `comment.created` event are de-duplicated from the timeline and
represented by one `agent_progress` item. Ordinary comments are represented
once by their `comment` item; their `comment.created` event is not emitted as
a second change. Events and comments written before the history migration
remain readable, with legacy progress events represented as generic
`task_change` items rather than inferred rich progress records.

### Built-in bug tracker (internal MVP)

Bug tracking uses the existing task resource. Create a bug by sending
`kind: "bug"` and a nested `bug` object; `bug.actual_behavior` is required on
creation. A normal task may send `kind: "task"` and omit `bug`; omitting kind
preserves the legacy default of `task`. The nested bug
details may contain `reporter_id`, nullable `severity` (`s1` through `s4`),
`actual_behavior`, `expected_behavior`, `reproduction_steps`, `environment`,
`affected_version`, and lifecycle fields `resolution`, `resolved_by`,
`resolved_at`, and `duplicate_of`. Unset optional fields are omitted. PATCH
merges the supplied nested bug fields, so omitted fields are unchanged.
`reporter_id` is populated from the authenticated creator; resolution fields
are controlled by the lifecycle actions rather than arbitrary client input.

The bug lifecycle is intentionally small:

- A newly-created bug is open and may be untriaged. `POST .../triage` requires
  `severity` and may also change `priority`, `assignee`/`assignee_id`, or
  `column`/`column_id`.
- `POST .../resolve` requires `resolution`. The allowed values are `fixed`,
  `duplicate`, `not_planned`, `cannot_reproduce`, and `works_as_designed`.
  `duplicate_of` is required only for `duplicate` resolutions. Resolution
  records the actor and timestamp in `resolved_by` and `resolved_at`.
- `POST .../reopen` requires a non-empty `reason` and clears the resolution,
  resolved actor/timestamp, and duplicate reference. The reason is retained
  as a task comment and in the associated event history.

Severity describes the impact of a defect (`s1` is highest impact and `s4` is
lowest). Priority remains the scheduling signal for all work items. They are
independent: triage never changes priority unless the request supplies one,
and a high-priority task can still have a low-severity bug.

All three bug actions require the exact quoted `If-Match` task ETag and accept
`Idempotency-Key`. They return the same full-or-reduced task mutation response
as other task mutations and obey the `tasks:write` scope, task-read response
rules, claim checks, stale-version `409`, and idempotent replay rules. Every
accepted action records
the actor and appends its bug lifecycle event atomically with the task update;
clients can poll those events through `/api/v1/events?after=...`.

This is an internal-only MVP. It does not provide public issue intake, external
issue-tracker synchronization, email/Slack notifications, attachments,
service-level agreements, or a separate bug-permission model. Bugs remain
project tasks and use the existing board columns, claims, labels, comments,
scopes, and audit/event feed.

`GET /api/v1/issues` is the agent-friendly global bug listing. It requires
`tasks:read`, returns only `kind: "bug"` tasks, and respects the caller's
project ceiling. Unscoped identities may omit `project` to search all visible
projects; project-scoped bearer tokens may also omit `project` to search the
union of their permitted projects. An optional `project` ID, key, or slug
narrows the result to one permitted project. Optional filters are `project`,
`state`, `column`, `priority`, `severity`, `label`, `assignee`, `reporter`,
`resolution`, `agent_state`, `action_needed`, `q`,
`updated_after`, `cursor`, and `limit`. Use `severity=untriaged` and
`resolution=unresolved` to find bugs whose corresponding lifecycle field is
unset. Pagination and validation follow the existing collection contract.

## Pagination and filters

Collection routes default to 50 records and cap `limit` at 200. Non-positive,
malformed, or repeated `limit` values return `400`; values above 200 are capped.
Their `next_cursor` is an opaque offset cursor (the runtime accepts raw decimal
or URL-safe base64 offset input); malformed, negative, empty, or repeated
supplied cursor values return `400`. The terminal value is the literal empty
string, not JSON null. Boolean filters (`archived`, `favorite`, `action_needed`,
and agent `disabled`) reject empty, malformed, or repeated values. Agents are listed as
`kind=agent` only and disabled agents are excluded by default; pass
`disabled=true` to include them.

Task listings support `state`, `column`, `kind`, `priority`, `severity`,
`label`, `assignee`, `reporter`, `resolution`, `dependency`, `q`,
`updated_after`, `cursor`, `agent_state`, `action_needed`, and `limit`.
`dependency=blocked` selects tasks with at least one unmet live,
same-project prerequisite. `dependency=ready` selects tasks with at least one
live prerequisite and none unmet; tasks with no prerequisites do not match.
`kind` filters `task` or `bug`;
`severity`, `reporter`, and `resolution` apply to bug details. `agent_state`
accepts the published states `working`, `waiting`, `verifying`, and `handoff`,
plus the read-time conditions `stale` and `missing`. `missing` selects tasks
without a snapshot; `stale` selects snapshots whose 15-minute liveness window
has elapsed. `action_needed`
is a boolean filter and `action_needed=true` matches a snapshot whose state is
`waiting`/`handoff` or whose 15-minute liveness window has elapsed. These
filters classify unfinished work only; completed tasks never match them even
though their last snapshot remains available on the Task response. Filters
are evaluated against the server's current time. Column names and
label names are matched case-insensitively. Enum values must use the
documented lowercase spelling; mixed-case values are rejected. Enum,
identifier, boolean, and timestamp filters reject empty, malformed, repeated,
or out-of-range values with `400`; search values are capped at 200 characters
and repeated values are rejected.

`GET /api/v1/my-work` defaults to the existing assigned-or-actively-claimed
view for the current actor. `view=live` instead lists tasks with published
agent-work snapshots, including work by other agents, and accepts the
`agent_state`, `action_needed`, `dependency`, `state`, `priority`, `label`, `q`,
`updated_after`, `cursor`, and `limit` filters. `dependency=blocked` selects
work with an unmet live prerequisite. `dependency=ready` requires at least one
live prerequisite and none unmet; tasks with no prerequisites do not match. For
an unscoped human identity,
`view=live` can aggregate across all visible projects. A project-scoped bearer
token must supply `project` and may select only one permitted project;
omitting it returns `403`, just as for the global roadmap route. The normal
task-read scope and collection pagination rules still apply. Events use a
separate monotonic integer `after` cursor: a supplied empty, negative,
non-integer, malformed, or repeated value returns `400`; event `next_cursor`
is the last event's decimal cursor or the empty string.

## Roadmap, events, and agent administration

- `GET /api/v1/roadmap`
- `GET /api/v1/projects/{project}/roadmap`
- `GET /api/v1/my-work`
- `GET /api/v1/events?after={cursor}&project={project}`
- `GET|POST /api/v1/agents`
- `POST /api/v1/agents/{agent}/tokens`
- `DELETE /api/v1/tokens/{token}`

Roadmap summaries include `task_total`, `completed`, `completion_percent`,
top-level `state_counts` for backlog/ready/active/blocked/completed, `overdue`,
`due_soon`, `upcoming`, `upcoming_tasks`, and `recent_activity`. `due_soon` counts incomplete
tasks due from now through the next seven days. Global responses include
per-project totals in `projects`; those current project entries omit
`state_counts`. Project-scoped responses include `project` instead.

Bearer tokens accessing roadmap routes require `projects:read`. For bearer
tokens without `tasks:read`, both `upcoming` and `upcoming_tasks` are present as empty arrays; without
`events:read`, `recent_activity` is an empty array. A token receives only the
collections covered by its read scopes, while project totals and other project
aggregates remain available under `projects:read`. Humans and tokens with both
read scopes receive the full collections.

The global `/api/v1/roadmap` route aggregates all projects for an unscoped
identity. A project-scoped bearer token must supply a `project` query or use
`/api/v1/projects/{project}/roadmap`; omitting it returns `403`. The same
project query requirement applies to `/api/v1/my-work` for project-scoped
tokens.

`GET /api/v1/projects`, `GET /api/v1/projects/{project}/columns`,
`GET /api/v1/tasks/{task}/comments`, `GET /api/v1/projects/{project}/labels`,
and `GET /api/v1/roadmap` return `400` for invalid pagination or query values.
`POST /api/v1/auth/logout` returns `400` when an `Idempotency-Key` is supplied;
authentication routes do not cache idempotent responses.

Agent administration is human-admin-only (`403` for other actors): this applies
to listing/creating agents, issuing tokens, and revoking tokens. Bearer agent
tokens are not an alternative security scheme for these operations. Agent
records support descriptions and a project access ceiling. Token plaintext is
returned only once when a token is created; tokens are hashed at rest and may
be limited to project IDs. Token issuance deliberately rejects Idempotency-Key
with `400` because replay caching a secret is unsafe.

## Idempotency, limits, and errors

Create, update, delete, and task-action mutations that expose
`Idempotency-Key` replay the original response for the same authenticated
principal, key, method, path, and payload. Authentication, required scope, and
administrator checks happen before replay; mutable target lookup and ordinary
body validation happen only after a cache miss. Thus valid retries preserve
their original response even after a target is deleted or its state/version
changes. Successful replays preserve the original `Location` (and `ETag`)
headers. Bearer-token idempotency namespaces are isolated by immutable token
credential, so two tokens owned by one actor cannot replay each other's key.
For the same authenticated principal and key, a different method, path, or
payload returns `409`; different principals (and bearer credentials) may reuse
the visible key independently.

Validation, malformed request, and unknown JSON field errors are `400` (the
runtime does not use `422`). Common statuses are `401` authentication required, `403` forbidden
or CSRF/scope failure, `404` missing resource, `409` conflict/stale state,
`413` request body too large, `429` agent rate limited, `500` internal
error, `503` temporary authentication/body admission saturation with
`Retry-After: 1`, and `507` exhausted agent mutation resources.

The request body limit is 2 MiB. Agent bearer traffic is limited per actor to
20 requests/second with burst 40; mutations have a separate 1 request/second
bucket with burst 10. Agent mutation persistence also has a 64 MiB lifetime
budget; exhaustion returns `507`. Body-buffer saturation may return `503` with
`Retry-After`.

All accepted mutations record their actor and append an event in the same
database transaction. Bug lifecycle actions append `bug.triaged`,
`bug.resolved` (or `bug.duplicated` for duplicate resolution), and
`bug.reopened` events with lifecycle/version metadata; resolution events carry
the selected resolution, while a reopen reason is retained in the atomic
`comment.created` event and task comment. They are available through the same
`/api/v1/events` cursor and require `events:read` for bearer tokens.

Dependency mutations append `task.dependency_added` or
`task.dependency_removed` in the same transaction. The enclosing event's
`task_id` is the dependent task, and the payload shape is:

```json
{
  "dependent_id": "task_42",
  "dependent_key": "OPS-42",
  "prerequisite_id": "task_41",
  "prerequisite_key": "OPS-41",
  "version": 8
}
```

The `version` is the resulting dependent-task version. Whenever a
prerequisite's satisfied state changes, the server emits one
`task.dependency_state_changed` event for each direct live dependent. The
enclosing event's `task_id` is the dependent task. Its payload is:

```json
{
  "dependent_id": "task_43",
  "dependent_key": "OPS-43",
  "prerequisite_id": "task_41",
  "prerequisite_key": "OPS-41",
  "satisfied": true
}
```

This is a derived-readiness invalidation only: it does not increment the
dependent's editable task version. Polling clients should refetch each
affected task or its dependency graph, then recompute `dependency=blocked|ready`
views as needed. The notification is bounded to the 200 direct-dependent
limit and applies only to live, same-project relations.
