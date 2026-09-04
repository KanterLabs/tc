# Authentication

`scripts/helm.py` resolves configuration in this order:

1. Environment variables (`HELM_*` canonical; legacy aliases are accepted only when they do not conflict).
2. A JSON file selected by `HELM_CONFIG` or the compatibility alias `TC_ROADMAP_CONFIG`.
3. `~/.config/helm/credentials.json`, falling back read-only to `~/.config/tc-roadmap/credentials.json`.

Supported environment variables are:

- `HELM_URL`
- `HELM_TOKEN`
- `HELM_CF_ACCESS_CLIENT_ID`
- `HELM_CF_ACCESS_CLIENT_SECRET`
- `TC_ROADMAP_URL` (compatibility alias)
- `TC_ROADMAP_TOKEN` (compatibility alias)
- `ROADMAP_TOKEN` (compatibility alias)
- `TC_CF_ACCESS_CLIENT_ID` (compatibility alias)
- `TC_CF_ACCESS_CLIENT_SECRET` (compatibility alias)

The JSON file may contain:

```json
{
  "base_url": "https://tc.example.com",
  "token": "agent-token",
  "cf_access_client_id": "optional-service-token-id",
  "cf_access_client_secret": "optional-service-token-secret"
}
```

The credential file must be owned by the current user and have no group or world permissions; mode `0600` is recommended. The helper refuses a broader mode. Cloudflare fields are optional for deployments whose API is not behind Cloudflare Access.

`HELM_TOKEN` is the Helm application credential issued to an agent actor. It
authorizes API scopes such as `tasks:read`, `tasks:write`, `tasks:claim`, and
`events:read`. `HELM_CF_ACCESS_CLIENT_ID` and
`HELM_CF_ACCESS_CLIENT_SECRET` are separate Cloudflare Access edge credentials
used only to cross the protected tunnel. Supplying Cloudflare credentials
cannot replace a missing Helm token, and the two kinds of credential must not
be copied into one another's fields.

Create a dedicated agent identity and grant only the scopes needed for tracking: `projects:read`, `tasks:read`, `tasks:write`, `tasks:claim`, and `events:read`. Restrict the token to relevant projects when practical. Token plaintext is returned only once by Helm and must never be committed, pasted into Helm tasks, or printed in logs. The stable API origin remains `https://tc.shanekanterman.dev`, with requests under `/api/v1`.

To verify endpoint reachability and the Helm application identity without
performing a mutation, run `python3 scripts/helm.py auth-check`. The command
returns only bounded actor metadata and emits a sanitized machine-readable
error on failure; credential values never appear in its output.
