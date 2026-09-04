import { expect, test, type APIRequestContext } from '@playwright/test';

type Project = { id: string; key: string; name: string; slug: string };
type Column = { id: string; semantic_state: string };
type Task = { id: string; key: string; title: string; project_id: string; column_id: string };
type Actor = { id: string };

const e2eOrigin = new URL(
  process.env.HELM_E2E_BASE_URL || process.env.ROADMAP_E2E_BASE_URL || 'http://127.0.0.1:18080'
).origin;

async function json<T>(response: Awaited<ReturnType<APIRequestContext['get']>>, description: string): Promise<T> {
  expect(response.ok(), `${description} returned HTTP ${response.status()}`).toBeTruthy();
  return await response.json() as T;
}

async function post<T>(request: APIRequestContext, path: string, body: unknown, key: string): Promise<T> {
  const response = await request.post(path, {
    data: body,
    headers: { 'Content-Type': 'application/json', Origin: e2eOrigin, 'Idempotency-Key': key }
  });
  return json<T>(response, `POST ${path}`);
}

test('searches across projects, saves a view, and returns to cross-project My Work', async ({ page, request }) => {
  test.setTimeout(60_000);
  const runID = `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 7)}`.toUpperCase();
  const projectA = await post<Project>(request, '/api/v1/projects', { key: `SXA${runID}`.slice(0, 16), name: `Search Alpha ${runID}` }, `search-e2e-${runID}-project-a`);
  const projectB = await post<Project>(request, '/api/v1/projects', { key: `SXB${runID}`.slice(0, 16), name: `Search Beta ${runID}` }, `search-e2e-${runID}-project-b`);
  const actor = await json<Actor>(await request.get('/api/v1/auth/me'), 'GET /api/v1/auth/me');
  const columnsA = await json<{ data: Column[] }>(await request.get(`/api/v1/projects/${projectA.id}/columns?limit=20`), 'GET columns A');
  const columnsB = await json<{ data: Column[] }>(await request.get(`/api/v1/projects/${projectB.id}/columns?limit=20`), 'GET columns B');
  const readyA = columnsA.data.find((column) => column.semantic_state === 'ready') as Column;
  const readyB = columnsB.data.find((column) => column.semantic_state === 'ready') as Column;
  const alphaTitle = `Global needle alpha ${runID}`;
  const betaTitle = `Global needle beta ${runID}`;
  await post<Task>(request, `/api/v1/projects/${projectA.id}/tasks`, { title: alphaTitle, column_id: readyA.id, assignee: actor.id }, `search-e2e-${runID}-task-a`);
  await post<Task>(request, `/api/v1/projects/${projectB.id}/tasks`, { title: betaTitle, column_id: readyB.id, assignee: actor.id }, `search-e2e-${runID}-task-b`);

  await page.goto('/search');
  await expect(page.getByRole('heading', { name: 'Search everything', exact: true })).toBeVisible();
  const searchInput = page.getByRole('textbox', { name: 'Search all tasks' });
  await searchInput.fill('Global needle');
  await searchInput.press('Enter');
  await expect(page.getByRole('button', { name: new RegExp(alphaTitle) })).toBeVisible();
  await expect(page.getByRole('button', { name: new RegExp(betaTitle) })).toBeVisible();

  await page.getByLabel('Saved view name').fill(`Needles ${runID}`);
  await page.getByLabel('Share').check();
  await page.getByRole('button', { name: 'Save view', exact: true }).click();
  await expect(page.getByRole('heading', { name: `Needles ${runID}`, exact: true })).toBeVisible();
  await expect(page).toHaveURL(/\/search\?view=/);

  await page.getByRole('button', { name: 'Search anything', exact: true }).click();
  const commandDialog = page.getByRole('dialog', { name: 'Search Helm' });
  await commandDialog.getByRole('combobox', { name: 'Search projects and views, tasks, issues, and actions' }).fill(alphaTitle);
  await expect(commandDialog.getByRole('option', { name: new RegExp(alphaTitle) })).toBeVisible();
  await commandDialog.getByRole('option', { name: new RegExp(alphaTitle) }).click();
  await expect(page.locator('.task-drawer')).toBeVisible();
  await expect(page).toHaveURL(new RegExp(`/p/${projectA.slug}/tasks/`));
  await page.getByRole('button', { name: 'Close task details', exact: true }).click();
  await expect(page).toHaveURL(/\/search\?view=/);
  await expect(page.getByRole('heading', { name: `Needles ${runID}`, exact: true })).toBeVisible();

  await page.getByRole('button', { name: 'My work', exact: true }).first().click();
  await page.getByRole('button', { name: 'Assigned', exact: true }).click();
  await expect(page.getByRole('button', { name: new RegExp(alphaTitle) })).toBeVisible();
  await expect(page.getByRole('button', { name: new RegExp(betaTitle) })).toBeVisible();
  await page.getByRole('button', { name: new RegExp(betaTitle) }).click();
  await expect(page.locator('.task-drawer')).toBeVisible();
  await expect(page).toHaveURL(new RegExp(`/p/${projectB.slug}/tasks/`));
  await page.getByRole('button', { name: 'Close task details', exact: true }).click();
  await expect(page).toHaveURL('/my-work');
  await expect(page.getByRole('heading', { name: 'My work', exact: true })).toBeVisible();
});
