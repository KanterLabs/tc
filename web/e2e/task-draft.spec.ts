import { expect, test } from '@playwright/test';

test('keeps Connect Codex usable during a background account refresh', async ({ page }) => {
  let accountReads = 0;
  await page.route('**/api/v1/codex/account*', async (route) => {
    accountReads += 1;
    if (accountReads > 1) await new Promise((resolve) => setTimeout(resolve, 1_500));
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ connected: false, requires_openai_auth: true })
    });
  });
  await page.route('**/api/v1/codex/login', (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({
      login_id: 'login-refresh-test',
      verification_url: 'https://auth.openai.com/codex/device',
      user_code: 'TEST-CODE'
    })
  }));

  await page.goto('/settings');
  const connect = page.getByRole('button', { name: 'Connect Codex', exact: true });
  await expect(connect).toBeEnabled();

  const navigation = page.getByRole('navigation', { name: 'Primary navigation' });
  await navigation.getByRole('button', { name: 'Issues', exact: true }).click();
  await navigation.getByRole('button', { name: 'Settings', exact: true }).click();
  await expect.poll(() => accountReads).toBeGreaterThan(1);
  await expect(connect).toBeEnabled();
  await connect.click();
  await expect(page.getByText('Finish connecting in ChatGPT')).toBeVisible();
  await expect(page.getByText('TEST-CODE')).toBeVisible();
});

test('previews and selectively applies a Luna task draft', async ({ page }) => {
  await page.route('**/api/v1/codex/account*', (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({ connected: true, account_type: 'chatgpt', plan_type: 'plus', requires_openai_auth: true })
  }));
  await page.route('**/api/v1/projects/*/task-draft', (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({
      title: 'Connect personal Codex subscriptions',
      description: 'Add an isolated connection for each Helm user.',
      acceptance_criteria: ['Each user can connect independently', 'Priority is recommended from project context'],
      priority: 'high',
      rationale: 'This unlocks the assisted task workflow.',
      supporting_task_keys: []
    })
  }));

  await page.goto('/');
  const trigger = page.getByRole('button', { name: 'New task', exact: true });
  await trigger.click();
  const dialog = page.getByRole('dialog', { name: 'Create a task' });
  const idea = dialog.getByLabel('Rough idea');
  await expect(idea).toBeFocused();
  await idea.fill('Help users connect Codex and pick a priority');
  await dialog.getByRole('button', { name: /Assist with Luna/ }).click();
  await expect(dialog.getByRole('heading', { name: 'Review before applying' })).toBeVisible();

  const titleSuggestion = dialog.locator('[data-luna-field="title"]');
  const titleApply = titleSuggestion.getByRole('button');
  await titleApply.click();
  await expect(titleApply).toHaveText('✓ Applied');
  await expect(titleApply).toBeDisabled();
  await expect(dialog.getByRole('status')).toContainText('Title suggestion applied');
  await dialog.getByLabel('Task title').fill('Edited before applying all');
  await expect(titleSuggestion.getByRole('button', { name: /Apply title suggestion/ })).toHaveText('Apply');

  const prioritySuggestion = dialog.locator('[data-luna-field="priority"]');
  await prioritySuggestion.getByRole('button', { name: /Apply priority suggestion/ }).click();
  await expect(dialog.locator('.task-details-fields').getByLabel('Priority')).toHaveValue('high');
  await dialog.getByRole('button', { name: 'Apply all' }).click();
  await expect(dialog.getByRole('status')).toContainText('Luna applied all suggested fields');
  await expect(dialog.getByRole('button', { name: '✓ All applied' })).toBeDisabled();
  await expect(dialog.locator('#luna-suggestion-details')).toBeHidden();
  const reviewSuggestion = dialog.getByRole('button', { name: 'Review suggestion', exact: true });
  await expect(reviewSuggestion).toHaveAttribute('aria-expanded', 'false');
  await expect(dialog.getByLabel('Task title')).toHaveValue('Connect personal Codex subscriptions');
  await expect(dialog.locator('.task-details-fields').getByLabel(/Description/)).toHaveValue(/## Acceptance criteria/);
  await reviewSuggestion.click();
  await expect(dialog.locator('#luna-suggestion-details')).toBeVisible();
  await expect(dialog.getByRole('button', { name: 'Hide suggestion', exact: true })).toHaveAttribute('aria-expanded', 'true');
  await dialog.getByLabel('Task title').fill('Editable after Luna');
  await expect(titleSuggestion.getByRole('button', { name: /Apply title suggestion/ })).toHaveText('Apply');
  await expect(dialog.getByRole('status')).toContainText('Task details changed after applying');
  await expect(dialog.getByRole('button', { name: 'Reapply all' })).toBeEnabled();
  await expect(dialog.getByRole('button', { name: 'Create task' })).toBeEnabled();

  await page.keyboard.press('Escape');
  await expect(dialog).toBeHidden();
  await expect(trigger).toBeFocused();
});

test('keeps the task modal within the viewport on desktop and mobile', async ({ page }) => {
  for (const viewport of [{ width: 1280, height: 900 }, { width: 390, height: 844 }]) {
    await page.setViewportSize(viewport);
    await page.goto('/');
    const trigger = page.getByRole('button', { name: 'New task', exact: true });
    await trigger.click();
    const dialog = page.getByRole('dialog', { name: 'Create a task' });
    await expect(dialog).toBeVisible();
    const geometry = await dialog.evaluate((element) => {
      const box = element.getBoundingClientRect();
      return {
        left: box.left,
        right: box.right,
        viewportWidth: window.innerWidth,
        hasHorizontalOverflow: element.scrollWidth > element.clientWidth + 1
      };
    });
    expect(geometry.left).toBeGreaterThanOrEqual(0);
    expect(geometry.right).toBeLessThanOrEqual(geometry.viewportWidth + 1);
    expect(geometry.hasHorizontalOverflow).toBeFalsy();
    await page.keyboard.press('Escape');
    await expect(dialog).toBeHidden();
  }
});
