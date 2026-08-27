import { expect, test } from '@playwright/test';

test('first local administrator setup signs into the workspace', async ({ page, request }) => {
  const statusResponse = await request.get('/api/v1/auth/status');
  expect(statusResponse.ok()).toBeTruthy();
  const status = (await statusResponse.json()) as { mode?: string; setup_required?: boolean };
  test.skip(status.mode !== 'local' || !status.setup_required, 'requires a fresh local-auth database');

  await page.goto('/');
  await page.getByLabel('Full name').fill('Onboarding Admin');
  await page.getByLabel('Email').fill('onboarding@example.test');
  await page.getByLabel('Password').fill('LocalOnboarding-2026!');
  await page.getByRole('button', { name: 'Create workspace', exact: true }).click();

  await expect(page.getByRole('navigation', { name: 'Primary navigation' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'New project', exact: true })).toBeVisible();
  await expect(page.getByText('Onboarding Admin', { exact: true })).toBeVisible();

  await page.reload();
  await expect(page.getByRole('navigation', { name: 'Primary navigation' })).toBeVisible();
  await expect(page.getByText('Onboarding Admin', { exact: true })).toBeVisible();
});
