import {
  expect,
  test,
  type APIRequestContext,
  type APIResponse,
  type Locator,
  type Response as BrowserResponse
} from '@playwright/test';

type Project = { id: string; key: string; name: string; slug: string };
type Column = { id: string; name: string; semantic_state: string; position: number; ordering_version?: number };
type Task = { id: string; key: string; title: string; column_id: string; version: number; position: number };
type Collection<T> = { data: T[]; next_cursor?: string | null };

const e2eOrigin = new URL(process.env.HELM_E2E_BASE_URL || process.env.ROADMAP_E2E_BASE_URL || 'http://127.0.0.1:18080').origin;

function mutationHeaders(version?: number, key = `ordering-e2e-${crypto.randomUUID()}`): Record<string, string> {
  return {
    Origin: e2eOrigin,
    'Content-Type': 'application/json',
    'Idempotency-Key': key,
    ...(version === undefined ? {} : { 'If-Match': `"v${version}"` })
  };
}

async function json<T>(response: APIResponse, description: string): Promise<T> {
  expect(response.ok(), `${description} returned HTTP ${response.status()}`).toBeTruthy();
  return await response.json() as T;
}

async function browserJson<T>(response: BrowserResponse, description: string): Promise<T> {
  expect(response.ok(), `${description} returned HTTP ${response.status()}`).toBeTruthy();
  return await response.json() as T;
}

async function createTask(request: APIRequestContext, project: Project, column: Column, title: string): Promise<Task> {
  return json<Task>(await request.post(`/api/v1/projects/${project.id}/tasks`, {
    data: { title, column_id: column.id, priority: 'normal' },
    headers: mutationHeaders()
  }), `create ${title}`);
}

async function visibleKeys(column: Locator): Promise<string[]> {
  return (await column.locator('.task-key').allTextContents()).map((value) => value.trim());
}

test('preserves precise filtered order with keyboard announcements and mobile controls', async ({ page, request }) => {
  test.setTimeout(120_000);
  const status = await json<{ mode?: string }>(await request.get('/api/v1/auth/status'), 'read auth status');
  expect(status.mode, 'The E2E server must run with HELM_AUTH_MODE=disabled').toBe('disabled');

  const runID = `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 7)}`.toUpperCase();
  const project = await json<Project>(await request.post('/api/v1/projects', {
    data: { key: `ORD${runID}`.slice(0, 16), name: `Ordering E2E ${runID}`, description: 'Precise ordering browser fixture.' },
    headers: mutationHeaders()
  }), 'create ordering project');
  const columnResponse = await json<Collection<Column> | Column[]>(
    await request.get(`/api/v1/projects/${project.id}/columns?limit=20`),
    'list ordering project columns'
  );
  const columns = Array.isArray(columnResponse) ? columnResponse : columnResponse.data;
  const backlog = columns.find((column) => column.semantic_state === 'backlog');
  const ready = columns.find((column) => column.semantic_state === 'ready');
  expect(backlog, 'the project should have a Backlog column').toBeTruthy();
  expect(ready, 'the project should have a Ready column').toBeTruthy();

  const alpha = await createTask(request, project, ready!, `Visible alpha ${runID}`);
  const hidden = await createTask(request, project, ready!, `Filtered neighbor ${runID}`);
  const omega = await createTask(request, project, ready!, `Visible omega ${runID}`);
  const moving = await createTask(request, project, backlog!, `Visible mover ${runID}`);
  const refreshedColumns = await json<Collection<Column> | Column[]>(
    await request.get(`/api/v1/projects/${project.id}/columns?limit=20`),
    'refresh ordering revisions'
  );
  const currentColumns = Array.isArray(refreshedColumns) ? refreshedColumns : refreshedColumns.data;
  const currentBacklog = currentColumns.find((column) => column.id === backlog!.id)!;
  const currentReady = currentColumns.find((column) => column.id === ready!.id)!;
  const crossColumn = await request.post(`/api/v1/tasks/${moving.id}/reorder`, {
    data: {
      destination_column_id: currentReady.id,
      expected_source_column_id: currentBacklog.id,
      before_task_id: alpha.id,
      placement: 'before',
      expected_source_ordering_version: currentBacklog.ordering_version,
      expected_destination_ordering_version: currentReady.ordering_version,
      source: 'ordering-e2e'
    },
    headers: mutationHeaders(moving.version)
  });
  const crossed = await json<Task>(crossColumn, 'cross-column reorder');
  expect(crossed.column_id).toBe(currentReady.id);
  expect(crossed.version).toBe(moving.version + 1);

  await page.goto(`/p/${project.slug}`);
  const board = page.locator('section.board');
  await expect(board).toBeVisible();
  const readyColumn = board.locator('.board-column').filter({
    has: page.getByRole('heading', { name: 'Ready', exact: true })
  });
  await expect(readyColumn).toBeVisible();

  const moverCard = () => readyColumn.locator('.task-card').filter({ hasText: moving.title });
  const omegaCard = () => readyColumn.locator('.task-card').filter({ hasText: omega.title });
  await page.getByLabel('Search tasks').fill('Visible');
  await expect.poll(() => visibleKeys(readyColumn)).toEqual([moving.key, alpha.key, omega.key]);
  const enabledNextMoveButton = readyColumn.getByRole('button', {
    name: `Move ${moving.key} to next position`,
    exact: true
  });
  await expect(enabledNextMoveButton).toBeEnabled({ timeout: 30_000 });

  // Hold the next metadata response so the loaded cards remain rendered while
  // a manual full refresh is active. The ordering gate must disable immediately
  // and then settle back to the exact accessible label after that read ends.
  let releaseMetadataRefresh!: () => void;
  const metadataRefreshHeld = new Promise<void>((resolve) => { releaseMetadataRefresh = resolve; });
  let metadataRefreshRequests = 0;
  let holdNextMetadataRefresh = true;
  const columnsRoute = `**/api/v1/projects/${project.id}/columns?**`;
  await page.route(columnsRoute, async (route) => {
    if (holdNextMetadataRefresh) {
      holdNextMetadataRefresh = false;
      metadataRefreshRequests += 1;
      await metadataRefreshHeld;
    }
    await route.continue();
  });
  try {
    await page.getByRole('button', { name: 'Refresh board', exact: true }).click();
    await expect.poll(() => metadataRefreshRequests).toBe(1);
    const refreshingNextMoveButton = moverCard().locator('button.order-move').nth(2);
    await expect(refreshingNextMoveButton).toBeDisabled();
    await expect(refreshingNextMoveButton).toHaveAttribute('aria-label', /Unavailable: Precise ordering is unavailable while this board column is refreshing/);
  } finally {
    releaseMetadataRefresh();
  }
  await expect(enabledNextMoveButton).toBeEnabled({ timeout: 30_000 });

  // Dropping in the upper half of Omega asks the API for the visible gap
  // after Alpha/before Omega. The hidden card remains in that gap server-side.
  // Drag starts from the dedicated handle; task cards themselves remain
  // ordinary buttons/containers so opening a card never starts a drag.
  const dragHandle = moverCard().locator('.task-drag-handle');
  await omegaCard().scrollIntoViewIfNeeded();
  const omegaBounds = await omegaCard().boundingBox();
  expect(omegaBounds, 'Omega must have a rendered drop target').not.toBeNull();
  if (!omegaBounds) throw new Error('Omega must have a rendered drop target.');
  const dropPoint = { clientX: omegaBounds.x + 20, clientY: omegaBounds.y + 8 };
  const dataTransfer = await page.evaluateHandle(() => new DataTransfer());
  const reorderResponsePromise = page.waitForResponse(
    (response) => response.request().method() === 'POST' && response.url().endsWith(`/api/v1/tasks/${moving.id}/reorder`),
    { timeout: 30_000 }
  );
  await dragHandle.dispatchEvent('dragstart', { dataTransfer });
  expect(await dataTransfer.evaluate((transfer) => transfer.getData('text/plain'))).toBe(moving.id);
  const dragoverPrevented = await omegaCard().evaluate((target, point) => {
    const event = new DragEvent('dragover', {
      bubbles: true,
      cancelable: true,
      clientX: point.clientX,
      clientY: point.clientY,
      dataTransfer: new DataTransfer()
    });
    target.dispatchEvent(event);
    return event.defaultPrevented;
  }, dropPoint);
  expect(dragoverPrevented, 'the rendered card must cancel dragover before accepting a drop').toBe(true);
  await omegaCard().dispatchEvent('dragover', { dataTransfer, ...dropPoint });
  await omegaCard().dispatchEvent('drop', { dataTransfer, ...dropPoint });
  await dragHandle.dispatchEvent('dragend', { dataTransfer });
  const reordered = await browserJson<Task>(await reorderResponsePromise, 'precise filtered reorder');
  expect(reordered.id).toBe(moving.id);
  expect(reordered.column_id).toBe(currentReady.id);
  await dataTransfer.dispose();
  await expect.poll(() => visibleKeys(readyColumn)).toEqual([alpha.key, moving.key, omega.key]);
  await page.getByLabel('Search tasks').fill('');
  await expect.poll(() => visibleKeys(readyColumn)).toEqual([alpha.key, moving.key, hidden.key, omega.key]);

  const liveAnnouncement = page.locator('div.sr-only[aria-live="polite"]').last();
  const focusMover = async () => {
    await moverCard().locator('.task-main').focus();
    await expect(moverCard().locator('.task-main')).toBeFocused();
  };
  await focusMover();
  await expect(moverCard().locator('.task-main')).toHaveAttribute('aria-keyshortcuts', /Alt\+Home/);
  await page.keyboard.press('Alt+Home');
  await expect.poll(() => visibleKeys(readyColumn)).toEqual([moving.key, alpha.key, hidden.key, omega.key]);
  await expect(liveAnnouncement).toContainText(`${moving.key} moved to first position.`);

  await focusMover();
  await page.keyboard.press('Alt+ArrowDown');
  await expect.poll(() => visibleKeys(readyColumn)).toEqual([alpha.key, moving.key, hidden.key, omega.key]);
  await expect(liveAnnouncement).toContainText(`${moving.key} moved to next position.`);

  await focusMover();
  await page.keyboard.press('Alt+End');
  await expect.poll(() => visibleKeys(readyColumn)).toEqual([alpha.key, hidden.key, omega.key, moving.key]);
  await expect(liveAnnouncement).toContainText(`${moving.key} moved to last position.`);

  await focusMover();
  await page.keyboard.press('Alt+ArrowUp');
  await expect.poll(() => visibleKeys(readyColumn)).toEqual([alpha.key, hidden.key, moving.key, omega.key]);
  await expect(liveAnnouncement).toContainText(`${moving.key} moved to previous position.`);

  // The same controls retain accessible names and usable hit areas on mobile.
  await page.setViewportSize({ width: 390, height: 844 });
  await page.reload();
  await expect(page.getByRole('navigation', { name: 'Primary navigation' })).toBeVisible();
  await expect(moverCard().getByRole('button', { name: `Move ${moving.key} to first position` })).toBeVisible();
  await expect(moverCard().getByRole('button', { name: `Move ${moving.key} to previous position` })).toBeVisible();
  await expect(moverCard().getByRole('button', { name: `Move ${moving.key} to next position` })).toBeVisible();
  await expect(moverCard().getByRole('button', { name: `Move ${moving.key} to last position` })).toBeVisible();
});
