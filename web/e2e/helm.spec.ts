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
    await movedCard.locator('[data-task-trigger]').click();
    const drawer = page.locator('.task-drawer');
    await expect(drawer).toBeVisible();
    await expect(drawer.getByLabel('Task title')).toHaveValue(taskTitle);
    await expect(drawer.locator('[data-dialog-initial-focus]')).toBeFocused();
    await expect(drawer.getByLabel('Task title')).not.toBeFocused();
    await drawer.getByRole('button', { name: 'Close task details', exact: true }).focus();
    await page.keyboard.press('Shift+Tab');
    await expect(drawer.getByRole('button', { name: 'Delete task', exact: true })).toBeFocused();
    await page.keyboard.press('Tab');
    await expect(drawer.getByRole('button', { name: 'Close task details', exact: true })).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(drawer).toBeHidden();
    await movedCard.locator('[data-task-trigger]').click();
    await expect(drawer).toBeVisible();
    await drawer.getByLabel('Task title').fill(editedTaskTitle);
    await drawer.getByLabel('Priority').selectOption('high');
    await drawer.getByLabel(/Labels/).fill(labelName);
    await drawer.getByRole('button', { name: 'Save changes', exact: true }).click();

    await expect(movedCard).toContainText(editedTaskTitle);
    await expect(movedCard.locator('.label-chip')).toHaveText(labelName);

    const deleteLabel = drawer.getByRole('button', { name: `Delete label ${labelName}`, exact: true });
    await expect(deleteLabel).toBeVisible();
    await deleteLabel.click();
    const labelDialog = page.getByRole('alertdialog');
    await expect(labelDialog).toContainText(`Delete label “${labelName}”?`);
    await labelDialog.getByRole('button', { name: 'Delete label', exact: true }).click();
    await expect(deleteLabel).toHaveCount(0);
    await expect(movedCard.locator('.label-chip')).toHaveCount(0);

    await drawer.getByRole('button', { name: 'Delete task', exact: true }).click();
    const taskDialog = page.getByRole('alertdialog');
    await expect(taskDialog).toContainText(`Delete ${taskKey}?`);
    await taskDialog.getByRole('button', { name: 'Delete task', exact: true }).click();
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
    const commandInput = commandDialog.getByRole('combobox', { name: 'Search projects and views, tasks, issues, and actions' });
    await expect(commandInput).toBeFocused();
    await page.keyboard.press('Shift+Tab');
    await expect(commandDialog.getByRole('option').last()).toBeFocused();
    await page.keyboard.press('Tab');
    await expect(commandInput).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(commandDialog).toBeHidden();
    await expect(commandTrigger).toBeFocused();
  });

  test('reveals the workspace shell while project data is still loading', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    let releaseAuth = () => {};
    let releaseProjects = () => {};
    const authGate = new Promise<void>((resolve) => { releaseAuth = resolve; });
    const projectsGate = new Promise<void>((resolve) => { releaseProjects = resolve; });
    await page.route(/\/api\/v1\/auth\/status$/, async (route) => {
      await authGate;
      await route.continue();
    });
    await page.route(/\/api\/v1\/projects(?:\?.*)?$/, async (route) => {
      await projectsGate;
      await route.continue();
    });

    await page.goto('/');
    const splashSpinner = page.locator('.splash .spinner');
    await expect(splashSpinner).toBeVisible();
    expect(await splashSpinner.evaluate((element) => getComputedStyle(element).borderTopColor)).toBe('rgb(109, 94, 252)');

    releaseAuth();
    await expect(page.locator('.app-shell')).toBeVisible();
    await expect(page.locator('.workspace-loading')).toBeVisible();
    await expect(page.locator('.splash')).toBeHidden();

    releaseProjects();
    await expect(page.locator('.workspace-loading')).toBeHidden();
  });

  test('offers a retry when the bootstrap connection fails', async ({ page }) => {
    let attempts = 0;
    await page.route(/\/api\/v1\/auth\/status$/, async (route) => {
      attempts += 1;
      if (attempts === 1) await route.abort('connectionfailed');
      else await route.continue();
    });

    await page.goto('/');
    const retry = page.getByRole('button', { name: 'Retry', exact: true });
    await expect(retry).toBeVisible();
    await retry.click();
    await expect(page.locator('.app-shell')).toBeVisible();
    expect(attempts).toBe(2);
  });

  test('keeps the primary navigation and board usable on small screens', async ({ page }) => {
    const runId = `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 7)}`.toUpperCase();
    const projectName = `Mobile layout ${runId}`;
    const projectKey = `MOB${runId}`.slice(0, 16);

    await page.goto('/');
    const origin = new URL(page.url()).origin;
    const response = await page.request.post('/api/v1/projects', {
      data: { key: projectKey, name: projectName, description: 'Phone-sized board acceptance fixture.' },
      headers: {
        Origin: origin,
        'Content-Type': 'application/json',
        'Idempotency-Key': `mobile-layout-${crypto.randomUUID()}`
      }
    });
    expect(response.ok()).toBeTruthy();
    const project = await response.json() as { slug: string };

    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto(`/p/${project.slug}`);
    const navigation = page.locator('.mobile-nav');
    await expect(navigation).toBeVisible();
    for (const label of ['Board', 'Issues', 'My work', 'Roadmap', 'Settings']) {
      await expect(navigation.getByRole('button', { name: label, exact: true })).toBeVisible();
    }

    const board = page.locator('section.board');
    await expect(board).toBeVisible();
    await expect(board.locator('.board-column')).toHaveCount(5);
    const layout = await page.evaluate(() => {
      const nav = document.querySelector('.mobile-nav')?.getBoundingClientRect();
      const columns = Array.from(document.querySelectorAll<HTMLElement>('section.board .board-column'))
        .slice(0, 2)
        .map((column) => column.getBoundingClientRect());
      const search = document.querySelector('.filter-search')?.getBoundingClientRect();
      const newTask = document.querySelector<HTMLElement>('.board-heading .button.primary')?.getBoundingClientRect();
      const board = document.querySelector<HTMLElement>('section.board');
      return {
        viewportWidth: window.innerWidth,
        documentWidth: document.documentElement.scrollWidth,
        bodyWidth: document.body.scrollWidth,
        boardClientWidth: board?.clientWidth,
        boardScrollWidth: board?.scrollWidth,
        boardSnapType: board ? getComputedStyle(board).scrollSnapType : '',
        navBottom: nav?.bottom,
        navButtonHeight: document.querySelector('.mobile-nav button')?.getBoundingClientRect().height,
        searchHeight: search?.height,
        newTaskHeight: newTask?.height,
        firstColumn: columns[0] ? { top: columns[0].top, bottom: columns[0].bottom, left: columns[0].left, right: columns[0].right } : null,
        secondColumn: columns[1] ? { top: columns[1].top, left: columns[1].left, right: columns[1].right } : null
      };
    });
    expect(layout.documentWidth).toBeLessThanOrEqual(layout.viewportWidth);
    expect(layout.bodyWidth).toBeLessThanOrEqual(layout.viewportWidth);
    expect(layout.navBottom).toBeCloseTo(844, 0);
    expect(layout.navButtonHeight).toBeGreaterThanOrEqual(44);
    expect(layout.searchHeight).toBeGreaterThanOrEqual(44);
    expect(layout.newTaskHeight).toBeGreaterThanOrEqual(44);
    expect(layout.boardScrollWidth).toBeGreaterThan(layout.boardClientWidth!);
    expect(layout.boardSnapType).toMatch(/x/);
    expect(layout.boardSnapType).toMatch(/mandatory/);
    expect(layout.firstColumn).not.toBeNull();
    expect(layout.secondColumn).not.toBeNull();
    expect(layout.secondColumn!.top).toBeCloseTo(layout.firstColumn!.top, 0);
    expect(layout.secondColumn!.left).toBeGreaterThan(layout.firstColumn!.right);
  });

  test('lets an administrator manage project metadata and columns with confirmations', async ({ page, request }) => {
    const statusResponse = await request.get('/api/v1/auth/status');
    const status = (await statusResponse.json()) as { mode?: string };
    expect(status.mode, 'The E2E server must run with HELM_AUTH_MODE=disabled').toBe('disabled');

    const runId = `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 7)}`.toUpperCase();
    const projectName = `Admin E2E ${runId}`;
    const projectKey = `ADM${runId}`.slice(0, 16);

    await page.goto('/');
    await page.getByRole('button', { name: 'New project', exact: true }).click();
    const projectDialog = page.getByRole('dialog', { name: 'Create a project' });
    await projectDialog.getByLabel('Project name').fill(projectName);
    await projectDialog.getByLabel('Project key').fill(projectKey);
    await projectDialog.getByRole('button', { name: 'Create project', exact: true }).click();
    await expect(page.getByRole('heading', { name: projectName, exact: true })).toBeVisible();

    await page.getByRole('navigation', { name: 'Primary navigation' }).getByRole('button', { name: 'Settings', exact: true }).first().click();
    const administration = page.getByRole('heading', { name: 'Projects & columns', exact: true });
    await expect(administration).toBeVisible();
    const adminSection = page.locator('section[aria-labelledby="project-admin-heading"]');
    await expect(adminSection.locator('#admin-project-select')).toHaveValue(/.+/);

    await adminSection.getByLabel('Project name').fill(`${projectName} renamed`);
    await adminSection.getByLabel('Description').fill('Managed through the administrator flow.');
    await adminSection.getByLabel('Checklist completion policy').selectOption('require');
    await adminSection.getByRole('button', { name: 'Save project', exact: true }).click();
    const projectSaveConfirmation = page.getByRole('alertdialog', { name: `Save ${projectName} renamed settings?` });
    await expect(projectSaveConfirmation).toBeVisible();
    await expect(projectSaveConfirmation).toContainText('stable project key and URL stay unchanged');
    await projectSaveConfirmation.getByRole('button', { name: 'Save project', exact: true }).click();
    await expect(projectSaveConfirmation).toBeHidden();
    const savedProjectResponse = await request.get(`/api/v1/projects/${encodeURIComponent(projectKey)}`);
    expect(savedProjectResponse.ok()).toBeTruthy();
    await expect(savedProjectResponse.json()).resolves.toMatchObject({
      name: `${projectName} renamed`,
      description: 'Managed through the administrator flow.',
      checklist_completion_policy: 'require'
    });

    await adminSection.getByLabel('New column name').fill('Intake');
    await adminSection.locator('.admin-column-create select').selectOption('backlog');
    await adminSection.getByRole('button', { name: /Add column/ }).click();
    const addConfirmation = page.getByRole('alertdialog', { name: 'Add Intake?' });
    await expect(addConfirmation).toBeVisible();
    await addConfirmation.getByRole('button', { name: 'Add column', exact: true }).click();
    await expect(addConfirmation).toBeHidden();
    const intakeRow = adminSection.locator('.admin-column-row').filter({ has: page.locator('input[aria-label^="Column name for Intake"]') });
    await expect(intakeRow).toBeVisible();

    await intakeRow.getByRole('button', { name: 'Move Intake up', exact: true }).click();
    const moveConfirmation = page.getByRole('alertdialog', { name: 'Move Intake?' });
    await expect(moveConfirmation).toBeVisible();
    await moveConfirmation.getByRole('button', { name: 'Move column', exact: true }).click();
    await expect(moveConfirmation).toBeHidden();

    await intakeRow.getByLabel('Column name for Intake').fill('Intake renamed');
    await intakeRow.getByRole('button', { name: 'Save', exact: true }).click();
    const columnSaveConfirmation = page.getByRole('alertdialog', { name: 'Save Intake renamed?' });
    await expect(columnSaveConfirmation).toBeVisible();
    await columnSaveConfirmation.getByRole('button', { name: 'Save column', exact: true }).click();
    await expect(columnSaveConfirmation).toBeHidden();
    const renamedIntakeRow = adminSection.locator('.admin-column-row').filter({ has: page.locator('input[aria-label="Column name for Intake renamed"]') });
    await expect(renamedIntakeRow).toBeVisible();

    await renamedIntakeRow.getByRole('button', { name: 'Archive', exact: true }).click();
    const archiveConfirmation = page.getByRole('alertdialog', { name: 'Archive Intake renamed?' });
    await expect(archiveConfirmation).toBeVisible();
    await expect(archiveConfirmation).toContainText('Tasks in this column will move');
    await archiveConfirmation.getByRole('button', { name: 'Archive column', exact: true }).click();
    await expect(archiveConfirmation).toBeHidden();
    await expect(renamedIntakeRow).toHaveClass(/archived/);

    for (const name of ['After archive first', 'After archive second']) {
      await adminSection.getByLabel('New column name').fill(name);
      await adminSection.locator('.admin-column-create select').selectOption('backlog');
      await adminSection.getByRole('button', { name: /Add column/ }).click();
      const createConfirmation = page.getByRole('alertdialog', { name: `Add ${name}?` });
      await expect(createConfirmation).toBeVisible();
      await createConfirmation.getByRole('button', { name: 'Add column', exact: true }).click();
      await expect(createConfirmation).toBeHidden();
      await expect(adminSection.locator('.admin-column-row').filter({ has: page.locator(`input[aria-label="Column name for ${name}"]`) })).toBeVisible();
    }
    const postArchiveRows = adminSection.locator('.admin-column-row');
    const renderedColumnNames = await postArchiveRows.evaluateAll((rows) => rows.map((row) => row.querySelector('input')?.value || ''));
    expect(renderedColumnNames.indexOf('After archive first')).toBeLessThan(renderedColumnNames.indexOf('Intake renamed'));
    expect(renderedColumnNames.indexOf('After archive second')).toBeLessThan(renderedColumnNames.indexOf('Intake renamed'));
    const firstPostArchiveRow = adminSection.locator('.admin-column-row').filter({ has: page.locator('input[aria-label="Column name for After archive first"]') });
    const secondPostArchiveRow = adminSection.locator('.admin-column-row').filter({ has: page.locator('input[aria-label="Column name for After archive second"]') });
    await expect(firstPostArchiveRow.getByRole('button', { name: 'Move After archive first up', exact: true })).toBeEnabled();
    await expect(firstPostArchiveRow.getByRole('button', { name: 'Move After archive first down', exact: true })).toBeEnabled();
    await expect(secondPostArchiveRow.getByRole('button', { name: 'Move After archive second up', exact: true })).toBeEnabled();
    await expect(secondPostArchiveRow.getByRole('button', { name: 'Move After archive second down', exact: true })).toBeDisabled();

    await adminSection.getByRole('button', { name: 'Archive project', exact: true }).click();
    const projectConfirmation = page.getByRole('alertdialog', { name: `Archive ${projectName} renamed?` });
    await expect(projectConfirmation).toBeVisible();
    await projectConfirmation.getByRole('button', { name: 'Archive project', exact: true }).click();
    await expect(projectConfirmation).toBeHidden();
    await expect(adminSection.getByText('Archived', { exact: true })).toBeVisible();

    await adminSection.getByRole('button', { name: 'Restore project', exact: true }).click();
    const restoreConfirmation = page.getByRole('alertdialog', { name: `Restore ${projectName} renamed?` });
    await expect(restoreConfirmation).toBeVisible();
    await restoreConfirmation.getByRole('button', { name: 'Restore project', exact: true }).click();
    await expect(restoreConfirmation).toBeHidden();
    await expect(adminSection.getByText('Active', { exact: true })).toBeVisible();
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
    await commandDialog.getByRole('combobox', { name: 'Search projects and views, tasks, issues, and actions' }).fill(bugTitle);
    await commandDialog.getByRole('option', { name: new RegExp(escapeRegExp(bugTitle)) }).click();
    await expect(drawer.getByLabel('Task title')).toHaveValue(bugTitle);
  });
});
