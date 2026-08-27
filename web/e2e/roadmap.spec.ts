import { expect, test } from '@playwright/test';

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

test.describe('Roadmap workspace', () => {
  test('loads in disabled-auth mode and manages a task from the board', async ({ page, request }) => {
    const statusResponse = await request.get('/api/v1/auth/status');
    expect(statusResponse.ok()).toBeTruthy();
    const status = (await statusResponse.json()) as { mode?: string };
    expect(status.mode, 'The E2E server must run with ROADMAP_AUTH_MODE=disabled').toBe('disabled');

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
    const commandDialog = page.getByRole('dialog', { name: 'Search Roadmap' });
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
    for (const label of ['Board', 'My work', 'Roadmap', 'Settings']) {
      await expect(navigation.getByRole('button', { name: label, exact: true })).toBeVisible();
    }
  });
});
