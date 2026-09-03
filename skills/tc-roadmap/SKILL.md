---
name: tc-roadmap
description: Compatibility alias for the Helm agent-work skill. Use existing $tc-roadmap invocations safely during migration; new work should prefer $helm. Track substantive agent work, goals, progress, blockers, and completion without changing existing task or project IDs.
---

# TC Roadmap compatibility

This directory is retained for installed agents and explicit `$tc-roadmap`
invocations during the Helm migration. The canonical public skill is
`skills/helm`; this compatibility package keeps the existing script names,
state root, API paths, task IDs, and project IDs intact.

`scripts/update_skill.py` now fetches `https://github.com/KanterLabs/helm.git`
and installs the canonical sibling at `skills/helm` without deleting this
directory or credentials. Once Helm is present, `scripts/install_hooks.py`
reconciles the canonical hook while preserving any existing legacy command.

Use TC as the durable project record for substantive work. The chat plan is temporary; the Roadmap task is the shared source of truth humans and later agent sessions can inspect.

## Start

1. Run `python3 scripts/tc_roadmap.py projects` and match the current repository or workstream to an existing project. Do not silently create a project or put work in an unrelated project.
2. Search for an existing open task before creating one:

   ```sh
   python3 scripts/tc_roadmap.py tasks --project TC --query "short task title"
   ```

3. Resume and claim the matching task:

   ```sh
   python3 scripts/tc_roadmap.py resume \
     --task TC-1 \
     --operation-id "stable-session-local-id"
   ```

   Or start one with a concrete goal and measurable checkpoints:

   ```sh
   python3 scripts/tc_roadmap.py start \
     --project TC \
     --title "Publish Roadmap skill" \
     --goal "Ship a validated public skill and session updater" \
     --step "Validate the skill" \
     --step "Verify the updater" \
     --operation-id "stable-session-local-id"
   ```

   Resume is a claim-and-activate operation: the task must be claimed by this
   session and placed in the project's Active/In progress column before work
   continues. Keep the returned task key. Mention it in normal progress
   updates when useful.

4. On a machine where the lifecycle hook is not already installed, run
   `python3 scripts/install_hooks.py`. The installer merges only the TC
   command into `hooks.json`, preserves existing policy and commands, and
   never edits Codex trust hashes or `config.toml`. If it reports a change,
   review and trust the new command once in Codex `/hooks`; a later no-op does
   not require another review.

For planning work that should remain unclaimed in Backlog, create a separate,
implementation-sized card with acceptance criteria:

```sh
python3 scripts/tc_roadmap.py backlog \
  --project TC \
  --title "Show live agent progress on task cards" \
  --goal "Make active agent work understandable without opening activity" \
  --step "Show agent, phase, checkpoint count, and last update" \
  --operation-id "stable-planning-item-id"
```

Do not use `backlog` for the work currently being performed; the current
workstream still needs one active, claimed task.

If the correct project is missing or credentials do not permit the work, report that clearly and continue the underlying task when safe. Do not widen token scopes or create credentials without Shane's authorization.

## Keep progress current

Post only meaningful milestones, decisions, validation results, and scope changes:

```sh
python3 scripts/tc_roadmap.py progress \
  --task TC-1 \
  --state working \
  --phase "Updater" \
  --completed 1 \
  --total 3 \
  --message "Skill validation passed; installing the session updater next" \
  --next "Verify a no-op update" \
  --checkpoint-ref "skills/tc-roadmap/SKILL.md" \
  --checkpoint-ref "skills/tc-roadmap/scripts/tc_roadmap.py" \
  --checkpoint-ref "skills/tc-roadmap/scripts/test_tc_roadmap.py" \
  --operation-id "stable-session-local-id"
```

Do not post narration, secrets, raw logs, customer data, tokens, local credential paths, or prompt contents. Progress belongs at meaningful checkpoints and before a long external wait, after compaction recovery, and after a material phase completes. Do not turn routine polling or hook heartbeats into comments.

Use the structured options as the canonical progress API so the in-progress
experience can show a concise agent pulse without forcing Shane to read the
full activity log. Structured progress is sent as a claimed, version-checked
task update; when `--state` is omitted, it defaults to `working`. States are
`working`, `waiting`, `verifying`, and `handoff`. Counts must be measurable
checkpoints, not an invented percentage or ETA, and `--checkpoint-ref` can be
repeated for relevant files, tests, or other safe evidence references. Keep
the message to the result or decision and `--next` to the next concrete
action. Use plain `--message` only for backward compatibility when structured
context is unavailable; that form remains a task comment.

The installed lifecycle hook may refresh a claimed task's agent-work liveness
quietly; it does not renew the claim lease. A heartbeat must not create a
comment or activity event, and it must not stand in for a meaningful
structured update. After a session resumes from
compaction, restore the saved task mapping, inspect the safe metadata, and
post the next meaningful checkpoint before continuing; call `resume` only when
the task or claim actually needs reactivation (or when beginning a genuinely
resumed workstream). Local hook state may contain only safe recovery metadata
such as the task key, operation ID, checkpoint counters, and timestamps; it
must never contain tokens, prompts, task content, comments, raw tool output,
or credential paths.

## Read-only Board Audit

Run a Board Audit only when the user or an explicitly delegated task requests
an inspection. It is on-demand and never runs from lifecycle hooks:

```sh
python3 scripts/tc_roadmap.py audit --project TC
```

The command follows every task page for the `backlog` and `active` semantic
states by default. Repeat `--state` (or `--semantic-state`) to inspect a
different set of semantic states; `--limit` controls the API page size and
`--evidence-limit` bounds recent comment evidence per task. Every request made
by `audit` is `GET`, including optional comments used for bounded evidence.

Each returned task context includes its key, immutable ID, version, current
column semantic state/name, title, bounded description plus parsed goal and
acceptance criteria, priority, claim lease, agent-work snapshot, liveness,
recent safe evidence, and a rubric verdict: `correct`, `needs_attention`, or
`move_proposed`. A suggested destination, numeric confidence, concise reason,
warnings, and evidence references accompany the verdict. Stale or missing
agent pulses are warnings only; they never prove that work is abandoned or
complete. The v1 audit considers semantic placement only and never changes a
task or reorders its numeric position.

Agents must analyze the returned `rubric` and task contexts without mutating
the board. During an audit do not claim, renew, release, move, complete,
block, comment, publish progress, emit activity, or call any write endpoint.
The agent owns the semantic judgment in each finding. If a durable record is
requested, save the audit JSON and submit it separately; submission persists
the agent's findings but does not move tasks:

```sh
python3 scripts/tc_roadmap.py submit \
  --project TC \
  --input audit.json \
  --scope board \
  --operation-id "audit-session-1"
```

Submission creates a run, appends each finding with a deterministic
idempotency key, and finalizes the run. Submitted findings always begin
pending; approval is never imported from the input file. When the web UI has
already queued the run, process that exact run instead of creating a duplicate:

```sh
python3 scripts/tc_roadmap.py submit \
  --project TC \
  --audit AUDIT_ID \
  --input audit.json \
  --operation-id "audit-session-1"
```

The CLI resolves `--project`, so bearer tokens need `projects:read` plus
`tasks:read`; submission and reconcile apply additionally need `tasks:write`.
To inspect
approved findings, reconciliation is preview-first and read-only by default:

```sh
python3 scripts/tc_roadmap.py reconcile --audit AUDIT_ID
```

The preview re-fetches paginated findings, current columns, and current tasks;
it skips queued, running, or failed runs, changed snapshots, unapproved
findings, active claims, unavailable
destinations, and lifecycle destinations (`active`, `blocked`, or `completed`)
while reporting the required follow-up action. It never mutates a task unless
`--apply` is explicitly supplied. Applying an eligible backlog/ready move
uses the captured version and source column with `source: board_audit` and a
stable idempotency key, so rerunning a command is safe. Reconciliation never
reorders numeric positions; lifecycle actions remain explicit claim/resume,
block, or complete operations.

## Protect persistent data

When work changes storage, schemas, migrations, backup/restore, or deployment,
make data preservation an explicit goal and checkpoint. Inspect the existing
upgrade and recovery path before editing it. Do not reset, recreate, replace,
or restore over user data as a normal upgrade strategy.

Prefer transactional, retry-safe, additive migrations that remain compatible
with every retained rollback binary. Verify the change against a populated
database, including integrity, relationships, stable identifiers, and expected
row counts. Confirm a verified pre-upgrade backup exists before production
migration and that rollback does not silently restore an older database or
discard writes. Treat a database restore as a separate destructive recovery
operation requiring explicit authorization, an exact backup target, and a
pre-restore snapshot.

## Finish

Complete the task only after the requested outcome and proportionate verification are finished:

```sh
python3 scripts/tc_roadmap.py complete \
  --task TC-1 \
  --comment "Published and verified the public skill and session updater" \
  --operation-id "stable-session-local-id"
```

If work cannot proceed, move it to Blocked with a specific actionable reason:

```sh
python3 scripts/tc_roadmap.py block \
  --task TC-1 \
  --reason "Repository visibility change requires organization approval" \
  --operation-id "stable-session-local-id"
```

Before ending a session, leave exactly one durable outcome: complete the task,
block it with an actionable reason, or publish a structured `handoff` or
`waiting` update with the next action. A final prose message or a quiet hook
heartbeat alone is not a handoff. Do not mark a task complete merely because
the current chat is ending; leave safe recovery metadata for the next session
when work remains.

## Authentication and failures

The helper accepts canonical `HELM_URL`/`HELM_TOKEN` settings and the
compatibility aliases `TC_ROADMAP_URL`, `TC_ROADMAP_TOKEN`, and
`ROADMAP_TOKEN`; conflicting values fail closed. It also reads the existing
mode-`0600` JSON file at `~/.config/tc-roadmap/credentials.json` (or a
canonical Helm credential file) and supports Cloudflare Access service
credentials without printing them. Read
[references/authentication.md](references/authentication.md) only when
configuring or troubleshooting access.

The helper writes JSON results to stdout and sanitized errors to stderr. Treat `409` as a concurrency signal: re-read the task and preserve the other actor's work. Never paste credential values into commands, chat, task fields, logs, or source control.
