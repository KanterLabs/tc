# Roadmap

Roadmap is a small, self-hosted project board and internal bug tracker for
teams where humans and software agents move work together. Humans get a
focused Kanban workspace; agents get a stable, auditable API for discovering,
claiming, updating, and finishing tasks without sharing a human login.

Roadmap v1 intentionally keeps the model small: one board per project,
ordered semantic columns, and SQLite persistence. It is not intended to be a
Trello-compatible API or a full team-suite replacement.

## What you can do

- Organize work into projects with stable keys and URLs, favorites, recents,
  and a persistent project switcher (`Cmd/Ctrl+K`).
- Work from a board with Backlog, Ready, In progress, Blocked, and Done
  columns. Create tasks quickly, move them with drag-and-drop or keyboard
  controls, and filter by text, state, kind, priority, severity, label,
  assignee, reporter, or resolution.
- Keep task context in Markdown descriptions, priorities, due dates, labels,
  assignees, comments, and chronological human/agent activity. Record bugs
  with actual versus expected behavior, reproduction steps, environment, and
  affected version.
- See assigned and claimed work across projects in **My work**, or inspect
  completion, overdue work, upcoming deadlines, and recent activity in the
  **Roadmap** view.
- Coordinate safely through atomic leased claims, renew/release/complete/block
  actions, bug triage/resolve/reopen actions, optimistic versions
  (`ETag`/`If-Match`), idempotency keys, and a cursor-based event feed.
- Create agents and issue project-scoped bearer tokens with independently
  selected read, write, claim, and event scopes.

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
                               ├─ Go Roadmap server
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
set `ROADMAP_DEMO_SEED=false` before starting:

```sh
ROADMAP_DEMO_SEED=false docker compose up --build --detach
```

Stop the stack with:

```sh
docker compose down
```

`docker compose down -v` also deletes the local `roadmap-data` volume; use it
only when intentionally discarding local data.

## Authentication and agent access

`ROADMAP_AUTH_MODE` selects the human-facing mode:

- `local` (default): the first-run setup creates a local administrator; users
  sign in with an email/password session cookie.
- `cloudflare`: the service verifies Cloudflare Access identity assertions.
  Configure the HTTPS `ROADMAP_CLOUDFLARE_ISSUER` and
  `ROADMAP_CF_ACCESS_AUDIENCES` as a comma-separated list containing the UI
  and `/api/v1/*` application AUD tags. `ROADMAP_CLOUDFLARE_JWKS_URL` is
  optional when the issuer's standard certificates endpoint is usable.
  Production also sets `ROADMAP_PUBLIC_ORIGIN`,
  `ROADMAP_SECURE_COOKIES=true`, `ROADMAP_DEMO_SEED=false`, and binds only to
  loopback. Password login and ordinary proxy identity headers are not used in
  Cloudflare mode.
- `disabled`: development-only authentication bypass. Never use it for a
  reachable or production deployment.

Agents are separate actors. An administrator creates an agent, then issues a
token from Settings or `POST /api/v1/agents/{agent}/tokens`. Send it as:

```sh
export ROADMAP_TOKEN='store-this-in-your-secret-manager'
curl --fail \
  -H "Authorization: Bearer ${ROADMAP_TOKEN}" \
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

## Codex Roadmap skill

The installable [`tc-roadmap`](skills/tc-roadmap/SKILL.md) skill makes TC the
durable work record for coding agents. It creates or resumes one task for a
substantive workstream, records the goal and checkpoints, posts meaningful
progress, and completes or blocks the task with the same optimistic-concurrency
and claim semantics as any other API client.

Install it from the public repository with Codex's skill installer using the
GitHub path `KanterLabs/tc/tree/main/skills/tc-roadmap`. The installed
`scripts/update_skill.py` command refreshes the skill atomically from `main`.
Agent and optional Cloudflare Access credentials stay in environment variables
or a mode-`0600` local file; see
[`skills/tc-roadmap/references/authentication.md`](skills/tc-roadmap/references/authentication.md).

The Playwright suite (`npm run e2e`) expects an already-running server at
`http://127.0.0.1:18080` by default; install its browser once with
`npm run e2e:install`. CI shows the complete disposable-server setup in
[`.github/workflows/ci.yml`](.github/workflows/ci.yml).

## Production deployment

The intended homelab path is:

```text
Cloudflare Access
  → tc.shanekanterman.dev (UI and /api/v1/* applications)
  → roadmap-homelab Tunnel
  → cloudflared in the Debian 12 `roadmap` LXC
  → roadmap.service on 127.0.0.1:8080
  → /var/lib/roadmap/data/roadmap.db
```

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
`/var/lib/roadmap/backups`. The database is kept outside release directories
and is never replaced by an executable upgrade. By default, five releases and
fourteen backups are retained.

The active release is switched atomically. Failed Roadmap health or
cloudflared checks automatically restore the previous release and restart the
services. A retained release can also be selected with the workflow's
`workflow_dispatch` `rollback_sha` input, or from a configured deployment
shell with:

```sh
./deploy/deploy-ci.sh rollback <40-character-release-sha>
```

The live validator can optionally send the Cloudflare service-token headers to
`/api/v1/roadmap` without an application bearer token. CI requires this probe
with `CF_ACCESS_CLIENT_ID` and `CF_ACCESS_CLIENT_SECRET` (mapped from the
production secrets above); it expects the origin's JSON `401` error and echoed
`X-Request-ID`, which distinguishes Roadmap from an Access edge error.

For manual backups, database restores, first-time bootstrap, and the exact
operator permissions, follow [`docs/OPERATIONS.md`](docs/OPERATIONS.md).
