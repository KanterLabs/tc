# Agent-First Trello Clone — Implementation Plan

## 1. Product goal

Build a small, self-hosted project board that is primarily operated by software
agents through a stable API while remaining fast and pleasant for a human to
inspect and update through a web UI.

The product should optimize for:

1. Reliable coordination between multiple agents.
2. Switching between projects in one or two interactions.
3. Simple installation, upgrades, and backups.
4. A focused task model instead of feature parity with Trello.

## 2. Decisions for the first release

- A project is the top-level organizational unit.
- Each project has one board in v1.
- Each board has ordered, configurable columns.
- Columns carry a stable semantic state: `backlog`, `ready`, `active`,
  `blocked`, or `completed`.
- Humans authenticate with local accounts.
- Agents authenticate with scoped bearer tokens.
- Every state-changing request records the responsible actor.
- The v1 task model includes an internal-only bug kind with a small triage and
  resolution lifecycle; public issue intake and external tracker integrations
  are deferred.
- SQLite is the default and only supported v1 database.
- The application deploys as one container with one persistent data volume.

## 3. Recommended architecture

### Backend

- Go HTTP service.
- SQLite with WAL mode, foreign keys, and a configured busy timeout.
- Versioned REST API under `/api/v1`.
- OpenAPI 3.1 document served at `/openapi.json`.
- Embedded database migrations.
- Compiled frontend assets embedded into the server binary.

### Frontend

- Svelte, TypeScript, and Vite.
- Client-side navigation with stable project URLs.
- Accessible drag-and-drop with keyboard alternatives.
- API-generated TypeScript client where practical.

### Deployment

- One production Docker image.
- One Compose file for local and self-hosted operation.
- Persistent `/data` volume containing the database and configuration.
- Health and readiness endpoints.
- Documented file-level backup and restore procedure.

## 4. Core domain model

### Actor

A human or agent responsible for an action.

- `id`
- `kind`: `human` or `agent`
- `name`
- `disabled_at`

### Project

- `id`
- `key`: short uppercase key used in task references
- `slug`: stable URL identifier
- `name`
- `description`
- `archived_at`

### Column

- `id`
- `project_id`
- `name`
- `semantic_state`
- `position`
- Optional work-in-progress limit after v1

### Task

- Internal immutable ID
- Project-local number and display key, such as `OPS-42`
- `project_id`
- `kind`: `task` or `bug`
- `column_id`
- `title`
- Markdown `description`
- `priority`: `low`, `normal`, `high`, or `urgent`
- Optional nested bug details: reporter, nullable severity (`s1`–`s4`), actual
  and expected behavior, reproduction steps, environment, affected version,
  resolution, resolver/timestamp, and duplicate reference
- `claimed_by`
- `claim_expires_at`
- `due_at`
- Monotonic `version`
- Created, updated, and completed timestamps

### Supporting records

- Comments
- Labels and task-label relationships
- API tokens and scopes
- Append-only activity events
- Idempotency request records

## 5. API behavior

The API is the primary product interface. The web UI consumes the same public
API rather than a separate private backend.

### Initial resources and actions

```text
GET    /api/v1/projects
POST   /api/v1/projects
GET    /api/v1/projects/{project}
PATCH  /api/v1/projects/{project}

GET    /api/v1/projects/{project}/columns
POST   /api/v1/projects/{project}/columns
PATCH  /api/v1/columns/{column}

GET    /api/v1/projects/{project}/tasks
POST   /api/v1/projects/{project}/tasks
GET    /api/v1/issues
GET    /api/v1/tasks/{task}
PATCH  /api/v1/tasks/{task}

POST   /api/v1/tasks/{task}/claim
POST   /api/v1/tasks/{task}/release
POST   /api/v1/tasks/{task}/complete
POST   /api/v1/tasks/{task}/triage
POST   /api/v1/tasks/{task}/resolve
POST   /api/v1/tasks/{task}/reopen
POST   /api/v1/tasks/{task}/comments

GET    /api/v1/events
```

### Agent-safety requirements

- Claiming is an atomic conditional database update.
- A claim can have a lease duration and be renewed by its owner.
- An expired claim may be acquired by another actor.
- Mutation requests accept an `Idempotency-Key` header.
- Task updates require the last observed version or `If-Match` value.
- A stale update returns `409 Conflict` with the current task representation.
- Collection endpoints use cursor pagination and bounded page sizes.
- Errors use one consistent machine-readable envelope.
- Project task listing supports state, column, kind, priority, severity, label,
  assignee, reporter, resolution, and update-time filters.
- `GET /api/v1/issues` provides a global, `tasks:read`-scoped bug listing with
  optional project, lifecycle, board, ownership, search, and pagination
  filters; project-scoped tokens remain inside their project ceiling.
- All identifiers and timestamps have stable, documented formats.

### Internal bug lifecycle

- Creating a bug requires `kind: bug` and `bug.actual_behavior`; ordinary tasks
  use `kind: task` and omit bug details.
- Triage requires severity and may optionally set priority, assignee, or
  column. Severity describes impact; priority remains the scheduling signal.
- Resolve requires a resolution; duplicate resolutions also require
  `duplicate_of`. Reopen requires a reason and clears resolution metadata.
- Triage, resolve, and reopen require `If-Match` and accept an
  `Idempotency-Key`, return the scope-aware task mutation body, and honor the
  same claim/concurrency checks as other task actions.

### Event feed

Agents can incrementally poll `/api/v1/events?after=<cursor>` for project,
task, claim, comment, completion, and bug lifecycle (`bug.created`,
`bug.updated`, `bug.triaged`, `bug.resolved`, `bug.duplicated`, and
`bug.reopened`) changes. Webhooks and streaming transports are deferred until
there is a demonstrated need.

## 6. Human experience

### Global shell

- Persistent project switcher in the application header.
- `Cmd/Ctrl+K` opens searchable project navigation.
- Recent and favorite projects appear before the full project list.
- Stable project route: `/p/{project-slug}`.
- Global “My work” view for assigned or claimed tasks across projects.
- One-step global task creation with an explicit destination project.

### Board

- Horizontally arranged columns with card counts.
- Drag-and-drop movement plus keyboard-accessible move controls.
- Quick-add at the top or bottom of each column.
- Filters for label, priority, assignee, and task text.
- Task changes from other actors appear without a full-page reload.

### Task detail

- Open in a side drawer so board context is preserved.
- Edit title, description, priority, labels, due date, and assignment.
- For bugs, show reporter and reproduction context, severity, and triage,
  resolve, and reopen controls.
- Show claim owner and expiration clearly.
- Show comments and chronological activity together.
- Identify every action as human- or agent-generated.

## 7. Delivery milestones

### Milestone 0 — Foundation

Deliverables:

- Initialize Git and the repository layout.
- Establish Go and frontend workspaces.
- Add formatting, linting, unit-test, and build commands.
- Add the base container and Compose configuration.
- Record architecture decisions in short ADRs.

Exit criteria:

- One command starts the development environment.
- One command runs all initial validation.
- The production image serves a placeholder UI and health endpoint.

### Milestone 1 — Projects and tasks API

Deliverables:

- Database migrations and repository layer.
- Actor, project, column, task, label, and comment models.
- Project-local task numbering.
- Project, column, task, and comment CRUD endpoints.
- Internal bug task kind with nested report context and lifecycle actions.
- Consistent validation and error responses.
- OpenAPI contract and API examples.

Exit criteria:

- A client can create a project, create a task, move it, comment, and list it.
- A client can create, triage, resolve, reopen, and filter a bug without a
  separate issue-tracker integration.
- Data survives container restarts.
- API contract tests cover success, validation, and not-found behavior.

### Milestone 2 — Agent coordination

Deliverables:

- Agent creation and scoped token management.
- Atomic claim, renew, release, complete, and block actions.
- Optimistic, idempotent triage, resolve, and reopen actions for bugs.
- Lease expiration behavior.
- Optimistic concurrency.
- Idempotent mutations.
- Append-only events and cursor polling.

Exit criteria:

- Two concurrent clients cannot successfully claim the same task.
- Retrying a create request with the same idempotency key creates one task.
- A stale client cannot silently overwrite a newer task version.
- An agent can complete the full poll, claim, update, and finish workflow.

### Milestone 3 — Core web UI

Deliverables:

- Login and application shell.
- Project creation, favorites, recents, and command-menu switching.
- Kanban board and quick-add.
- Task detail drawer, editing, comments, and activity.
- Search and core filters.
- Global “My work” view.

Exit criteria:

- A human can create and switch projects without visiting a settings page.
- All routine board actions work with mouse and keyboard.
- The UI clearly distinguishes human and agent activity.
- Common interactions remain responsive with at least 1,000 tasks in a project.

### Milestone 4 — Self-hosting hardening

Deliverables:

- First-run administrator setup.
- Secure token hashing and session cookie settings.
- Request limits, structured logs, and health checks.
- Backup, restore, upgrade, and rollback documentation.
- Database integrity and migration tests.
- Responsive layout and accessibility pass.

Exit criteria:

- A fresh installation can be running from documented steps in under ten
  minutes.
- Backup and restore are verified against a populated installation.
- Restarting or upgrading does not lose accepted writes.
- The release has no known high-severity security findings.

## 8. Testing strategy

- Unit tests for domain rules and request validation.
- SQLite integration tests for transactions, claims, and migrations.
- Contract tests against the generated OpenAPI definition.
- Concurrency tests for claims, versions, and idempotency.
- Frontend component tests for project switching and task editing.
- A small browser suite for the main human workflow.
- A black-box agent workflow test using only documented API endpoints.

The black-box workflow is the release-defining test: two agents discover the
same ready task, exactly one claims it, the winner posts progress and completes
it, and a human can observe the entire history in the UI.

## 9. Explicitly deferred scope

- Multiple boards per project
- Trello-compatible API behavior
- Trello import
- Attachments and object storage
- Public bug/issue intake and external issue-tracker synchronization
- Bug notifications, service-level agreements, and separate bug permissions
- OAuth, SAML, and directory synchronization
- Complex team roles and per-task permissions
- Email and push notifications
- Webhooks and WebSocket event delivery
- Rule-based automations
- Rich-text collaborative editing
- Native mobile applications

## 10. Release definition

Version 1 is ready when a small team can deploy one container, create several
projects, switch between them rapidly in the UI, and coordinate multiple agents
through the documented API without duplicate claims, lost updates, or ambiguous
authorship.

## 11. First implementation slice

Start with a vertical slice rather than building every layer independently:

1. Start the application and initialize SQLite.
2. Create and list projects through the API.
3. Create and list tasks in one project.
4. Display those tasks in a minimal project board.
5. Switch between two projects from a persistent selector.
6. Package the slice in the production container.

This proves the project boundary, API-to-UI path, persistence model, and
self-hosting shape before implementing coordination features.
