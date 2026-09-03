import { expect, test, type APIRequestContext, type APIResponse } from '@playwright/test';

type Project = {
  id: string;
  key: string;
  name: string;
  slug: string;
};

type Column = {
  id: string;
  name: string;
  semantic_state: string;
  position: number;
};

type AgentWork = {
  operation_id: string;
  actor_id: string;
  state: 'working' | 'waiting' | 'verifying' | 'handoff';
  phase: string;
  summary: string;
  next_action: string;
  checkpoint_refs: string[];
  checkpoint_completed?: number;
  checkpoint_total?: number;
  started_at: string;
  updated_at: string;
  stale?: boolean;
  action_needed?: boolean;
};

type Task = {
  id: string;
  key: string;
  title: string;
  project_id: string;
  column_id: string;
  priority: string;
  version: number;
  agent_work?: AgentWork | null;
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
  headers: Record<string, string> = {}
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

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

/**
 * The public API intentionally has no clock-setting endpoint. The browser
 * workflow publishes the pulses through the real API, then ages read
 * responses at the API boundary so stale rendering is deterministic without
 * a fifteen-minute sleep or a database-specific test hook.
 */
function ageTaskInPayload(value: unknown, taskID: string, updatedAt: string): boolean {
  if (Array.isArray(value)) return value.some((item) => ageTaskInPayload(item, taskID, updatedAt));
  if (!isRecord(value)) return false;

  if (value.id === taskID && isRecord(value.agent_work)) {
    value.agent_work = {
      ...value.agent_work,
      updated_at: updatedAt,
      stale: true,
      action_needed: true
    };
    return true;
  }

  return Array.isArray(value.data) && value.data.some((item) => ageTaskInPayload(item, taskID, updatedAt));
}

function expireTaskClaimInPayload(value: unknown, taskID: string, expiresAt: string): boolean {
  if (Array.isArray(value)) return value.some((item) => expireTaskClaimInPayload(item, taskID, expiresAt));
  if (!isRecord(value)) return false;

  if (value.id === taskID) {
    value.claim_expires_at = expiresAt;
    return true;
  }

  return Array.isArray(value.data) && value.data.some((item) => expireTaskClaimInPayload(item, taskID, expiresAt));
}

test('shows live agent work across the board, drawer, and My Work', async ({ page, request }) => {
  test.setTimeout(90_000);

  const status = await getJSON<{ mode?: string }>(request, '/api/v1/auth/status');
  expect(status.mode, 'The E2E server must run with HELM_AUTH_MODE=disabled').toBe('disabled');

  const runID = `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 7)}`.toUpperCase();
  const suffix = runID.slice(-8);
  const projectAName = `Agent Work Alpha ${suffix}`;
  const projectBName = `Agent Work Beta ${suffix}`;
  const projectAKey = `AWA${suffix}`.slice(0, 16);
  const projectBKey = `AWB${suffix}`.slice(0, 16);
  let mutationNumber = 0;
  const mutationKey = () => `agent-work-e2e-${runID}-${++mutationNumber}`;

  // Create both projects and all tasks through the public HTTP API. Keeping
  // the fixtures in two projects exercises the cross-project My Work view.
  const projectA = await postJSON<Project>(request, '/api/v1/projects', {
    key: projectAKey,
    name: projectAName,
    description: 'Cross-project live work acceptance fixture.'
  }, { 'Idempotency-Key': mutationKey() });
  const projectB = await postJSON<Project>(request, '/api/v1/projects', {
    key: projectBKey,
    name: projectBName,
    description: 'Cross-project live work acceptance fixture.'
  }, { 'Idempotency-Key': mutationKey() });

  const columnsA = collectionData(await getJSON<Collection<Column> | Column[]>(request, `/api/v1/projects/${projectA.id}/columns?limit=20`));
  const columnsB = collectionData(await getJSON<Collection<Column> | Column[]>(request, `/api/v1/projects/${projectB.id}/columns?limit=20`));
  const activeA = columnFor(columnsA, 'active');
  const blockedA = columnFor(columnsA, 'blocked');
  const readyB = columnFor(columnsB, 'ready');
  const activeB = columnFor(columnsB, 'active');

  const createTask = async (project: Project, column: Column, title: string): Promise<Task> =>
    postJSON<Task>(request, `/api/v1/projects/${project.id}/tasks`, {
      title,
      column_id: column.id,
      priority: 'normal'
    }, { 'Idempotency-Key': mutationKey() });

  let workingTask = await createTask(projectA, activeA, `Working pulse ${suffix}`);
  let waitingTask = await createTask(projectA, blockedA, `Waiting pulse ${suffix}`);
  let verifyingTask = await createTask(projectB, readyB, `Verifying pulse ${suffix}`);
  let staleTask = await createTask(projectB, activeB, `Stale pulse ${suffix}`);
  let staleWaitingTask = await createTask(projectB, activeB, `Stale dependency wait ${suffix}`);
  let completedTask = await createTask(projectA, activeA, `Completed pulse ${suffix}`);
  const missingTask = await createTask(projectA, activeA, `Missing pulse ${suffix}`);

  const claimTask = async (task: Task): Promise<Task> =>
    postJSON<Task>(request, `/api/v1/tasks/${task.id}/claim`, { lease_seconds: 3600 }, {
      'If-Match': etagForVersion(task.version),
      'Idempotency-Key': mutationKey()
    });

  workingTask = await claimTask(workingTask);
  waitingTask = await claimTask(waitingTask);
  verifyingTask = await claimTask(verifyingTask);
  staleTask = await claimTask(staleTask);
  staleWaitingTask = await claimTask(staleWaitingTask);
  completedTask = await claimTask(completedTask);

  const publish = async (task: Task, state: AgentWork['state'], phase: string, summary: string, nextAction: string, refs: string[], completed: number, total: number): Promise<Task> =>
    postJSON<Task>(request, `/api/v1/tasks/${task.id}/progress`, {
      operation_id: `agent-work/${runID}/${task.key}`,
      state,
      phase,
      summary,
      next_action: nextAction,
      checkpoint_refs: refs,
      checkpoint_completed: completed,
      checkpoint_total: total
    }, {
      'If-Match': etagForVersion(task.version),
      'Idempotency-Key': mutationKey()
    });

  workingTask = await publish(
    workingTask,
    'working',
    'Implementing the browser workflow',
    'The live working pulse is visible on the board.',
    'Wait for the remote progress event.',
    ['board', 'drawer'],
    1,
    2
  );
  waitingTask = await publish(
    waitingTask,
    'waiting',
    'Dependency review',
    'Waiting for the API fixture to become available.',
    'Confirm the fixture and continue.',
    ['dependency', 'decision'],
    1,
    2
  );
  verifyingTask = await publish(
    verifyingTask,
    'verifying',
    'QA verification',
    'The cross-project rendering is ready for a final check.',
    'Run the acceptance checks.',
    ['rendering', 'acceptance'],
    1,
    2
  );
  staleTask = await publish(
    staleTask,
    'working',
    'Background worker',
    'This pulse will be aged at the read boundary.',
    'Restart the worker after review.',
    ['heartbeat'],
    0,
    1
  );
  staleWaitingTask = await publish(
    staleWaitingTask,
    'waiting',
    'Waiting on a stale dependency',
    'This waiting pulse will also be aged at the read boundary.',
    'Escalate the dependency if it remains unanswered.',
    ['dependency', 'stale'],
    1,
    2
  );
  completedTask = await publish(
    completedTask,
    'waiting',
    'Finished work history',
    'This retained snapshot must not remain live after completion.',
    'No action remains.',
    ['implementation', 'completion'],
    2,
    2
  );
  completedTask = await postJSON<Task>(request, `/api/v1/tasks/${completedTask.id}/complete`, {}, {
    'If-Match': etagForVersion(completedTask.version),
    'Idempotency-Key': mutationKey()
  });

  const staleReadTimestamp = new Date(Date.now() - 16 * 60 * 1000).toISOString();
  const expiredClaimReadTimestamp = new Date(Date.now() - 60 * 1000).toISOString();
  let eventReads = 0;
  await page.route('**/api/v1/**', async (route) => {
    const url = new URL(route.request().url());
    const pathname = url.pathname;
    const isEventRead = route.request().method() === 'GET' && pathname === '/api/v1/events';
    const isTaskRead = route.request().method() === 'GET' && (
      pathname === '/api/v1/my-work' ||
      /\/api\/v1\/projects\/[^/]+\/tasks$/.test(pathname) ||
      /^\/api\/v1\/tasks\/[^/]+$/.test(pathname)
    );

    if (!isEventRead && !isTaskRead) {
      await route.continue();
      return;
    }

    if (isEventRead) eventReads += 1;
    const response = await route.fetch();
    if (!response.ok()) {
      await route.fulfill({ response });
      return;
    }

    const payload = await response.json() as unknown;
    if (isTaskRead) {
      ageTaskInPayload(payload, staleTask.id, staleReadTimestamp);
      ageTaskInPayload(payload, staleWaitingTask.id, staleReadTimestamp);
      ageTaskInPayload(payload, completedTask.id, staleReadTimestamp);
      // The API has no clock-setting hook, so make this fixture's own claim
      // look expired only at the browser read boundary. This verifies that an
      // expired own claim offers Claim rather than Renew without waiting.
      expireTaskClaimInPayload(payload, staleTask.id, expiredClaimReadTimestamp);
    }
    await route.fulfill({ response, json: payload });
  });

  await page.goto(`/p/${encodeURIComponent(projectA.slug)}`);
  await expect(page.getByRole('heading', { name: projectAName, exact: true })).toBeVisible();
  const board = page.locator('section.board');
  await expect(board).toBeVisible();

  const workingCard = board.locator('.task-card').filter({ hasText: workingTask.title });
  await expect(workingCard).toBeVisible();
  const compactPulse = workingCard.locator('.agent-pulse');
  await expect(compactPulse).toContainText('Working');
  await expect(compactPulse).toContainText('Implementing the browser workflow');
  await expect(compactPulse).toContainText('1/2');

  const missingCard = board.locator('.task-card').filter({ hasText: missingTask.title });
  await expect(missingCard).toBeVisible();
  const completedCard = board.locator('.task-card').filter({ hasText: completedTask.title });
  await expect(completedCard).toBeVisible();
  await expect(completedCard.locator('.agent-pulse'), 'completed cards hide retained live-work snapshots').toHaveCount(0);
  const boardWorkFilter = page.getByLabel('Filter by agent work');
  await boardWorkFilter.selectOption('working');
  await expect(workingCard).toBeVisible();
  await expect(missingCard, 'a task without an agent pulse is not live working work').toBeHidden();
  await boardWorkFilter.selectOption('missing');
  await expect(missingCard).toBeVisible();
  await expect(missingCard.locator('.agent-pulse')).toContainText('No live pulse');
  await expect(missingCard.locator('.agent-pulse')).toHaveAttribute('aria-label', /No live pulse/);
  await expect(workingCard).toBeHidden();
  await expect(completedCard).toBeHidden();
  await boardWorkFilter.selectOption('stale');
  await expect(completedCard, 'completed work does not match the stale filter').toBeHidden();
  await boardWorkFilter.selectOption('action-needed');
  await expect(completedCard, 'completed work does not match the action-needed filter').toBeHidden();
  await boardWorkFilter.selectOption('all');

  await completedCard.locator('[data-task-trigger]').click();
  const completedDrawer = page.locator('.task-drawer');
  await expect(completedDrawer).toBeVisible();
  await expect(completedDrawer.locator('.agent-work-panel'), 'completed drawers hide live-work details').toHaveCount(0);
  await expect(completedDrawer.getByRole('button', { name: '✓ Completed', exact: true })).toBeDisabled();
  await completedDrawer.getByRole('button', { name: 'Close task details', exact: true }).click();
  await expect(completedDrawer).toBeHidden();

  await workingCard.locator('[data-task-trigger]').click();
  const drawer = page.locator('.task-drawer');
  await expect(drawer).toBeVisible();
  await expect(drawer.getByLabel('Task title')).toHaveValue(workingTask.title);

  const workPanel = drawer.locator('.agent-work-panel');
  await expect(workPanel).toBeVisible();
  await expect(workPanel.getByRole('heading', { name: /Working/ })).toBeVisible();
  await expect(workPanel.locator('.agent-work-details')).toContainText('Implementing the browser workflow');
  await expect(workPanel.locator('.agent-work-details')).toContainText('1 of 2 checkpoints (50%)');
  await expect(workPanel.locator('.agent-work-details')).toContainText('The live working pulse is visible on the board.');
  await expect(workPanel.locator('.agent-work-details')).toContainText('Wait for the remote progress event.');
  await expect(workPanel.locator('.agent-work-details dt')).toHaveText(['Phase', 'Progress', 'Summary', 'Next', 'Started', 'Updated']);
  await expect(workPanel.locator('.agent-work-details time')).toHaveCount(2);

  // A remote progress write creates task.progressed/comment.created events.
  // The draft must survive the event poll and authoritative detail refresh.
  const draftTitle = `Unsaved title draft ${suffix}`;
  const remoteSummary = `Remote progress survived the poll refresh ${suffix}.`;
  await drawer.getByLabel('Task title').fill(draftTitle);
  workingTask = await publish(
    workingTask,
    'working',
    'Remote event received',
    remoteSummary,
    'Keep the title draft until review.',
    ['api', 'poll'],
    2,
    2
  );

  await expect.poll(() => eventReads, { timeout: 30_000 }).toBeGreaterThan(1);
  await expect(drawer.locator('.agent-work-details')).toContainText(remoteSummary, { timeout: 30_000 });
  await expect(drawer.locator('.agent-work-details')).toContainText('Remote event received');
  await expect(drawer.locator('.agent-work-details')).toContainText('2 of 2 checkpoints (100%)');
  await expect(drawer.locator('.agent-work-details')).toContainText('Keep the title draft until review.');
  await expect(drawer.getByLabel('Task title')).toHaveValue(draftTitle);

  await drawer.getByRole('button', { name: 'Close task details', exact: true }).click();
  await expect(drawer).toBeHidden();
  const sidebar = page.locator('nav.sidebar');
  await sidebar.getByRole('button', { name: 'My work', exact: true }).click();
  await expect(page).toHaveURL(/\/my-work\/?$/);
  await expect(page.getByRole('heading', { name: 'Live work', exact: true })).toBeVisible();

  const sourceToggle = page.getByRole('group', { name: 'My work source' });
  const liveToggle = sourceToggle.getByRole('button', { name: 'Live', exact: true });
  const assignedToggle = sourceToggle.getByRole('button', { name: 'Assigned', exact: true });
  await expect(liveToggle).toHaveAttribute('aria-pressed', 'true');
  await expect(assignedToggle).toBeVisible();

  const liveFilters = page.getByRole('group', { name: 'Filter live work' });
  const rowFor = (group: ReturnType<typeof page.locator>, title: string) => {
    const exactTitle = new RegExp(`^${escapeRegExp(title)}$`);
    return group.locator('strong').filter({ hasText: exactTitle }).locator('xpath=ancestor::button[contains(concat(" ", normalize-space(@class), " "), " live-work-row ")][1]');
  };
  const rowIn = async (group: ReturnType<typeof page.locator>, title: string, projectName: string, columnName: string) => {
    const row = rowFor(group, title);
    await expect(row).toHaveCount(1);
    await expect(row).toBeVisible();
    await expect(row.locator('.work-project-name')).toHaveText(`${projectName} · ${columnName}`);
  };

  // Action-needed contains both waiting and stale work. Selecting the filter
  // makes its rows visible even though the default all-groups view collapses
  // the duplicate action-needed rows to avoid showing them twice.
  await liveFilters.getByRole('button', { name: /^Action needed/ }).click();
  const actionGroup = page.locator('section[aria-labelledby="action-needed-heading"]');
  await rowIn(actionGroup, waitingTask.title, projectAName, blockedA.name);
  await rowIn(actionGroup, staleTask.title, projectBName, activeB.name);
  await rowIn(actionGroup, staleWaitingTask.title, projectBName, activeB.name);
  await expect(rowFor(actionGroup, waitingTask.title).locator('.agent-pulse')).toContainText('Waiting');
  await expect(rowFor(actionGroup, staleTask.title).locator('.agent-pulse')).toHaveAttribute('aria-label', /^Stale,/);
  await expect(rowFor(actionGroup, staleWaitingTask.title).locator('.agent-pulse')).toHaveAttribute('aria-label', /^Waiting, Waiting update is stale/);

  await liveFilters.getByRole('button', { name: 'Stale', exact: true }).click();
  const staleGroup = page.locator('section[aria-labelledby="stale-heading"]');
  await rowIn(staleGroup, staleTask.title, projectBName, activeB.name);
  await rowIn(staleGroup, staleWaitingTask.title, projectBName, activeB.name);
  await expect(rowFor(staleGroup, staleTask.title).locator('.agent-pulse')).toHaveAttribute('aria-label', /^Stale,/);
  await expect(rowFor(staleGroup, staleWaitingTask.title).locator('.agent-pulse')).toHaveAttribute('aria-label', /^Waiting, Waiting update is stale/);

  // The fixture's claim is expired only in browser reads. The drawer must
  // offer a fresh claim instead of incorrectly offering claim renewal.
  await rowFor(staleGroup, staleTask.title).click();
  await expect(drawer).toBeVisible();
  await expect(drawer.getByRole('button', { name: /Claim task/ })).toHaveText(/Claim task/);
  await expect(drawer.getByRole('button', { name: /Renew claim/ })).toHaveCount(0);
  await drawer.getByRole('button', { name: 'Close task details', exact: true }).click();
  await expect(drawer).toBeHidden();

  await liveFilters.getByRole('button', { name: 'Waiting', exact: true }).click();
  const waitingGroup = page.locator('section[aria-labelledby="waiting-heading"]');
  await rowIn(waitingGroup, waitingTask.title, projectAName, blockedA.name);
  await expect(rowFor(waitingGroup, waitingTask.title).locator('.agent-pulse')).toHaveAttribute('aria-label', /^Waiting,/);

  await liveFilters.getByRole('button', { name: 'Verifying', exact: true }).click();
  const verifyingGroup = page.locator('section[aria-labelledby="verifying-heading"]');
  await rowIn(verifyingGroup, verifyingTask.title, projectBName, readyB.name);
  await expect(rowFor(verifyingGroup, verifyingTask.title).locator('.agent-pulse')).toHaveAttribute('aria-label', /^Verifying,/);

  await liveFilters.getByRole('button', { name: 'Working', exact: true }).click();
  const workingGroup = page.locator('section[aria-labelledby="working-heading"]');
  await rowIn(workingGroup, workingTask.title, projectAName, activeA.name);
  await expect(rowFor(workingGroup, workingTask.title).locator('.agent-pulse')).toHaveAttribute('aria-label', /^Working,/);

  await liveFilters.getByRole('button', { name: 'All', exact: true }).click();
  for (const heading of ['Action needed', 'Stale', 'Waiting', 'Verifying', 'Working']) {
    await expect(page.getByRole('heading', { name: heading, exact: true })).toBeVisible();
  }
  await expect(actionGroup.locator('.live-work-row:visible'), 'action-needed work is shown once in its state group').toHaveCount(0);
  await expect(rowFor(staleGroup, staleWaitingTask.title)).toHaveCount(1);
  await expect(rowFor(workingGroup, missingTask.title), 'missing pulse is excluded from Live Working').toHaveCount(0);

  // Assigned remains a supported source even after the live coordination
  // view has loaded. Switch back so the drawer is opened from live work.
  await assignedToggle.click();
  await expect(assignedToggle).toHaveAttribute('aria-pressed', 'true');
  await expect(page.locator('.work-list')).toBeVisible();
  await expect(page.locator('.work-list .work-row').filter({ hasText: workingTask.title })).toBeVisible();
  await liveToggle.click();
  await expect(liveToggle).toHaveAttribute('aria-pressed', 'true');
  await expect(page.getByRole('heading', { name: 'Live work', exact: true })).toBeVisible();

  const liveWorkingGroup = page.locator('section[aria-labelledby="working-heading"]');
  await liveWorkingGroup.locator('.live-work-row').filter({ hasText: workingTask.title }).click();
  await expect(drawer).toBeVisible();
  await expect(drawer.locator('.agent-work-panel')).toBeVisible();
  await expect(drawer.getByRole('button', { name: 'Save changes', exact: true })).toBeVisible();
  await expect(drawer.getByRole('button', { name: /Complete/ })).toBeVisible();
  await drawer.getByRole('button', { name: 'Close task details', exact: true }).click();
  await expect(drawer).toBeHidden();
  await expect(page).toHaveURL(/\/my-work\/?$/);
  await expect(assignedToggle).toBeVisible();

  // The compact mobile layout keeps status and source actions usable and
  // confines the intentionally scrollable filter strip to its own element.
  await page.setViewportSize({ width: 390, height: 844 });
  const mobileNavigation = page.getByRole('navigation', { name: 'Primary navigation' });
  await expect(mobileNavigation).toBeVisible();
  await expect(mobileNavigation.getByRole('button', { name: 'My work', exact: true })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Live work', exact: true })).toBeVisible();
  await expect(assignedToggle).toBeVisible();
  await expect(rowFor(page.locator('section[aria-labelledby="waiting-heading"]'), waitingTask.title).locator('.agent-pulse')).toBeVisible();

  await rowFor(page.locator('section[aria-labelledby="working-heading"]'), workingTask.title).click();
  await expect(drawer).toBeVisible();
  await expect(drawer.locator('.agent-work-panel')).toBeVisible();
  await expect(drawer.getByRole('button', { name: 'Save changes', exact: true })).toBeVisible();
  const dimensions = await page.evaluate(() => ({
    viewport: window.innerWidth,
    documentWidth: document.documentElement.scrollWidth,
    bodyWidth: document.body.scrollWidth
  }));
  expect(dimensions.documentWidth, 'mobile live work should not overflow horizontally').toBeLessThanOrEqual(dimensions.viewport);
  expect(dimensions.bodyWidth, 'mobile live work should not overflow horizontally').toBeLessThanOrEqual(dimensions.viewport);
  await drawer.getByRole('button', { name: 'Close task details', exact: true }).click();
  await expect(drawer).toBeHidden();
  await expect(page).toHaveURL(/\/my-work\/?$/);

  await page.goto(`/p/${encodeURIComponent(projectA.slug)}/roadmap/`);
  await expect(page).toHaveURL(new RegExp(`/p/${encodeURIComponent(projectA.slug)}/roadmap/?$`));
  await expect(page.getByRole('heading', { name: `${projectAName} progress`, exact: true })).toBeVisible();

  const roadmapLiveWork = page.locator('.roadmap-live-work');
  await expect(roadmapLiveWork.getByRole('heading', { name: 'Agent work', exact: true })).toBeVisible();
  await expect(roadmapLiveWork.getByLabel(`Open ${workingTask.key}: ${workingTask.title} activity`)).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Recent activity', exact: true })).toBeVisible();
  await expect(page.getByRole('group', { name: 'Filter recent activity' })).toBeVisible();

  await roadmapLiveWork.getByLabel(`Open ${workingTask.key}: ${workingTask.title} activity`).click();
  await expect(drawer).toBeVisible();
  await expect(page).toHaveURL(new RegExp(`/p/${encodeURIComponent(projectA.slug)}/tasks/${workingTask.key}\\?view=activity$`));
  const activityTab = drawer.getByRole('tab', { name: 'Activity', exact: true });
  const detailsTab = drawer.getByRole('tab', { name: 'Details', exact: true });
  await expect(activityTab).toHaveAttribute('aria-selected', 'true');
  await expect(drawer.getByRole('heading', { name: 'Activity', exact: true })).toBeVisible();
  await expect(drawer.locator('.task-timeline-item[data-kind="agent_progress"]').first()).toContainText(remoteSummary);

  await activityTab.focus();
  await activityTab.press('ArrowLeft');
  await expect(detailsTab).toBeFocused();
  await expect(detailsTab).toHaveAttribute('aria-selected', 'true');
  await detailsTab.press('ArrowRight');
  await expect(activityTab).toBeFocused();
  await expect(activityTab).toHaveAttribute('aria-selected', 'true');

  const timelineFilters = drawer.getByRole('group', { name: 'Filter task activity' });
  await timelineFilters.getByRole('button', { name: 'Agent', exact: true }).click();
  await expect(drawer.locator('.task-timeline-item[data-kind="agent_progress"]')).not.toHaveCount(0);
  await expect(drawer.locator('.task-timeline-item[data-kind="comment"]')).toHaveCount(0);
  await timelineFilters.getByRole('button', { name: 'All', exact: true }).click();
  await drawer.getByRole('button', { name: 'Close task details', exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`/p/${encodeURIComponent(projectA.slug)}/roadmap/?$`));

  const boardTimelineComment = `Board timeline comment ${suffix}`;
  await postJSON(request, `/api/v1/tasks/${workingTask.id}/comments`, { body: boardTimelineComment }, {
    'Idempotency-Key': mutationKey()
  });
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto(`/p/${encodeURIComponent(projectA.slug)}/timeline`);
  await expect(page).toHaveURL(new RegExp(`/p/${encodeURIComponent(projectA.slug)}/timeline/?$`));
  const boardViewTimelineTab = page.getByRole('tab', { name: 'Timeline', exact: true });
  const boardViewBoardTab = page.getByRole('tab', { name: 'Board', exact: true });
  await expect(boardViewTimelineTab).toHaveAttribute('aria-selected', 'true');
  await boardViewTimelineTab.focus();
  await boardViewTimelineTab.press('ArrowLeft');
  await expect(boardViewBoardTab).toBeFocused();
  await expect(page).toHaveURL(new RegExp(`/p/${encodeURIComponent(projectA.slug)}/?$`));
  await boardViewBoardTab.press('ArrowRight');
  await expect(boardViewTimelineTab).toBeFocused();
  await expect(page).toHaveURL(new RegExp(`/p/${encodeURIComponent(projectA.slug)}/timeline/?$`));
  const boardTimeline = page.locator('.board-timeline');
  await expect(boardTimeline.getByRole('heading', { name: 'Recent work', exact: true })).toBeVisible();
  await expect(boardTimeline.locator('.board-timeline-row').filter({ hasText: workingTask.title }).first()).toBeVisible();
  await expect(boardTimeline).not.toContainText(verifyingTask.title);

  const boardTimelineFilters = boardTimeline.getByRole('group', { name: 'Filter board timeline' });
  await boardTimelineFilters.getByRole('button', { name: 'Comments', exact: true }).click();
  await expect(boardTimeline.locator('.board-timeline-row').filter({ hasText: boardTimelineComment })).toBeVisible();
  await expect(boardTimeline.locator('.board-timeline-item[data-kind="agent_progress"]')).toHaveCount(0);
  await boardTimelineFilters.getByRole('button', { name: 'Agent', exact: true }).click();
  await expect(boardTimeline.locator('.board-timeline-row').filter({ hasText: remoteSummary })).toBeVisible();

  const liveTimelineSummary = `Board timeline refreshed live ${suffix}.`;
  workingTask = await publish(
    workingTask,
    'working',
    'Board timeline live refresh',
    liveTimelineSummary,
    'Review the board-wide history.',
    ['timeline', 'polling'],
    2,
    2
  );
  await expect(boardTimeline.locator('.board-timeline-row').filter({ hasText: liveTimelineSummary })).toBeVisible({ timeout: 30_000 });

  await boardTimelineFilters.getByRole('button', { name: 'All', exact: true }).click();
  const timelineTaskRow = boardTimeline.locator('.board-timeline-row').filter({ hasText: liveTimelineSummary });
  await timelineTaskRow.click();
  await expect(drawer).toBeVisible();
  await expect(page).toHaveURL(new RegExp(`/p/${encodeURIComponent(projectA.slug)}/tasks/${workingTask.key}\\?view=activity$`));
  await expect(drawer.getByRole('tab', { name: 'Activity', exact: true })).toHaveAttribute('aria-selected', 'true');
  await drawer.getByRole('button', { name: 'Close task details', exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`/p/${encodeURIComponent(projectA.slug)}/timeline/?$`));

  await page.setViewportSize({ width: 390, height: 844 });
  await expect(boardTimeline).toBeVisible();
  const timelineDimensions = await page.evaluate(() => ({
    viewport: window.innerWidth,
    documentWidth: document.documentElement.scrollWidth,
    bodyWidth: document.body.scrollWidth
  }));
  expect(timelineDimensions.documentWidth, 'mobile board timeline should not overflow horizontally').toBeLessThanOrEqual(timelineDimensions.viewport);
  expect(timelineDimensions.bodyWidth, 'mobile board timeline should not overflow horizontally').toBeLessThanOrEqual(timelineDimensions.viewport);
});
