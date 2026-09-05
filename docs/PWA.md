# Helm iOS PWA

Helm ships an installable, read-only offline shell for iOS Home Screen web
apps. The shell is a delivery aid for the offline viewer; it is not an API
cache and it never makes mutations while the device is offline.

## Build design

`web/vite.config.ts` emits a root-scoped `sw.js` during production builds. The
worker names its cache with the build version and a digest of the generated
bundle. It precaches only the same-origin app shell (`/` and `/index.html`),
generated static bundle assets, the manifest, and the checked-in icon assets.
Each precache response must be a successful, same-origin, non-redirect,
non-opaque response. HTML must contain Helm's `#app` mount and module script;
login/sign-in HTML is rejected. A failed required precache fails that worker's
install, leaving the currently active worker in place.

Navigation is network-first. A response with `401`, `403`, or a `5xx` status is
returned unchanged; only a genuinely rejected network request can fall back to
the validated shell. This also makes offline deep links such as `/p/acme`
open the known shell so the read-only viewer can restore its local snapshot.

Known static assets use cache-first delivery. Old Helm caches are retained for
open clients' old hashed assets and pruned to three app caches. API, auth,
login/logout, `/cdn-cgi/`, arbitrary same-origin paths, cross-origin requests,
and credentials are never cached by the worker. Static requests may use
same-origin credentials to pass an access gateway, but the cache key is a
credential-free URL and only a validated static response is stored.

Updates remain in `registration.waiting`; the worker does not call
`skipWaiting()` during install. `PwaStatus` asks for explicit user consent,
invokes the host `beforeUpdate` guard, sends the waiting worker its message,
waits for `controllerchange`, and then reloads. A native `beforeunload` handler
can still protect unsaved drafts during that reload.

## Files and integration API

The production output contains:

- `/sw.js` — generated service worker.
- `/manifest.webmanifest` — `id`, `scope`, and `start_url` are `/`; display is
  `standalone`.
- `/icons/icon-180.png` — iOS `apple-touch-icon`.
- `/icons/icon-192.png`, `/icons/icon-512.png` — manifest icons.
- `/icons/icon-512-maskable.png` — manifest maskable icon.

`web/scripts/generate-pwa-icons.mjs` deterministically rasterises the existing
`web/public/helm-mark.svg` with Node built-ins; it adds no dependency. Rerun it
when the source mark changes.

Call the registration helper once from `web/src/main.ts`:

```ts
import { registerPwa } from './lib/pwa';

void registerPwa();
```

Mount the status surface from `App.svelte` and pass the host's dirty-draft
guard:

```svelte
<PwaStatus beforeUpdate={() => window.confirm('Reload to update Helm? Unsaved changes will be lost.')} />
```

`beforeUpdate` may return a boolean or a promise. The module also exports
`getPwaState()`, `subscribePwa()`, and `applyPwaUpdate()` for a host shell that
needs its own presentation. State includes `appReady`, `cacheReady`,
`cacheError`, `updateAvailable`, `online`, `controlled`, and `updateApplying`.

`PwaStatus` shows Safari's install path (Share → Add to Home Screen), a
consent-gated update notice, and compact offline/cache error status. Its
`showInstallInstructions` and `showCacheStatus` props allow a host layout to
hide either notice; cache errors remain visible so a failed shell is not
silently mistaken for offline support.

## Storage and data expectations

Previously loaded board pages are saved automatically in a separate IndexedDB
store, not in the service worker cache. Snapshots contain only project/column
names and task keys, titles, descriptions and priorities: no tokens, account
settings, comments, attachments or API response envelopes. The store retains
at most five boards, 100 columns and 500 tasks per board, with bounded text.
Snapshots older than seven days are not shown. Filtered, truncated and paged
boards are explicitly labeled as partial; offline search only searches saved
tasks.

The offline viewer has no editing or dragging controls. The API client also
rejects every mutation while offline or awaiting session revalidation. Failed
writes are never queued or automatically retried. Reconnect checks the current
session before enabling edits. A cached navigation marks the HTML shell as
offline, so this also works when `navigator.onLine` is inaccurate.

Sign-out, observed 401/403 responses and account changes clear saved snapshots.
A fresh project list also invalidates the saved set if a project is no longer
accessible. Cross-tab invalidation and a local clear marker guard against
stale saves and storage failures. The offline viewer offers **Clear saved
boards**. Offline access cannot revalidate a server-side permission change:
anyone with access to the browser profile/device can read its saved snapshots
until they are cleared or expire from the viewer. Use this only on a trusted
device; browser storage is not an encrypted vault or backup.

The service worker never stores API responses or credentials. iOS/WebKit storage is
best-effort: even a standalone app can have Cache API/IndexedDB data evicted
under storage pressure or inactivity. Helm requests persistence where the
platform exposes it, but this is only a hint and is not a durability promise.

Validate installation and offline deep links on a physical iPhone/iPad. The
browser preview and desktop emulation do not prove the Home Screen manifest,
safe-area behavior, storage policy, or access-gateway behavior.

For device acceptance, install from Safari's Share menu, launch from the Home
Screen and sign in there, open a board, then enable Airplane Mode and fully
relaunch. Confirm the saved timestamp/read-only view, reconnect, and check that
sign-out removes saved boards. Check keyboard and safe-area layout, then
confirm that a later app update waits for an explicit reload decision.

Automated coverage is in `web/e2e/pwa-offline.spec.ts`, `offlineBoards.test.ts`,
`offlineApi.test.ts`, and `pwa.test.ts`. The browser tests use real service
workers and network failures rather than mocked cached API responses. No
server database migration or production deployment is part of this change.

Background: [Web Push for Web Apps on iOS and iPadOS](https://webkit.org/blog/13878/web-push-for-web-apps-on-ios-and-ipados/)
describes `standalone`, Home Screen icons, and manifest `id`; [Updates to
Storage Policy](https://webkit.org/blog/14403/updates-to-storage-policy/) notes
that Cache API and IndexedDB data remain subject to quota and eviction.
