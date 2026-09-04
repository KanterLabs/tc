import { expect, test, type Page, type Route } from '@playwright/test';

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

async function createProjectWithTask(page: Page) {
  const runId = `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 7)}`.toUpperCase();
  const projectName = `Palette E2E ${runId}`;
  const projectKey = `PAL${runId}`.slice(0, 16);
  const taskTitle = `Command task ${runId}`;

  await page.goto('/');
  await expect(page.getByRole('navigation', { name: 'Primary navigation' })).toBeVisible();
  await page.getByRole('button', { name: 'New project', exact: true }).click();
  const projectDialog = page.getByRole('dialog', { name: 'Create a project' });
  await projectDialog.getByLabel('Project name').fill(projectName);
  await projectDialog.getByLabel('Project key').fill(projectKey);
  await projectDialog.getByRole('button', { name: 'Create project', exact: true }).click();
  await expect(page.getByRole('heading', { name: projectName, exact: true })).toBeVisible();

  const board = page.locator('section.board');
  const backlogColumn = board.locator('.board-column').filter({
    has: page.getByRole('heading', { name: 'Backlog', exact: true })
  });
  await expect(backlogColumn).toBeVisible();
  await backlogColumn.getByRole('button', { name: /Add task$/ }).click();
  await backlogColumn.getByRole('textbox', { name: 'New task in Backlog' }).fill(taskTitle);
  await backlogColumn.getByRole('button', { name: 'Add task', exact: true }).click();

  const createdCard = board.locator('.task-card').filter({ hasText: taskTitle });
  await expect(createdCard).toBeVisible();
  const taskKey = (await createdCard.locator('.task-key').textContent())?.trim();
  expect(taskKey).toMatch(/^[A-Z0-9_-]+-\d+$/);
  return { taskKey: taskKey as string, taskTitle };
}

async function openPalette(page: Page) {
  const trigger = page.getByRole('button', { name: 'Search anything', exact: true });
  await trigger.click();
  const dialog = page.getByRole('dialog', { name: 'Search Helm' });
  const input = dialog.getByRole('combobox', { name: 'Search projects and views, tasks, issues, and actions' });
  await expect(input).toBeFocused();
  return { trigger, dialog, input, listbox: dialog.getByRole('listbox', { name: 'Command results' }) };
}

test.describe('command palette', () => {
  test('indexes board tasks and activates actions with keyboard and mouse', async ({ page }) => {
    const { taskKey, taskTitle } = await createProjectWithTask(page);
    const { trigger, dialog, input, listbox } = await openPalette(page);

    await expect(dialog).toHaveAttribute('aria-modal', 'true');
    await expect(input).toHaveAttribute('role', 'combobox');
    await expect(input).toHaveAttribute('aria-expanded', 'true');
    await expect(input).toHaveAttribute('aria-controls', 'command-results');
    await expect(input).toHaveAttribute('aria-autocomplete', 'list');
    expect(await listbox.getByRole('option').count()).toBeGreaterThan(0);
    await expect(listbox.locator('[role="option"] button')).toHaveCount(0);

    const firstOptionId = await input.getAttribute('aria-activedescendant');
    expect(firstOptionId).toBeTruthy();
    await page.keyboard.press('ArrowDown');
    expect(await input.getAttribute('aria-activedescendant')).not.toBe(firstOptionId);
    await expect(listbox.getByRole('option').nth(1)).toHaveAttribute('aria-selected', 'true');
    await page.keyboard.press('End');
    await expect(listbox.getByRole('option').last()).toHaveAttribute('aria-selected', 'true');
    await page.keyboard.press('Home');
    await expect(listbox.getByRole('option').first()).toHaveAttribute('aria-selected', 'true');

    await page.keyboard.press('Tab');
    await expect(listbox.getByRole('option').first()).toBeFocused();
    await page.keyboard.press('Shift+Tab');
    await expect(input).toBeFocused();
    await page.keyboard.press('Shift+Tab');
    await expect(listbox.getByRole('option').last()).toBeFocused();
    await page.keyboard.press('Tab');
    await expect(input).toBeFocused();

    await input.fill(taskKey);
    const taskOption = listbox.getByRole('option', { name: new RegExp(`${escapeRegExp(taskTitle)}.*${escapeRegExp(taskKey)}`) });
    await expect(taskOption).toBeVisible();
    await expect(taskOption).toHaveAttribute('aria-selected', 'true');
    await page.keyboard.press('Enter');

    const drawer = page.getByRole('dialog', { name: new RegExp(`${escapeRegExp(taskKey)}: ${escapeRegExp(taskTitle)}`) });
    await expect(drawer).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(drawer).toBeHidden();
    await expect(trigger).toBeFocused();

    let palette = await openPalette(page);
    await palette.input.fill('New task');
    await page.keyboard.press('Enter');
    const taskModal = page.getByRole('dialog', { name: 'Create a task' });
    await expect(taskModal).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(taskModal).toBeHidden();
    await expect(palette.trigger).toBeFocused();

    palette = await openPalette(page);
    await palette.input.fill('Report bug');
    await page.keyboard.press('Enter');
    const bugModal = page.getByRole('dialog', { name: 'Report a bug' });
    await expect(bugModal).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(bugModal).toBeHidden();
    await expect(palette.trigger).toBeFocused();

    const beforeTheme = await page.locator('html').getAttribute('data-theme');
    palette = await openPalette(page);
    await palette.input.fill('Toggle theme');
    const toggleOption = palette.listbox.getByRole('option', { name: /Toggle theme/ });
    await toggleOption.hover();
    await expect(toggleOption).toHaveAttribute('aria-selected', 'true');
    await toggleOption.click();
    await expect(page.locator('html')).toHaveAttribute('data-theme', beforeTheme === 'dark' ? 'light' : 'dark');
    await expect(palette.dialog).toBeHidden();
    await expect(palette.trigger).toBeFocused();
  });

  test('announces issue loading, errors, and no-match states accessibly', async ({ page }) => {
    // Keep the full issue collection pending without blocking the scalar
    // /issues/metrics request that bootstrap awaits before rendering the app.
    const issuePattern = (url: URL) => url.pathname === '/api/v1/issues';
    let releaseIssues!: () => void;
    const issueGate = new Promise<void>((resolve) => { releaseIssues = resolve; });
    const delayedIssues = async (route: Route) => {
      await issueGate;
      await route.continue();
    };
    await page.route(issuePattern, delayedIssues);
    await page.goto('/');
    const loadingPalette = await openPalette(page);
    const loadingStatus = loadingPalette.dialog.getByRole('status').filter({ hasText: 'Searching Helm…' }).first();
    await expect(loadingStatus).toBeVisible();
    await expect(loadingPalette.listbox).toHaveAttribute('aria-busy', 'true');
    releaseIssues();
    await expect(loadingStatus).toBeHidden();
    await expect(loadingPalette.listbox).toHaveAttribute('aria-busy', 'false');
    await page.keyboard.press('Escape');
    await page.unroute(issuePattern, delayedIssues);

    await page.reload();
    const failedIssues = async (route: Route) => route.abort('failed');
    await page.route(issuePattern, failedIssues);
    const errorPalette = await openPalette(page);
    const errorStatus = errorPalette.dialog.getByRole('alert');
    await expect(errorStatus).toBeVisible();
    await expect(errorStatus).toContainText(/Issue results|fetch/i);
    await expect(errorPalette.listbox).toHaveAttribute('aria-busy', 'false');
    await errorPalette.input.fill('no command matches this text');
    await expect(errorPalette.dialog.getByRole('status').filter({ hasText: 'No commands match' })).toBeVisible();
    await page.keyboard.press('Escape');
  });
});
