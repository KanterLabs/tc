import { expect, test } from '@playwright/test';

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

test.describe('Helm workspace', () => {
  test('renders the Helm identity and preserves retained browser preferences', async ({ page, request }) => {
    await page.addInitScript(() => {
      localStorage.setItem('roadmap.theme', 'dark');
      localStorage.removeItem('helm.theme');
    });
    await page.emulateMedia({ reducedMotion: 'reduce' });
    await page.goto('/');

    await expect(page).toHaveTitle('Helm');
    await expect(page.locator('meta[name="description"]')).toHaveAttribute('content', /Helm/);
    await expect(page.locator('link[rel="icon"]')).toHaveAttribute('href', '/favicon.svg');
    await expect(page.getByRole('navigation', { name: 'Primary navigation' })).toBeVisible();
    await expect(page.locator('svg.helm-mark').filter({ visible: true }).first()).toBeVisible();
    await expect(page.locator('.app-shell')).toHaveClass(/dark-mode/);

    const preferences = await page.evaluate(() => ({
      canonical: localStorage.getItem('helm.theme'),
      retained: localStorage.getItem('roadmap.theme')
    }));
    expect(preferences).toEqual({ canonical: 'dark', retained: 'dark' });
    expect(await page.locator('.new-project-button').evaluate((element) => getComputedStyle(element).transitionDuration)).toBe('0s');

    for (const asset of ['/favicon.svg', '/helm-mark.svg']) {
      const response = await request.get(asset);
      expect(response.status()).toBe(200);
      expect(response.headers()['content-type']).toContain('image/svg+xml');
    }
    expect((await request.get('/assets/missing-helm-asset.svg')).status()).toBe(404);
  });

  test('loads in disabled-auth mode and manages a task from the board', async ({ page, request }) => {
    const statusResponse = await request.get('/api/v1/auth/status');
    expect(statusResponse.ok()).toBeTruthy();
    const status = (await statusResponse.json()) as { mode?: string };
    expect(status.mode, 'The E2E server must run with HELM_AUTH_MODE=disabled').toBe('disabled');

    const runId = `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 7)}`.toUpperCase();
    const projectName = `Browser E2E ${runId}`;
    const projectKey = `E2E${runId}`.slice(0, 16);
    const taskTitle = `Quick task ${runId}`;
    const editedTaskTitle = `Edited task ${runId}`;
    const labelName = `Browser label ${runId}`;

    await page.goto('/');
    await expect(page.getByRole('navigation', { name: 'Primary navigation' })).toBeVisible();

    // Create a unique project so this workflow is repeatable against a
    // persistent development database that may already contain demo data.
    await page.getByRole('button', { name: 'New project', exact: true }).click();
    const projectDialog = page.getByRole('dialog', { name: 'Create a project' });
    await expect(projectDialog).toBeVisible();
    await expect(projectDialog.getByLabel('Project name')).toBeFocused();
    await projectDialog.getByRole('button', { name: 'Close', exact: true }).focus();
    await page.keyboard.press('Shift+Tab');
    await expect(projectDialog.getByRole('button', { name: 'Cancel', exact: true })).toBeFocused();
    await page.keyboard.press('Tab');
    await expect(projectDialog.getByRole('button', { name: 'Close', exact: true })).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(projectDialog).toBeHidden();
    await expect(page.getByRole('button', { name: 'New project', exact: true })).toBeFocused();

    await page.getByRole('button', { name: 'New project', exact: true }).click();
    await expect(projectDialog).toBeVisible();
    await projectDialog.getByLabel('Project name').fill(projectName);
    await projectDialog.getByLabel('Project key').fill(projectKey);
    await projectDialog.getByRole('button', { name: 'Create project', exact: true }).click();
    await expect(projectDialog).toBeHidden();
    await expect(page.getByRole('heading', { name: projectName, exact: true })).toBeVisible();

    const board = page.locator('section.board');
    await expect(board).toBeVisible();
    const backlogColumn = board.locator('.board-column').filter({
      has: page.getByRole('heading', { name: 'Backlog', exact: true })
    });
    const readyColumn = board.locator('.board-column').filter({
      has: page.getByRole('heading', { name: 'Ready', exact: true })
    });
    await expect(backlogColumn).toBeVisible();
    await expect(readyColumn).toBeVisible();

    // Quick-add through the column's user-facing trigger and form.
    await backlogColumn.getByRole('button', { name: /Add task$/ }).click();
    await backlogColumn.getByRole('textbox', { name: 'New task in Backlog' }).fill(taskTitle);
    await backlogColumn.getByRole('button', { name: 'Add task', exact: true }).click();

    const createdCard = board.locator('.task-card').filter({ hasText: taskTitle });
    await expect(createdCard).toBeVisible();
    const taskKey = (await createdCard.locator('.task-key').textContent())?.trim();
    expect(taskKey, 'The created task should expose its stable task key').toMatch(/^[A-Z0-9_-]+-\d+$/);

    // Move with the accessible next-column action, then verify the card's
    // rendered column changed after the API mutation completed.
    const moveNext = createdCard.getByRole('button', {
      name: `Move ${taskKey} to next column`
    });
    await expect(moveNext).toBeEnabled();
    await moveNext.click();
    await expect(readyColumn.locator('.task-card').filter({ hasText: taskTitle })).toBeVisible();
    await expect(backlogColumn.locator('.task-card').filter({ hasText: taskTitle })).toHaveCount(0);

    const movedCard = readyColumn.locator('.task-card').filter({ hasText: taskKey as string });
    await expect(movedCard).toBeVisible();

    // Open the task with its accessible card button and edit the labeled
    // title/priority controls in the task drawer.
    await movedCard
      .getByRole('button', {
        name: new RegExp(`${escapeRegExp(taskKey as string)}.*${escapeRegExp(taskTitle)}`)
      })
      .click();
    const drawer = page.locator('.task-drawer');
    await expect(drawer).toBeVisible();
    await expect(drawer.getByLabel('Task title')).toHaveValue(taskTitle);
    await expect(drawer.getByLabel('Task title')).toBeFocused();
    await drawer.getByRole('button', { name: 'Close task details', exact: true }).focus();
    await page.keyboard.press('Shift+Tab');
    await expect(drawer.getByRole('button', { name: 'Delete task', exact: true })).toBeFocused();
    await page.keyboard.press('Tab');
    await expect(drawer.getByRole('button', { name: 'Close task details', exact: true })).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(drawer).toBeHidden();
    await movedCard
      .getByRole('button', {
        name: new RegExp(`${escapeRegExp(taskKey as string)}.*${escapeRegExp(taskTitle)}`)
      })
      .click();
    await expect(drawer).toBeVisible();
    await drawer.getByLabel('Task title').fill(editedTaskTitle);
    await drawer.getByLabel('Priority').selectOption('high');
    await drawer.getByLabel(/Labels/).fill(labelName);
    await drawer.getByRole('button', { name: 'Save changes', exact: true }).click();

    await expect(movedCard).toContainText(editedTaskTitle);
    await expect(movedCard.locator('.label-chip')).toHaveText(labelName);

    const deleteLabel = drawer.getByRole('button', { name: `Delete label ${labelName}`, exact: true });
    await expect(deleteLabel).toBeVisible();
    page.once('dialog', (dialog) => dialog.accept());
    await deleteLabel.click();
    await expect(deleteLabel).toHaveCount(0);
    await expect(movedCard.locator('.label-chip')).toHaveCount(0);

    page.once('dialog', (dialog) => dialog.accept());
    await drawer.getByRole('button', { name: 'Delete task', exact: true }).click();
    await expect(drawer).toBeHidden();
    await expect(board.locator('.task-card').filter({ hasText: taskKey as string })).toHaveCount(0);

    // The project progress view is a real API-backed route. At this point the
    // task has been deleted, so the project should report no remaining work.
    await page.getByRole('button', { name: 'Progress', exact: true }).click();
    await expect(page).toHaveURL(/\/p\/[^/]+\/roadmap\/?$/);
    await expect(page.getByRole('heading', { name: `${projectName} progress`, exact: true })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Project progress', exact: true })).toBeVisible();
    await expect(page.getByText('0 total tasks', { exact: true })).toBeVisible();
    await expect(page.getByText('0 completed', { exact: true })).toBeVisible();

    const commandTrigger = page.getByRole('button', { name: 'Search anything', exact: true });
    await commandTrigger.click();
    const commandDialog = page.getByRole('dialog', { name: 'Search Helm' });
    const commandInput = commandDialog.getByRole('textbox', { name: 'Search projects and views' });
    await expect(commandInput).toBeFocused();
    await page.keyboard.press('Shift+Tab');
    await expect(commandDialog.getByRole('button').last()).toBeFocused();
    await page.keyboard.press('Tab');
    await expect(commandInput).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(commandDialog).toBeHidden();
    await expect(commandTrigger).toBeFocused();
  });

  test('keeps the primary navigation available on small screens', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto('/');
    const navigation = page.getByRole('navigation', { name: 'Primary navigation' });
    await expect(navigation).toBeVisible();
    for (const label of ['Board', 'Issues', 'My work', 'Roadmap', 'Settings']) {
      await expect(navigation.getByRole('button', { name: label, exact: true })).toBeVisible();
    }
  });

  test('reports, triages, resolves, and reopens a bug', async ({ page }) => {
    const statusResponse = await page.request.get('/api/v1/auth/status');
    const status = (await statusResponse.json()) as { mode?: string };
    expect(status.mode, 'The E2E server must run with HELM_AUTH_MODE=disabled').toBe('disabled');

    const runId = `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 7)}`.toUpperCase();
    const projectName = `Issue E2E ${runId}`;
    const projectKey = `ISS${runId}`.slice(0, 16);
    const bugTitle = `Preview session expires ${runId}`;
    const labelName = `regression-${runId.toLowerCase()}`;

    await page.goto('/');
    await page.getByRole('button', { name: 'New project', exact: true }).click();
    const projectDialog = page.getByRole('dialog', { name: 'Create a project' });
    await projectDialog.getByLabel('Project name').fill(projectName);
    await projectDialog.getByLabel('Project key').fill(projectKey);
    await projectDialog.getByRole('button', { name: 'Create project', exact: true }).click();
    await expect(page.getByRole('heading', { name: projectName, exact: true })).toBeVisible();

    await page.getByRole('button', { name: 'Report bug', exact: true }).click();
    const reportDialog = page.getByRole('dialog', { name: 'Report a bug' });
    await expect(reportDialog.getByLabel('Bug title')).toBeFocused();
    await reportDialog.getByLabel('Bug title').fill(bugTitle);
    await reportDialog.getByLabel('Actual behavior').fill('The authenticated preview asks the user to sign in again.');
    await reportDialog.getByLabel('Expected behavior').fill('The preview keeps the authenticated session.');
    await reportDialog.getByLabel(/Reproduction steps/).fill('Open the preview, authenticate, and reload.');
    await reportDialog.getByLabel(/Environment/).fill('Desktop Chrome');
    await reportDialog.getByLabel(/Affected version/).fill('preview');
    await reportDialog.getByLabel(/Labels/).fill(labelName);
    await reportDialog.getByRole('button', { name: 'Report bug', exact: true }).click();
    await expect(reportDialog).toBeHidden();

    await page.getByRole('button', { name: 'Issues', exact: true }).first().click();
    await expect(page.getByRole('region', { name: 'Issue health' })).toBeVisible();
    const severityFilter = page.getByLabel('Filter by severity');
    await severityFilter.selectOption('untriaged');
    await expect(page).toHaveURL(/severity=untriaged/);
    await page.reload();
    await expect(page.getByLabel('Filter by severity')).toHaveValue('untriaged');
    await page.getByLabel('Filter by severity').selectOption('all');
    const issueRow = page.getByRole('button', { name: new RegExp(escapeRegExp(bugTitle)) });
    await expect(issueRow).toContainText('Untriaged');
    await issueRow.click();

    const drawer = page.locator('.task-drawer');
    await expect(drawer.getByRole('heading', { name: 'Bug report' })).toBeVisible();
    await drawer.getByLabel('Bug severity').selectOption('s2');
    await drawer.getByLabel('Triage priority').selectOption('high');
    await drawer.getByRole('button', { name: 'Triage issue', exact: true }).click();
    await expect(drawer.locator('.severity-badge').filter({ hasText: 'S2 · High' })).toBeVisible();

    await drawer.getByLabel('Bug resolution').selectOption('fixed');
    await drawer.getByLabel(/Resolution note/).fill('The preview now retains its authenticated session.');
    await drawer.getByRole('button', { name: 'Resolve issue', exact: true }).click();
    await expect(drawer.getByRole('heading', { name: 'Resolved as Fixed' })).toBeVisible();

    await drawer.getByLabel('Reopen reason').fill('The regression returned in a later preview.');
    await drawer.getByRole('button', { name: 'Reopen issue', exact: true }).click();
    await expect(drawer.getByRole('heading', { name: 'Resolve' })).toBeVisible();

    await drawer.getByRole('button', { name: 'Close task details', exact: true }).click();
    await page.getByRole('button', { name: 'Search anything', exact: true }).click();
    const commandDialog = page.getByRole('dialog', { name: 'Search Helm' });
    await commandDialog.getByRole('textbox', { name: 'Search projects and views' }).fill(bugTitle);
    await commandDialog.getByRole('button', { name: new RegExp(escapeRegExp(bugTitle)) }).click();
    await expect(drawer.getByLabel('Task title')).toHaveValue(bugTitle);
  });
});
