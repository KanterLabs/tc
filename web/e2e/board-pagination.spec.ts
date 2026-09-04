import { expect, test, type APIRequestContext, type APIResponse } from '@playwright/test';

type Project = { id: string; key: string; name: string; slug: string };
type Column = { id: string; name: string; semantic_state: string; position: number };
type Task = { id: string; key: string; title: string; column_id: string; position: number; version: number };
type Collection<T> = { data: T[]; next_cursor?: string | null };

const e2eOrigin = new URL(
  process.env.HELM_E2E_BASE_URL || process.env.ROADMAP_E2E_BASE_URL || 'http://127.0.0.1:18080'
).origin;
const boardPageSize = 50;

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

function columnFor(columns: Column[], semanticState: string): Column {
  const column = columns.find((item) => item.semantic_state === semanticState);
  expect(column, `the project should have a ${semanticState} column`).toBeTruthy();
  return column as Column;
}

function taskRequestParams(url: string): URLSearchParams {
  return new URL(url).searchParams;
}

test('keeps large board pages bounded, filterable, and reconciled live', async ({ page, request }) => {
  test.setTimeout(120_000);

  const status = await json<{ mode?: string }>(await request.get('/api/v1/auth/status'), 'read auth status');
  expect(status.mode, 'The E2E server must run with HELM_AUTH_MODE=disabled').toBe('disabled');

  const runID = `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 7)}`.toUpperCase();
  const project = await postJSON<Project>(request, '/api/v1/projects', {
    key: `PG${runID}`.slice(0, 16),
    name: `Board pagination E2E ${runID}`,
    description: 'Large-board pagination and reconciliation fixture.'
  }, `board-pagination-${runID}-project`);
  const columnPayload = await json<Collection<Column> | Column[]>(
    await request.get(`/api/v1/projects/${project.id}/columns?limit=20`),
    'list board pagination columns'
  );
  const ready = columnFor(collectionData(columnPayload), 'ready');

  const fixtureTasks: Task[] = [];
  for (let index = 1; index <= boardPageSize * 2 + 2; index += 1) {
    fixtureTasks.push(await postJSON<Task>(request, `/api/v1/projects/${project.id}/tasks`, {
      title: `Page fixture ${String(index).padStart(2, '0')} ${runID}`,
      column_id: ready.id,
      priority: 'normal'
    }, `board-pagination-${runID}-task-${index}`));
  }
  const outsideInitialPage = fixtureTasks[fixtureTasks.length - 1];
  expect(outsideInitialPage, 'the fixture should include a task outside page one').toBeTruthy();

  const taskRequests: string[] = [];
  let eventResponses = 0;
  page.on('request', (browserRequest) => {
    if (browserRequest.method() !== 'GET') return;
    const url = new URL(browserRequest.url());
    if (url.pathname.startsWith('/api/v1/projects/') && url.pathname.endsWith('/tasks')) {
      taskRequests.push(url.toString());
    }
  });
  page.on('response', (response) => {
    const url = new URL(response.url());
    if (response.request().method() === 'GET' && url.pathname === '/api/v1/events') eventResponses += 1;
  });

  // The fixture necessarily creates more than 50 task.created events. Drain
  // that pre-existing event backlog in the browser's first poll so the live
  // mutation below is observed on the next 15-second poll deterministically.
  await page.route('**/api/v1/events**', async (route) => {
    const url = new URL(route.request().url());
    url.searchParams.set('limit', '200');
    const response = await route.fetch({ url: url.toString() });
    await route.fulfill({ response });
  });

  // Fake timers keep the real 15-second poll and the 160ms filter debounce
  // deterministic without changing the server or production polling cadence.
  await page.clock.install();
  await page.goto(`/p/${project.slug}`);

  const board = page.locator('section.board');
  await expect(board).toBeVisible();
  const readyColumn = board.locator('.board-column').filter({
    has: page.getByRole('heading', { name: ready.name, exact: true })
  });
  await expect(readyColumn).toBeVisible();
  await expect(readyColumn.locator('.task-card')).toHaveCount(boardPageSize);
  await expect(page.locator('.task-card')).toHaveCount(boardPageSize);
  await expect(page.getByRole('button', { name: 'Retry columns', exact: true })).toHaveCount(0);
  await expect.poll(() => eventResponses, { timeout: 15_000 }).toBeGreaterThan(0);

  const firstReadyRequest = taskRequests.find((url) => {
    const params = taskRequestParams(url);
    return params.get('column') === ready.id && !params.get('cursor') && !params.get('q');
  });
  expect(firstReadyRequest, 'the initial Ready request should be observable').toBeTruthy();
  const firstReadyParams = taskRequestParams(firstReadyRequest as string);
  expect(firstReadyParams.get('limit')).toBe(String(boardPageSize));
  expect(firstReadyParams.get('sort')).toBe('position');
  expect(firstReadyParams.get('order')).toBe('asc');

  const search = page.getByRole('textbox', { name: 'Search tasks' });
  await search.fill(outsideInitialPage.title);
  await page.clock.runFor(250);
  const outsideCard = readyColumn.locator('.task-card').filter({ hasText: outsideInitialPage.key });
  await expect(outsideCard).toBeVisible();
  await expect(readyColumn.locator('.task-card')).toHaveCount(1);
  await expect.poll(() => taskRequests.some((url) => {
    const params = taskRequestParams(url);
    return params.get('column') === ready.id
      && params.get('q') === outsideInitialPage.title
      && params.get('limit') === String(boardPageSize)
      && !params.get('cursor');
  }), { timeout: 15_000 }).toBeTruthy();

  await search.fill('');
  await page.clock.runFor(250);
  await expect(readyColumn.locator('.task-card')).toHaveCount(boardPageSize);

  const loadMore = readyColumn.getByRole('button', { name: 'Load more tasks', exact: true });
  await expect(loadMore).toBeVisible();
  await expect(loadMore).toBeEnabled();
  await loadMore.focus();
  await expect(loadMore).toBeFocused();
  await loadMore.press('Enter');
  await expect(readyColumn.locator('.task-card')).toHaveCount(boardPageSize * 2);
  await expect(outsideCard).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Retry columns', exact: true })).toHaveCount(0);

  // A real failed page retains the loaded cards and offers a working retry.
  // Ordinary pagination must not show that failure warning.
  await page.route(`**/api/v1/projects/${project.id}/tasks?**`, async (route) => {
    await route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({
      error: { code: 'unavailable', message: 'Temporary page failure' }
    }) });
  }, { times: 1 });
  await loadMore.click();
  await expect(page.getByRole('button', { name: 'Retry columns', exact: true })).toBeVisible();
  await expect(readyColumn.locator('.task-card')).toHaveCount(boardPageSize * 2);
  await readyColumn.getByRole('button', { name: 'Retry', exact: true }).click();
  await expect(outsideCard).toBeVisible();
  await expect(readyColumn.locator('.task-card')).toHaveCount(fixtureTasks.length);
  await expect(page.getByRole('button', { name: 'Retry columns', exact: true })).toHaveCount(0);
  await expect(loadMore).toHaveCount(0);
  await expect.poll(() => taskRequests.some((url) => {
    const params = taskRequestParams(url);
    return params.get('column') === ready.id && Boolean(params.get('cursor')) && !params.get('q');
  }), { timeout: 15_000 }).toBeTruthy();

  // Create an external task after the complete fixture is loaded. Placing it
  // at the head keeps a new mutation visible when the event poll refreshes
  // the bounded first page.
  const liveTask = await postJSON<Task>(request, `/api/v1/projects/${project.id}/tasks`, {
    title: `Live mutation ${runID}`,
    column_id: ready.id,
    priority: 'normal',
    position: 0
  }, `board-pagination-${runID}-live`);
  const liveCard = readyColumn.locator('.task-card').filter({ hasText: liveTask.key });
  await expect(liveCard).toHaveCount(0);
  const reconciliationAlert = page.locator('.content-alert[role="status"]').filter({
    hasText: 'outside the loaded board page'
  });
  // Advance only the browser's polling clock; this still exercises the real
  // events endpoint and subsequent authoritative board reload.
  await page.clock.fastForward(15_100);
  let outcome = 'pending';
  await expect.poll(async () => {
    outcome = await liveCard.isVisible().catch(() => false)
      ? 'visible'
      : await reconciliationAlert.isVisible().catch(() => false) ? 'excluded' : 'pending';
    return outcome;
  }, { timeout: 15_000, intervals: [250] }).not.toBe('pending');
  expect(outcome).toMatch(/visible|excluded/);
  if (outcome === 'visible') {
    await expect(liveCard).toBeVisible();
  } else {
    await expect(reconciliationAlert).toContainText('Refresh or load more to find it.');
  }
});
