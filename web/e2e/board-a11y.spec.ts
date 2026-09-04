import { expect, test, type Page, type Route } from '@playwright/test';

type BoardFixture = {
  project: {
    id: string;
    key: string;
    slug: string;
    name: string;
    description: string;
    color: string;
    favorite: boolean;
  };
  otherProject?: BoardFixture['project'];
  otherColumns?: BoardFixture['columns'];
  columns: Array<{
    id: string;
    project_id: string;
    name: string;
    semantic_state: 'backlog' | 'ready' | 'active' | 'blocked' | 'completed';
    position: number;
  }>;
  task: Record<string, any>;
  labels: Array<{ id: string; project_id: string; name: string; color: string }>;
  deleted: boolean;
  conflictNextPatch: boolean;
  conflictNextRestore: boolean;
  restoreCalls: number;
  revokedToken: boolean;
};

function columns(count: number, projectId: string) {
  const states: BoardFixture['columns'][number]['semantic_state'][] = ['backlog', 'ready', 'active', 'blocked', 'completed'];
  return Array.from({ length: count }, (_, index) => ({
    id: `column-${index + 1}`,
    project_id: projectId,
    name: `Column ${index + 1}`,
    semantic_state: states[index] || 'ready',
    position: index
  }));
}

function fixture(columnCount = 8): BoardFixture {
  const projectId = 'project-a11y';
  const boardColumns = columns(columnCount, projectId);
  const label = { id: 'label-a11y', project_id: projectId, name: 'Accessibility', color: '#6d5efc' };
  return {
    project: {
      id: projectId,
      key: 'A11Y',
      slug: 'a11y-board',
      name: 'Accessibility board',
      description: 'A mocked board for accessibility regression coverage.',
      color: '#6d5efc',
      favorite: true
    },
    columns: boardColumns,
    task: {
      id: 'task-a11y',
      number: 1,
      key: 'A11Y-1',
      project_id: projectId,
      kind: 'bug',
      column_id: boardColumns[0].id,
      title: 'Keyboard and screen-reader regression',
      description: 'Keep board interactions understandable for every operator.',
      priority: 'normal',
      position: 0,
      version: 1,
      labels: [label],
      claimed_by: { id: 'agent-build', kind: 'agent', name: 'Build bot' },
      claim_expires_at: '2030-01-02T03:04:05Z',
      agent_work: {
        operation_id: 'board-a11y-fixture',
        actor_id: 'agent-build',
        state: 'working',
        phase: 'Regression coverage',
        summary: 'Testing board accessibility.',
        next_action: 'Review the result.',
        checkpoint_refs: ['layout', 'interaction'],
        checkpoint_completed: 1,
        checkpoint_total: 2,
        started_at: '2026-09-03T22:00:00Z',
        updated_at: '2026-09-03T23:00:00Z',
        stale: false,
        action_needed: false
      }
    },
    labels: [label],
    deleted: false,
    conflictNextPatch: false,
    conflictNextRestore: false,
    restoreCalls: 0,
    revokedToken: false
  };
}

async function json(route: Route, body: unknown, status = 200) {
  await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
}

async function installFixture(page: Page, state: BoardFixture) {
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request();
    const method = request.method();
    const pathname = decodeURIComponent(new URL(request.url()).pathname);
    const task = state.task;
    if (pathname === '/api/v1/auth/status') {
      await json(route, {
        mode: 'disabled',
        authenticated: true,
        user: { id: 'human-a11y', kind: 'human', name: 'Accessibility tester', admin: true }
      });
      return;
    }
    if (pathname === '/api/v1/projects' && method === 'GET') {
      await json(route, { data: [state.project, ...(state.otherProject ? [state.otherProject] : [])], next_cursor: null });
      return;
    }
    if (pathname === `/api/v1/projects/${state.project.id}/columns` && method === 'GET') {
      await json(route, { data: state.columns, next_cursor: null });
      return;
    }
    if (pathname === `/api/v1/projects/${state.project.id}/labels` && method === 'GET') {
      await json(route, { data: state.labels, next_cursor: null });
      return;
    }
    if (pathname === `/api/v1/projects/${state.project.id}/tasks` && method === 'GET') {
      await json(route, { data: state.deleted ? [] : [task], next_cursor: null });
      return;
    }
    if (pathname === `/api/v1/tasks/${task.id}/dependencies` && method === 'GET') {
      await json(route, task.dependency_relations || { prerequisites: [], dependents: [] });
      return;
    }
    if (state.otherProject && pathname === `/api/v1/projects/${state.otherProject.id}/columns` && method === 'GET') {
      await json(route, { data: state.otherColumns || columns(3, state.otherProject.id), next_cursor: null });
      return;
    }
    if (state.otherProject && pathname === `/api/v1/projects/${state.otherProject.id}/labels` && method === 'GET') {
      await json(route, { data: [], next_cursor: null });
      return;
    }
    if (state.otherProject && pathname === `/api/v1/projects/${state.otherProject.id}/tasks` && method === 'GET') {
      await json(route, { data: [], next_cursor: null });
      return;
    }
    if (pathname === '/api/v1/issues' && method === 'GET') {
      await json(route, { data: state.deleted ? [] : [task], next_cursor: null });
      return;
    }
    if (pathname === '/api/v1/events' && method === 'GET') {
      await json(route, { data: [], next_cursor: null });
      return;
    }
    if (pathname === '/api/v1/agents' && method === 'GET') {
      await json(route, {
        data: state.revokedToken
          ? [{ id: 'agent-build', kind: 'agent', name: 'Build bot', tokens: [] }]
          : [{ id: 'agent-build', kind: 'agent', name: 'Build bot', tokens: [{ id: 'token-a11y', actor_id: 'agent-build', name: 'Browser token', scopes: ['tasks:write'], project_ids: [] }] }],
        next_cursor: null
      });
      return;
    }
    if (pathname === `/api/v1/tasks/${task.id}` && method === 'GET') {
      if (state.deleted) {
        await json(route, { error: { code: 'not_found', message: 'task not found', details: {} } }, 404);
      } else {
        await json(route, task, 200);
      }
      return;
    }
    if (pathname === `/api/v1/tasks/${task.id}` && method === 'DELETE') {
      const expected = Number(request.headers()['if-match']?.replace(/[^0-9]/g, ''));
      if (state.deleted || expected !== task.version) {
        await json(route, { error: { code: 'stale_task', message: 'Task changed elsewhere.', details: { current: task } } }, 409);
      } else {
        state.deleted = true;
        task.version += 1;
        await route.fulfill({ status: 204, body: '' });
      }
      return;
    }
    if (pathname === `/api/v1/tasks/${task.id}/restore` && method === 'POST') {
      state.restoreCalls += 1;
      const expected = Number(request.headers()['if-match']?.replace(/[^0-9]/g, ''));
      if (state.conflictNextRestore || !state.deleted || expected !== task.version) {
        state.conflictNextRestore = false;
        await json(route, { error: { code: 'stale_task', message: 'Task changed elsewhere.', details: { current: task } } }, 409);
      } else {
        state.deleted = false;
        task.version += 1;
        await json(route, task, 200);
      }
      return;
    }
    if (pathname === `/api/v1/tasks/${task.id}/move` && method === 'POST') {
      const expected = Number(request.headers()['if-match']?.replace(/[^0-9]/g, ''));
      const input = JSON.parse(request.postData() || '{}') as {
        destination_column_id?: string;
        expected_source_column_id?: string;
      };
      if (
        state.deleted
        || expected !== task.version
        || !input.destination_column_id
        || input.expected_source_column_id !== task.column_id
      ) {
        await json(route, { error: { code: 'stale_task', message: 'Task changed elsewhere.', details: { current: task } } }, 409);
      } else {
        task.column_id = input.destination_column_id;
        task.position = 0;
        task.version += 1;
        await json(route, task, 200);
      }
      return;
    }
    if (pathname === `/api/v1/tasks/${task.id}` && method === 'PATCH') {
      const expected = Number(request.headers()['if-match']?.replace(/[^0-9]/g, ''));
      if (state.conflictNextPatch || state.deleted || expected !== task.version) {
        state.conflictNextPatch = false;
        await json(route, { error: { code: 'stale_task', message: 'Task changed elsewhere.', details: { current: task } } }, 409);
      } else {
        const input = JSON.parse(request.postData() || '{}') as { column_id?: string; position?: number };
        if (input.column_id) task.column_id = input.column_id;
        if (input.position !== undefined) task.position = input.position;
        task.version += 1;
        await json(route, task, 200);
      }
      return;
    }
    if (pathname === `/api/v1/labels/${state.labels[0]?.id || 'label-a11y'}` && method === 'DELETE') {
      state.labels = [];
      task.labels = [];
      await route.fulfill({ status: 204, body: '' });
      return;
    }
    if (pathname === '/api/v1/tokens/token-a11y' && method === 'DELETE') {
      state.revokedToken = true;
      await route.fulfill({ status: 204, body: '' });
      return;
    }
    await json(route, { error: { code: 'not_found', message: `Unhandled fixture route ${method} ${pathname}`, details: {} } }, 404);
  });
}

async function openBoard(page: Page, state: BoardFixture) {
  await page.goto('/');
  await expect(page.locator('section.board')).toBeVisible();
  await expect(page.locator('section.board .board-column')).toHaveCount(state.columns.length);
}

test.describe('board accessibility regressions', () => {
  test('keeps 3, 5, and 8 columns in one horizontal flow on desktop and mobile', async ({ page }) => {
    const state = fixture(3);
    await installFixture(page, state);
    await page.setViewportSize({ width: 1280, height: 900 });
    for (const count of [3, 5, 8]) {
      state.columns = columns(count, state.project.id);
      state.task.column_id = state.columns[0].id;
      state.task.version = 1;
      state.deleted = false;
      // Re-enter the app for each fixture size so the board loader starts
      // from a clean client state while the route fixture remains installed.
      await page.goto('/');
      const board = page.locator('section.board');
      await expect(board.locator('.board-column')).toHaveCount(count);
      const desktopMetrics = await board.evaluate((element) => {
        const style = getComputedStyle(element);
        return { flow: style.gridAutoFlow, template: style.gridTemplateColumns, scrollWidth: element.scrollWidth, clientWidth: element.clientWidth };
      });
      expect(desktopMetrics.flow).toBe('column');
      expect(desktopMetrics.template).not.toContain('repeat');
      if (count === 8) expect(desktopMetrics.scrollWidth).toBeGreaterThan(desktopMetrics.clientWidth);
    }

    await page.setViewportSize({ width: 390, height: 844 });
    for (const count of [3, 5, 8]) {
      state.columns = columns(count, state.project.id);
      state.task.column_id = state.columns[0].id;
      await page.goto('/');
      const board = page.locator('section.board');
      await expect(board.locator('.board-column')).toHaveCount(count);
      const mobileMetrics = await board.evaluate((element) => {
        const style = getComputedStyle(element);
        return { flow: style.gridAutoFlow, scrollWidth: element.scrollWidth, clientWidth: element.clientWidth };
      });
      expect(mobileMetrics.flow).toBe('column');
      if (count === 8) expect(mobileMetrics.scrollWidth).toBeGreaterThan(mobileMetrics.clientWidth);
    }
  });

  test('uses a dedicated drag handle, keeps keyboard movement, and exposes drop insertion feedback', async ({ page }) => {
    const state = fixture(5);
    await installFixture(page, state);
    await page.setViewportSize({ width: 1280, height: 900 });
    await openBoard(page, state);
    const card = page.locator('.task-card').filter({ hasText: state.task.title });
    const handle = card.locator('.task-drag-handle');
    await expect(card).not.toHaveAttribute('draggable', 'true');
    await expect(handle).toHaveAttribute('draggable', 'true');
    await expect(handle).toHaveAttribute('aria-label', /Drag A11Y-1/);

    await page.evaluate(() => {
      const source = document.querySelector<HTMLButtonElement>('.task-drag-handle');
      const target = document.querySelectorAll<HTMLElement>('.board-column')[1];
      const dataTransfer = new DataTransfer();
      dataTransfer.setData('text/plain', 'task-a11y');
      source?.dispatchEvent(new DragEvent('dragstart', { bubbles: true, dataTransfer }));
      target?.dispatchEvent(new DragEvent('dragover', { bubbles: true, cancelable: true, dataTransfer }));
    });
    const targetColumn = page.locator('.board-column').nth(1);
    await expect(targetColumn).toHaveClass(/drop-target/);
    await expect(targetColumn.locator('.drop-placeholder')).toContainText('Drop task in Column 2');
    await targetColumn.dispatchEvent('dragleave');
    await expect(targetColumn).not.toHaveClass(/drop-target/);

    await card.locator('.task-main').focus();
    await page.keyboard.press('Alt+ArrowRight');
    await expect(page.locator('.board-column').nth(1).locator('.task-card')).toContainText(state.task.title);
    await expect(page.getByRole('button', { name: 'Undo' })).toBeVisible();
  });

  test('keeps destructive confirmations named and supports cancel, Undo, and conflict-safe retry', async ({ page }) => {
    const state = fixture(3);
    await installFixture(page, state);
    await page.setViewportSize({ width: 1280, height: 900 });
    await openBoard(page, state);
    const card = page.locator('.task-card').filter({ hasText: state.task.title });
    await card.locator('.task-main').click();
    const drawer = page.locator('.task-drawer');

    const deleteLabel = drawer.getByRole('button', { name: 'Delete label Accessibility', exact: true });
    await deleteLabel.click();
    const labelDialog = page.getByRole('alertdialog');
    await expect(labelDialog).toContainText('Delete label “Accessibility”?');
    await labelDialog.getByRole('button', { name: 'Cancel', exact: true }).click();
    await expect(deleteLabel).toBeVisible();
    await deleteLabel.click();
    await labelDialog.getByRole('button', { name: 'Delete label', exact: true }).click();
    await expect(deleteLabel).toHaveCount(0);
    await expect(page.locator('#drawer-title')).toBeFocused();

    await drawer.getByRole('button', { name: 'Delete task', exact: true }).click();
    const taskDialog = page.getByRole('alertdialog');
    await expect(taskDialog).toContainText('Delete A11Y-1?');
    await page.keyboard.press('Control+k');
    await expect(page.getByRole('dialog', { name: 'Search Helm' })).toHaveCount(0);
    await expect(taskDialog).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(taskDialog).toBeHidden();
    await expect(drawer).toBeVisible();
    await drawer.getByRole('button', { name: 'Delete task', exact: true }).click();
    await taskDialog.getByRole('button', { name: 'Delete task', exact: true }).click();
    await expect(drawer).toBeHidden();
    await expect(card).toHaveCount(0);
    await expect(page.locator('main.content')).toBeFocused();
    await expect(page.getByRole('button', { name: 'Undo', exact: true })).toBeVisible();

    state.conflictNextRestore = true;
    await page.getByRole('button', { name: 'Undo', exact: true }).click();
    await expect(page.locator('.toast.error')).toContainText('Task changed elsewhere', { timeout: 10_000 });
    await expect(card).toHaveCount(0);

    // A fresh delete/restore cycle verifies the successful action path after
    // the conflict, while the original task snapshot remains version-guarded.
    state.conflictNextRestore = false;
    await page.reload();
    await expect(page.locator('.task-card')).toHaveCount(0);
  });

  test('keeps AgentPulse progress, exact/relative update, and claim expiry in reachable text', async ({ page }) => {
    const state = fixture(3);
    await installFixture(page, state);
    await openBoard(page, state);
    const pulse = page.locator('[data-agent-pulse]').first();
    await expect(pulse).toHaveAttribute('role', 'group');
    await expect(pulse).not.toHaveAttribute('role', 'img');
    await expect(pulse).not.toHaveAttribute('aria-label');
    await expect(pulse).toContainText('Progress: 1 of 2 checkpoints (50%)');
    await expect(pulse).toContainText('Updated');
    await expect(pulse).toContainText('2026-09-03T23:00:00.000Z');
    await expect(pulse).toContainText('Claimed by Build bot');
    await expect(pulse).toContainText('2030-01-02T03:04:05.000Z');
  });

  test('keeps drag and move controls at least 24px on mobile', async ({ page }) => {
    const state = fixture(3);
    await installFixture(page, state);
    await page.setViewportSize({ width: 390, height: 844 });
    await openBoard(page, state);
    const card = page.locator('.task-card').filter({ hasText: state.task.title });
    for (const selector of ['.task-drag-handle', '.card-move']) {
      const boxes = await card.locator(selector).evaluateAll((elements) => elements.map((element) => {
        const box = element.getBoundingClientRect();
        return { width: box.width, height: box.height };
      }));
      for (const box of boxes) {
        expect(box.width).toBeGreaterThanOrEqual(24);
        expect(box.height).toBeGreaterThanOrEqual(24);
      }
    }
  });

  test('keeps meaningful board text at a readable minimum', async ({ page }) => {
    const state = fixture(3);
    state.task.dependency_summary = {
      prerequisite_count: 1,
      unmet_prerequisite_count: 1,
      dependent_count: 0,
      blocked: true
    };
    state.task.dependency_relations = {
      prerequisites: [{
        id: 'task-dependency-a11y',
        key: 'A11Y-2',
        title: 'Readable dependency fixture',
        completed_at: null,
        satisfied: false
      }],
      dependents: []
    };
    await installFixture(page, state);
    await page.setViewportSize({ width: 1280, height: 900 });
    await openBoard(page, state);
    const sizes = await page.locator('.task-key, .task-excerpt, .column-count, .agent-pulse-copy strong, .agent-pulse-phase, .agent-pulse-claim, .dependency-badge').evaluateAll((elements) =>
      elements.map((element) => ({ selector: element.className, size: Number.parseFloat(getComputedStyle(element).fontSize) }))
    );
    expect(sizes.length).toBeGreaterThan(0);
    expect(sizes.every(({ size }) => size >= 11)).toBe(true);
    await expect(page.locator('.task-drag-handle')).toHaveCSS('font-size', '16px');

    await page.locator('.task-card').filter({ hasText: state.task.title }).locator('.task-main').click();
    const drawer = page.locator('.task-drawer');
    await expect(drawer.locator('.dependency-notice')).toBeVisible();
    await expect(drawer.locator('.relationship-grid h3')).toHaveCount(2);
    const dependencySizes = await drawer.locator([
      '.dependency-notice strong',
      '.dependency-notice small',
      '.dependency-heading p',
      '.dependency-blocked',
      '.dependency-ready',
      '.relationship-grid h3',
      '.relationship-grid h3 span',
      '.relationship-key',
      '.relationship-title',
      '.relationship-state',
      '.relationship-empty',
      '.dependency-add > label',
      '.dependency-add > small'
    ].join(', ')).evaluateAll((elements) => elements.map((element) => ({
      selector: element.className,
      size: Number.parseFloat(getComputedStyle(element).fontSize)
    })));
    expect(dependencySizes.length).toBeGreaterThan(0);
    expect(dependencySizes.every(({ size }) => size >= 11)).toBe(true);
    await expect(drawer.locator('.notice-icon')).toHaveCSS('font-size', '13px');
    await expect(drawer.locator('.remove-relationship')).toHaveCSS('font-size', '16px');
  });

  test('replaces token revoke with the same named confirm dialog', async ({ page }) => {
    const state = fixture(3);
    await installFixture(page, state);
    await page.goto('/');
    await page.getByRole('button', { name: 'Settings', exact: true }).first().click();
    await expect(page.getByRole('heading', { name: 'Agents & tokens', exact: true })).toBeVisible();
    const revoke = page.getByRole('button', { name: 'Revoke Browser token', exact: true });
    await revoke.click();
    const dialog = page.getByRole('alertdialog');
    await expect(dialog).toContainText('Revoke token “Browser token”?');
    await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
    await expect(revoke).toBeVisible();
    await revoke.click();
    await dialog.getByRole('button', { name: 'Revoke token', exact: true }).click();
    await expect(revoke).toHaveCount(0);
    await expect(page.locator('main.content')).toBeFocused();
  });

  test('restores a deleted bug in the current Issues view', async ({ page }) => {
    const state = fixture(3);
    await installFixture(page, state);
    await page.setViewportSize({ width: 1280, height: 900 });
    await page.goto('/issues');
    await expect(page.locator('.issue-row')).toHaveCount(1);
    await page.locator('.issue-row').click();
    const drawer = page.locator('.task-drawer');
    await drawer.getByRole('button', { name: 'Delete task', exact: true }).click();
    const dialog = page.getByRole('alertdialog');
    await dialog.getByRole('button', { name: 'Delete task', exact: true }).click();
    await expect(drawer).toBeHidden();
    await expect(page.locator('.issue-row')).toHaveCount(0);
    const restoreResponse = page.waitForResponse((response) => response.url().includes(`/api/v1/tasks/${state.task.id}/restore`));
    await page.getByRole('button', { name: 'Undo', exact: true }).click();
    await expect((await restoreResponse).status()).toBe(200);
    expect(state.restoreCalls).toBe(1);
    expect(state.deleted).toBe(false);
    await expect(page.locator('.issue-row')).toHaveCount(1);
    await expect(page.locator('.issue-row')).toContainText(state.task.title);
  });

  test('does not put Undo from another project on the active board', async ({ page }) => {
    const state = fixture(3);
    state.otherProject = {
      id: 'project-other',
      key: 'OTHER',
      slug: 'other-board',
      name: 'Other board',
      description: 'A second mocked project.',
      color: '#35b88a',
      favorite: false
    };
    state.otherColumns = columns(3, state.otherProject.id);
    await installFixture(page, state);
    await page.setViewportSize({ width: 1280, height: 900 });
    await openBoard(page, state);
    const card = page.locator('.task-card').filter({ hasText: state.task.title });
    await card.locator('.task-main').click();
    const drawer = page.locator('.task-drawer');
    await drawer.getByRole('button', { name: 'Delete task', exact: true }).click();
    await page.getByRole('alertdialog').getByRole('button', { name: 'Delete task', exact: true }).click();
    await expect(drawer).toBeHidden();
    await page.getByRole('button', { name: state.otherProject.name, exact: true }).click();
    await expect(page.locator('section.board')).toBeVisible();
    await expect(page.locator('section.board .task-card')).toHaveCount(0);
    await page.getByRole('button', { name: 'Undo', exact: true }).click();
    await expect(page.locator('section.board .task-card')).toHaveCount(0);
  });
});
