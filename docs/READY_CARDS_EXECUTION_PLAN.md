# Seven Ready cards execution plan

## Goal and scope

Deliver the exact seven cards currently in the TC Ready column:

1. **TC-9 — Add task dependencies and blocker relationships** (umbrella).
2. **TC-102 — Persist task dependency graph and summaries.**
3. **TC-103 — Expose task dependency API and activity.**
4. **TC-104 — Enforce prerequisite ordering across task lifecycles.**
5. **TC-105 — Manage dependencies in the task drawer.**
6. **TC-106 — Surface dependency readiness on boards and validate release.**
7. **TC-108 — Rebrand the application to Helm.**

TC-9 is an outcome card, not a sixth dependency implementation slice. Its work
is delivered by TC-102 through TC-106 and it closes only after their integrated
release gate passes. TC-108 remains one card with internal checkpoints; this
plan does not create more Roadmap cards.

## Recommended order

Although TC-108 is currently first in Ready, implement the dependency feature
before the repository-wide rebrand:

| Order | Card | Completion gate |
| --- | --- | --- |
| 0 | Scope gate | Resolve the cross-project mismatch and freeze Helm compatibility decisions. |
| 1 | TC-102 | Migration-safe graph, store operations, and batched summaries pass populated-data and retained-binary tests. |
| 2 | TC-103 | Scoped, versioned, idempotent API and activity contract is documented and tested. |
| 3 | TC-104 | Every lifecycle path enforces prerequisites atomically. |
| 4 | TC-105 | Accessible drawer management works without losing edits. |
| 5 | TC-106 | Board visibility, filters, end-to-end journeys, migration checks, and release checks pass. |
| 6 | TC-9 | Umbrella criteria are reconciled against the shipped feature and closed. |
| 7 | TC-108 | Helm is the canonical product/repository identity; compatibility, deployment, skill, and hosted-surface checks pass. |

Doing Helm last avoids renaming the agent skill and deployment tooling in the
middle of dependency delivery, prevents broad rebrand edits from colliding
with the store/API/UI work, and lets the final old-name scan cover every file
added by the dependency feature.

TC-103 and TC-104 can run concurrently only after TC-102's store interfaces
are fixed and each worker has explicit file ownership. The default is serial
delivery because both slices touch task mutations, events, errors, and tests.
TC-108 should have one implementation owner because it is repository-wide;
read-only reviewers can audit individual surfaces in parallel.

## Gate 0 — decisions required before implementation

### Reconcile TC-9 with TC-102 through TC-106

TC-9 currently requires relationships "across allowed projects." The detailed
feature plan and all five implementation slices deliberately support only live
tasks in the same project and reject cross-project edges.

The recommended v1 decision is **same-project dependencies**:

- update TC-9's criterion to match the prepared same-project design;
- retain a stable `dependency_cross_project` error;
- treat cross-project scheduling as explicitly deferred work.

If cross-project relationships are required now, do not start TC-102. Rework
the storage, authorization, cycle, project archive/delete, redaction, search,
UI, and test designs first. The current five slices are not sufficient for
that larger security and lifecycle boundary.

### Lock dependency safety and freshness contracts

Before TC-102, freeze these implementation rules as well:

- Use a tested immediate-writer transaction path for graph reads followed by
  writes. The current generic transaction helper begins a deferred SQLite
  transaction, so the implementation must not assume its first graph read has
  already serialized competing writers.
- Keep direct graph reads bounded. Default to at most 200 prerequisites and
  200 dependents per task, reject an edge that would exceed either bound with a
  stable error, and never return an unbounded relationship collection.
- Require `Idempotency-Key` explicitly in dependency mutation handlers; the
  existing generic wrapper permits mutations without one.
- Add typed dependency errors to the server mapping and redact bounded linked
  task details for callers that cannot read them.
- Do not rely on retained pre-012 binaries simply ignoring the new table. Add
  database-level fail-closed lifecycle guards, or another tested compatibility
  mechanism, so an old retained binary cannot claim, start, complete, reopen,
  or delete work in violation of live dependency rows.
- When a prerequisite changes completion state, publish bounded invalidation
  for each affected direct dependent (or an equivalent targeted contract) so
  open drawers and board caches refresh derived summaries without pretending
  the dependent itself was edited.

### Freeze what "Helm" changes

Use these defaults unless the owner deliberately expands the card:

- Helm is the product, repository, binary, image, package/module, service,
  documentation, and agent-skill brand.
- Keep the existing typography, color system, and layout. Replace the current
  `R` mark and Roadmap product copy with a reusable Helm mark/wordmark, favicon,
  and matching accessible names; a visual redesign is separate work.
- Keep **Roadmap** where it is a generic feature noun: the Roadmap overview,
  `/api/v1/roadmap`, related response types, and internal feature components
  need not be renamed merely to produce a zero-result text scan.
- Keep API paths, database schema identifiers, task/project IDs, existing
  project keys such as `TC`, event names, compatibility headers, health fields,
  and the session-cookie contract stable.
- Do not rename persistent storage or backup identities during this card.
  `/var/lib/roadmap`, `roadmap.db`, existing backup filenames, the Compose
  volume identity, and the production Unix account are rollback/data contracts,
  not user-facing brand. Record them in a reviewed legacy allowlist.
- Introduce `HELM_*` application and deployment configuration names while
  accepting documented `ROADMAP_*` aliases for a compatibility window. If both
  forms are set with different values, fail closed instead of silently choosing.
- Keep `tc.shanekanterman.dev` as a supported legacy production origin. A new
  canonical hostname requires a separate, explicitly approved parallel DNS and
  Cloudflare Access cutover; it must not be inferred from the visual rebrand.
- Rename the public repository and other hosted metadata only after code,
  updaters, CI, and deployment consumers understand the new canonical name.

## Preflight and integration discipline

At planning time the worktree contains unrelated, uncommitted visual/frontend
changes in `web/src/App.svelte`, `web/src/app.css`,
`web/src/lib/components/AgentPulse.svelte`, and `web/src/lib/state.ts`. Land,
stash, or otherwise isolate that owner’s work before claiming TC-105 or TC-108.
Do not overwrite it.

Use two stacked workstreams:

1. `feature/card-dependencies`, with a reviewable commit/checkpoint for each of
   TC-102 through TC-106.
2. `feature/helm-rebrand`, created from the dependency-integrated main branch.

Run focused tests before each card is completed and the full gate before each
workstream merges. Do not mark TC-9 complete merely because its child cards
exist, and do not combine a production hostname cutover with a code release.

## Dependency delivery

The detailed design remains in `docs/CARD_DEPENDENCIES_PLAN.md`. The execution
contract below adds the gates needed to carry it through the Ready queue.

### TC-102 — persistence and read model

Implementation:

- Add append-only migration `012_task_dependencies.sql`; `011` is the current
  latest migration, so no existing migration is edited or renumbered.
- Add compact dependency reference, collection, and summary types.
- Add transactional list/add/remove operations, same-project and live-task
  validation, self/duplicate rejection, and recursive direct/transitive cycle
  detection under a transaction that demonstrably acquires the writer lock
  before graph validation.
- Batch summaries for task collections and My Work rather than enriching each
  card with separate prerequisite/dependent queries.
- Guard soft deletion and retain existing bug `task_links` semantics.
- Add database-level fail-closed guards for the lifecycle boundaries a retained
  pre-012 binary can otherwise bypass, while allowing unrelated old-binary
  task edits to remain compatible.
- Enforce the documented direct-edge bounds in the same transaction as insert.

Verification:

- Store tests cover empty, one, many, fan-in, fan-out, duplicate, self-link,
  direct cycle, long cycle, soft deletion, and concurrent opposing inserts.
- Migrate a populated pre-012 database and compare task, link, comment, event,
  actor, and project counts plus integrity and foreign-key checks.
- Run a retained pre-012 binary against the upgraded copy and prove its normal
  unrelated task reads/writes remain usable while dependency-invalid claim,
  start, complete, reopen, and delete mutations fail without changing rows.
- Verify summary loading is bounded and does not become an N+1 query path.
- Prove opposing concurrent edge inserts have one deterministic winner without
  depending on a deferred read transaction.

### TC-103 — API and activity

Implementation:

- Add dependency GET/POST/DELETE handlers and routing with existing task ID/key
  resolution and project-ceiling authorization.
- Require `If-Match` and a non-empty `Idempotency-Key` in dependency mutation
  handlers; preserve claim ownership and administrator override behavior.
- Return stable error codes and bounded safe details for self, duplicate,
  cross-project, limit, cycle, stale-version, and inaccessible references.
- Extend central error mapping and scope-aware redaction for dependency details
  instead of collapsing the new failures to generic invalid/conflict responses.
- Emit one add/remove event and readable timeline entry per committed mutation.
- Define and document a bounded dependent-invalidation event/payload contract,
  and make polling able to refresh the related tasks.
- Add dependency filters to project task collections and My Work.
- Update `openapi.yaml`, regenerate checked-in JSON/client artifacts, and align
  `docs/API_CONTRACT.md`.

Verification:

- Test human, administrator, read-only, write-only, claim-only, scoped, and
  cross-project callers, including redacted conflict responses.
- Test exact idempotent replay, same-key/different-body rejection, stale ETags,
  missing idempotency headers, collection limits, and the absence of events for
  rejected mutations.
- Run OpenAPI generation/check and contract alignment tests.

### TC-104 — lifecycle ordering

Implementation:

- Centralize unmet-prerequisite checks in store transactions rather than HTTP
  handlers.
- Apply them to claim, generic patch/move into active or completed, complete,
  ordinary task reopen (including completed-to-blocked), bug triage/resolve/
  reopen, semantic column changes, and task soft deletion.
- Reject adding an unfinished prerequisite to already started/completed work.
- Protect a completed prerequisite from reopening when an unfinished dependent
  is active or actively claimed.
- Publish the TC-103 invalidation contract in the same transaction whenever
  completion/reopen changes a direct dependent's derived readiness.

Verification:

- Exercise every guarded path through direct store calls and HTTP contracts.
- Prove each failure is atomic: task, version, claim, graph, comments, links,
  and events remain unchanged.
- Race completion/reopen, dependency edits, claims, and lifecycle transitions.
- Prove completing the final prerequisite makes the dependent immediately
  eligible and refreshes dependent views without moving it or manufacturing a
  manual Blocked state.

### TC-105 — drawer management

Implementation:

- Add frontend models and API methods plus an independently loading dependency
  section in the task drawer.
- Present `Waiting on` and `Blocking` lists with key, title, completion state,
  and navigation to the linked task.
- Add a project-local accessible combobox that excludes the current task and
  existing edges.
- Keep dependency mutations separate from ordinary drawer Save state, with
  per-edge pending/error state and authoritative ETag refresh.

Verification:

- Component tests cover loading, empty, success, authorization failure, cycle,
  stale ETag, polling refresh, add/remove focus restoration, and unsaved edits.
- Keyboard and screen-reader behavior follows combobox/listbox semantics;
  status is not color-only, success is announced, and mobile targets are at
  least 44 pixels.

### TC-106 — board and release gate

Implementation:

- Add compact prerequisite/dependent badges, distinct dependency-blocked
  treatment, blocked action explanations, and blocked/ready filters.
- Merge authoritative dependency summaries correctly during polling and after
  drawer graph mutations.
- Add end-to-end coverage for graph creation, cycle rejection, blocked claim,
  blocked completion/resolve, prerequisite completion, linked navigation,
  refresh, filters, and mobile behavior.

Verification and release:

- Run Go tests/vet/race, web checks/unit tests/build, OpenAPI checks, browser
  tests, deployment-security checks, container build/smoke, and release checks.
- Before production migration, verify a pre-upgrade backup and migrate a
  populated copy. Compare row counts, relationships, stable IDs, schema state,
  integrity, and foreign keys.
- Verify the retained rollback binary against the upgraded database. Rollback
  changes the binary only; it never restores an older database automatically.
- Deploy, smoke the live API/UI, create and remove a test dependency, and
  confirm the event/timeline and newly-ready behavior before closing TC-106.

### TC-9 — umbrella closeout

Re-read every reconciled TC-9 criterion against evidence from TC-102 through
TC-106. Complete TC-9 only when the integrated release is live and verified.
Its completion comment should link the five child cards and state the chosen
same-project or cross-project scope explicitly.

## TC-108 — Helm rebrand

### 1. Produce a classified inventory and allowlist

Start from tracked source, not secret-bearing runtime directories:

```sh
git grep -n -i -E 'roadmap|trello|tc-roadmap|KanterLabs/tc|ROADMAP_'
git ls-files | rg -i 'roadmap|trello|tc-roadmap'
```

Classify every result as one of:

- product identity to rename;
- generic Roadmap feature terminology to keep;
- stable API/data/storage compatibility identifier to keep;
- historical/comparative wording to rewrite or intentionally retain;
- generated artifact to regenerate from its source.

Never scan or print ignored credentials, `.tc-deploy.env`, `data/`, or `dist/`
contents into reports. Commit a small reviewed legacy-identifier allowlist so
the final scan proves that every remaining occurrence is intentional.

### 2. Rebrand product UI and assets

- Replace product lockups in splash, authentication, desktop header, mobile
  header, settings/about copy, command-search labels, HTML title/description,
  and fallback embedded pages.
- Replace the current `R` blocks with one reusable Helm SVG mark and add the
  favicon/static variants actually referenced by the application. Keep the
  mark decorative when adjacent text supplies the accessible name.
- Clean each generated embed destination before copying the new Vite output so
  stale hashed Roadmap assets cannot survive beside the Helm bundle.
- Update product-specific tests and snapshots while retaining Roadmap labels
  for the genuine overview feature.
- Inspect signed-out, splash, board, drawer, settings, and mobile states in
  light and dark themes; test zoom, contrast, focus, reduced motion, and missing
  asset behavior.

### 3. Rename repository and build identities

- Change the Go module to the final canonical module path, move
  `cmd/roadmap` to `cmd/helm`, update internal imports, and build `helm`.
- Rename the web package, image/artifact names, Make targets or variables where
  they expose the old brand, and source filenames that represent the product
  rather than the Roadmap feature.
- Update Docker, Compose, CI display names, artifact names, concurrency groups,
  documentation, templates, tests, release manifests, and generated assets.
- Preserve the organization runner contract: short checks/deploy orchestration
  stay on `homelab`; browser, race, container, and other heavy jobs stay on
  `homelab-heavy`.

### 4. Add a rollback-compatible runtime bridge

- Make `HELM_*` configuration canonical and support explicit `ROADMAP_*`
  fallbacks with precedence/conflict tests. Do not rename secret values or
  expose them while changing GitHub secret names.
- Migrate browser storage keys with read-old/write-new behavior, or retain them
  in the legacy allowlist; never silently discard favorites, recents, or theme.
- Teach bundle/install/verify/rollback scripts to accept both retained legacy
  release layouts and new Helm layouts during the rollback window.
- Introduce `helm.service`, the Helm binary, and Helm release artifacts without
  deleting retained Roadmap binaries or overwriting the database.
- Keep the existing state root, database file, backup set names, volume, and
  Unix ownership contract. Existing backup and restore validation must still
  recognize all retained artifacts.
- Verify install, upgrade, failed-health automatic rollback, explicit rollback,
  backup, and isolated restore using populated data before production cutover.

### 5. Rename and republish the agent skill safely

TC-108's scope includes the bundled `tc-roadmap` skill. The current updater
hard-codes both `KanterLabs/tc` and `skills/tc-roadmap`, and installed lifecycle
hooks point into that directory, so an in-place deletion would strand agents.

- Publish the canonical Helm skill and terminology, script names, metadata,
  examples, authentication docs, updater source, and user-agent string.
- Install it alongside the legacy skill first. Add `HELM_*`/Helm credential
  locations with read-only fallbacks to existing TC Roadmap configuration.
- Reconcile lifecycle hooks atomically and prove unrelated hooks are preserved;
  trust and exercise the new hook before removing the old command.
- Keep a thin legacy updater/shim for a documented transition. It must lead
  existing installations to the canonical Helm package without auto-deleting
  credentials or unrelated skills.
- Run all skill, updater, hook, pagination, concurrency, and safe-redaction
  tests against fresh and upgraded installations.

### 6. Cut over hosted surfaces last

- With explicit authorization, rename `KanterLabs/tc` to the chosen Helm
  repository name and update description, topics, homepage, badges, templates,
  workflow labels, release text, and local remotes.
- Verify clone/fetch, raw updater downloads, branch protection, environments,
  repository variables/secrets by name only, Actions, and release publishing.
- If a new Helm hostname is approved, provision it in parallel, validate both
  UI and API Cloudflare Access applications and audiences, switch canonical
  links only after health/auth checks, and keep the TC hostname as a tested
  compatibility path through the observation window.
- Do not combine destructive credential cleanup, old-host removal, or persistent
  path migration with the initial rebrand release.

### 7. Helm completion gate

- Run the classified old-name scan; every remaining result is allowlisted and
  justified, not merely ignored.
- Run `make test`, `make vet`, `make lint`, `make openapi-check`, `make build`,
  the Go race suite, browser suite, Docker/container smoke, bundle verification,
  deployment-security tests, and skill tests.
- Verify the generated OpenAPI JSON and both embedded frontend trees match their
  sources.
- Verify a fresh install and an upgrade from the last Roadmap release against a
  populated database, then exercise binary rollback without database restore.
- Verify product copy/assets on desktop and mobile, agent discovery/claim/
  progress/complete through the republished skill, and live health/readiness.
- Close TC-108 only after hosted metadata and any approved production cutover
  are verified; otherwise leave a precise handoff for the external action.

## Final evidence package

The seven-card work is finished when the Roadmap record contains:

- one Gate 0 scope decision;
- per-card test and review evidence for TC-102 through TC-106;
- a verified populated-data migration and retained-binary rollback result;
- TC-9 closeout mapped to all five delivery cards;
- the Helm old-name classification/allowlist, asset inspection, compatibility
  matrix, skill upgrade test, build/test results, and hosted-surface checks;
- no automatic database restore, no lost credentials, and no unreviewed
  external rename or DNS mutation.
