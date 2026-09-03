# Beta branch and deployment plan

## Goal

Give Helm a permanent `beta` branch whose successful pushes deploy to an
isolated test installation. Production remains reachable only from `main`, so
promoting a tested beta revision requires an explicit pull request or merge to
`main`.

## Fixed environment identities

| Boundary | Production | Beta |
| --- | --- | --- |
| Git branch | `main` | `beta` |
| GitHub environment | `production` | `beta` |
| Deploy lock | `helm-production` | `helm-beta` |
| Public origin | `https://tc.shanekanterman.dev` | `https://beta.shanekanterman.dev` |
| Proxmox guest | CT 103, `roadmap`, `10.0.0.38` | CT 106, `helm-beta`, `10.0.0.39` |
| Deploy account | `roadmap-deploy` | `helm-beta-deploy` |
| Host state | `/var/lib/roadmap-deploy` | `/var/lib/helm-beta-deploy` |
| Signing trust | `/etc/roadmap-deploy` | `/etc/helm-beta-deploy` |
| Cloudflare tunnel | `roadmap-homelab` | `helm-beta-homelab` |
| Access resources | `Helm owner UI`, `Helm agents API` | `Helm beta owner UI`, `Helm beta agents API` |

The beta guest intentionally retains the in-guest compatibility paths and
service names (`/var/lib/roadmap`, `/etc/roadmap`, `helm.service`, and
`cloudflared.service`). They are isolated by the LXC boundary. Beta must start
with a fresh database; production databases, releases, backups, tunnel tokens,
deploy keys, signing keys, and Access credentials are never copied into it.

## Dependency order

1. Add fail-closed production and beta deployment profiles to the Proxmox
   gateway, bootstrap helper, Cloudflare reconciler, and live validator.
2. Add branch-aware CI routing with distinct GitHub environments, secrets,
   concurrency locks, origins, and rollback jobs.
3. Extend deployment security tests to prove a beta action cannot select any
   production identity and a production action cannot select beta.
4. Provision the beta deploy/signing identities, CT, tunnel, Access apps, and
   DNS without changing production resources.
5. Push `beta`, require the full checks and live smoke test, then protect
   `main` so production changes arrive through an explicit reviewed merge.

## Promotion and rollback

- A push to `beta` builds and tests that exact commit and may deploy only to
  the beta GitHub environment and beta gateway.
- A pull request from `beta` to `main` runs the normal checks. Merging it makes
  a new immutable `main` commit, which is the only automatic production
  deployment trigger.
- A workflow dispatch from `beta` with `rollback_sha` selects only a retained
  beta release. The same dispatch from `main` selects only production.
- Failed live validation automatically rolls the selected environment back to
  its previous retained release. Binary rollback never restores a database.

## Verification gates

- Shell syntax, actionlint, deployment-security tests, frontend/OpenAPI checks,
  Go tests, race tests, browser tests, and container smoke checks pass.
- The beta gateway reports CT 106 and the production gateway reports CT 103.
- Beta and production use different forced SSH users and Ed25519 signing keys.
- Cloudflare reconciliation finds exactly one environment-specific tunnel,
  DNS record, UI app, API app, owner policy, and service-auth policy.
- The beta live validator proves the revision and Access boundary at the beta
  hostname; the production revision, database, releases, and services remain
  unchanged during beta deploy and rollback tests.
