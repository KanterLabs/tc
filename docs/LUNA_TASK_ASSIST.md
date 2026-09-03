# Luna task assistance

Luna turns a rough idea in the new-task modal into a reviewable task draft. It
does not create or update a task. The user explicitly applies individual
fields (or all fields), can edit them freely, and submits through Helm's normal
task-creation API.

## Account and isolation model

Each human connects a Codex-enabled ChatGPT subscription with Codex App
Server's device-code flow. Helm starts one App Server session per actor and
sets a hashed, mode-`0700` `CODEX_HOME` beneath `HELM_CODEX_HOME_ROOT`. Host
`OPENAI_API_KEY` and `CODEX_API_KEY` values are removed from child environments
so a shared operator credential cannot silently become a user fallback.

Codex owns token persistence and refresh. Helm returns only connection state,
email, plan type, and the one-time device-code instructions. It never reads,
logs, or returns access or refresh credentials. Logout asks App Server to
remove the managed login and closes that actor's process. The persistent home
allows completed login state to survive Helm restarts.

## Context and safety bounds

The context builder examines at most 500 live tasks from the selected project,
then returns at most 12 completed and 12 open references within a 24,000-byte
text budget. Individual descriptions are capped at 1,200 bytes. Deleted and
cross-project tasks are excluded in SQL. Text relevance, recency, labels, and
direct dependency relationships determine a stable rank.

Historical text is serialized as explicitly untrusted JSON. Draft turns are
ephemeral, use approval policy `never`, a read-only sandbox with network access
disabled, and an actor-home working directory. The response must satisfy a
closed JSON schema. Helm rejects unknown fields, invalid lengths, unsupported
priorities, and any supporting task key absent from the supplied context.

## Tuning and evaluation gates

Defaults are `HELM_LUNA_MODEL=gpt-5.6-luna` and
`HELM_LUNA_EFFORT=medium`. Supported effort values are `low`, `medium`, `high`,
`xhigh`, `max`, and `ultra`. Tune the model only after the deterministic fixture
suite passes for empty, new, mature, and noisy histories.

The offline evaluation gate checks:

- 100% schema-valid responses for checked-in fixtures;
- 100% cited-key membership in the bounded context;
- exact priority agreement with each fixture's expected recommendation;
- local validation under 50 ms per fixture;
- correct cancellation and timeout classification.

Production calls have a 90-second handler timeout. Failures, malformed output,
expired/revoked accounts, and unavailable App Server processes leave the
manual task form usable. Usage-limit responses return `429` with a retry hint;
other unavailable outcomes return a retryable `503`.

## Privacy-safe operations

Each attempted model turn writes one bounded structured log containing only
`outcome` and `duration_ms` (capped at 300,000). Allowed outcomes include
`succeeded`, `invalid_output`, `incomplete`, `limit_reached`, `timed_out`,
`canceled`, and `unavailable`. Logs exclude actor and project identifiers,
account metadata, prompts, task text, model output, and credentials.

For an immediate kill switch, set:

```sh
HELM_LUNA_ENABLED=false
```

and restart Helm. Account connection and logout remain available, but draft
requests fail closed with `luna_disabled`; ordinary task creation is unchanged.
