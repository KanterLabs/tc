---
name: tc-roadmap
description: Track substantive agent work, goals, progress, blockers, and completion in a TC Roadmap project. Use at the start of multi-step implementation, deployment, CI, maintenance, research, or other project work that should remain visible to Shane; keep the Roadmap task current throughout the work. Do not use for quick factual answers or casual conversation.
---

# TC Roadmap

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

4. Keep the returned task key. Mention it in normal progress updates when useful.

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
  --operation-id "stable-session-local-id"
```

Do not post narration, secrets, raw logs, customer data, tokens, local credential paths, or prompt contents. Update the Roadmap before a long external wait and after a material phase completes.

Use the structured options when they are known so the in-progress experience
can show a concise agent pulse without forcing Shane to read the full activity
log. States are `working`, `waiting`, `verifying`, and `handoff`. Counts must be
measurable checkpoints, not an invented percentage or ETA. Keep the message to
the result or decision and `--next` to the next concrete action. Use plain
`--message` for backward compatibility when structured context is unavailable.

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

Do not mark a task complete merely because the current chat is ending. Leave a concise progress comment when handing unfinished work to another session.

## Authentication and failures

The helper reads `TC_ROADMAP_TOKEN` or a mode-`0600` JSON file at `~/.config/tc-roadmap/credentials.json`. It also supports Cloudflare Access service credentials without printing them. Read [references/authentication.md](references/authentication.md) only when configuring or troubleshooting access.

The helper writes JSON results to stdout and sanitized errors to stderr. Treat `409` as a concurrency signal: re-read the task and preserve the other actor's work. Never paste credential values into commands, chat, task fields, logs, or source control.
