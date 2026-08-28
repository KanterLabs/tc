import { expect, test, type Page, type Route } from '@playwright/test';

const project = {
  id: 'project-audit',
  key: 'AUD',
  slug: 'audit-demo',
  name: 'Audit Demo',
  description: 'A deterministic board used by the audit review browser tests.',
  color: '#6d5efc',
  favorite: true
};

const columns = [
  { id: 'column-backlog', project_id: project.id, name: 'Backlog', semantic_state: 'backlog', position: 0 },
  { id: 'column-ready', project_id: project.id, name: 'Ready', semantic_state: 'ready', position: 1 },
  { id: 'column-active', project_id: project.id, name: 'In progress', semantic_state: 'active', position: 2 },
  { id: 'column-blocked', project_id: project.id, name: 'Blocked', semantic_state: 'blocked', position: 3 },
  { id: 'column-done', project_id: project.id, name: 'Done', semantic_state: 'completed', position: 4 }
];

const tasks = [
  {
    id: 'task-approved', number: 1, key: 'AUD-1', project_id: project.id, column_id: 'column-backlog',
    title: 'Move this approved recommendation', priority: 'normal', position: 0, version: 2
  },
  {
    id: 'task-changed', number: 2, key: 'AUD-2', project_id: project.id, column_id: 'column-ready',
    title: 'Changed after the audit', priority: 'high', position: 0, version: 3
  },
  {
    id: 'task-pending', number: 3, key: 'AUD-3', project_id: project.id, column_id: 'column-active',
    title: 'Needs a human review', priority: 'normal', position: 0, version: 1
  }
];

const findings = [
  {
    id: 'finding-approved', audit_id: 'audit-queued', task_id: 'task-approved', captured_version: 2,
    source_column: 'column-backlog', verdict: 'move_proposed', proposed_semantic_destination: 'ready',
    confidence: 0.92, reason: 'This task has the shape of work that is ready to start.',
    evidence_refs: ['column-pattern:ready', 'task:acceptance-criteria'], review_state: 'approved', version: 1,
    changed_since_audit: false, created_at: '2026-08-28T08:00:00Z', updated_at: '2026-08-28T08:00:00Z'
  },
  {
    id: 'finding-changed', audit_id: 'audit-queued', task_id: 'task-changed', captured_version: 2,
    source_column: 'column-backlog', verdict: 'move_proposed', proposed_semantic_destination: 'ready',
    confidence: 0.78, reason: 'The task looked ready when this audit was captured.',
    evidence_refs: ['task:updated-after-capture'], review_state: 'pending', version: 1,
    changed_since_audit: true, created_at: '2026-08-28T08:01:00Z', updated_at: '2026-08-28T08:01:00Z'
  },
  {
    id: 'finding-pending', audit_id: 'audit-queued', task_id: 'task-pending', captured_version: 1,
    source_column: 'column-active', verdict: 'needs_attention', confidence: 0.64,
    reason: 'The task needs a human decision before it can move forward.',
    evidence_refs: ['task:missing-owner'], review_state: 'pending', version: 1,
    changed_since_audit: false, created_at: '2026-08-28T08:02:00Z', updated_at: '2026-08-28T08:02:00Z'
  }
];

const runs = [
  {
    id: 'audit-queued', project_id: project.id, actor_id: 'human-audit', scope: 'board', status: 'queued',
    started_at: '2026-08-28T08:00:00Z', created_at: '2026-08-28T08:00:00Z', updated_at: '2026-08-28T08:00:00Z', finding_count: findings.length
  },
  {
    id: 'audit-complete', project_id: project.id, actor_id: 'human-audit', scope: 'board', status: 'complete',
    started_at: '2026-08-26T08:00:00Z', finalized_at: '2026-08-26T08:10:00Z', created_at: '2026-08-26T08:00:00Z', updated_at: '2026-08-26T08:10:00Z', finding_count: 0
  }
];

type AuditMockState = {
  patchRequests: Array<{ id: string; body: Record<string, unknown>; ifMatch: string | null }>;
  moveRequests: Array<{ taskId: string; body: Record<string, unknown>; ifMatch: string | null }>;
  requestedPaths: string[];
};

function json(route: Route, body: unknown, status = 200): Promise<void> {
  return route.fulfill({
    status,
    contentType: 'application/json',
    body: JSON.stringify(body)
  });
}

async function installAuditMocks(page: Page, primaryStatus: 'queued' | 'complete' = 'complete'): Promise<AuditMockState> {
  const state: AuditMockState = { patchRequests: [], moveRequests: [], requestedPaths: [] };
  const mockRuns = runs.map((run) => run.id === 'audit-queued'
    ? {
        ...run,
        status: primaryStatus,
        finalized_at: primaryStatus === 'complete' ? '2026-08-28T08:10:00Z' : undefined
      }
    : run);
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname;
    state.requestedPaths.push(`${request.method()} ${path}${url.search}`);

    if (path === '/api/v1/auth/status' && request.method() === 'GET') {
      await json(route, {
        mode: 'disabled', configured: true, setup_required: false, authenticated: true,
        actor: { id: 'human-audit', kind: 'human', name: 'Audit reviewer', admin: true },
        user: { id: 'human-audit', kind: 'human', name: 'Audit reviewer', admin: true }
      });
      return;
    }
    if (path === '/api/v1/projects' && request.method() === 'GET') {
      await json(route, { data: [project], next_cursor: null });
      return;
    }
    if (path === `/api/v1/projects/${project.id}/columns` && request.method() === 'GET') {
      await json(route, { data: columns, next_cursor: null });
      return;
    }
    if (path === `/api/v1/projects/${project.id}/tasks` && request.method() === 'GET') {
      await json(route, { data: tasks, next_cursor: null });
      return;
    }
    if (path === `/api/v1/projects/${project.id}/labels` && request.method() === 'GET') {
      await json(route, { data: [], next_cursor: null });
      return;
    }
    if (path === '/api/v1/events' && request.method() === 'GET') {
      await json(route, { data: [], next_cursor: null });
      return;
    }
    if (path === `/api/v1/projects/${project.id}/audits` && request.method() === 'GET') {
      await json(route, { data: mockRuns, next_cursor: null });
      return;
    }
    if (path.startsWith('/api/v1/audits/') && path.endsWith('/findings') && request.method() === 'GET') {
      const cursor = url.searchParams.get('cursor');
      if (cursor === 'findings-page-2') {
        await json(route, { data: [findings[2]], next_cursor: null });
      } else {
        await json(route, { data: [findings[0], findings[1]], next_cursor: 'findings-page-2' });
      }
      return;
    }
    if (path.startsWith('/api/v1/audits/') && request.method() === 'GET') {
      const auditId = decodeURIComponent(path.split('/').pop() || '');
      const run = mockRuns.find((item) => item.id === auditId);
      if (!run) {
        await json(route, { error: { code: 'not_found', message: 'Audit not found' } }, 404);
        return;
      }
      // The current client intentionally fetches run metadata and the
      // paginated /findings collection separately.
      await json(route, run);
      return;
    }
    if (path.startsWith('/api/v1/audit-findings/') && request.method() === 'PATCH') {
      const id = decodeURIComponent(path.split('/').pop() || '');
      const body = (request.postDataJSON() || {}) as Record<string, unknown>;
      const current = findings.find((finding) => finding.id === id);
      if (!current) {
        await json(route, { error: { code: 'not_found', message: 'Finding not found' } }, 404);
        return;
      }
      state.patchRequests.push({ id, body, ifMatch: request.headers()['if-match'] || null });
      const updated = {
        ...current,
        ...body,
        proposed_semantic_destination: Object.prototype.hasOwnProperty.call(body, 'proposed_semantic_destination')
          ? body.proposed_semantic_destination || null
          : current.proposed_semantic_destination,
        version: current.version + 1
      };
      await json(route, updated);
      return;
    }
    if (path.startsWith('/api/v1/tasks/') && path.endsWith('/move') && request.method() === 'POST') {
      const taskId = decodeURIComponent(path.split('/').slice(-2, -1)[0] || '');
      const body = (request.postDataJSON() || {}) as Record<string, unknown>;
      state.moveRequests.push({ taskId, body, ifMatch: request.headers()['if-match'] || null });
      const task = tasks.find((item) => item.id === taskId);
      await json(route, {
        ...(task || tasks[0]),
        column_id: body.destination_column_id,
        version: (task?.version || 1) + 1
      });
      return;
    }

    // All API requests relevant to this fixture are above. Returning a
    // structured 404 makes a missing route fail at the assertion that caused
    // it instead of leaking a network request to a real deployment.
    await json(route, { error: { code: 'mock_route_missing', message: `Unhandled mock route ${request.method()} ${path}` } }, 404);
  });
  return state;
}

async function openAuditList(page: Page): Promise<void> {
  await page.goto(`/p/${project.slug}/audits`);
  await expect(page.getByRole('heading', { name: 'Board audits', exact: true })).toBeVisible();
}

async function openAuditDetail(page: Page): Promise<void> {
  await openAuditList(page);
  await page.getByRole('button', { name: /audit-queued/ }).click();
  await expect(page).toHaveURL(new RegExp(`/p/${project.slug}/audits/audit-queued$`));
  await expect(page.getByRole('heading', { name: 'Board audit', exact: true })).toBeVisible();
}

test.describe('Board audits', () => {
  test('renders queued state and deep-link detail with paginated findings', async ({ page }) => {
    const state = await installAuditMocks(page, 'queued');
    await page.goto(`/p/${project.slug}/audits/audit-queued`);

    await expect(page.getByRole('heading', { name: 'Board audit', exact: true })).toBeVisible();
    await expect(page.getByText('Queued', { exact: true }).first()).toBeVisible();
    await expect(page.getByText('An agent is still processing this audit', { exact: false })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Finalize audit', exact: true })).toHaveCount(0);
    await expect(page.getByText('3 findings to review', { exact: true })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Looks correct', exact: true })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Needs attention', exact: true })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Move proposed', exact: true })).toBeVisible();
    await expect(page.getByText('column-pattern:ready', { exact: true })).toBeVisible();
    await expect(page.getByText('Captured v2', { exact: true }).first()).toBeVisible();
    await expect(page.getByText('Current v2', { exact: true }).first()).toBeVisible();
    const queuedFinding = page.locator('.audit-finding').filter({ hasText: 'Move this approved recommendation' });
    await expect(queuedFinding.getByRole('button', { name: 'Preview approved finding', exact: true })).toBeDisabled();
    expect(state.requestedPaths).toContain(`GET /api/v1/audits/audit-queued`);
    expect(state.requestedPaths.some((path) => path.includes('/api/v1/audits/audit-queued/findings') && path.includes('cursor=findings-page-2'))).toBeTruthy();
  });

  test('locks changed findings while accepting an authoritative review response', async ({ page }) => {
    const state = await installAuditMocks(page);
    await openAuditDetail(page);

    const changed = page.locator('.audit-finding').filter({ hasText: 'Changed after the audit' });
    await expect(changed).toContainText('Changed since audit');
    await expect(changed.getByRole('button', { name: 'Approve', exact: true })).toBeDisabled();
    await expect(changed).toContainText('Approval and apply are locked');

    const pending = page.locator('.audit-finding').filter({ hasText: 'Needs a human review' });
    await pending.getByRole('button', { name: 'Approve', exact: true }).click();
    await expect(pending.locator('.audit-review-state')).toHaveText('Approved');
    await expect(pending.getByRole('button', { name: 'Preview approved finding', exact: true })).toBeVisible();
    expect(state.patchRequests).toHaveLength(1);
    expect(state.patchRequests[0]).toMatchObject({
      id: 'finding-pending',
      ifMatch: '"v1"',
      body: { review_state: 'approved' }
    });
  });

  test('keeps preview and apply as separate explicit steps', async ({ page }) => {
    const state = await installAuditMocks(page);
    await openAuditDetail(page);

    const approved = page.locator('.audit-finding').filter({ hasText: 'Move this approved recommendation' });
    const previewButton = approved.getByRole('button', { name: 'Preview approved finding', exact: true });
    await previewButton.click();
    await expect(page.getByRole('dialog', { name: 'Preview approved finding' })).toBeVisible();
    expect(state.moveRequests).toHaveLength(0);

    await page.getByRole('button', { name: 'Continue to apply', exact: true }).click();
    const confirmation = page.getByRole('alertdialog', { name: 'Apply this move?' });
    await expect(confirmation).toBeVisible();
    expect(state.moveRequests).toHaveLength(0);

    await confirmation.getByRole('button', { name: 'Cancel', exact: true }).click();
    await expect(page.getByRole('dialog', { name: 'Preview approved finding' })).toBeVisible();
    expect(state.moveRequests).toHaveLength(0);
    await page.getByRole('button', { name: 'Continue to apply', exact: true }).click();
    await page.getByRole('alertdialog', { name: 'Apply this move?' }).getByRole('button', { name: 'Apply move', exact: true }).click();

    await expect(page.getByRole('alertdialog', { name: 'Apply this move?' })).toBeHidden();
    expect(state.moveRequests).toHaveLength(1);
    expect(state.moveRequests[0]).toMatchObject({
      taskId: 'task-approved',
      ifMatch: '"v2"',
      body: {
        destination_column_id: 'column-ready',
        expected_source_column_id: 'column-backlog',
        source: 'board_audit'
      }
    });
  });

  test('stacks at 390px, exposes accessible states, and traps/returns modal focus', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await installAuditMocks(page);
    await openAuditDetail(page);

    await expect(page.locator('.audit-finding')).toHaveCount(3);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBeTruthy();
    await expect(page.getByText('This task changed after the audit', { exact: false })).toBeVisible();

    const approved = page.locator('.audit-finding').filter({ hasText: 'Move this approved recommendation' });
    const previewButton = approved.getByRole('button', { name: 'Preview approved finding', exact: true });
    await previewButton.click();
    const preview = page.getByRole('dialog', { name: 'Preview approved finding' });
    await expect(preview).toBeVisible();
    const continueButton = preview.getByRole('button', { name: 'Continue to apply', exact: true });
    const closeButton = preview.getByRole('button', { name: 'Close preview', exact: true });
    await expect(continueButton).toBeFocused();
    await closeButton.focus();
    await page.keyboard.press('Shift+Tab');
    await expect(continueButton).toBeFocused();
    await page.keyboard.press('Tab');
    await expect(closeButton).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(preview).toBeHidden();
    await expect(previewButton).toBeFocused();
  });
});
