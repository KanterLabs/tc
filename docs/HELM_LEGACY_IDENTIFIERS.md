# Helm compatibility identifiers

Helm is the canonical product, repository, module, binary, image, service,
configuration, documentation, artwork, and agent-skill name. The identifiers
below intentionally retain `roadmap` or `TC` because they are public contracts,
persistent data identities, rollback boundaries, or historical records. They
must not be renamed by a cosmetic cleanup or an automated text replacement.

## API and browser contracts

- The Roadmap overview remains a product feature, including
  `/api/v1/roadmap`, `/api/v1/projects/{project}/roadmap`, Roadmap response
  types, routes, component names, and user-facing navigation labels.
- `X-Roadmap-Revision` remains the release compatibility header.
- `roadmap_session` remains the session-cookie name so rebranding does not
  sign users out.
- Existing `roadmap.*` browser-storage keys are read-only migration sources.
  Helm copies their values to `helm.*` keys and never deletes the originals.

## Persistent data and rollback

- `/var/lib/roadmap`, `/var/lib/roadmap/data/roadmap.db`,
  `/var/lib/roadmap/releases`, `/var/lib/roadmap/backups`, `/etc/roadmap`,
  `roadmap.env`, `roadmap-*.db` backup names, and migration metadata remain
  stable.
- The production Unix user/group remains `roadmap`.
- The Compose volume key remains `roadmap-data` and its external identity
  remains `roadmap_roadmap-data`.
- Retained releases may contain either `roadmap`/`roadmap.sha256` or
  `helm`/`helm.sha256`. `helm.service` runs both through a fixed-path launcher.
- `roadmap.service`, `roadmap-backup.service`, `roadmap-backup.timer`, and the
  `/usr/local/sbin/roadmap-{backup,restore,rollback}` commands are compatibility
  aliases to their canonical Helm counterparts.

## Signed gateway envelope

The separately managed Proxmox gateway verifies the original signed envelope.
Until that trust boundary is independently migrated, every Helm release keeps
the exact `roadmap-release-manifest-v1` header and exact member names:

```text
cloudflared
cloudflared.service
cloudflared.token
compose.yaml
install-inside-lxc.sh
nftables.conf
roadmap
roadmap-backup.service
roadmap-backup.sh
roadmap-backup.timer
roadmap.env
roadmap-restore.sh
roadmap-rollback.sh
roadmap.service
roadmap.sha256
release.manifest
release.manifest.sig
release.sha
```

The `roadmap` member contains the Helm binary. The guest installer converts
the legacy envelope into canonical Helm runtime names. Adding, removing, or
renaming an envelope member would make the existing root gateway reject the
release before touching the guest.

## Infrastructure and hosted configuration

- `tc.shanekanterman.dev` remains the production hostname.
- The Proxmox guest name/tag `roadmap`, CTID 103, its reviewed address, the
  `roadmap-homelab` tunnel, the `roadmap-deploy` account, and
  `/var/lib/roadmap-deploy` plus `/etc/roadmap-deploy` remain fixed allowlist
  identities.
- Existing GitHub Actions secrets retain their `ROADMAP_*` names because
  secret values cannot be copied or renamed through repository code. The
  workflow maps them into canonical `HELM_*` runtime variables.
- `ROADMAP_*` application and deployment variables remain equal-value aliases
  for retained binaries and operator configuration. Conflicting non-empty Helm
  and Roadmap spellings fail closed.

## Agent and historical compatibility

- `skills/tc-roadmap`, `~/.config/tc-roadmap/credentials.json`, legacy hook
  state, and the `ROADMAP_TOKEN`/`TC_ROADMAP_*` variables remain migration
  inputs. New installations and writes use `skills/helm`, `~/.config/helm`,
  and `HELM_*`.
- The `TC` project key, existing task keys and IDs, historical activity,
  planning documents, old release evidence, and Git commit messages are not
  rewritten.

Any change to this list needs an explicit data, authorization, or gateway
migration plan with retained-binary tests and a verified pre-change backup.
