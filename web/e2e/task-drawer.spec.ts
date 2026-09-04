import { expect, test, type APIRequestContext, type APIResponse } from '@playwright/test';

type Project = { id: string; key: string; name: string; slug: string };
type Column = { id: string; name: string; semantic_state: string; position: number };
type Task = {
  id: string;
  key: string;
  title: string;
  project_id: string;
  column_id: string;
  version: number;
  kind?: string;
  bug?: { severity?: string | null; resolution?: string | null } | null;
};
type Collection<T> = { data: T[]; next_cursor?: string | null };

const e2eOrigin = new URL(process.env.HELM_E2E_BASE_URL || process.env.ROADMAP_E2E_BASE_URL || 'http://127.0.0.1:18080').origin;

function mutationHeaders(version?: number, key = `task-drawer-e2e-${crypto.randomUUID()}`): Record<string, string> {
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

function collectionData<T>(payload: Collection<T> | T[]): T[] {
  return Array.isArray(payload) ? payload : payload.data;
}

function columnFor(columns: Column[], semanticState: string): Column {
  const column = columns.find((item) => item.semantic_state === semanticState);
  expect(column, `the project should have a ${semanticState} column`).toBeTruthy();
  return column as Column;
}

async function createProject(request: APIRequestContext, suffix: string): Promise<Project> {
  return json<Project>(await request.post('/api/v1/projects', {
    data: {
      key: `TD${suffix}`.slice(0, 16),
      name: `Task drawer E2E ${suffix}`,
      description: 'Task drawer acceptance fixture.'
    },
    headers: mutationHeaders()
  }), 'create task drawer project');
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

function mutateTaskPayload(value: unknown, taskID: string, update: (task: Record<string, unknown>) => void): boolean {
  if (Array.isArray(value)) return value.some((item) => mutateTaskPayload(item, taskID, update));
  if (!value || typeof value !== 'object') return false;
  const record = value as Record<string, unknown>;
  if (record.id === taskID) {
    update(record);
    return true;
  }
  return Array.isArray(record.data) && record.data.some((item) => mutateTaskPayload(item, taskID, update));
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

test.describe('task drawer safeguards', () => {
  test('keeps bug drafts safe through triage, close guards, focus, and one-scroll layouts', async ({ page, request }) => {
    test.setTimeout(120_000);
    const status = await json<{ mode?: string }>(await request.get('/api/v1/auth/status'), 'read auth status');
    expect(status.mode, 'The E2E server must run with HELM_AUTH_MODE=disabled').toBe('disabled');

    const suffix = `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 7)}`.toUpperCase();
    const project = await createProject(request, suffix);
    const columns = collectionData(await json<Collection<Column> | Column[]>(
      await request.get(`/api/v1/projects/${project.id}/columns?limit=20`),
      'list task drawer columns'
    ));
    const ready = columnFor(columns, 'ready');
    const bug = await createTask(request, project, ready, `Draft-safe bug ${suffix}`, {
      kind: 'bug',
      bug: {
        actual_behavior: 'The drawer loses context during triage.',
        expected_behavior: 'Triage keeps all draft context.',
        reproduction_steps: 'Open the bug and edit its title.',
        environment: 'Browser E2E',
        affected_version: 'drawer-test'
      }
    });

    await page.setViewportSize({ width: 1280, height: 900 });
    await page.goto(`/p/${project.slug}`);
    const board = page.locator('section.board');
    const card = board.locator('.task-card').filter({ hasText: bug.key });
    const trigger = card.locator('[data-task-trigger]');
    await expect(trigger).toBeVisible();
    await trigger.focus();
    await trigger.click();

    const drawer = page.locator('.task-drawer');
    await expect(drawer).toBeVisible();
    await expect(drawer.locator('[data-dialog-initial-focus]')).toBeFocused();
    await expect(drawer.getByLabel('Task title')).not.toBeFocused();

    const saveBar = drawer.locator('.drawer-save-bar');
    await expect(saveBar).toContainText('All changes saved');
    expect(await saveBar.evaluate((element) => getComputedStyle(element).position)).toBe('sticky');

    const editedTitle = `Triage keeps this draft ${suffix}`;
    await drawer.getByLabel('Task title').fill(editedTitle);
    await expect(saveBar).toContainText('Unsaved changes');
    await expect(saveBar.getByRole('button', { name: 'Save changes', exact: true })).toBeEnabled();
    await drawer.getByRole('button', { name: 'Triage issue', exact: true }).click();
    await expect(drawer.getByRole('button', { name: 'Update triage', exact: true })).toBeVisible();
    await expect(drawer.getByLabel('Task title')).toHaveValue(editedTitle);
    const triaged = await json<Task>(await request.get(`/api/v1/tasks/${bug.id}`), 'reload triaged bug');
    expect(triaged.title).toBe(editedTitle);

    const resolvedTitle = `Resolve keeps this draft ${suffix}`;
    await drawer.getByLabel('Task title').fill(resolvedTitle);
    await drawer.getByLabel(/Resolution note/).fill('The drawer draft survives resolution.');
    await drawer.getByRole('button', { name: 'Resolve issue', exact: true }).click();
    await expect(drawer.getByLabel('Task title')).toHaveValue(resolvedTitle);
    await expect(drawer.getByRole('button', { name: 'Reopen issue', exact: true })).toBeVisible();
    await drawer.getByLabel('Reopen reason').fill('The regression needs another look.');
    await drawer.getByRole('button', { name: 'Reopen issue', exact: true }).click();
    await expect(drawer.getByRole('button', { name: 'Resolve issue', exact: true })).toBeVisible();
    await expect(drawer.getByLabel('Task title')).toHaveValue(resolvedTitle);
    const reopened = await json<Task>(await request.get(`/api/v1/tasks/${bug.id}`), 'reload reopened bug');
    expect(reopened.title).toBe(resolvedTitle);

    // A clean drawer closes immediately and returns focus to its card trigger.
    await drawer.getByRole('button', { name: 'Close task details', exact: true }).click();
    await expect(drawer).toBeHidden();
    await expect(trigger).toBeFocused();

    // The backdrop uses the same dirty guard as the close button.
    await trigger.click();
    await expect(drawer).toBeVisible();
    const backdropTitle = `Backdrop keeps this draft ${suffix}`;
    await drawer.getByLabel('Task title').fill(backdropTitle);
    page.once('dialog', (dialog) => dialog.dismiss());
    await page.locator('.drawer-backdrop').click({ position: { x: 10, y: 10 } });
    await expect(drawer.getByLabel('Task title')).toHaveValue(backdropTitle);
    page.once('dialog', (dialog) => dialog.accept());
    await page.locator('.drawer-backdrop').click({ position: { x: 10, y: 10 } });
    await expect(drawer).toBeHidden();

    // Dirty Escape is guarded; dismissing the confirmation leaves both the
    // drawer and the draft intact, while accepting it closes normally.
    await trigger.click();
    await expect(drawer).toBeVisible();
    const dirtyTitle = `Discard confirmation ${suffix}`;
    await drawer.getByLabel('Task title').fill(dirtyTitle);
    page.once('dialog', (dialog) => dialog.dismiss());
    await page.keyboard.press('Escape');
    await expect(drawer).toBeVisible();
    await expect(drawer.getByLabel('Task title')).toHaveValue(dirtyTitle);
    page.once('dialog', (dialog) => dialog.accept());
    await page.keyboard.press('Escape');
    await expect(drawer).toBeHidden();

    // The bug report is in the drawer's main scroll region, not a nested
    // scrollbox. Check both target viewport classes and header stability.
    for (const viewport of [{ width: 1280, height: 900 }, { width: 390, height: 844 }]) {
      await page.setViewportSize(viewport);
      await trigger.click();
      await expect(drawer).toBeVisible();
      const drawerScroll = drawer.locator('[data-drawer-scroll]');
      await expect(drawerScroll).toHaveCount(1);
      await expect(drawer.locator('.drawer-activity-scroll')).toHaveCount(0);
      const scrollStyle = await drawer.locator('.drawer-bug-controls').evaluate((element) => ({
        overflowY: getComputedStyle(element).overflowY,
        maxHeight: getComputedStyle(element).maxHeight
      }));
      expect(scrollStyle.overflowY).toBe('visible');
      expect(scrollStyle.maxHeight).toBe('none');
      const headerBefore = await drawer.locator('.drawer-header').boundingBox();
      await drawerScroll.evaluate((element) => { element.scrollTop = element.scrollHeight; });
      const headerAfter = await drawer.locator('.drawer-header').boundingBox();
      expect(headerAfter?.y).toBe(headerBefore?.y);
      await drawer.getByRole('button', { name: 'Close task details', exact: true }).click();
      await expect(drawer).toBeHidden();
    }
  });

  test('shows claim lease state and preserves drafts through renew, release, block, and conflicts', async ({ page, request }) => {
    test.setTimeout(120_000);
    const status = await json<{ mode?: string }>(await request.get('/api/v1/auth/status'), 'read auth status');
    expect(status.mode, 'The E2E server must run with HELM_AUTH_MODE=disabled').toBe('disabled');

    const suffix = `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 7)}`.toUpperCase();
    const project = await createProject(request, suffix);
    const columns = collectionData(await json<Collection<Column> | Column[]>(
      await request.get(`/api/v1/projects/${project.id}/columns?limit=20`),
      'list claim columns'
    ));
    const ready = columnFor(columns, 'ready');
    let claimed = await createTask(request, project, ready, `Claim lifecycle ${suffix}`);
    let conflict = await createTask(request, project, ready, `Claim conflict ${suffix}`);

    claimed = await json<Task>(await request.post(`/api/v1/tasks/${claimed.id}/claim`, {
      data: { lease_seconds: 300 },
      headers: mutationHeaders(claimed.version)
    }), 'claim lifecycle task');
    conflict = await json<Task>(await request.post(`/api/v1/tasks/${conflict.id}/claim`, {
      data: { lease_seconds: 300 },
      headers: mutationHeaders(conflict.version)
    }), 'seed conflict task');

    await page.route('**/api/v1/**', async (route) => {
      if (route.request().method() !== 'GET') {
        await route.continue();
        return;
      }
      const response = await route.fetch();
      let payload: unknown;
      try {
        payload = await response.json();
      } catch {
        await route.fulfill({ response });
        return;
      }
      const changed = mutateTaskPayload(payload, conflict.id, (task) => {
        task.claimed_by = { id: 'foreign-agent', kind: 'agent', name: 'Foreign agent' };
        task.claim_expires_at = new Date(Date.now() + 240_000).toISOString();
      });
      if (!changed) {
        await route.fulfill({ response, json: payload });
        return;
      }
      await route.fulfill({ response, json: payload });
    });

    await page.setViewportSize({ width: 1280, height: 900 });
    await page.goto(`/p/${project.slug}`);
    const board = page.locator('section.board');
    const claimedCard = board.locator('.task-card').filter({ hasText: claimed.title });
    await expect(claimedCard.locator('.agent-pulse-claim')).toContainText('Claimed by');
    await expect(claimedCard.locator('.agent-pulse-claim')).toContainText('task version v');
    await expect(claimedCard.locator('.agent-pulse-claim')).toContainText('expiring soon');

    await claimedCard.locator('[data-task-trigger]').click();
    const drawer = page.locator('.task-drawer');
    await expect(drawer).toBeVisible();
    await expect(drawer.locator('.claim-lease')).toContainText('Task version v');
    await expect(drawer.locator('.claim-lease')).toContainText('Expires');
    await expect(drawer.locator('.claim-lease')).toContainText(/\d+m left/);
    await expect(drawer.locator('.claim-lease')).toContainText('Expiring soon');

    const draftTitle = `Draft survives claim actions ${suffix}`;
    await drawer.getByLabel('Task title').fill(draftTitle);
    await drawer.getByRole('button', { name: /Renew claim/ }).click();
    await expect(drawer.getByLabel('Task title')).toHaveValue(draftTitle);
    await drawer.getByRole('button', { name: 'Release', exact: true }).click();
    await expect(drawer.getByLabel('Task title')).toHaveValue(draftTitle);
    await drawer.getByRole('button', { name: /Claim task/ }).click();
    await expect(drawer.getByLabel('Task title')).toHaveValue(draftTitle);
    await drawer.getByRole('button', { name: 'Release', exact: true }).click();
    await expect(drawer.getByLabel('Task title')).toHaveValue(draftTitle);

    await drawer.getByRole('button', { name: '■ Block', exact: true }).click();
    const reason = `Waiting on a decision ${suffix}`;
    await drawer.getByRole('textbox', { name: 'Why is this task blocked?' }).fill(reason);
    await drawer.getByRole('button', { name: 'Block task', exact: true }).click();
    await expect(drawer.getByLabel('Task title')).toHaveValue(draftTitle);
    await drawer.getByRole('tab', { name: 'Activity', exact: true }).click();
    await expect(drawer.locator('.task-timeline')).toContainText(reason, { timeout: 20_000 });
    page.once('dialog', (dialog) => dialog.accept());
    await drawer.getByRole('button', { name: 'Close task details', exact: true }).click();
    await expect(drawer).toBeHidden();

    const conflictCard = board.locator('.task-card').filter({ hasText: conflict.title });
    await conflictCard.locator('[data-task-trigger]').click();
    await expect(drawer.locator('.claim-lease')).toHaveAttribute('role', 'alert');
    await expect(drawer.locator('.claim-lease')).toContainText('Foreign agent');
    await expect(drawer.locator('.claim-lease')).toContainText('Task version v');
    await expect(drawer.getByRole('button', { name: /Claimed by Foreign agent/ })).toBeDisabled();
    await expect(drawer.getByRole('button', { name: /Renew claim/ })).toHaveCount(0);
  });

  test('guards dirty drafts when switching tasks from My Work and the command palette', async ({ page, request }) => {
    test.setTimeout(120_000);
    const status = await json<{ mode?: string }>(await request.get('/api/v1/auth/status'), 'read auth status');
    expect(status.mode, 'The E2E server must run with HELM_AUTH_MODE=disabled').toBe('disabled');

    const suffix = `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 7)}`.toUpperCase();
    const project = await createProject(request, suffix);
    const columns = collectionData(await json<Collection<Column> | Column[]>(
      await request.get(`/api/v1/projects/${project.id}/columns?limit=20`),
      'list navigation columns'
    ));
    const ready = columnFor(columns, 'ready');
    const source = await createTask(request, project, ready, `Navigation source ${suffix}`);
    const issue = await createTask(request, project, ready, `Navigation issue ${suffix}`, {
      kind: 'bug',
      bug: {
        actual_behavior: 'The command palette can replace a draft.',
        expected_behavior: 'Task switching asks before replacing a draft.',
        reproduction_steps: 'Edit one task, then open another task.',
        environment: 'Browser E2E',
        affected_version: 'drawer-test'
      }
    });
    let work = await createTask(request, project, ready, `Navigation work ${suffix}`);
    work = await json<Task>(await request.post(`/api/v1/tasks/${work.id}/claim`, {
      data: { lease_seconds: 300 },
      headers: mutationHeaders(work.version)
    }), 'claim navigation work task');

    await page.setViewportSize({ width: 1280, height: 900 });
    await page.goto(`/p/${project.slug}`);
    const board = page.locator('section.board');
    const drawer = page.locator('.task-drawer');
    const sourceCard = board.locator('.task-card').filter({ hasText: source.key });
    await sourceCard.locator('[data-task-trigger]').click();
    await expect(drawer).toBeVisible();

    const myWorkDraft = `My Work draft ${suffix}`;
    await drawer.getByLabel('Task title').fill(myWorkDraft);
    await page.locator('nav.sidebar').getByRole('button', { name: 'My work', exact: true }).dispatchEvent('click');
    await expect(page).toHaveURL(/\/my-work\/?$/);
    const workSource = page.getByRole('group', { name: 'My work source' });
    const assignedToggle = workSource.getByRole('button', { name: 'Assigned', exact: true });
    await assignedToggle.dispatchEvent('click');
    const workRow = page.locator('.work-list .work-row').filter({ hasText: work.title });
    await expect(workRow).toBeVisible();

    const dismissMyWorkDialog = page.waitForEvent('dialog').then((dialog) => dialog.dismiss());
    await workRow.dispatchEvent('click');
    await dismissMyWorkDialog;
    await expect(drawer.getByLabel('Task title')).toHaveValue(myWorkDraft);

    const acceptMyWorkDialog = page.waitForEvent('dialog').then((dialog) => dialog.accept());
    await workRow.dispatchEvent('click');
    await acceptMyWorkDialog;
    await expect(drawer.getByLabel('Task title')).toHaveValue(work.title);

    // Keep the loaded issue collection while returning to the board so the
    // command palette can resolve the issue choice without a second fetch.
    await drawer.getByRole('button', { name: 'Close task details', exact: true }).click();
    await page.locator('nav.sidebar').getByRole('button', { name: 'Issues', exact: true }).dispatchEvent('click');
    await expect(page).toHaveURL(/\/issues\/?$/);
    await expect(page.getByRole('button', { name: new RegExp(escapeRegExp(issue.title)) })).toBeVisible();
    await page.locator('nav.sidebar').getByRole('button', { name: 'Go to current project', exact: true }).dispatchEvent('click');
    await expect(page).toHaveURL(new RegExp(`/p/${escapeRegExp(project.slug)}/?$`));
    await sourceCard.locator('[data-task-trigger]').click();
    await expect(drawer).toBeVisible();
    const commandDraft = `Command palette draft ${suffix}`;
    await drawer.getByLabel('Task title').fill(commandDraft);
    await page.keyboard.press('Control+k');
    const commandDialog = page.getByRole('dialog', { name: 'Search Helm' });
    await expect(commandDialog).toBeVisible();
    const commandInput = commandDialog.getByRole('combobox', { name: 'Search projects and views, tasks, issues, and actions' });
    await commandInput.fill(issue.title);
    const issueChoice = commandDialog.getByRole('option', { name: new RegExp(escapeRegExp(issue.title)) });
    await expect(issueChoice).toBeVisible();
    const dismissCommandDialog = page.waitForEvent('dialog').then((dialog) => dialog.dismiss());
    await issueChoice.click();
    await dismissCommandDialog;
    await expect(drawer.getByLabel('Task title')).toHaveValue(commandDraft);

    await page.keyboard.press('Control+k');
    const secondCommandDialog = page.getByRole('dialog', { name: 'Search Helm' });
    await secondCommandDialog.getByRole('combobox', { name: 'Search projects and views, tasks, issues, and actions' }).fill(issue.title);
    const secondIssueChoice = secondCommandDialog.getByRole('option', { name: new RegExp(escapeRegExp(issue.title)) });
    await expect(secondIssueChoice).toBeVisible();
    const acceptCommandDialog = page.waitForEvent('dialog').then((dialog) => dialog.accept());
    await secondIssueChoice.click();
    await acceptCommandDialog;
    await expect(drawer.getByLabel('Task title')).toHaveValue(issue.title);
  });

  test('hydrates clean drawer drafts from polling while preserving dirty drafts', async ({ page, request }) => {
    test.setTimeout(120_000);
    const status = await json<{ mode?: string }>(await request.get('/api/v1/auth/status'), 'read auth status');
    expect(status.mode, 'The E2E server must run with HELM_AUTH_MODE=disabled').toBe('disabled');

    const suffix = `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 7)}`.toUpperCase();
    const project = await createProject(request, suffix);
    const columns = collectionData(await json<Collection<Column> | Column[]>(
      await request.get(`/api/v1/projects/${project.id}/columns?limit=20`),
      'list polling columns'
    ));
    const ready = columnFor(columns, 'ready');
    const task = await createTask(request, project, ready, `Polling source ${suffix}`);

    // Liveness refreshes are intentionally infrequent in production. Shorten
    // only that interval in this browser so the test exercises the real
    // refreshDrawerTask path without waiting a minute.
    await page.addInitScript(() => {
      const nativeSetInterval = window.setInterval.bind(window);
      window.setInterval = ((handler: TimerHandler, timeout?: number, ...args: any[]) =>
        nativeSetInterval(handler, timeout === 60_000 ? 100 : timeout, ...args)) as typeof window.setInterval;
    });
    let refreshTitle = '';
    await page.route('**/api/v1/tasks/**', async (route) => {
      const url = new URL(route.request().url());
      if (
        route.request().method() !== 'GET'
        || url.pathname !== `/api/v1/tasks/${task.id}`
        || !refreshTitle
      ) {
        await route.continue();
        return;
      }
      const response = await route.fetch();
      const payload = await response.json() as Record<string, unknown>;
      payload.title = refreshTitle;
      await route.fulfill({ response, json: payload });
    });

    await page.setViewportSize({ width: 1280, height: 900 });
    await page.goto(`/p/${project.slug}`);
    const board = page.locator('section.board');
    const card = board.locator('.task-card').filter({ hasText: task.key });
    await card.locator('[data-task-trigger]').click();
    const drawer = page.locator('.task-drawer');
    await expect(drawer).toBeVisible();
    await expect(drawer.getByLabel('Task title')).toHaveValue(task.title);

    const cleanRefreshTitle = `Authoritative clean refresh ${suffix}`;
    refreshTitle = cleanRefreshTitle;
    await expect(drawer.getByLabel('Task title')).toHaveValue(cleanRefreshTitle, { timeout: 10_000 });
    await expect(drawer.locator('.drawer-save-bar')).toContainText('All changes saved');

    const dirtyDraftTitle = `Local dirty draft ${suffix}`;
    await drawer.getByLabel('Task title').fill(dirtyDraftTitle);
    const dirtyRefreshTitle = `Authoritative dirty refresh ${suffix}`;
    refreshTitle = dirtyRefreshTitle;
    await expect(drawer.getByLabel('Task title')).toHaveValue(dirtyDraftTitle, { timeout: 10_000 });
    await expect(drawer.locator('.drawer-save-bar')).toContainText('Unsaved changes');
  });
});
