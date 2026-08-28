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
completion timestamp are omitted when unset.

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
writeable states.

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
`resolution`, `q`,
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
`label`, `assignee`, `reporter`, `resolution`, `q`, `updated_after`, `cursor`,
`agent_state`, `action_needed`, and `limit`. `kind` filters `task` or `bug`;
`severity`, `reporter`, and `resolution` apply to bug details. `agent_state`
accepts the published states `working`, `waiting`, `verifying`, and `handoff`,
plus the read-time conditions `stale` and `missing`. `missing` selects tasks
without a snapshot; `stale` selects snapshots whose 15-minute liveness window
has elapsed. `action_needed`
is a boolean filter and `action_needed=true` matches a snapshot whose state is
`waiting`/`handoff` or whose 15-minute liveness window has elapsed. These
filters are evaluated against the server's current time. Column names and
label names are matched case-insensitively. Enum values must use the
documented lowercase spelling; mixed-case values are rejected. Enum,
identifier, boolean, and timestamp filters reject empty, malformed, repeated,
or out-of-range values with `400`; search values are capped at 200 characters
and repeated values are rejected.

`GET /api/v1/my-work` defaults to the existing assigned-or-actively-claimed
view for the current actor. `view=live` instead lists tasks with published
agent-work snapshots, including work by other agents, and accepts the
`agent_state`, `action_needed`, `state`, `priority`, `label`, `q`,
`updated_after`, `cursor`, and `limit` filters. For an unscoped human identity,
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
Reusing a key for a different request returns `409`.

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
