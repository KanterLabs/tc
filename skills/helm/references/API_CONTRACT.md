# Helm agent API reference

This is the compact contract reference shipped with the Helm agent skill. The
canonical, human-edited repository contract is
[`docs/API_CONTRACT.md`](../../../docs/API_CONTRACT.md); this file keeps the
agent workflow self-contained after the skill is installed.

## Authentication and safe output

The skill reads `HELM_URL` and `HELM_TOKEN` from the process environment or a
user-owned mode-`0600` JSON file. The Helm application token is sent as
`Authorization: Bearer ...`. Optional `HELM_CF_ACCESS_CLIENT_ID` and
`HELM_CF_ACCESS_CLIENT_SECRET` are Cloudflare Access edge credentials; they are
not a substitute for the Helm application token and are sent only as the two
`CF-Access-*` headers. Never put either credential in argv, task data, logs, or
temporary files.

`auth-check` performs `GET /api/v1/auth/me`, which is read-only. Success is
sanitized to actor identity fields. Failure is a bounded JSON error with the
HTTP status, machine code, message, and an actionable hint. The CLI emits
machine-readable errors on stderr for all commands.

## Common request rules

- API paths are relative to `/api/v1`.
- Task and project references may be opaque IDs or stable keys/slugs; task key
  matching is case-insensitive.
- `If-Match` must be the exact strong ETag version, formatted as `"vN"`, for
  task mutations. Re-read after a `409` conflict and preserve other actors'
  work.
- New workflow mutations use one UUIDv4 idempotency key per logical operation.
  The generated `operation_id` is returned in the result; pass it again to
  replay the same operation safely. Existing commands use UUIDv4 by default;
  explicit non-UUID operation IDs remain in the deterministic `helm-<sha256>`
  namespace for compatibility with older automation. Multi-step commands
  deterministically derive a distinct UUIDv4 key per HTTP mutation from the
  operation ID, method, path, and body because the server reserves keys per
  request target.
- A normal write is retried only when it carries an idempotency key. Heartbeat
  is the sole keyless write and is server-side idempotent.
- Responses may be reduced to `{ "id": "...", "version": N }` for write-only
  bearer tokens. Do not assume a mutation response contains task details.

## Lease commands

```sh
python3 scripts/helm.py renew --task TC-42 --lease-seconds 1800
python3 scripts/helm.py release --task TC-42
```

Both commands resolve the task first, then send the exact current `If-Match`
version and one UUIDv4 idempotency key to the corresponding lease endpoint.
Only the current claim owner may renew or release an active lease.

## Read commands

```sh
python3 scripts/helm.py auth-check
python3 scripts/helm.py projects --all
python3 scripts/helm.py tasks --project TC --all
python3 scripts/helm.py events --after 0 --project TC
python3 scripts/helm.py timeline --task TC-42 --kind agent_progress
python3 scripts/helm.py timeline --project TC --before CURSOR
python3 scripts/helm.py dependencies list --task TC-42
python3 scripts/helm.py issues --severity untriaged --resolution unresolved
```

Collection results use `{ "data": [...], "next_cursor": "..." }` (projects
and tasks additionally identify their project). Other collection routes retain
opaque offset cursors supplied back as `--cursor`; task board routes return
`tc1` keyset cursors carrying the ordering boundary, project, project revision,
task-collection revision, and fixed `read_at`. If either revision changes,
continuation returns typed `409 task_collection_changed` with
`details.restart=true`; discard the cursor and restart from the first page.
Task/project timelines use the opaque keyset cursor as `--before`; the event
feed uses a monotonic integer `--after` cursor. `--all` follows pages and
returns an empty terminal cursor. Timeline `--kind` accepts `agent_progress`,
`comment`, or `task_change`.

## Dependency commands

```sh
python3 scripts/helm.py dependencies add \
  --task TC-42 --prerequisite TC-41 --operation-id UUIDV4
python3 scripts/helm.py dependencies remove \
  --task TC-42 --prerequisite TC-41 --operation-id UUIDV4
```

The client reads the dependent task version before each mutation and sends
`POST /tasks/{task}/dependencies` with exactly
`{"prerequisite":"..."}`, or `DELETE
/tasks/{task}/dependencies/{prerequisite}`. The server enforces same-project
edges, project ceilings, direct-edge limits, cycle prevention, claims, exact
ETags, and replay-safe idempotency. Stable conflict codes include
`dependency_cycle`, `dependency_limit_exceeded`, `dependency_cross_project`,
`dependency_already_exists`, and `idempotency_key_reused`.

## Bug workflows

```sh
python3 scripts/helm.py bug-report --project TC --title "Crash" \
  --actual-behavior "The command exits" --expected-behavior "The command completes"
python3 scripts/helm.py bug-triage --task TC-43 --severity s2
python3 scripts/helm.py bug-resolve --task TC-43 --resolution fixed
python3 scripts/helm.py bug-duplicate --task TC-43 --duplicate-of TC-44
python3 scripts/helm.py bug-reopen --task TC-43 --reason "Regression is reproducible"
```

Bug creation posts `kind: "bug"` with nested `bug.actual_behavior` (required)
and optional structured fields. Issue discovery uses `GET /issues` and supports
the server filters, cursor, and project ceiling. Triage requires severity;
resolve accepts `fixed`, `duplicate`, `not_planned`, `cannot_reproduce`, or
`works_as_designed`; duplicate resolution additionally requires
`duplicate_of`; reopen requires a reason. All lifecycle mutations send the
current task `If-Match` and one UUIDv4 key.
