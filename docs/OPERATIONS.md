# Roadmap operations

Roadmap is an agent-first project board. The production path is intentionally
narrow:

```text
Cloudflare Access (owner UI or agent service token)
  -> tc.shanekanterman.dev /api/v1/*
  -> roadmap-homelab Tunnel
  -> cloudflared (LXC loopback)
  -> roadmap.service at 127.0.0.1:8080
  -> /var/lib/roadmap/data/roadmap.db
```

The target is the unprivileged Debian 12 LXC `roadmap` (CTID 103) on
`10.0.0.20`, address `10.0.0.38/24`, one CPU, 2048 MiB RAM, and a 16 GiB
`local-lvm` root disk. The guest has no inbound application or SSH port. Its
nftables input policy is default-drop; both the application and cloudflared
connector use loopback while cloudflared makes the outbound tunnel connection.

## Local development and image checks

The Compose file runs exactly one unprivileged Roadmap container. It publishes
only `127.0.0.1:8080`, mounts a persistent `/data` volume, drops all Linux
capabilities, uses a read-only root filesystem, and has a `/healthz`
healthcheck.

```sh
make build                 # npm ci, check, test, build, embed, then Go build
make test
make vet
make lint
docker compose up --build -d
curl http://127.0.0.1:8080/healthz
docker compose down
```

The local defaults use local authentication and demo seed data. Never use
`ROADMAP_AUTH_MODE=disabled` outside a disposable development environment.
Production uses the immutable `dist/roadmap` binary from a release bundle;
the Compose image is also built in CI as the container packaging check and is
available for self-hosting.

## One-time Proxmox deploy identity

Create a deploy-only key that is unrelated to the operator's login key, then
install its public half on the Proxmox host from a trusted shell:

```sh
ssh-keygen -t ed25519 -C roadmap-deploy -f ./roadmap-deploy-key
openssl genpkey -algorithm ED25519 -out ./roadmap-release-signing-private.pem
openssl pkey -in ./roadmap-release-signing-private.pem -pubout \
  -out ./roadmap-release-signing-public.pem
chmod 0600 ./roadmap-release-signing-private.pem
cp .tc-deploy.env.example .tc-deploy.env
./deploy/bootstrap-proxmox.sh ./roadmap-deploy-key.pub \
  ./roadmap-release-signing-public.pem
```

The installer creates `roadmap-deploy` with one forced SSH command that runs
only `sudo -n /usr/local/sbin/roadmap-deploy-gateway`, with no agent, X11,
port, PTY, or user-rc forwarding. Its only sudo rule permits that exact
root-owned command (and preserves only the SSH original-command variables).
The gateway accepts only a deployment, rollback, or status request with a
validated SHA and only operates on CTID 103. Do not add this account to
`docker`, `sudo`, or another supplementary group. Keep the private key in the GitHub secret
`ROADMAP_DEPLOY_SSH_KEY`, never in the repository. The separate Ed25519
release-signing private key must never be installed on PVE or included in a
bundle; store its PEM contents in the production GitHub environment secret
`ROADMAP_RELEASE_SIGNING_KEY`. The public key installed by bootstrap is the
only release key trusted by the root gateway. To rotate it, generate a new
keypair, install the new public key with bootstrap during a maintenance window,
then replace the GitHub secret and test one signed release.

Before the first run, inspect the existing guest and template on the PVE host:

```sh
pct config 103
pveam list local
```

The gateway checks the QEMU VMID namespace before every status, deploy, or
rollback request. A QEMU guest at VMID 103 is a hard collision: the request
fails closed instead of treating the missing LXC config as `current_sha=none`.

The gateway refuses to reuse a CTID with a different hostname, address, or
privilege mode. Deploy CI sends the validated release bundle only on standard
input for the `deploy <sha>` request; the root gateway captures it in a
root-owned mode-0600 staging file, reads at most 512 MiB + 1 byte to reject
oversize input, then validates archive paths, links, modes, and release SHA
before extraction. Every archive member is a regular file from the exact
allowlist and the detached Ed25519 signature covers a canonical manifest of
each payload member's exact name, byte size, and SHA-256. The deployment
account has no usable scp, SFTP, or shell subsystem. An incomplete deploy
stream is bounded by the gateway's ingest timeout as well as its 512 MiB cap.

## Cloudflare Access and tunnel bootstrap

The Cloudflare API token used by CI needs only the permissions required to
manage the account's Access applications/policies, service tokens, tunnel,
and the `tc.shanekanterman.dev` DNS record. Set it as the GitHub secret
`ROADMAP_CLOUDFLARE_API_TOKEN`.

The first `prepare` call, before DNS publication, creates or verifies all of
these resources:

* a Cloudflare Access identity provider named `Cloudflare` with
  `type: "cloudflare"` and `config.restrict_to_account_members: true`; an
  existing provider is reused only when exactly one provider of that type has
  that restriction, otherwise the bootstrap fails closed;
* a self-hosted owner UI application for exactly
  `tc.shanekanterman.dev`;
* a separate self-hosted API application for exactly
  `tc.shanekanterman.dev/api/v1/*` (path applications do not inherit the UI
  policy);
* an exact-owner Allow policy on each application using the restricted
  Cloudflare identity provider;
* a `Roadmap agents` service token (with a future expiry no longer than one
  year) and a
  `Roadmap agents Service Auth` (`non_identity`) policy on the API application;
* the remote-managed `roadmap-homelab` tunnel, with both application audience
  tags in `originRequest.access.audTag` and `required: true`.

Run it with an output path outside source control:

```sh
export CLOUDFLARE_API_TOKEN='provided by the approved secret manager'
./deploy/cloudflare.sh prepare \
  dist/cloudflared.token \
  dist/owner.env \
  dist/roadmap-access-token.env
```

The first bootstrap must be run from a trusted, persistent operator shell
before enabling the GitHub workflow. The service-token secret is returned only
once; a mode-0600 file on an ephemeral CI runner is not a durable capture.
Copy `dist/roadmap-access-token.env` into the approved secret manager (or an
equivalent encrypted operator vault) immediately after this first run, and
verify that the working-tree path is ignored. Later CI runs find the existing
`Roadmap agents` token and reconcile its policy without needing to read its
one-time secret again.

CI sets `ROADMAP_REQUIRE_DURABLE_SERVICE_TOKEN_CAPTURE=1`, so it refuses to
create this token when the one-time secret would be written only to the runner.
Manual `prepare` leaves this guard disabled and remains the supported way to
perform the first bootstrap.

`prepare` renders the owner environment with literal, validated substitutions
(including the `https://` issuer), verifies every required key/value, and only
then atomically installs the mode-0600 outputs. A failed API request or
template render leaves any previously valid output files unchanged. If either
output rename fails during the final commit, the staged token and owner
environment are both restored byte-for-byte before `prepare` reports failure.

The tunnel token is copied into the LXC's mode-0640 root/cloudflared file and
is not retained on PVE after the gateway finishes. The service-token output is
different: Cloudflare returns its `CF-Access-Client-Secret` only once, so the
first bootstrap writes `CF_ACCESS_CLIENT_ID` and
`CF_ACCESS_CLIENT_SECRET` to the explicitly requested mode-0600 ignored file.
Subsequent runs find the existing named token and do not require its secret;
if the local output is present, its client ID is checked against Cloudflare.
Do not print, upload, or commit either secret.

The generated `dist/owner.env` pins Roadmap's Cloudflare mode to the Access
team issuer and both application audience tags (`ROADMAP_CF_ACCESS_AUDIENCES`):
the owner UI application and the `/api/v1/*` application. Roadmap validates
the RS256 `Cf-Access-Jwt-Assertion` against the team's `/cdn-cgi/access/certs`
endpoint and does not authorize from ordinary identity headers.

`prepare` never removes an existing one-time-PIN provider. Such providers may
remain in the account for other applications, but Roadmap's applications are
bound only to the restricted Cloudflare provider. A missing provider is
created with the exact API shape above; duplicate or nonconforming Cloudflare
providers stop the bootstrap instead of being guessed at or silently changed.

After a successful deploy, create the proxied CNAME and validate it:

```sh
./deploy/cloudflare.sh publish
./deploy/validate-live.sh
```

The live validator checks both Access applications and their policies, the
named tunnel status, the proxied CNAME, and Access responses for `/`,
`/api/v1/roadmap`, `/healthz`, and `/openapi.json`. Health and OpenAPI are not
bypassed; an unauthenticated request must receive an Access redirect (or the
configured service-auth 401 for the API path). In CI, the validator also sends
`CF_ACCESS_CLIENT_ID` and `CF_ACCESS_CLIENT_SECRET` to `/api/v1/roadmap`
without an application bearer token and requires the origin's JSON
`unauthorized` response with the echoed `X-Request-ID`; this distinguishes the
Roadmap service from an Access edge error. A local health check through the
LXC loopback is part of every install and rollback.

### Service-token rotation

Service-token secrets cannot be read back from the Cloudflare API. To rotate
one, create a new token named for the rotation, add it to the API application's
`non_identity` Service Auth policy, validate an API request with the new
`CF-Access-Client-Id` and `CF-Access-Client-Secret`, then remove the old token
from the policy and revoke it. Replace the mode-0600 output only after the new
token works. Never put service credentials in `roadmap.env`, Compose, the
container image, or an agent's task data.

## Release, backup, and rollback

The push-to-main workflow runs checks on `homelab`, the browser suite and
container build on `homelab-heavy`, and deployment orchestration on
`homelab`. Its production concurrency is non-canceling, so a second main push
waits for the first deployment. CI builds the frontend and Go binary, creates
an immutable SHA-tagged bundle, prepares Access/tunnel resources, deploys via
the constrained PVE identity, publishes DNS, and performs live validation.

Normal deployment requires these secrets:

* `ROADMAP_CLOUDFLARE_API_TOKEN`
* `ROADMAP_DEPLOY_SSH_KEY`
* `ROADMAP_DEPLOY_KNOWN_HOSTS`
* `ROADMAP_RELEASE_SIGNING_KEY`
* `ROADMAP_CF_ACCESS_CLIENT_ID`
* `ROADMAP_CF_ACCESS_CLIENT_SECRET`

The install transaction stops the connector and application, creates a
SQLite online-backup in `/var/lib/roadmap/backups`, verifies its checksum and
`PRAGMA integrity_check`, then atomically switches
`/var/lib/roadmap/current` to the new release. The database remains at
`/var/lib/roadmap/data/roadmap.db` and is never replaced by a release. The
state root, retained releases, and backups are root-owned; only the dedicated
data directory is writable by `roadmap`. Failed
health or connector checks automatically switch back and restart the previous
release. Five releases and fourteen backups are retained by default; a
randomized persistent systemd timer also runs the same verified backup helper
daily. Pruning never removes the active release.

To make an explicit backup (for example before an operator migration):

```sh
ssh roadmap-deploy@10.0.0.20 \
  'status'
# Run from a PVE console or approved pct wrapper:
pct exec 103 -- /usr/local/sbin/roadmap-backup manual
```

To roll back to a retained release from CI or an approved operator shell:

```sh
./deploy/deploy-ci.sh rollback <40-character-release-sha>
```

The workflow also exposes this as a `workflow_dispatch` `rollback_sha` input.
Rollback validates that the release is retained, uses the same atomic link
switch, and health-checks both Roadmap and cloudflared. An application-health
or connector start/active failure restores the release that was active before
the request, restarts and verifies both prior services, and returns failure so
the requested rollback is never reported as successful. If that recovery also
fails, leave the guest on the prior release link and inspect both units before
retrying.

For a manual database restore, use only the root-owned helper with one
explicit retained backup path:

```sh
pct exec 103 -- /usr/local/sbin/roadmap-restore \
  /var/lib/roadmap/backups/roadmap-20260827T120000Z-<sha-or-manual>.db
```

The helper validates the matching checksum/metadata sidecars and SQLite
integrity, stops both services, preserves the current database as a new
recoverable `pre-restore` backup, prunes older backups under the configured
retention while protecting both that recovery snapshot and the selected source,
and atomically installs the candidate. It
overlays the current authorization state (including actor password, admin,
disabled/deleted state, setup, and valid project grants), disables and clears
credentials for actors that exist only in the old backup, and clears all
sessions, application tokens, and idempotency records before restart. Rotate
the Cloudflare service token and any credentials that may have been exposed by
the incident; reissue agent credentials after validation. Never swap a raw
SQLite file or bypass this helper.

## Recovery checks

Useful read-only checks from the PVE host are:

```sh
pct status 103
pct exec 103 -- systemctl is-active roadmap.service cloudflared.service
pct exec 103 -- ss -ltn
pct exec 103 -- curl --fail http://127.0.0.1:8080/healthz
pct exec 103 -- nft list ruleset
```

The expected listener is only `127.0.0.1:8080`; guest SSH units are masked.
If the app is healthy locally but the public URL redirects incorrectly, check
the two Access application paths and both audience tags before changing the
tunnel route. If the tunnel is unhealthy, inspect
`journalctl -u cloudflared.service` in the guest and confirm that the token
file remains mode 0640 and owned by `root:cloudflared`.
