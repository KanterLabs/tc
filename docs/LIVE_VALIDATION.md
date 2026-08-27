# Live deployment validation

Validated on 2026-08-27 UTC for `https://tc.shanekanterman.dev`.

## Runtime

- Proxmox LXC 103 (`roadmap`), unprivileged Debian 12, at `10.0.0.38`.
- Roadmap listens only on `127.0.0.1:8080`; Cloudflare Tunnel is the public ingress.
- nftables uses default-drop input and forward policies. SSH and the unused
  Postfix aggregate, template, and default-instance units are masked.
- `roadmap.service`, `cloudflared.service`, `nftables.service`, and
  `roadmap-backup.timer` are enabled and healthy.

## Cloudflare Access

- Unauthenticated `/`, `/healthz`, `/openapi.json`, and `/api/v1/roadmap`
  requests were all intercepted by Access with HTTP 302 responses.
- A valid Access service token reached the origin API and received Roadmap's
  JSON HTTP 401 response without an application bearer token. The response
  preserved `X-Request-ID` and reported the active release revision.
- TLS certificate verification succeeded and the public endpoint negotiated
  HTTP/2.
- CI also verified the exact Access applications and policies, tunnel ingress,
  service-token policy, proxied DNS record, and tunnel health through the
  Cloudflare API.

## Update and persistence

The first live revision was `6f70227801013fc955251fdd6a89c2d7284ff988`.
A push to `main` automatically deployed
`f305d293e1bb4347538b452eb5ec96785b87eb3f` in GitHub Actions run
`33056133832`; checks, browser tests, race detection, immutable container build,
deployment, DNS publication, and live Access validation all passed.

Before and after that deployment, SQLite retained the same inode (`656662`),
database SHA-256
`8fa9864905b21b4d88796497852648faef511523a7c5f7dc6ed731fff4b3255b`,
schema SHA-256
`78001af84c22f2a74b9a923f6a97c8b15ceebc502240047f8ebb1c1b0db51c78`,
and logical-dump SHA-256
`29c7f15b6eeeccc3fdb17d73c757d5ad6e0530d23313fad963ce10ae89a4e94e`.
`PRAGMA integrity_check` returned `ok`. The deployment also published a
checksum-valid, integrity-valid pre-install backup.

## Rollback

Workflow-dispatch run `33056662604` rolled the retained release back from
`f305d293e1bb4347538b452eb5ec96785b87eb3f` to
`6f70227801013fc955251fdd6a89c2d7284ff988` and passed post-rollback live
Access validation. The database inode, all three hashes, integrity result,
backup count, and service health remained unchanged.
