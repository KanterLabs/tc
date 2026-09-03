import { expect, test, type APIRequestContext, type APIResponse } from '@playwright/test';

type Project = { id: string; key: string; name: string; slug: string };
type Column = { id: string; name: string; semantic_state: string; position: number };
type DependencySummary = {
  prerequisite_count: number;
  unmet_prerequisite_count: number;
  dependent_count: number;
  blocked: boolean;
};
type Task = {
  id: string;
  key: string;
  title: string;
  project_id: string;
  column_id: string;
  version: number;
  dependency_summary: DependencySummary;
};
type Collection<T> = { data: T[]; next_cursor?: string | null };

const e2eOrigin = new URL(process.env.HELM_E2E_BASE_URL || process.env.ROADMAP_E2E_BASE_URL || 'http://127.0.0.1:18080').origin;

function mutationHeaders(version?: number, key = `dependencies-e2e-${crypto.randomUUID()}`): Record<string, string> {
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

async function createTask(
  request: APIRequestContext,
  project: Project,
  column: Column,
  title: string,
  extra: Record<string, unknown> = {}
): Promise<Task> {
  return json<Task>(await request.post(`/api/v1/projects/${project.id}/tasks`, {
    data: { title, column_id: column.id, priority: 'normal', ...extra },
    headers: mutationHeaders()
  }), `create ${title}`);
}

async function expectDependencyConflict(response: APIResponse, operation: string): Promise<void> {
  expect(response.status(), operation).toBe(409);
  const body = await response.json() as { error?: { code?: string } };
  expect(body.error?.code, operation).toBe('unmet_dependencies');
}

test('orders dependent work across drawer, board, filters, polling, and mobile', async ({ page, request }) => {
  test.setTimeout(120_000);
  const status = await json<{ mode?: string }>(await request.get('/api/v1/auth/status'), 'read auth status');
  expect(status.mode, 'The E2E server must run with HELM_AUTH_MODE=disabled').toBe('disabled');

  const runID = `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 7)}`.toUpperCase();
  const suffix = runID.slice(-8);
  const project = await json<Project>(await request.post('/api/v1/projects', {
    data: {
      key: `DEP${suffix}`.slice(0, 16),
      name: `Dependency E2E ${suffix}`,
      description: 'Ordered-work browser acceptance fixture.'
    },
    headers: mutationHeaders()
  }), 'create dependency project');
  const columns = (await json<Collection<Column> | Column[]>(
    await request.get(`/api/v1/projects/${project.id}/columns?limit=20`),
    'list dependency project columns'
  ));
  const columnList = Array.isArray(columns) ? columns : columns.data;
  const ready = columnList.find((column) => column.semantic_state === 'ready');
  expect(ready, 'the project should have a Ready column').toBeTruthy();

  let prerequisite = await createTask(request, project, ready!, `Release API contract ${suffix}`);
  let dependent = await createTask(request, project, ready!, `Deploy dependent service ${suffix}`);
  let bug = await createTask(request, project, ready!, `Resolve dependent regression ${suffix}`, {
    kind: 'bug',
    bug: {
      actual_behavior: 'The dependent flow fails.',
      expected_behavior: 'The ordered flow succeeds.',
      reproduction_steps: 'Attempt the dependent release.',
      environment: 'Browser E2E',
      affected_version: 'dependency-test'
    }
  });
  bug = await json<Task>(await request.post(`/api/v1/tasks/${bug.id}/dependencies`, {
    data: { prerequisite: prerequisite.id },
    headers: mutationHeaders(bug.version)
  }), 'link bug prerequisite');

  await page.goto(`/p/${project.slug}`);
  const board = page.locator('section.board');
  await expect(board).toBeVisible();
  const card = (title: string) => board.locator('.task-card').filter({ hasText: title });

  await card(dependent.title).locator('.task-main').click();
  const drawer = page.locator('.task-drawer');
  await expect(drawer).toBeVisible();
  const dependencySearch = drawer.getByRole('combobox', { name: 'Add prerequisite' });
  await dependencySearch.fill(prerequisite.key);
  await drawer.getByRole('option', { name: new RegExp(prerequisite.key) }).click();
  await expect(drawer.getByRole('button', { name: new RegExp(`Open ${prerequisite.key}`) })).toBeVisible();
  await expect(drawer.getByText('1 unfinished prerequisite', { exact: true })).toBeVisible();
  dependent = await json<Task>(await request.get(`/api/v1/tasks/${dependent.id}`), 'reload linked dependent');

  await expectDependencyConflict(await request.post(`/api/v1/tasks/${dependent.id}/claim`, {
    data: { lease_seconds: 3600 }, headers: mutationHeaders(dependent.version)
  }), 'blocked claim');
  await expectDependencyConflict(await request.post(`/api/v1/tasks/${dependent.id}/complete`, {
    headers: mutationHeaders(dependent.version)
  }), 'blocked completion');
  await expectDependencyConflict(await request.post(`/api/v1/tasks/${bug.id}/resolve`, {
    data: { resolution: 'fixed', note: 'This must remain blocked.' }, headers: mutationHeaders(bug.version)
  }), 'blocked bug resolution');

  const cycle = await request.post(`/api/v1/tasks/${prerequisite.id}/dependencies`, {
    data: { prerequisite: dependent.id },
    headers: mutationHeaders(prerequisite.version)
  });
  expect(cycle.status()).toBe(409);
  expect((await cycle.json() as { error?: { code?: string } }).error?.code).toBe('dependency_cycle');

  await drawer.getByRole('button', { name: 'Close task details', exact: true }).click();
  const dependentCard = card(dependent.title);
  await expect(dependentCard).toHaveClass(/dependency-blocked/);
  await expect(dependentCard.locator('.dependency-badge.blocked')).toContainText('1 waiting');
  const readinessFilter = page.getByLabel('Filter by dependency readiness');
  await readinessFilter.selectOption('blocked');
  await expect(dependentCard).toBeVisible();
  await expect(card(prerequisite.title)).toHaveCount(0);
  await readinessFilter.selectOption('all');

  await card(bug.title).locator('.task-main').click();
  await expect(drawer.getByRole('button', { name: /Triage issue|Update triage/ })).toBeDisabled();
  await expect(drawer.getByRole('button', { name: 'Resolve issue', exact: true })).toBeDisabled();
  await drawer.getByRole('button', { name: 'Close task details', exact: true }).click();

  await dependentCard.locator('.task-main').click();
  await expect(drawer.getByRole('button', { name: /Claim task/ })).toBeDisabled();
  await expect(drawer.getByRole('button', { name: '✓ Complete', exact: true })).toBeDisabled();
  await expect(drawer.getByText(/Finish it before you start or complete this task/)).toBeVisible();
  await drawer.getByRole('button', { name: new RegExp(`Open ${prerequisite.key}`) }).click();
  await expect(drawer.getByLabel('Task title')).toHaveValue(prerequisite.title);
  await expect(page).toHaveURL(new RegExp(`/p/${project.slug}/tasks/${prerequisite.key}`));

  await drawer.getByRole('button', { name: '✓ Complete', exact: true }).click();
  await expect(drawer.getByRole('button', { name: '✓ Completed', exact: true })).toBeDisabled();
  prerequisite = await json<Task>(await request.get(`/api/v1/tasks/${prerequisite.id}`), 'reload completed prerequisite');
  expect(prerequisite.dependency_summary.dependent_count).toBe(2);
  await drawer.getByRole('button', { name: 'Close task details', exact: true }).click();

  // Completion emits equal-version invalidations for both dependents. The
  // board poll must accept the derived summaries without losing card state.
  await expect(card(dependent.title).locator('.dependency-badge.ready')).toContainText('1 ready', { timeout: 20_000 });
  await expect(card(dependent.title)).not.toHaveClass(/dependency-blocked/);
  await readinessFilter.selectOption('ready');
  await expect(card(dependent.title)).toBeVisible();
  await expect(card(bug.title)).toBeVisible();
  await expect(card(prerequisite.title)).toHaveCount(0);
  await readinessFilter.selectOption('all');

  await page.setViewportSize({ width: 390, height: 844 });
  await card(dependent.title).locator('.task-main').click();
  await expect(drawer.getByRole('button', { name: /Claim task/ })).toBeEnabled();
  const mobileSearchBox = await drawer.getByRole('combobox', { name: 'Add prerequisite' }).boundingBox();
  expect(mobileSearchBox?.height).toBeGreaterThanOrEqual(44);
  const remove = drawer.getByRole('button', { name: `Remove ${prerequisite.key} as a prerequisite`, exact: true });
  await expect(remove).toBeVisible();
  const removeBox = await remove.boundingBox();
  expect(removeBox?.height).toBeGreaterThanOrEqual(44);
  await remove.click();
  await expect(drawer.getByText('No prerequisites yet.', { exact: true })).toBeVisible();
  await drawer.getByRole('button', { name: 'Close task details', exact: true }).click();
  await expect(card(dependent.title).locator('.dependency-badge')).toHaveCount(0);
});
