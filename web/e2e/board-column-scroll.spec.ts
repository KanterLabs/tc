import { expect, test, type APIRequestContext, type APIResponse, type Locator, type Page } from '@playwright/test';

type Project = { id: string; key: string; name: string; slug: string };
type Column = { id: string; name: string; semantic_state: string; position: number };
type Task = { id: string; key: string; title: string; column_id: string; position: number; version: number };
type Collection<T> = { data: T[]; next_cursor?: string | null };
type BoardFixture = { project: Project; columns: Column[]; manyColumn: Column; manyTaskCount: number };
type Viewport = { width: number; height: number; name: string };

const e2eOrigin = new URL(
  process.env.HELM_E2E_BASE_URL || process.env.ROADMAP_E2E_BASE_URL || 'http://127.0.0.1:18080'
).origin;

function mutationHeaders(key: string): Record<string, string> {
  return {
    'Content-Type': 'application/json',
    Origin: e2eOrigin,
    'Idempotency-Key': key
  };
}

async function json<T>(response: APIResponse, description: string): Promise<T> {
  expect(response.ok(), `${description} returned HTTP ${response.status()}`).toBeTruthy();
  return await response.json() as T;
}

async function postJSON<T>(request: APIRequestContext, path: string, body: unknown, key: string): Promise<T> {
  return json<T>(await request.post(path, {
    data: body,
    headers: mutationHeaders(key)
  }), `POST ${path}`);
}

function collectionData<T>(payload: Collection<T> | T[]): T[] {
  return Array.isArray(payload) ? payload : payload.data;
}

async function createFixture(request: APIRequestContext): Promise<BoardFixture> {
  const runID = `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 7)}`.toUpperCase();
  const project = await postJSON<Project>(request, '/api/v1/projects', {
    key: `COL${runID}`.slice(0, 16),
    name: `Board column scroll ${runID}`,
    description: 'Viewport-bounded board column regression fixture.'
  }, `board-column-scroll-${runID}-project`);

  let columns = collectionData(await json<Collection<Column> | Column[]>(
    await request.get(`/api/v1/projects/${project.id}/columns?limit=20`),
    'list board column scroll columns'
  ));

  // Keep eight columns in the fixture so horizontal board scrolling remains
  // exercised even on a wide desktop viewport. The first five are created by
  // the project endpoint; the extra columns use the public column API.
  for (let index = columns.length; index < 8; index += 1) {
    await postJSON<Column>(request, `/api/v1/projects/${project.id}/columns`, {
      name: `Review ${index + 1}`,
      semantic_state: 'ready',
      position: index
    }, `board-column-scroll-${runID}-column-${index + 1}`);
  }
  columns = collectionData(await json<Collection<Column> | Column[]>(
    await request.get(`/api/v1/projects/${project.id}/columns?limit=20`),
    'reload board column scroll columns'
  ));
  expect(columns.length, 'the fixture should have enough columns to overflow the board').toBe(8);

  // Empty, few-card, and 100-card columns are deliberately adjacent in the
  // same board. The many-card column is paged once in the browser so all 100
  // cards participate in the flex/overflow regression.
  const counts = [0, 1, 0, 0, 0, 0, 0, 100];
  const manyColumn = columns[columns.length - 1];
  expect(manyColumn).toBeTruthy();
  for (const [columnIndex, count] of counts.entries()) {
    const column = columns[columnIndex];
    for (let taskIndex = 0; taskIndex < count; taskIndex += 1) {
      const longCard = column.id === manyColumn.id && taskIndex === 0;
      await postJSON<Task>(request, `/api/v1/projects/${project.id}/tasks`, {
        title: longCard
          ? `Viewport card ${String(taskIndex + 1).padStart(3, '0')} keeps a longer title measurable in a bounded card`
          : `Viewport card ${String(taskIndex + 1).padStart(3, '0')}`,
        column_id: column.id,
        priority: 'normal',
        ...(longCard ? {
          description: 'This deliberately verbose card description makes the fixture exercise intrinsic card sizing before the column body starts scrolling.'
        } : {})
      }, `board-column-scroll-${runID}-task-${columnIndex + 1}-${taskIndex + 1}`);
    }
  }

  return { project, columns, manyColumn: manyColumn as Column, manyTaskCount: counts[counts.length - 1] };
}

function boardColumn(board: Locator, column: Column): Locator {
  return board.getByRole('article', { name: `${column.name} column`, exact: true });
}

async function settleLayout(page: Page): Promise<void> {
  await page.evaluate(() => new Promise<void>((resolve) => {
    requestAnimationFrame(() => requestAnimationFrame(() => resolve()));
  }));
}

async function openBoard(page: Page, fixture: BoardFixture, viewport: Viewport): Promise<Locator> {
  await page.setViewportSize({ width: viewport.width, height: viewport.height });
  await page.goto(`/p/${fixture.project.slug}`);
  const board = page.locator('section.board');
  await expect(board).toBeVisible();
  await expect(board.locator('.board-column')).toHaveCount(fixture.columns.length);
  await board.scrollIntoViewIfNeeded();
  await settleLayout(page);
  return board;
}

async function loadAllCards(column: Locator, expectedCount: number): Promise<void> {
  await expect(column.locator('.task-card')).toHaveCount(Math.min(50, expectedCount));
  if (expectedCount <= 50) return;

  const loadMore = column.getByRole('button', { name: 'Load more tasks', exact: true });
  await expect(loadMore).toBeVisible();
  await loadMore.click();
  await expect(column.locator('.task-card')).toHaveCount(expectedCount);
}

test('bounds equal-height columns while retaining independent board and body scrolling', async ({ page, request }) => {
  test.setTimeout(120_000);

  const status = await json<{ mode?: string }>(await request.get('/api/v1/auth/status'), 'read auth status');
  expect(status.mode, 'The E2E server must run with HELM_AUTH_MODE=disabled').toBe('disabled');

  const fixture = await createFixture(request);
  const viewports: Viewport[] = [
    { name: 'desktop', width: 1280, height: 900 },
    { name: 'phone portrait', width: 390, height: 844 },
    { name: 'short landscape', width: 900, height: 480 }
  ];

  // The fixture creates a large mutation-event backlog. Layout assertions do
  // not need live reconciliation, and an empty feed keeps the board DOM
  // attached while each viewport is measured.
  await page.route('**/api/v1/events**', async (route) => {
    if (route.request().method() !== 'GET') {
      await route.continue();
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: [], next_cursor: null })
    });
  });

  for (const viewport of viewports) {
    const board = await openBoard(page, fixture, viewport);
    const emptyColumn = boardColumn(board, fixture.columns[0]);
    const fewColumn = boardColumn(board, fixture.columns[1]);
    const manyColumn = boardColumn(board, fixture.manyColumn);
    await expect(emptyColumn.locator('.column-empty')).toBeVisible();
    await expect(fewColumn.locator('.task-card')).toHaveCount(1);
    await loadAllCards(manyColumn, fixture.manyTaskCount);

    // Loading the final page must not alter the board's horizontal overflow;
    // only each column's card body should own vertical overflow.
    await board.scrollIntoViewIfNeeded();
    await settleLayout(page);
    const boardMetrics = await board.evaluate((element) => {
      const box = (target: Element) => {
        const rect = (target as HTMLElement).getBoundingClientRect();
        return { top: rect.top, bottom: rect.bottom, left: rect.left, right: rect.right, height: rect.height };
      };
      const root = document.scrollingElement || document.documentElement;
      const style = getComputedStyle(element);
      const columns = Array.from(element.querySelectorAll<HTMLElement>('.board-column')).map((column) => ({
        ...box(column),
        cards: box(column.querySelector('.column-cards') as HTMLElement)
      }));
      return {
        viewportWidth: window.innerWidth,
        viewportHeight: window.innerHeight,
        boardClientWidth: element.clientWidth,
        boardScrollWidth: element.scrollWidth,
        boardOverflowX: style.overflowX,
        documentWidth: document.documentElement.scrollWidth,
        bodyWidth: document.body.scrollWidth,
        bodyScrollHeight: root.scrollHeight,
        bodyClientHeight: root.clientHeight,
        columns
      };
    });
    const columnHeights = boardMetrics.columns.map((column) => column.height);
    expect(Math.max(...columnHeights) - Math.min(...columnHeights), `${viewport.name} columns should share one height`).toBeLessThanOrEqual(2);
    expect(Math.min(...columnHeights), `${viewport.name} columns should retain a usable minimum height`).toBeGreaterThanOrEqual(220);
    expect(Math.max(...boardMetrics.columns.map((column) => column.bottom)), `${viewport.name} columns should fit after the board is aligned`).toBeLessThanOrEqual(viewport.height + 2);
    expect(boardMetrics.boardScrollWidth, `${viewport.name} board should retain horizontal scrolling`).toBeGreaterThan(boardMetrics.boardClientWidth + 10);
    expect(boardMetrics.boardOverflowX, `${viewport.name} board should own horizontal overflow`).toMatch(/auto|scroll/);
    expect(boardMetrics.documentWidth, `${viewport.name} board overflow must not widen the document`).toBeLessThanOrEqual(boardMetrics.viewportWidth + 2);
    expect(boardMetrics.bodyWidth, `${viewport.name} board overflow must not widen the body`).toBeLessThanOrEqual(boardMetrics.viewportWidth + 2);

    const manyMetrics = await manyColumn.evaluate((column) => {
      const box = (target: Element) => {
        const rect = (target as HTMLElement).getBoundingClientRect();
        return { top: rect.top, bottom: rect.bottom, left: rect.left, right: rect.right, height: rect.height };
      };
      const cards = column.querySelector<HTMLElement>('.column-cards');
      const header = column.querySelector<HTMLElement>('.column-header');
      const footer = column.querySelector<HTMLElement>('.quick-add-wrap');
      if (!cards || !header || !footer) throw new Error('board column is missing its scroll, header, or footer region');
      const headerBefore = box(header);
      const footerBefore = box(footer);
      cards.scrollTop = cards.scrollHeight;
      return {
        cardHeights: Array.from(column.querySelectorAll<HTMLElement>('.task-card')).map((card) => card.getBoundingClientRect().height),
        cardsClientHeight: cards.clientHeight,
        cardsScrollHeight: cards.scrollHeight,
        cardsScrollTop: cards.scrollTop,
        headerBefore,
        headerAfter: box(header),
        footerBefore,
        footerAfter: box(footer)
      };
    });
    expect(manyMetrics.cardHeights, `${viewport.name} should render all cards in the 100-card column`).toHaveLength(fixture.manyTaskCount);
    expect(Math.min(...manyMetrics.cardHeights), `${viewport.name} cards must keep their natural height`).toBeGreaterThan(24);
    expect(manyMetrics.cardsScrollHeight, `${viewport.name} card body should be vertically scrollable`).toBeGreaterThan(manyMetrics.cardsClientHeight + 100);
    expect(manyMetrics.cardsScrollTop, `${viewport.name} inner scroll should reach the card body end`).toBeGreaterThan(0);
    expect(Math.abs(manyMetrics.headerAfter.top - manyMetrics.headerBefore.top), `${viewport.name} column header should remain stationary`).toBeLessThanOrEqual(1);
    expect(Math.abs(manyMetrics.footerAfter.top - manyMetrics.footerBefore.top), `${viewport.name} Add task footer should remain stationary`).toBeLessThanOrEqual(1);
    expect(await emptyColumn.locator('.column-cards').evaluate((element) => element.scrollTop), `${viewport.name} empty column should keep its own scroll position`).toBe(0);
    if (viewport.name === 'phone portrait') {
      const mobileNav = await page.locator('.mobile-nav').boundingBox();
      expect(mobileNav, 'phone portrait should expose the fixed mobile navigation').not.toBeNull();
      expect(manyMetrics.footerAfter.bottom, 'the Add task footer should remain above the fixed mobile navigation').toBeLessThanOrEqual((mobileNav?.y || 0) + 2);
    }

    // A short landscape viewport intentionally leaves the page itself
    // scrollable because the board starts below the heading and toolbar. Body
    // scrolling must not consume or reset the column's independent scroll.
    if (viewport.name === 'short landscape') {
      expect(boardMetrics.bodyScrollHeight).toBeGreaterThan(boardMetrics.bodyClientHeight);
      const bodyScroll = await page.evaluate(() => {
        const root = document.scrollingElement || document.documentElement;
        const cards = document.querySelector<HTMLElement>('.board-column:last-child .column-cards');
        // The board was aligned into view above, which may already place the
        // document at its maximum scroll position in a short viewport. Start
        // from the top so this check proves the page itself still scrolls.
        root.scrollTop = 0;
        const before = { body: root.scrollTop, cards: cards?.scrollTop || 0 };
        root.scrollTop = root.scrollHeight;
        return { before, after: { body: root.scrollTop, cards: cards?.scrollTop || 0 } };
      });
      expect(bodyScroll.after.body).toBeGreaterThan(bodyScroll.before.body);
      expect(bodyScroll.after.cards).toBe(bodyScroll.before.cards);
    }

    // Changing board criteria is a navigation within the board. It should
    // reset the current column's vertical page to the top before new cards
    // replace the old result set.
    if (viewport.name === 'phone portrait') {
      await manyColumn.locator('.column-cards').evaluate((element) => {
        element.scrollTop = 120;
        element.dispatchEvent(new Event('scroll'));
      });
      await page.getByRole('button', { name: 'Refresh board', exact: true }).click();
      await expect(manyColumn.locator('.task-card')).toHaveCount(50);
      await expect.poll(() => manyColumn.locator('.column-cards').evaluate((element) => element.scrollTop)).toBe(120);
      const priorityFilter = page.getByLabel('Filter by priority');
      await priorityFilter.selectOption('high');
      await expect.poll(() => manyColumn.locator('.column-cards').evaluate((element) => element.scrollTop)).toBe(0);
    }
  }
});
