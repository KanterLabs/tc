# Helm

Helm is a small, self-hosted project board and internal bug tracker for
teams where humans and software agents move work together. Humans get a
focused Kanban workspace; agents get a stable, auditable API for discovering,
claiming, updating, and finishing tasks without sharing a human login.

Helm v1 intentionally keeps the model small: one board per project,
ordered semantic columns, and SQLite persistence. It is not intended to be a
Trello-compatible API or a full team-suite replacement.

## What you can do

- Organize work into projects with stable keys and URLs, favorites, recents,
  and a persistent project switcher (`Cmd/Ctrl+K`).
- Work from a board with Backlog, Ready, In progress, Blocked, and Done
  columns. Create tasks quickly, move them with drag-and-drop or keyboard
  controls, and filter by text, state, kind, priority, severity, label,
  assignee, reporter, resolution, or agent-work state. Claimed agent tasks
  expose a compact live pulse on the board and a fuller progress panel in the
  task drawer.
- Keep task context in Markdown descriptions, priorities, due dates, labels,
  assignees, comments, and chronological human/agent activity. Record bugs
  with actual versus expected behavior, reproduction steps, environment, and
  affected version.
- Follow assigned work in **My work** and all published agent pulses across
  permitted projects in cross-project **Live Work**, or inspect completion,
  overdue work, upcoming deadlines, and recent activity in **Roadmap**.
- Coordinate safely through atomic leased claims, renew/release/complete/block
  actions, bug triage/resolve/reopen actions, optimistic versions
  (`ETag`/`If-Match`), idempotency keys, and a cursor-based event feed.
- Capture read-only board audits, review immutable findings, and explicitly
  preview and apply guarded recommendations without moving work implicitly.
- Create agents and issue project-scoped bearer tokens with independently
  selected read, write, claim, and event scopes.
- Keep the live view current through bounded polling. The responsive browser
  UI remains usable on mobile widths and supports keyboard navigation,
  focus-visible controls, and screen-reader status announcements.

The built-in bug tracker is an internal MVP. Bugs use the existing task,
board, claim, comment, scope, and event model. Public issue intake, external
tracker synchronization, notifications, attachments, service-level
agreements, and a separate bug-permission model are out of scope for now. See
[`docs/API_CONTRACT.md`](docs/API_CONTRACT.md) for the lifecycle and complete
request/response contract.

Screenshots are not checked in yet; run the local stack below to see the
current UI.

## Architecture

```text
Browser (Svelte + TypeScript) ─┐
                               ├─ Go Helm server
Agent clients (JSON API) ──────┘    ├─ /api/v1 REST API
                                    ├─ /healthz and /readyz
                                    ├─ embedded frontend and migrations
                                    └─ SQLite (WAL, foreign keys, /data)
```

The browser and external agents use the same versioned API. The production
image is built as one unprivileged container; only the database/configuration
volume is writable. The checked-in `compose.yaml` binds local traffic to
`127.0.0.1:8080`, uses a persistent `roadmap-data` volume, drops Linux
capabilities, and runs with a read-only root filesystem.

## Run locally with Docker Compose

The commands below assume the repository checkout root and the checked-in
`compose.yaml`, `Dockerfile`, and `Makefile`.

Prerequisite: Docker Engine with the Docker Compose plugin.

```sh
docker compose up --build --detach
curl --fail http://127.0.0.1:8080/healthz
```

Visit <http://localhost:8080>. The local Compose defaults are local account
authentication, `http://localhost:8080` as the public origin, secure cookies
disabled for localhost, and demo seed data enabled. On the first visit, create
the local administrator in the setup screen. To start with an empty database,
set `HELM_DEMO_SEED=false` before starting:

```sh
HELM_DEMO_SEED=false docker compose up --build --detach
```

Stop the stack with:

```sh
docker compose down
```

`docker compose down -v` also deletes the local `roadmap-data` volume; use it
only when intentionally discarding local data.

Each signed-in human can connect their own Codex-enabled ChatGPT subscription
from **Settings → Your Codex subscription**. The container includes Codex and
uses the device-code flow, so this works on remote or headless installations.
Codex credentials remain in actor-isolated directories under the persistent
`/data/codex-users` volume; Helm never shares a host API key between users.
Non-container installs must make `codex` available on `PATH` or set
`HELM_CODEX_BINARY`, and may relocate the protected state root with
`HELM_CODEX_HOME_ROOT`.

Operators can immediately stop model turns without removing anyone's saved
connection by setting `HELM_LUNA_ENABLED=false` and restarting Helm. See
[`docs/LUNA_TASK_ASSIST.md`](docs/LUNA_TASK_ASSIST.md) for tuning, fallbacks,
privacy-safe metrics, and validation thresholds.

## Authentication and agent access

`HELM_AUTH_MODE` selects the human-facing mode:

- `local` (default): the first-run setup creates a local administrator; users
  sign in with an email/password session cookie.
- `cloudflare`: the service verifies Cloudflare Access identity assertions.
  Configure the HTTPS `HELM_CLOUDFLARE_ISSUER` and
  `HELM_CF_ACCESS_AUDIENCES` as a comma-separated list containing the UI
  and `/api/v1/*` application AUD tags. `HELM_CLOUDFLARE_JWKS_URL` is
  optional when the issuer's standard certificates endpoint is usable.
  Production also sets `HELM_PUBLIC_ORIGIN`,
  `HELM_SECURE_COOKIES=true`, `HELM_DEMO_SEED=false`, and binds only to
  loopback. Password login and ordinary proxy identity headers are not used in
  Cloudflare mode.
- `disabled`: development-only authentication bypass. Never use it for a
  reachable or production deployment.

`HELM_*` variables are canonical. Equal-value `ROADMAP_*` aliases remain
supported for retained releases and existing operator configuration; when
both spellings are set to different non-empty values, Helm fails closed.

Agents are separate actors. An administrator creates an agent, then issues a
token from Settings or `POST /api/v1/agents/{agent}/tokens`. Send it as:

```sh
export HELM_TOKEN='store-this-in-your-secret-manager'
curl --fail \
  -H "Authorization: Bearer ${HELM_TOKEN}" \
  http://127.0.0.1:8080/api/v1/projects
```

Available scopes are `projects:read`, `projects:write`, `tasks:read`,
`tasks:write`, `tasks:claim`, and `events:read`. An agent's project list is an
access ceiling; each token may narrow it but cannot widen it. Token plaintext
is returned only once and is hashed at rest. Do not commit or paste real
tokens into source, task data, or deployment artifacts.

## API and development

The API contract is documented in [`docs/API_CONTRACT.md`](docs/API_CONTRACT.md).
The human-edited OpenAPI source is [`openapi.yaml`](openapi.yaml); the checked-in
JSON document is [`internal/httpapi/openapi.json`](internal/httpapi/openapi.json)
and is served at `/openapi.json`.

Tasks declare `kind: task` or `kind: bug`. Bug creation requires nested
`bug.actual_behavior`; triage sets `severity` (`s1`–`s4`), resolve records a
documented resolution, and reopen records a reason. These mutations use the
same `If-Match`, idempotency, claim, and bearer-scope rules as other task
actions. Agents with `tasks:read` can use `GET /api/v1/issues` to list bugs
across their permitted projects with lifecycle, board, ownership, search, and
pagination filters.

Agent issue workflow:

1. Report with `POST /api/v1/projects/{project}/tasks`, `kind: "bug"`, nested
   bug details, and an `Idempotency-Key`; the server records the reporter.
2. Discover work with `GET /api/v1/issues?severity=untriaged`, then claim the
   selected task with `POST /api/v1/tasks/{task}/claim`.
3. Triage severity, priority, assignment, and destination together with
   `POST /api/v1/tasks/{task}/triage` and the current `If-Match` value.
4. Use the ordinary task patch, comment, claim-renewal, and event-polling APIs
   while working the bug.
5. Finish through `POST .../resolve` with an explicit resolution; use
   `POST .../reopen` with a reason if the regression returns. Retry mutations
   with the same idempotency key and refresh after a stale ETag conflict.

The Issues view exposes operational counts for open, untriaged, S1/S2,
recently resolved, and recently reopened bugs. Command search opens issue keys
and titles directly. Filters use the same global issue query vocabulary, so a
filtered URL can be bookmarked as a working view without a separate issue
permission or search system.

Agent work workflow:

1. Claim a task with `POST /api/v1/tasks/{task}/claim` using a token with the
   `tasks:claim` scope, then retain the returned strong task ETag.
2. Publish a complete progress snapshot with
   `POST /api/v1/tasks/{task}/progress`, the current `If-Match` value, and an
   `Idempotency-Key`. The request requires an `operation_id`, one of the
   documented agent-work states, and a non-empty `summary`; optional phase,
   next action, checkpoint references, and paired checkpoint counts are
   described in [`docs/API_CONTRACT.md`](docs/API_CONTRACT.md).
3. Refresh the ETag from each response before the next mutation. The server
   records the structured pulse and a readable activity comment atomically;
   ordinary `POST .../comments` remains available for notes that are not live
   progress snapshots.

The board and drawer read the same `Task.agent_work` snapshot. A pulse is
marked stale deterministically after 15 minutes without an update; stale is a
coordination signal, not a task failure or automatic claim release. Filter
task collections with `agent_state` or `action_needed=true`, and use
`/api/v1/my-work?view=live` for the Live Work view. Unscoped human identities
may see live work across their visible projects; a project-scoped bearer token
must include a permitted `project` query value.
Completed tasks retain their last snapshot as history, but its `stale` and
`action_needed` flags are inactive; completed tasks do not match live-work
filters and the browser does not render their snapshot as a live pulse.

Board-audit workflow:

1. List or start a project audit with `GET|POST
   /api/v1/projects/{project}/audits`. Audit reads require `tasks:read`; audit
   writes require `tasks:write`. Starting a run only captures audit metadata.
2. Read the bounded run summary and cursor-paginated findings with
   `GET /api/v1/audits/{audit}` and `GET
   /api/v1/audits/{audit}/findings`. The summary's `findings` array is always
   empty, and findings expose `changed_since_audit` drift.
3. Review a finding with `PATCH /api/v1/audit-findings/{finding}` using its
   `If-Match` ETag. Approval, dismissal, finalization, and all audit reads are
   task read/review operations; none moves a task.
4. After a separate read-only preview and explicit confirmation, call
   `POST /api/v1/tasks/{task}/move` with the current task `If-Match`, expected
   source column, provenance, and an `Idempotency-Key`. Only backlog and ready
   destinations are allowed, active claims reject the move, the server
   computes position atomically, and success emits `task.moved`. See
   [`docs/API_CONTRACT.md`](docs/API_CONTRACT.md) for exact finding, review,
   and move request fields and aliases.

The UI's Run audit action creates a queued run for an agent. The bundled Helm
skill processes that same run with `submit --audit AUDIT_ID`, so a
UI request is not duplicated before its findings are finalized for review.

```sh
# Contract and liveness checks
curl --fail http://127.0.0.1:8080/openapi.json

# Full build: npm ci, frontend check/test/build, asset embedding, Go build
make build

# Focused checks
make test
make vet
make lint
make web-check
make web-test
make openapi-check

# Regenerate the checked-in OpenAPI JSON after editing openapi.yaml
make openapi

# Compose helpers
make compose-up
make compose-down
make docker-build
```

The Makefile targets above are the source of truth for the repository's
validation sequence. Frontend-only development can use the scripts in
[`web/package.json`](web/package.json):

```sh
cd web
npm ci
npm run dev
npm run check
npm test
```

## Codex Helm skill

The installable [`helm`](skills/helm/SKILL.md) skill makes TC the
durable work record for coding agents. It creates or resumes one task for a
substantive workstream, records the goal and checkpoints, posts meaningful
progress, and completes or blocks the task with the same optimistic-concurrency
and claim semantics as any other API client.

Install it from the public repository with Codex's skill installer using the
GitHub path `KanterLabs/helm/tree/main/skills/helm`. The installed
`scripts/update_skill.py` command compares its recorded source revision with
GitHub `main` first, and only sparse-clones and atomically installs the skill
when the revision changed. A matching revision is a no-op for the skill fetch,
but still reconciles the local lifecycle hooks. The companion
[`scripts/install_hooks.py`](skills/helm/scripts/install_hooks.py)
additively installs bounded SessionStart, PostToolUse, PreCompact, and Stop
commands into `hooks.json`, preserving existing policy and never changing
Codex trust hashes; review/trust the command once in Codex `/hooks` after a
configuration change.
Agent and optional Cloudflare Access credentials stay in environment variables
or a mode-`0600` local file; see
[`skills/helm/references/authentication.md`](skills/helm/references/authentication.md).
The [`tc-roadmap`](skills/tc-roadmap/SKILL.md) package remains as a
compatibility shim for installed agents during the transition.

The Playwright suite (`npm run e2e`) expects an already-running server at
`http://127.0.0.1:18080` by default; install its browser once with
`npm run e2e:install`. CI shows the complete disposable-server setup in
[`.github/workflows/ci.yml`](.github/workflows/ci.yml).

## Production deployment

The intended homelab path is:

```text
Cloudflare Access
  → tc.shanekanterman.dev (UI and /api/v1/* applications)
  → roadmap-homelab Tunnel (retained infrastructure identity)
  → cloudflared in the Debian 12 `roadmap` LXC (retained guest identity)
  → helm.service on 127.0.0.1:8080
  → /var/lib/roadmap/data/roadmap.db
```

The data root, database and backup names, Unix account, Compose volume,
hostname, tunnel/guest identities, `X-Roadmap-Revision` header, Roadmap API
routes and schema names, and signed Roadmap v1 gateway envelope are stable
compatibility identifiers. See
[`docs/HELM_LEGACY_IDENTIFIERS.md`](docs/HELM_LEGACY_IDENTIFIERS.md) for the
reviewed allowlist.

The guest has no inbound application or SSH port; the application and
connector communicate over loopback. Releases use an immutable SHA-tagged Go
binary, a constrained Proxmox deployment identity, and Cloudflare Access/tunnel
reconciliation. The full bootstrap, host assumptions, firewall posture, and
recovery checks are in [`docs/OPERATIONS.md`](docs/OPERATIONS.md).

After the one-time Proxmox and Cloudflare bootstrap described there, pushes to
the `main` branch run [`.github/workflows/ci.yml`](.github/workflows/ci.yml).
The workflow runs Go/frontend checks, browser tests, and a container smoke
test, then deploys the release, publishes the proxied DNS record, and validates
<https://tc.shanekanterman.dev>. Normal deployment requires the GitHub Actions
secrets `ROADMAP_CLOUDFLARE_API_TOKEN`, `ROADMAP_DEPLOY_SSH_KEY`,
`ROADMAP_DEPLOY_KNOWN_HOSTS`, `ROADMAP_RELEASE_SIGNING_KEY`,
`ROADMAP_CF_ACCESS_CLIENT_ID`, and `ROADMAP_CF_ACCESS_CLIENT_SECRET`; secret
values stay in GitHub or the approved operator secret store and are never
placed in this README or the repository. Create the Cloudflare service token
and save its one-time secret with the manual `cloudflare.sh prepare` procedure
before enabling CI; CI refuses to create a token whose secret would remain
only on an ephemeral runner.

## Backups and rollback

Before each install, the host takes a SQLite online backup, verifies its
checksum and `PRAGMA integrity_check`, and stores it under
`/var/lib/roadmap/backups`. A pre-upgrade backup is complete only when its
release SHA, schema identifier/digest, database digest, and integrity metadata
are recorded alongside the verified backup. Additive schema migrations run in
transactions; the candidate release first performs its preflight checks on a
copy before the active database is touched. The database is kept outside
release directories and is never replaced by an executable upgrade. By
default, five releases and fourteen backups are retained.

The active release is switched atomically. Failed Helm health or
cloudflared checks automatically restore the previous release and restart the
services. A binary/release rollback changes only the active release pointer:
it never restores an older database or discards writes accepted by the
running service. A retained release can also be selected with the workflow's
`workflow_dispatch` `rollback_sha` input, or from a configured deployment
shell with:

```sh
./deploy/deploy-ci.sh rollback <40-character-release-sha>
```

The live validator can optionally send the Cloudflare service-token headers to
`/api/v1/roadmap` without an application bearer token. CI requires this probe
with `CF_ACCESS_CLIENT_ID` and `CF_ACCESS_CLIENT_SECRET` (mapped from the
production secrets above); it expects the origin's JSON `401` error and echoed
`X-Request-ID`, which distinguishes Helm from an Access edge error.

For manual backups, database restores, first-time bootstrap, and the exact
operator permissions, follow [`docs/OPERATIONS.md`](docs/OPERATIONS.md). A
database restore is a separate, explicitly requested operation against one
exact retained backup; the restore helper first takes a new recoverable
`pre-restore` snapshot of the current database before installing the selected
copy.
