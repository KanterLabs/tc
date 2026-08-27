# Authentication

`scripts/tc_roadmap.py` resolves configuration in this order:

1. Environment variables.
2. A JSON file selected by `TC_ROADMAP_CONFIG`.
3. `~/.config/tc-roadmap/credentials.json`.

Supported environment variables are:

- `TC_ROADMAP_URL`
- `TC_ROADMAP_TOKEN`
- `ROADMAP_TOKEN` (compatibility alias)
- `TC_CF_ACCESS_CLIENT_ID`
- `TC_CF_ACCESS_CLIENT_SECRET`

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

Create a dedicated agent identity and grant only the scopes needed for tracking: `projects:read`, `tasks:read`, `tasks:write`, `tasks:claim`, and `events:read`. Restrict the token to relevant projects when practical. Token plaintext is returned only once by TC and must never be committed, pasted into Roadmap tasks, or printed in logs.
