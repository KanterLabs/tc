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

  await dialog.locator('.luna-preview-field').filter({ hasText: 'Priority' }).getByRole('button', { name: 'Apply' }).click();
  await expect(dialog.getByLabel('Priority')).toHaveValue('high');
  await dialog.getByRole('button', { name: 'Apply all' }).click();
  await expect(dialog.getByLabel('Task title')).toHaveValue('Connect personal Codex subscriptions');
  await expect(dialog.getByLabel(/Description/)).toHaveValue(/## Acceptance criteria/);
  await dialog.getByLabel('Task title').fill('Editable after Luna');
  await expect(dialog.getByRole('button', { name: 'Create task' })).toBeEnabled();

  await page.keyboard.press('Escape');
  await expect(dialog).toBeHidden();
  await expect(trigger).toBeFocused();
});
