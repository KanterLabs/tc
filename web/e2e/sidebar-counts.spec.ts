import { expect, test, type APIRequestContext, type APIResponse } from '@playwright/test';

type Project = {
  id: string;
  key: string;
  name: string;
  slug: string;
};

type Column = {
  id: string;
  semantic_state: string;
};

type Task = {
  id: string;
  project_id: string;
  version: number;
};

type SidebarCounts = {
  issues: number;
  my_work: number;
  view: 'live' | 'assigned';
};

type IssueMetrics = {
  reopened: number;
  window_days: number;
};

type Collection<T> = {
  data: T[];
  next_cursor?: string | null;
};

const e2eOrigin = new URL(process.env.HELM_E2E_BASE_URL || process.env.ROADMAP_E2E_BASE_URL || 'http://127.0.0.1:18080').origin;

function etagForVersion(version: number): string {
  return `"v${version}"`;
}

async function assertJSON<T>(response: APIResponse, description: string): Promise<T> {
  expect(response.ok(), `${description} returned HTTP ${response.status()}`).toBeTruthy();
  return await response.json() as T;
}

async function getJSON<T>(request: APIRequestContext, path: string): Promise<T> {
  return assertJSON<T>(await request.get(path), `GET ${path}`);
}

async function postJSON<T>(
  request: APIRequestContext,
  path: string,
  body: unknown,
  headers: Record<string, string>
): Promise<T> {
  return assertJSON<T>(await request.post(path, {
    data: body,
    headers: {
      'Content-Type': 'application/json',
      Origin: e2eOrigin,
      ...headers
    }
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

test('bootstraps scalar navigation counts and refreshes global counts from another project', async ({ page, request }) => {
  test.setTimeout(90_000);

  const status = await getJSON<{ mode?: string }>(request, '/api/v1/auth/status');
  expect(status.mode, 'The E2E server must run with HELM_AUTH_MODE=disabled').toBe('disabled');

  const baselineCounts = await getJSON<SidebarCounts>(request, '/api/v1/sidebar-counts?view=live');
  const baselineMetrics = await getJSON<IssueMetrics>(request, '/api/v1/issues/metrics');
  expect(baselineCounts.view).toBe('live');
  expect(baselineMetrics.window_days).toBe(7);

  const runID = `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 7)}`.toUpperCase();
  const suffix = runID.slice(-8);
  let mutationNumber = 0;
  const mutationKey = () => `sidebar-counts-e2e-${runID}-${++mutationNumber}`;
  const projectA = await postJSON<Project>(request, '/api/v1/projects', {
    key: `SCA${suffix}`.slice(0, 16),
    name: `Sidebar Counts Alpha ${suffix}`
  }, { 'Idempotency-Key': mutationKey() });
  const columnsA = collectionData(await getJSON<Collection<Column> | Column[]>(request, `/api/v1/projects/${projectA.id}/columns?limit=20`));
  const activeA = columnFor(columnsA, 'active');

  const bug = await postJSON<Task>(request, `/api/v1/projects/${projectA.id}/tasks`, {
    title: `Reopened count fixture ${suffix}`,
    kind: 'bug',
    bug: {
      actual_behavior: 'The regression returns after a release.',
      expected_behavior: 'The regression remains fixed.',
      reproduction_steps: 'Open the fixture and repeat the workflow.'
    }
  }, { 'Idempotency-Key': mutationKey() });
  const resolved = await postJSON<Task>(request, `/api/v1/tasks/${bug.id}/resolve`, {
    resolution: 'fixed',
    note: 'Fixture resolution before reopening.'
  }, { 'If-Match': etagForVersion(bug.version), 'Idempotency-Key': mutationKey() });
  await postJSON<Task>(request, `/api/v1/tasks/${bug.id}/reopen`, {
    reason: 'Fixture regression for the bounded metric.'
  }, { 'If-Match': etagForVersion(resolved.version), 'Idempotency-Key': mutationKey() });

  let work = await postJSON<Task>(request, `/api/v1/projects/${projectA.id}/tasks`, {
    title: `Live count fixture ${suffix}`,
    column_id: activeA.id
  }, { 'Idempotency-Key': mutationKey() });
  work = await postJSON<Task>(request, `/api/v1/tasks/${work.id}/claim`, { lease_seconds: 3600 }, {
    'If-Match': etagForVersion(work.version),
    'Idempotency-Key': mutationKey()
  });
  await postJSON<Task>(request, `/api/v1/tasks/${work.id}/progress`, {
    operation_id: `sidebar-counts/${runID}`,
    state: 'working',
    summary: 'Fixture for the live My Work badge.',
    checkpoint_refs: ['sidebar-counts'],
    checkpoint_completed: 1,
    checkpoint_total: 1
  }, {
    'If-Match': etagForVersion(work.version),
    'Idempotency-Key': mutationKey()
  });

  const observedRequests: string[] = [];
  page.on('request', (browserRequest) => {
    if (browserRequest.method() === 'GET' && browserRequest.url().includes('/api/v1/')) observedRequests.push(browserRequest.url());
  });
  const bootstrapCounts = page.waitForResponse((response) => {
    const url = new URL(response.url());
    return response.request().method() === 'GET' && url.pathname === '/api/v1/sidebar-counts';
  });
  await page.goto('/');
  await bootstrapCounts;

  const workspaceNavigation = page.getByRole('navigation', { name: 'Workspace views' });
  const issueNavigationButton = workspaceNavigation.getByRole('button', { name: 'Issues', exact: true });
  const myWorkNavigationButton = workspaceNavigation.getByRole('button', { name: 'My work', exact: true });
  await expect(issueNavigationButton.locator('.nav-count')).toHaveText(String(baselineCounts.issues + 1));
  await expect(myWorkNavigationButton.locator('.nav-count')).toHaveText(String(baselineCounts.my_work + 1));
  expect(observedRequests.filter((url) => new URL(url).pathname === '/api/v1/issues')).toHaveLength(0);
  expect(observedRequests.filter((url) => new URL(url).pathname === '/api/v1/my-work')).toHaveLength(0);

  // Add a bug in a different project while the active project remains Alpha.
  // The scalar endpoint should refresh globally even though the board reload
  // predicate only considers events for the active project.
  const projectB = await postJSON<Project>(request, '/api/v1/projects', {
    key: `SCB${suffix}`.slice(0, 16),
    name: `Sidebar Counts Beta ${suffix}`
  }, { 'Idempotency-Key': mutationKey() });
  await postJSON<Task>(request, `/api/v1/projects/${projectB.id}/tasks`, {
    title: `Cross-project issue fixture ${suffix}`,
    kind: 'bug',
    bug: { actual_behavior: 'The cross-project fixture exists.' }
  }, { 'Idempotency-Key': mutationKey() });

  await expect(issueNavigationButton.locator('.nav-count')).toHaveText(String(baselineCounts.issues + 2), { timeout: 30_000 });
  expect(page.url()).not.toMatch(/\/issues\/?$/);

  await issueNavigationButton.click();
  await expect(page.getByRole('region', { name: 'Issue health' })).toBeVisible();
  const reopenedMetric = page.locator('section[aria-label="Issue health"] article').filter({ hasText: 'Reopened · 7d' });
  await expect(reopenedMetric).toContainText(String(baselineMetrics.reopened + 1));
});
