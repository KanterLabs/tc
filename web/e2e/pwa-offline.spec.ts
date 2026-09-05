import { request as httpRequest, createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http';
import { request as httpsRequest } from 'node:https';
import type { Socket } from 'node:net';
import { expect, test as base, type APIRequestContext, type APIResponse, type Locator, type Page } from '@playwright/test';

type Project = { id: string; key: string; name: string; slug: string };
type Column = { id: string; name: string; semantic_state: string; position: number };
type Task = { id: string; key: string; title: string; column_id: string; position: number; version: number };
type Collection<T> = { data: T[]; next_cursor?: string | null };
type BoardFixture = { project: Project; column: Column; task: Task };
type ManifestIcon = { src: string; sizes?: string; type?: string; purpose?: string };
type NetworkProxy = {
  origin: string;
  setOffline: (offline: boolean) => void;
  setAuthRejected: (rejected: boolean) => void;
  close: () => Promise<void>;
};

const e2eOrigin = new URL(
  process.env.HELM_E2E_BASE_URL || process.env.ROADMAP_E2E_BASE_URL || 'http://127.0.0.1:18080'
).origin;

/**
 * Keep the browser on a same-origin loopback origin while making a real
 * network outage independently controllable from Playwright's context-level
 * offline emulation. WebKit can throw before its service worker gets a chance
 * to answer a `goto()` while `context.setOffline(true)` is active; destroying
 * the proxy request lets the worker's network-first navigation fallback run in
 * all engines.
 */
async function startNetworkProxy(): Promise<NetworkProxy> {
  const targetOrigin = new URL(e2eOrigin);
  let offline = false;
  let authRejected = false;
  let closed = false;
  const sockets = new Set<Socket>();

  const server: Server = createServer((incoming: IncomingMessage, outgoing: ServerResponse) => {
    if (offline || closed) {
      incoming.destroy();
      outgoing.destroy();
      return;
    }
    if (authRejected && incoming.url?.split('?')[0] === '/api/v1/auth/status') {
      outgoing.writeHead(403, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' });
      outgoing.end(JSON.stringify({ error: { code: 'auth_invalidated', message: 'Session expired' } }));
      return;
    }

    const target = new URL(incoming.url || '/', targetOrigin);
    const headers = { ...incoming.headers };
    // The backend receives the request through this proxy, but its Host header
    // still needs to identify the configured E2E origin.
    headers.host = target.host;
    const options = {
      protocol: target.protocol,
      hostname: target.hostname,
      port: target.port || undefined,
      method: incoming.method || 'GET',
      path: `${target.pathname}${target.search}`,
      headers
    };
    const forwardResponse = (response: IncomingMessage) => {
      outgoing.writeHead(response.statusCode || 502, response.headers);
      response.pipe(outgoing);
    };
    const upstream = target.protocol === 'https:'
      ? httpsRequest(options, forwardResponse)
      : httpRequest(options, forwardResponse);

    upstream.on('error', (error) => outgoing.destroy(error));
    incoming.on('error', (error) => upstream.destroy(error));
    outgoing.on('close', () => upstream.destroy());
    incoming.pipe(upstream);
  });

  server.on('connection', (socket) => {
    sockets.add(socket);
    socket.once('close', () => sockets.delete(socket));
  });

  await new Promise<void>((resolve, reject) => {
    const onError = (error: Error) => {
      server.off('listening', onListening);
      reject(error);
    };
    const onListening = () => {
      server.off('error', onError);
      resolve();
    };
    server.once('error', onError);
    server.once('listening', onListening);
    server.listen(0, '127.0.0.1');
  });

  const address = server.address();
  if (!address || typeof address === 'string') {
    await new Promise<void>((resolve) => server.close(() => resolve()));
    throw new Error('the local E2E network proxy did not expose a TCP address');
  }

  return {
    origin: `http://127.0.0.1:${address.port}`,
    setAuthRejected(value: boolean) { authRejected = value; },
    setOffline(value: boolean) {
      offline = value;
      if (value) sockets.forEach((socket) => socket.destroy());
    },
    async close() {
      if (closed) return;
      closed = true;
      sockets.forEach((socket) => socket.destroy());
      await new Promise<void>((resolve) => server.close(() => resolve()));
    }
  };
}

const test = base.extend<{ networkProxy: NetworkProxy }>({
  networkProxy: async ({}, use) => {
    const proxy = await startNetworkProxy();
    try {
      await use(proxy);
    } finally {
      await proxy.close();
    }
  },
  baseURL: async ({ networkProxy }, use) => {
    await use(networkProxy.origin);
  }
});

function mutationHeaders(key: string): Record<string, string> {
  return {
    'Content-Type': 'application/json',
    Origin: e2eOrigin,
    'Idempotency-Key': key
  };
}

async function json<T>(response: APIResponse, description: string): Promise<T> {
  expect(response.ok(), `${description} returned HTTP ${response.status()}`).toBeTruthy();
  return await response.json() as T;
}

async function postJSON<T>(request: APIRequestContext, path: string, body: unknown, key: string): Promise<T> {
  return json<T>(await request.post(path, {
    data: body,
    headers: mutationHeaders(key)
  }), `POST ${path}`);
}

function collectionData<T>(payload: Collection<T> | T[]): T[] {
  return Array.isArray(payload) ? payload : payload.data;
}

async function createFixture(request: APIRequestContext): Promise<BoardFixture> {
  const runID = `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 7)}`.toUpperCase();
  const project = await postJSON<Project>(request, '/api/v1/projects', {
    key: `PWA${runID}`.slice(0, 16),
    name: `PWA offline ${runID}`,
    description: 'A small board used by the PWA offline browser checks.'
  }, `pwa-offline-${runID}-project`);

  let columns = collectionData(await json<Collection<Column> | Column[]>(
    await request.get(`/api/v1/projects/${project.id}/columns?limit=20`),
    'list PWA offline columns'
  ));
  if (!columns.length) {
    await postJSON<Column>(request, `/api/v1/projects/${project.id}/columns`, {
      name: 'Backlog',
      semantic_state: 'backlog',
      position: 0
    }, `pwa-offline-${runID}-column`);
    columns = collectionData(await json<Collection<Column> | Column[]>(
      await request.get(`/api/v1/projects/${project.id}/columns?limit=20`),
      'reload PWA offline columns'
    ));
  }
  const column = columns[0];
  expect(column, 'the PWA fixture needs one column').toBeTruthy();

  const task = await postJSON<Task>(request, `/api/v1/projects/${project.id}/tasks`, {
    title: `Cached PWA task ${runID}`,
    description: 'This title must remain available when the board is opened offline.',
    column_id: column.id,
    priority: 'normal',
    position: 0
  }, `pwa-offline-${runID}-task`);

  return { project, column: column as Column, task };
}

async function authMode(request: APIRequestContext): Promise<string | undefined> {
  const status = await json<{ mode?: string }>(await request.get('/api/v1/auth/status'), 'read auth status');
  return status.mode;
}

async function openOnlineBoard(page: Page, fixture: BoardFixture): Promise<void> {
  await page.goto(`/p/${fixture.project.slug}`, { waitUntil: 'domcontentloaded' });
  await expect(page.getByRole('heading', { name: fixture.project.name, exact: true })).toBeVisible();
  await expect(page.getByText(fixture.task.title, { exact: true })).toBeVisible();
}

async function awaitServiceWorkerControl(page: Page): Promise<void> {
  await page.evaluate(async () => {
    if (!('serviceWorker' in navigator)) throw new Error('Helm did not expose navigator.serviceWorker');
    await navigator.serviceWorker.ready;
  });

  // The first navigation registers the worker but may not yet be controlled;
  // one reload is the browser-standard transition into a controlled client.
  if (!await page.evaluate(() => Boolean(navigator.serviceWorker.controller))) {
    await page.reload({ waitUntil: 'domcontentloaded' });
  }
  await expect.poll(
    () => page.evaluate(() => Boolean(navigator.serviceWorker?.controller)),
    { timeout: 15_000, message: 'the board page should be controlled by the PWA service worker' }
  ).toBeTruthy();
}

async function offlinePage(page: Page) {
  const root = page.getByTestId('offline-board-view');
  await expect(root, 'offline mode should render its dedicated root').toBeVisible();
  await expect(root.getByText('Read-only', { exact: true })).toBeVisible();
  await expect(root.getByText('Offline', { exact: true })).toBeVisible();
  return root;
}

async function reconnectButton(root: Locator) {
  const button = root.getByRole('button', { name: /reconnect|retry|try again/i }).first();
  await expect(button, 'offline mode should offer a reconnect control').toBeVisible();
  return button;
}

/**
 * Read the production store without importing an application-only module.
 * The DB/store/key are part of the PWA contract. Reading only the current
 * record keeps the assertion tied to the persisted snapshot, not implementation
 * details of the store's normalization helpers.
 */
async function offlineStoreContains(page: Page, needle: string): Promise<boolean> {
  return await page.evaluate(async (value) => {
    if (typeof indexedDB.databases !== 'function') return false;
    const databases = await indexedDB.databases();
    if (!databases.some((database) => database.name === 'helm-offline-boards')) return false;

    const database = await new Promise<IDBDatabase>((resolve, reject) => {
      const request = indexedDB.open('helm-offline-boards');
      request.onerror = () => reject(request.error || new Error('could not open helm-offline-boards'));
      request.onsuccess = () => resolve(request.result);
    });
    try {
      if (!database.objectStoreNames.contains('snapshots')) return false;
      const record = await new Promise<unknown>((resolve) => {
        try {
          const request = database.transaction('snapshots', 'readonly').objectStore('snapshots').get('current');
          request.onerror = () => resolve(undefined);
          request.onsuccess = () => resolve(request.result);
        } catch {
          resolve(undefined);
        }
      });
      return JSON.stringify(record ?? '').includes(value);
    } finally {
      database.close();
    }
  }, needle);
}

async function decodedImageDimensions(page: Page, source: string): Promise<{ width: number; height: number }> {
  return await page.evaluate(async (url) => {
    const image = new Image();
    await new Promise<void>((resolve, reject) => {
      image.onload = () => resolve();
      image.onerror = () => reject(new Error(`could not decode ${url}`));
      image.src = url;
    });
    if (typeof image.decode === 'function') await image.decode();
    return { width: image.naturalWidth, height: image.naturalHeight };
  }, source);
}

async function cacheUrls(page: Page): Promise<string[]> {
  return await page.evaluate(async () => {
    const names = await caches.keys();
    const entries = await Promise.all(names.map(async (name) => {
      const cache = await caches.open(name);
      return (await cache.keys()).map((request) => request.url);
    }));
    return entries.flat();
  });
}

function trackApiMutations(page: Page): string[] {
  const mutations: string[] = [];
  page.on('request', (request) => {
    const url = new URL(request.url());
    if (!url.pathname.startsWith('/api/')) return;
    if (!['GET', 'HEAD', 'OPTIONS'].includes(request.method().toUpperCase())) {
      mutations.push(`${request.method()} ${url.pathname}`);
    }
  });
  return mutations;
}

async function goOfflineToCachedBoard(page: Page, networkProxy: NetworkProxy, fixture: BoardFixture): Promise<Locator> {
  networkProxy.setOffline(true);
  // Reload the already-open deep link. The proxy destroys this navigation's
  // network request, so the service worker must return the cached shell.
  await page.reload({ waitUntil: 'domcontentloaded' });
  const root = await offlinePage(page);
  await expect(root.getByText(fixture.task.title, { exact: true })).toBeVisible();
  return root;
}

test('serves a standalone install shell with static-only service-worker cache keys', async ({ page, request, networkProxy }) => {
  test.skip(await authMode(request) !== 'disabled', 'requires the built E2E server in disabled-auth mode');
  await page.goto('/', { waitUntil: 'domcontentloaded' });
  const manifestHref = await page.locator('link[rel="manifest"]').getAttribute('href');
  expect(manifestHref, 'the app shell should link a web manifest').toBeTruthy();

  const manifestResponse = await request.get(new URL(manifestHref as string, networkProxy.origin).toString());
  expect(manifestResponse.headers()['content-type']).toContain('application/manifest+json');
  const manifest = await json<{
    name?: string;
    display?: string;
    start_url?: string;
    scope?: string;
    icons?: ManifestIcon[];
  }>(manifestResponse, 'read PWA manifest');
  expect(manifest.name).toMatch(/helm/i);
  expect(manifest.display).toBe('standalone');
  expect(typeof manifest.start_url).toBe('string');
  expect(typeof manifest.scope).toBe('string');

  const start = new URL(manifest.start_url as string, networkProxy.origin);
  const scope = new URL(manifest.scope as string, networkProxy.origin);
  expect(start.origin).toBe(scope.origin);
  expect(start.pathname.startsWith(scope.pathname)).toBeTruthy();

  const icons = manifest.icons || [];
  const declaredSizes = new Set(icons.flatMap((icon) => (icon.sizes || '').split(/\s+/).filter(Boolean)));
  for (const size of ['180x180', '192x192', '512x512']) {
    expect(declaredSizes, `manifest should declare a ${size} icon`).toContain(size);
    const icon = icons.find((candidate) => (candidate.sizes || '').split(/\s+/).includes(size));
    expect(icon?.src, `${size} icon should have a source URL`).toBeTruthy();
    const iconURL = new URL(icon?.src as string, networkProxy.origin).toString();
    const iconResponse = await request.get(iconURL);
    expect(iconResponse.ok(), `${size} icon should be fetchable`).toBeTruthy();
    expect((await iconResponse.body()).byteLength, `${size} icon should contain decoded bytes`).toBeGreaterThan(0);
    const dimensions = await decodedImageDimensions(page, iconURL);
    const [width, height] = size.split('x').map(Number);
    expect(dimensions, `${size} icon should decode at its declared dimensions`).toEqual({ width, height });
  }

  const fixture = await createFixture(request);
  await openOnlineBoard(page, fixture);
  await awaitServiceWorkerControl(page);
  const urls = await cacheUrls(page);
  expect(urls.length, 'the service worker should cache the application shell').toBeGreaterThan(0);
  const forbidden = urls.filter((url) => /\/(?:api|auth)(?:\/|$)|\/cdn-cgi(?:\/|$)/i.test(new URL(url).pathname));
  expect(forbidden, 'service-worker cache keys must never include API, auth, or Cloudflare control paths').toEqual([]);
});

test('renders a saved board read-only offline, blocks writes, and re-authenticates on reconnect', async ({ page, request, context, networkProxy }) => {
  test.skip(await authMode(request) !== 'disabled', 'requires the built E2E server in disabled-auth mode');
  const fixture = await createFixture(request);
  const mutations = trackApiMutations(page);
  let authStatusReads = 0;
  page.on('request', (requestEvent) => {
    const path = new URL(requestEvent.url()).pathname;
    if (path === '/api/v1/auth/status') authStatusReads += 1;
  });

  await openOnlineBoard(page, fixture);
  await awaitServiceWorkerControl(page);
  await expect.poll(() => offlineStoreContains(page, fixture.task.title), {
    timeout: 15_000,
    message: 'authenticated board load should persist a snapshot in helm-offline-boards/snapshots/current'
  }).toBeTruthy();
  mutations.length = 0;

  await page.setViewportSize({ width: 390, height: 844 });
  // Without navigation, native online/offline events also revalidate auth.
  await context.setOffline(true);
  await expect(page.getByTestId('offline-board-view')).toBeVisible();
  await context.setOffline(false);
  await expect(page.locator('.app-shell')).toBeVisible();
  await expect(page.getByText(fixture.task.title, { exact: true })).toBeVisible();
  const root = await goOfflineToCachedBoard(page, networkProxy, fixture);
  await expect(root.getByRole('button', { name: /add task/i })).toHaveCount(0);
  await expect(root.locator('[draggable="true"]')).toHaveCount(0);
  await expect(root.locator('textarea, [contenteditable="true"], input:not([type="search"])')).toHaveCount(0);
  await expect(root.getByTestId('offline-search')).toBeVisible();

  // Exercise focus/keyboard handling and every available non-network control;
  // the read-only surface must not leak a mutation request while disconnected.
  await page.keyboard.press('Tab');
  await page.keyboard.press('ArrowDown');
  await page.keyboard.press('Escape');
  await (await reconnectButton(root)).click();
  await expect(root).toBeVisible();
  await expect.poll(() => mutations.length, { timeout: 1_000 }).toBe(0);

  networkProxy.setOffline(false);
  // Chromium's offline emulation can lose navigator.onLine across a cached
  // navigation. Exercise the user reconnect action as well as the event path.
  if (await root.isVisible()) {
    await expect(root.getByTestId('offline-reconnect')).toBeEnabled();
    await root.getByTestId('offline-reconnect').click();
  }
  await expect(page.locator('.app-shell')).toBeVisible();
  await expect(page.getByRole('heading', { name: fixture.project.name, exact: true })).toBeVisible();
  await expect(page.getByText(fixture.task.title, { exact: true })).toBeVisible();
  expect(authStatusReads, 'reconnect should perform a fresh auth/status read').toBeGreaterThanOrEqual(2);
  expect(mutations, 'offline interactions must not issue API writes').toEqual([]);
});

test('repairs an evicted shell cache while retaining the service worker registration', async ({ page, request, networkProxy }) => {
  test.skip(await authMode(request) !== 'disabled', 'requires the built E2E server in disabled-auth mode');
  const fixture = await createFixture(request);
  await openOnlineBoard(page, fixture);
  await awaitServiceWorkerControl(page);
  const originalUrls = await cacheUrls(page);
  await page.evaluate(async () => {
    for (const name of await caches.keys()) if (name.startsWith('helm-static-v')) await caches.delete(name);
  });
  await page.reload({ waitUntil: 'domcontentloaded' });
  await expect.poll(async () => (await cacheUrls(page)).length).toBe(originalUrls.length);
  await expect.poll(() => offlineStoreContains(page, fixture.task.title)).toBeTruthy();
  await goOfflineToCachedBoard(page, networkProxy, fixture);
});

test('clears offline snapshots from the UI and after sign-out', async ({ page, request, networkProxy }) => {
  test.skip(await authMode(request) !== 'disabled', 'requires the built E2E server in disabled-auth mode');
  const fixture = await createFixture(request);
  await openOnlineBoard(page, fixture);
  await awaitServiceWorkerControl(page);
  await expect.poll(() => offlineStoreContains(page, fixture.task.title), { timeout: 15_000 }).toBeTruthy();

  const root = await goOfflineToCachedBoard(page, networkProxy, fixture);
  const clear = root.getByTestId('offline-clear');
  await expect(clear, 'offline mode should expose a clear saved boards control').toBeVisible();
  page.once('dialog', (dialog) => dialog.accept());
  await clear.click();
  await expect.poll(() => offlineStoreContains(page, fixture.task.title), { timeout: 10_000 }).toBeFalsy();
  await expect(root.getByText(fixture.task.title, { exact: true })).toHaveCount(0);
  await expect(root.getByTestId('offline-empty')).toBeVisible();

  // Start a second saved session in the same context to verify the logout path
  // clears storage too. Going online first is required for the real auth/logout
  // request; no route mocks disable or bypass it.
  networkProxy.setOffline(false);
  await page.goto(`/p/${fixture.project.slug}`, { waitUntil: 'domcontentloaded' });
  await expect(page.getByText(fixture.task.title, { exact: true })).toBeVisible();
  await expect.poll(() => offlineStoreContains(page, fixture.task.title), { timeout: 15_000 }).toBeTruthy();
  await page.getByRole('button', { name: 'Sign out', exact: true }).click();
  await expect(page.locator('.auth-page')).toBeVisible();
  await expect.poll(() => offlineStoreContains(page, fixture.task.title), { timeout: 10_000 }).toBeFalsy();

  // Keep this a genuine outage navigation as well. Push the deep-link URL
  // without loading it while online, then let the service worker serve the
  // cached shell after the proxy starts destroying requests.
  await page.evaluate((path) => window.history.replaceState({}, '', path), `/p/${fixture.project.slug}`);
  networkProxy.setOffline(true);
  await page.reload({ waitUntil: 'domcontentloaded' });
  const emptyRoot = await offlinePage(page);
  await expect(emptyRoot.getByText(fixture.task.title, { exact: true })).toHaveCount(0);
  await expect(emptyRoot.getByTestId('offline-empty')).toBeVisible();
});

test('drops a cached snapshot when a fresh auth request is rejected', async ({ page, request, networkProxy }) => {
  test.skip(await authMode(request) !== 'disabled', 'requires the built E2E server in disabled-auth mode');
  const fixture = await createFixture(request);
  await openOnlineBoard(page, fixture);
  await awaitServiceWorkerControl(page);
  await expect.poll(() => offlineStoreContains(page, fixture.task.title), { timeout: 15_000 }).toBeTruthy();

  const root = await goOfflineToCachedBoard(page, networkProxy, fixture);
  // Reject at the network boundary: WebKit request routing can miss requests
  // from service-worker-controlled pages, even for uncached API routes.
  networkProxy.setAuthRejected(true);
  networkProxy.setOffline(false);
  await root.getByTestId('offline-reconnect').click();
  await expect.poll(() => offlineStoreContains(page, fixture.task.title), {
    timeout: 10_000,
    message: 'a rejected auth response should clear the offline snapshot'
  }).toBeFalsy();
  await expect(root.getByText(fixture.task.title, { exact: true })).toHaveCount(0);

  networkProxy.setAuthRejected(false);
  await (await reconnectButton(root)).click();
  await expect(page.locator('.app-shell')).toBeVisible();
  await expect(page.getByRole('heading', { name: fixture.project.name, exact: true })).toBeVisible();
  await expect(page.getByText(fixture.task.title, { exact: true })).toBeVisible();
});
