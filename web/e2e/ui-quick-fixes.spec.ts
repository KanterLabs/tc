import { expect, test, type Page } from '@playwright/test';

async function openBoard(page: Page) {
  await page.goto('/');
  await expect(page.getByRole('navigation', { name: 'Primary navigation' })).toBeVisible();
  await expect(page.locator('section.board')).toBeVisible();
}

test.describe('UI quick fixes', () => {
  test('focuses the current search with slash and leaves editable fields alone', async ({ page }) => {
    await openBoard(page);

    const boardSearch = page.getByRole('textbox', { name: 'Search tasks' });
    await page.getByRole('button', { name: 'Search anything', exact: true }).focus();
    await page.keyboard.press('/');
    await expect(boardSearch).toBeFocused();

    const quickAddTrigger = page.locator('[data-quick-add-trigger]').first();
    await quickAddTrigger.click();
    const quickAddInput = page.getByRole('textbox', { name: /New task in/ }).first();
    await expect(quickAddInput).toBeFocused();
    await page.keyboard.press('/');
    await expect(quickAddInput).toBeFocused();
    await page.getByRole('button', { name: 'Cancel', exact: true }).last().click();

    await page.getByRole('button', { name: 'Issues', exact: true }).first().click();
    const issueSearch = page.getByRole('textbox', { name: 'Search issues' });
    await expect(issueSearch).toBeVisible();
    await page.getByRole('button', { name: 'Search anything', exact: true }).focus();
    await page.keyboard.press('/');
    await expect(issueSearch).toBeFocused();
  });

  test('autofocuses quick add and returns focus to its trigger on cancel', async ({ page }) => {
    await openBoard(page);

    const trigger = page.locator('[data-quick-add-trigger]').first();
    await trigger.click();
    const input = page.getByRole('textbox', { name: /New task in/ }).first();
    await expect(input).toBeFocused();
    await page.getByRole('button', { name: 'Cancel', exact: true }).last().click();
    await expect(trigger).toBeFocused();
  });

  test('closes the project switcher on outside click, Escape, and selection', async ({ page }) => {
    await openBoard(page);

    const picker = page.locator('[data-project-picker-trigger]');
    const popover = page.locator('[data-project-switcher-popover]');
    await picker.click();
    await expect(popover).toBeVisible();
    await page.locator('main.content').click({ position: { x: 8, y: 8 } });
    await expect(popover).toBeHidden();

    await picker.click();
    await expect(popover).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(popover).toBeHidden();
    await expect(picker).toHaveAttribute('aria-expanded', 'false');

    await picker.click();
    await expect(popover).toBeVisible();
    const projectOption = popover.locator('.popover-project').first();
    await expect(projectOption).toBeVisible();
    await projectOption.click();
    await expect(popover).toBeHidden();
  });

  test('uses one platform-correct command shortcut in the trigger and footer', async ({ page }) => {
    await openBoard(page);

    const expectedShortcut = await page.evaluate(() => {
      const navigatorWithUserAgentData = navigator as Navigator & { userAgentData?: { platform?: string } };
      return /mac/i.test(navigatorWithUserAgentData.userAgentData?.platform || navigator.platform || navigator.userAgent)
        ? '⌘ K'
        : 'Ctrl K';
    });
    await expect(page.locator('[data-command-shortcut]').first()).toHaveText(expectedShortcut);

    await page.getByRole('button', { name: 'Search anything', exact: true }).click();
    const commandDialog = page.getByRole('dialog', { name: 'Search Helm' });
    await expect(commandDialog).toBeVisible();
    await expect(commandDialog.locator('[data-command-shortcut]')).toHaveText(expectedShortcut);
    await page.keyboard.press('Escape');
  });

  test('uses the OS theme only when no stored preference exists', async ({ page }) => {
    await page.emulateMedia({ colorScheme: 'dark' });
    await page.goto('/');
    await page.evaluate(() => {
      localStorage.removeItem('helm.theme');
      localStorage.removeItem('roadmap.theme');
    });
    await page.reload();
    await expect(page.getByRole('navigation', { name: 'Primary navigation' })).toBeVisible();
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');

    await page.evaluate(() => localStorage.setItem('helm.theme', 'light'));
    await page.reload();
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
    await expect(page.getByRole('button', { name: 'Use dark theme' })).toBeVisible();
    await page.getByRole('button', { name: 'Use dark theme' }).click();
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
    expect(await page.evaluate(() => localStorage.getItem('helm.theme'))).toBe('dark');
  });

  test('announces errors assertively and non-errors politely', async ({ page }) => {
    await openBoard(page);

    await page.route('**/api/v1/projects/**', async (route) => {
      if (route.request().method() === 'PATCH') {
        await route.fulfill({
          status: 500,
          contentType: 'application/json',
          body: JSON.stringify({ error: { message: 'forced test failure', code: 'test_failure' } })
        });
        return;
      }
      await route.continue();
    });
    await page.locator('.favorite-heading').click();
    const errorToast = page.locator('.toast.error');
    await expect(errorToast).toHaveAttribute('role', 'alert');
    await expect(errorToast).toHaveAttribute('aria-live', 'assertive');

    await page.unroute('**/api/v1/projects/**');
    const quickAddTrigger = page.locator('[data-quick-add-trigger]').first();
    await quickAddTrigger.click();
    const quickAddInput = page.getByRole('textbox', { name: /New task in/ }).first();
    await quickAddInput.fill('Toast accessibility probe');
    await page.getByRole('button', { name: 'Add task', exact: true }).last().click();
    const successToast = page.locator('.toast.success');
    await expect(successToast).toHaveAttribute('role', 'status');
    await expect(successToast).toHaveAttribute('aria-live', 'polite');
  });

  test('keeps mobile board snapping and reduced-motion scrolling accessible', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.emulateMedia({ reducedMotion: 'reduce' });
    await openBoard(page);

    const board = page.locator('section.board');
    // Poll the current DOM: a live refresh can replace the board between an
    // initial visibility assertion and a one-shot computed-style read.
    await expect(board).toHaveCSS('scroll-snap-type', /x.*mandatory/);
    await expect(board.locator('.board-column').first()).toHaveCSS('scroll-snap-align', 'start');
    await expect(board).toHaveCSS('scroll-behavior', 'auto');
  });
});
