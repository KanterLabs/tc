import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, collectionFrom, etagForVersion, portableImportReportFromError, request } from './api';
import { ApiError } from './types';

function response(body: unknown, status = 200, headers: Record<string, string> = {}): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers }
  });
}

afterEach(() => vi.restoreAllMocks());

describe('public API client', () => {
  it('uses the human Codex subscription lifecycle endpoints', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(response({ connected: false, requires_openai_auth: true }))
      .mockResolvedValueOnce(response({ login_id: 'login-1', verification_url: 'https://auth.openai.test/device', user_code: 'ABCD' }))
      .mockResolvedValueOnce(response({ status: 'canceled' }))
      .mockResolvedValueOnce(response({ ok: true }));
    await api.codexAccount(true);
    await api.startCodexLogin();
    await api.cancelCodexLogin('login-1');
    await api.logoutCodex();
    expect(String(fetchMock.mock.calls[0][0])).toContain('/api/v1/codex/account?refresh=true');
    expect((fetchMock.mock.calls[1][1] as RequestInit).method).toBe('POST');
    expect(JSON.parse(String((fetchMock.mock.calls[2][1] as RequestInit).body))).toEqual({ login_id: 'login-1' });
    expect(String(fetchMock.mock.calls[3][0])).toContain('/api/v1/codex/logout');
  });

  it('requests a project-scoped Luna draft without creating a task', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(response({
      title: 'Draft', description: 'Description', acceptance_criteria: ['Outcome'], priority: 'normal', rationale: 'Reason', supporting_task_keys: []
    }));
    await api.draftTask('project/one', 'rough idea');
    const [url, init] = fetchMock.mock.calls[0];
    expect(String(url)).toContain('/api/v1/projects/project%2Fone/task-draft');
    expect((init as RequestInit).method).toBe('POST');
    expect(JSON.parse(String((init as RequestInit).body))).toEqual({ query: 'rough idea' });
  });

  it('uses the version ETag format required by task mutations', () => {
    expect(etagForVersion(14)).toBe('"v14"');
  });

  it('sends precise reorder anchors with the guarded task contract', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(response({ id: 'task-1', version: 4 }));
    await api.reorderTask('task-1', {
      destination_column_id: 'ready',
      expected_source_column_id: 'backlog',
      before_task_id: 'task-3',
      after_task_id: 'task-2',
      placement: 'between',
      expected_source_ordering_version: 2,
      expected_destination_ordering_version: 8,
      source: 'board'
    }, 3);
    const [url, init] = fetchMock.mock.calls[0];
    expect(String(url)).toContain('/api/v1/tasks/task-1/reorder');
    expect((init as RequestInit).method).toBe('POST');
    expect((init?.headers as Headers).get('If-Match')).toBe('"v3"');
    expect(JSON.parse(String((init as RequestInit).body))).toMatchObject({
      before_task_id: 'task-3', after_task_id: 'task-2', placement: 'between'
    });
  });

  it('requests a board timeline with stable server-side filters', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(response({ data: [], next_cursor: null }));
    await api.listProjectTimeline('project/one', { before: 'cursor/2', limit: 25, kind: 'agent_progress' });
    const url = String(fetchMock.mock.calls[0][0]);
    expect(url).toContain('/api/v1/projects/project%2Fone/timeline?');
    expect(url).toContain('before=cursor%2F2');
    expect(url).toContain('limit=25');
    expect(url).toContain('kind=agent_progress');
  });

  it('reads one canonical comment for live timeline reconciliation', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(response({
      id: 'comment-1',
      task_id: 'task-1',
      actor_id: 'actor-1',
      body: 'updated',
      version: 2,
      created_at: '2026-01-01T00:00:00Z',
      updated_at: '2026-01-02T00:00:00Z'
    }));
    await api.getComment('task/1', 'comment/1');
    expect(String(fetchMock.mock.calls[0][0])).toContain('/api/v1/tasks/task%2F1/comments/comment%2F1');
    expect((fetchMock.mock.calls[0][1] as RequestInit).method).toBeUndefined();
  });

  it('normalizes collection envelopes and arrays', () => {
    expect(collectionFrom([{ id: 'one' }])).toEqual({ data: [{ id: 'one' }] });
    expect(collectionFrom({ data: [{ id: 'two' }], next_cursor: '3' })).toEqual({ data: [{ id: 'two' }], next_cursor: '3' });
  });

  it('sends credentials and query filters through the documented route', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(response({ data: [], next_cursor: null }));
    await api.listTasks('project/one', { q: 'ship', priority: 'high', sort: 'updated_at', order: 'desc', cursor: 'tc1.next', limit: 25 });
    expect(fetchMock).toHaveBeenCalledOnce();
    const [url, init] = fetchMock.mock.calls[0];
    expect(String(url)).toContain('/api/v1/projects/project%2Fone/tasks?');
    expect(String(url)).toContain('q=ship');
    expect(String(url)).toContain('priority=high');
    expect(String(url)).toContain('sort=updated_at');
    expect(String(url)).toContain('order=desc');
    expect(String(url)).toContain('cursor=tc1.next');
    expect((init as RequestInit).credentials).toBe('include');
    expect((init as RequestInit).headers).toBeInstanceOf(Headers);
  });

  it('forwards live-agent filters on task and issue collections', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(() => Promise.resolve(response({ data: [], next_cursor: null })));

    await api.listTasks('project-1', { agent_state: 'waiting', action_needed: true });
    await api.listIssues({ agent_state: 'verifying', action_needed: false });

    const taskQuery = new URL(String(fetchMock.mock.calls[0][0]), 'http://localhost').searchParams;
    expect(taskQuery.get('agent_state')).toBe('waiting');
    expect(taskQuery.get('action_needed')).toBe('true');
    const issueQuery = new URL(String(fetchMock.mock.calls[1][0]), 'http://localhost').searchParams;
    expect(issueQuery.get('agent_state')).toBe('verifying');
    expect(issueQuery.get('action_needed')).toBe('false');
  });

  it('passes issue filters and mutation payloads through the bug endpoints', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(response({ data: [], next_cursor: null }))
      .mockResolvedValueOnce(response({ id: 'bug-1', version: 3 }))
      .mockResolvedValueOnce(response({ id: 'bug-1', version: 4 }))
      .mockResolvedValueOnce(response({ id: 'bug-1', version: 5 }));

    await api.listTasks('project-1', { kind: 'bug', severity: 's1', reporter: 'alex', resolution: 'fixed' });
    expect(String(fetchMock.mock.calls[0][0])).toContain('kind=bug');
    expect(String(fetchMock.mock.calls[0][0])).toContain('severity=s1');
    expect(String(fetchMock.mock.calls[0][0])).toContain('reporter=alex');
    expect(String(fetchMock.mock.calls[0][0])).toContain('resolution=fixed');

    await api.triageTask('bug-1', 2, { severity: 's1', priority: 'urgent', assignee: 'alex' });
    await api.resolveTask('bug-1', 3, { resolution: 'fixed', note: 'Patched in release' });
    await api.reopenTask('bug-1', 4, { reason: 'Regression returned' });

    expect(String(fetchMock.mock.calls[1][0])).toContain('/api/v1/tasks/bug-1/triage');
    expect(JSON.parse(String((fetchMock.mock.calls[1][1] as RequestInit).body))).toEqual({ severity: 's1', priority: 'urgent', assignee: 'alex' });
    expect((fetchMock.mock.calls[1][1]?.headers as Headers).get('If-Match')).toBe('"v2"');
    expect(String(fetchMock.mock.calls[2][0])).toContain('/api/v1/tasks/bug-1/resolve');
    expect(JSON.parse(String((fetchMock.mock.calls[2][1] as RequestInit).body))).toEqual({ resolution: 'fixed', note: 'Patched in release' });
    expect(String(fetchMock.mock.calls[3][0])).toContain('/api/v1/tasks/bug-1/reopen');
  });

  it('guards claim, renew, and release actions with the task version', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(response({ id: 'task-1', version: 6 }))
      .mockResolvedValueOnce(response({ id: 'task-1', version: 7 }))
      .mockResolvedValueOnce(response({ id: 'task-1', version: 8 }));

    await api.claimTask('task-1', 5, 120);
    await api.renewTask('task-1', 6, 240);
    await api.releaseTask('task-1', 7);

    const claim = fetchMock.mock.calls[0];
    expect(String(claim[0])).toContain('/api/v1/tasks/task-1/claim');
    expect((claim[1]?.headers as Headers).get('If-Match')).toBe('"v5"');
    expect((claim[1]?.headers as Headers).get('Idempotency-Key')).toBeTruthy();
    expect(JSON.parse(String((claim[1] as RequestInit).body))).toEqual({ lease_seconds: 120 });

    const renew = fetchMock.mock.calls[1];
    expect(String(renew[0])).toContain('/api/v1/tasks/task-1/renew');
    expect((renew[1]?.headers as Headers).get('If-Match')).toBe('"v6"');
    expect(JSON.parse(String((renew[1] as RequestInit).body))).toEqual({ lease_seconds: 240 });

    const release = fetchMock.mock.calls[2];
    expect(String(release[0])).toContain('/api/v1/tasks/task-1/release');
    expect((release[1]?.headers as Headers).get('If-Match')).toBe('"v7"');
    expect((release[1]?.headers as Headers).get('Idempotency-Key')).toBeTruthy();
  });

  it('supports the global issue collection route', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(response({ data: [], next_cursor: null }));
    await api.listIssues({ project: 'project-1', severity: 'untriaged', resolution: 'unresolved', limit: 25 });
    expect(String(fetchMock.mock.calls[0][0])).toContain('/api/v1/issues?');
    expect(String(fetchMock.mock.calls[0][0])).toContain('project=project-1');
    expect(String(fetchMock.mock.calls[0][0])).toContain('severity=untriaged');
    expect(String(fetchMock.mock.calls[0][0])).toContain('resolution=unresolved');
  });

  it('loads bounded issue metrics and scalar sidebar counts', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(response({ reopened: 3, window_days: 7, since: '2026-09-01T00:00:00Z', as_of: '2026-09-03T00:00:00Z' }))
      .mockResolvedValueOnce(response({ issues: 4, my_work: 2, view: 'assigned' }));

    await expect(api.issueMetrics({ project: 'project-1' })).resolves.toMatchObject({ reopened: 3, window_days: 7 });
    await expect(api.sidebarCounts({ view: 'assigned' })).resolves.toMatchObject({ issues: 4, my_work: 2, view: 'assigned' });

    const metricsQuery = new URL(String(fetchMock.mock.calls[0][0]), 'http://localhost').searchParams;
    expect(metricsQuery.get('project')).toBe('project-1');
    expect(String(fetchMock.mock.calls[0][0])).toContain('/api/v1/issues/metrics?');
    const countsQuery = new URL(String(fetchMock.mock.calls[1][0]), 'http://localhost').searchParams;
    expect(countsQuery.get('view')).toBe('assigned');
    expect(String(fetchMock.mock.calls[1][0])).toContain('/api/v1/sidebar-counts?');
  });

  it('passes global search filters and saved-view lifecycle payloads', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(response({ data: [{ id: 'task-1' }], next_cursor: 'next' }))
      .mockResolvedValueOnce(response({ data: [{ id: 'view-1' }], next_cursor: null }))
      .mockResolvedValueOnce(response({ id: 'view-1', owner_id: 'actor-1', name: 'Urgent', filters: {}, sort: [], shared: true }))
      .mockResolvedValueOnce(response({ id: 'view-1', owner_id: 'actor-1', name: 'Updated', filters: {}, sort: [], shared: false }))
      .mockResolvedValueOnce(response(undefined, 204));

    await api.search({ q: 'needle', title: 'ship', claim_owner: 'agent-1', due_from: '2026-09-01T00:00:00Z', sort: 'updated_at:desc', limit: 25 });
    const searchURL = new URL(String(fetchMock.mock.calls[0][0]), 'http://localhost');
    expect(searchURL.pathname).toBe('/api/v1/search');
    expect(searchURL.searchParams.get('q')).toBe('needle');
    expect(searchURL.searchParams.get('claim_owner')).toBe('agent-1');
    expect(searchURL.searchParams.get('limit')).toBe('25');

    await api.listSavedViews({ limit: 25 });
    await api.getSavedView('view/1');
    await api.patchSavedView('view/1', { name: 'Updated', shared: false });
    await api.deleteSavedView('view/1');
    expect(String(fetchMock.mock.calls[1][0])).toContain('/api/v1/views?limit=25');
    expect(String(fetchMock.mock.calls[2][0])).toContain('/api/v1/views/view%2F1');
    expect((fetchMock.mock.calls[3][1] as RequestInit).method).toBe('PATCH');
    expect(JSON.parse(String((fetchMock.mock.calls[3][1] as RequestInit).body))).toEqual({ name: 'Updated', shared: false });
    expect((fetchMock.mock.calls[4][1] as RequestInit).method).toBe('DELETE');
  });

  it('collects every project page before rendering the project switcher', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(response({ data: [{ id: 'one' }], next_cursor: 'cursor-2' }))
      .mockResolvedValueOnce(response({ data: [{ id: 'two' }], next_cursor: null }));

    await expect(api.listAllProjects()).resolves.toEqual({
      data: [{ id: 'one' }, { id: 'two' }],
      next_cursor: null
    });
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(String(fetchMock.mock.calls[1][0])).toContain('cursor=cursor-2');
  });

  it('loads archived administration resources and sends guarded project/column mutations', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(response({ data: [{ id: 'project-1', version: 3, archived_at: null }], next_cursor: null }))
      .mockResolvedValueOnce(response({ data: [{ id: 'column-1', version: 4, archived_at: null }], next_cursor: null }))
      .mockResolvedValueOnce(response({ id: 'project-1', version: 4 }))
      .mockResolvedValueOnce(response({ id: 'column-1', version: 5 }));

    await api.listAllProjects({ includeArchived: true });
    await api.listAllColumns('project-1', { includeArchived: true });
    await api.patchProject('project-1', { name: 'Renamed', archived: true }, 3);
    await api.patchColumn('column-1', { name: 'Doing', position: 1, archived: true }, 4);

    const projectQuery = new URL(String(fetchMock.mock.calls[0][0]), 'http://localhost').searchParams;
    expect(projectQuery.get('archived')).toBe('true');
    const columnQuery = new URL(String(fetchMock.mock.calls[1][0]), 'http://localhost').searchParams;
    expect(columnQuery.get('archived')).toBe('true');
    const projectInit = fetchMock.mock.calls[2][1] as RequestInit;
    expect((projectInit.headers as Headers).get('If-Match')).toBe('"v3"');
    expect(JSON.parse(String(projectInit.body))).toEqual({ name: 'Renamed', archived: true });
    const columnInit = fetchMock.mock.calls[3][1] as RequestInit;
    expect((columnInit.headers as Headers).get('If-Match')).toBe('"v4"');
    expect(JSON.parse(String(columnInit.body))).toEqual({ name: 'Doing', position: 1, archived: true });
  });

  it('rejects a repeated pagination cursor instead of looping forever', async () => {
    vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(response({ data: [], next_cursor: 'cursor-2' }))
      .mockResolvedValueOnce(response({ data: [], next_cursor: 'cursor-2' }));

    await expect(api.listAllAgents()).rejects.toMatchObject({
      name: 'ApiError',
      code: 'invalid_pagination_cursor'
    });
  });

  it('adds If-Match and idempotency headers for a task patch', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(response({ id: 'task-1', version: 3 }));
    await api.patchTask('task-1', { title: 'Updated' }, 2);
    const init = fetchMock.mock.calls[0][1] as RequestInit;
    const headers = init.headers as Headers;
    expect(headers.get('If-Match')).toBe('"v2"');
    expect(headers.get('Idempotency-Key')).toMatch(/^helm-|^[0-9a-f-]{20,}$/);
    expect(JSON.parse(String(init.body))).toEqual({ title: 'Updated' });
  });

  it('restores a soft-deleted task with version and idempotency guards', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(response({ id: 'task-1', version: 4 }));
    await api.restoreTask('task/1', 3);
    const [url, init] = fetchMock.mock.calls[0];
    expect(String(url)).toContain('/api/v1/tasks/task%2F1/restore');
    expect((init as RequestInit).method).toBe('POST');
    expect((init?.headers as Headers).get('If-Match')).toBe('"v3"');
    expect((init?.headers as Headers).get('Idempotency-Key')).toMatch(/^helm-|^[0-9a-f-]{20,}$/);
    expect((init as RequestInit).body).toBeUndefined();
  });

  it('reads and mutates task dependencies with encoded references and guarded headers', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(response({ prerequisites: [], dependents: [] }))
      .mockResolvedValueOnce(response({ id: 'task-1', version: 4 }))
      .mockResolvedValueOnce(response({ id: 'task-1', version: 5 }));

    await api.getTaskDependencies('OPS/1');
    await api.addTaskDependency('OPS/1', 'OPS-2', 3);
    await api.removeTaskDependency('OPS/1', 'OPS/2', 4);

    expect(String(fetchMock.mock.calls[0][0])).toContain('/api/v1/tasks/OPS%2F1/dependencies');
    const add = fetchMock.mock.calls[1];
    expect(String(add[0])).toContain('/api/v1/tasks/OPS%2F1/dependencies');
    expect((add[1] as RequestInit).method).toBe('POST');
    expect(JSON.parse(String((add[1] as RequestInit).body))).toEqual({ prerequisite: 'OPS-2' });
    expect(((add[1] as RequestInit).headers as Headers).get('If-Match')).toBe('"v3"');
    expect(((add[1] as RequestInit).headers as Headers).get('Idempotency-Key')).toBeTruthy();

    const remove = fetchMock.mock.calls[2];
    expect(String(remove[0])).toContain('/api/v1/tasks/OPS%2F1/dependencies/OPS%2F2');
    expect((remove[1] as RequestInit).method).toBe('DELETE');
    expect(((remove[1] as RequestInit).headers as Headers).get('If-Match')).toBe('"v4"');
    expect(((remove[1] as RequestInit).headers as Headers).get('Idempotency-Key')).toBeTruthy();
  });

  it('forwards dependency readiness filters to task and My Work collections', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(() => Promise.resolve(response({ data: [], next_cursor: null })));
    await api.listTasks('project-1', { dependency: 'blocked' });
    await api.myWork({ dependency: 'ready' });

    expect(new URL(String(fetchMock.mock.calls[0][0]), 'http://localhost').searchParams.get('dependency')).toBe('blocked');
    expect(new URL(String(fetchMock.mock.calls[1][0]), 'http://localhost').searchParams.get('dependency')).toBe('ready');
  });

  it('publishes progress and blocks with task version and idempotency protection', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(response({ id: 'task-1', version: 8 }))
      .mockResolvedValueOnce(response({ id: 'task-1', version: 9 }));
    const input = {
      operation_id: 'run-1',
      state: 'working' as const,
      phase: 'Implementation',
      summary: 'Added the API client',
      next_action: 'Run checks',
      checkpoint_refs: ['api'],
      checkpoint_completed: 1,
      checkpoint_total: 2
    };

    await api.publishAgentWork('task/1', 7, input);
    await api.blockTask('task-1', 8, 'Waiting for credentials');

    const progressCall = fetchMock.mock.calls[0];
    expect(String(progressCall[0])).toContain('/api/v1/tasks/task%2F1/progress');
    const progressInit = progressCall[1] as RequestInit;
    expect(JSON.parse(String(progressInit.body))).toEqual(input);
    expect((progressInit.headers as Headers).get('If-Match')).toBe('"v7"');
    expect((progressInit.headers as Headers).get('Idempotency-Key')).toMatch(/^helm-|^[0-9a-f-]{20,}$/);

    const blockCall = fetchMock.mock.calls[1];
    const blockInit = blockCall[1] as RequestInit;
    expect(String(blockCall[0])).toContain('/api/v1/tasks/task-1/block');
    expect(JSON.parse(String(blockInit.body))).toEqual({ reason: 'Waiting for credentials' });
    expect((blockInit.headers as Headers).get('If-Match')).toBe('"v8"');
    expect((blockInit.headers as Headers).get('Idempotency-Key')).toMatch(/^helm-|^[0-9a-f-]{20,}$/);
  });

  it('lists a typed task timeline with stable cursor and kind filters', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(response({ data: [], next_cursor: 'older-cursor' }));
    await api.listTaskTimeline('task/1', { before: 'cursor/2', limit: 25, kind: 'comment' });
    expect(fetchMock).toHaveBeenCalledOnce();
    const url = new URL(String(fetchMock.mock.calls[0][0]), 'http://localhost');
    expect(url.pathname).toBe('/api/v1/tasks/task%2F1/timeline');
    expect(url.searchParams.get('before')).toBe('cursor/2');
    expect(url.searchParams.get('limit')).toBe('25');
    expect(url.searchParams.get('kind')).toBe('comment');
  });

  it('edits and deletes comments with encoded references and optimistic headers', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(response({ id: 'comment-1', task_id: 'task/1', body: 'updated', version: 2 }))
      .mockResolvedValueOnce(response(undefined, 200, { ETag: '"v3"' }));

    await api.patchComment('task/1', 'comment/1', 'updated', 1);
    await api.deleteComment('task/1', 'comment/1', 2);

    const patchCall = fetchMock.mock.calls[0];
    expect(String(patchCall[0])).toBe('/api/v1/tasks/task%2F1/comments/comment%2F1');
    expect((patchCall[1] as RequestInit).method).toBe('PATCH');
    expect(JSON.parse(String((patchCall[1] as RequestInit).body))).toEqual({ body: 'updated' });
    expect(((patchCall[1] as RequestInit).headers as Headers).get('If-Match')).toBe('"v1"');
    expect(((patchCall[1] as RequestInit).headers as Headers).get('Idempotency-Key')).toBeTruthy();

    const deleteCall = fetchMock.mock.calls[1];
    expect(String(deleteCall[0])).toBe('/api/v1/tasks/task%2F1/comments/comment%2F1');
    expect((deleteCall[1] as RequestInit).method).toBe('DELETE');
    expect(((deleteCall[1] as RequestInit).headers as Headers).get('If-Match')).toBe('"v2"');
    expect(((deleteCall[1] as RequestInit).headers as Headers).get('Idempotency-Key')).toBeTruthy();
  });

  it('supports assigned/live work views and preserves filters while collecting pages', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(response({ data: [], next_cursor: null }))
      .mockResolvedValueOnce(response({ data: [{ id: 'one' }], next_cursor: 'next' }))
      .mockResolvedValueOnce(response({ data: [{ id: 'two' }], next_cursor: null }));

    await api.myWork({ project: 'project-1', view: 'live', agent_state: 'verifying', action_needed: false, limit: 25 });
    const liveQuery = new URL(String(fetchMock.mock.calls[0][0]), 'http://localhost').searchParams;
    expect(liveQuery.get('project')).toBe('project-1');
    expect(liveQuery.get('view')).toBe('live');
    expect(liveQuery.get('agent_state')).toBe('verifying');
    expect(liveQuery.get('action_needed')).toBe('false');
    expect(liveQuery.get('limit')).toBe('25');

    await expect(api.allMyWork({ view: 'assigned', action_needed: true })).resolves.toEqual({
      data: [{ id: 'one' }, { id: 'two' }],
      next_cursor: null
    });
    const assignedQuery = new URL(String(fetchMock.mock.calls[1][0]), 'http://localhost').searchParams;
    expect(assignedQuery.get('view')).toBe('assigned');
    expect(assignedQuery.get('action_needed')).toBe('true');
    expect(assignedQuery.get('limit')).toBe('200');
    const nextQuery = new URL(String(fetchMock.mock.calls[2][0]), 'http://localhost').searchParams;
    expect(nextQuery.get('cursor')).toBe('next');
    expect(nextQuery.get('view')).toBe('assigned');
    expect(nextQuery.get('action_needed')).toBe('true');
  });

  it('lists and starts project-scoped audits without applying recommendations', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(response({ data: [{ id: 'audit-1', project_id: 'project-1', status: 'complete' }], next_cursor: 'next' }))
      .mockResolvedValueOnce(response({ data: [{ id: 'audit-2', project_id: 'project-1', status: 'running' }], next_cursor: null }))
      .mockResolvedValueOnce(response({ id: 'audit-3', project_id: 'project-1', status: 'queued' }));

    await expect(api.listAllAudits('project-1')).resolves.toMatchObject({
      data: [
        { id: 'audit-1', status: 'complete' },
        { id: 'audit-2', status: 'running' }
      ],
      next_cursor: null
    });
    const firstQuery = new URL(String(fetchMock.mock.calls[0][0]), 'http://localhost').searchParams;
    expect(firstQuery.get('limit')).toBe('200');
    expect(firstQuery.get('cursor')).toBeNull();
    expect(new URL(String(fetchMock.mock.calls[1][0]), 'http://localhost').searchParams.get('cursor')).toBe('next');

    await api.createAudit('project-1', { scope: 'board', status: 'queued' });
    const createCall = fetchMock.mock.calls[2];
    expect(String(createCall[0])).toContain('/api/v1/projects/project-1/audits');
    expect((createCall[1]?.method)).toBe('POST');
    expect(JSON.parse(String((createCall[1] as RequestInit).body))).toEqual({ scope: 'board', status: 'queued' });
    expect((createCall[1]?.headers as Headers).get('Idempotency-Key')).toMatch(/^helm-|^[0-9a-f-]{20,}$/);
  });

  it('loads audit metadata and every bounded finding page', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(response({ id: 'audit-1', project_id: 'project-1', status: 'complete', finding_count: 2 }))
      .mockResolvedValueOnce(response({ data: [{ id: 'finding-1', audit_id: 'audit-1' }], next_cursor: 'findings-next' }))
      .mockResolvedValueOnce(response({ data: [{ id: 'finding-2', audit_id: 'audit-1' }], next_cursor: null }));

    await expect(api.getAudit('audit-1')).resolves.toMatchObject({
      id: 'audit-1',
      finding_count: 2,
      findings: [{ id: 'finding-1' }, { id: 'finding-2' }]
    });
    expect(String(fetchMock.mock.calls[0][0])).toContain('/api/v1/audits/audit-1');
    const firstFindings = new URL(String(fetchMock.mock.calls[1][0]), 'http://localhost');
    const secondFindings = new URL(String(fetchMock.mock.calls[2][0]), 'http://localhost');
    expect(firstFindings.pathname).toContain('/api/v1/audits/audit-1/findings');
    expect(firstFindings.searchParams.get('limit')).toBe('200');
    expect(firstFindings.searchParams.get('cursor')).toBeNull();
    expect(secondFindings.searchParams.get('cursor')).toBe('findings-next');
  });

  it('guards audit finding review and task reconciliation with versions', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(response({ id: 'finding-1', review_state: 'approved', version: 4 }))
      .mockResolvedValueOnce(response({ id: 'task-1', column_id: 'ready-column', version: 8 }));

    await api.patchAuditFinding('finding-1', {
      review_state: 'approved',
      proposed_semantic_destination: 'ready'
    }, 3);
    await api.moveTask('task-1', {
      destination_column_id: 'ready-column',
      expected_source_column_id: 'backlog-column',
      source: 'board_audit',
      reason: 'Captured in the audit'
    }, 7);

    const reviewInit = fetchMock.mock.calls[0][1] as RequestInit;
    expect(String(fetchMock.mock.calls[0][0])).toContain('/api/v1/audit-findings/finding-1');
    expect(JSON.parse(String(reviewInit.body))).toEqual({ review_state: 'approved', proposed_semantic_destination: 'ready' });
    expect((reviewInit.headers as Headers).get('If-Match')).toBe('"v3"');
    expect((reviewInit.headers as Headers).get('Idempotency-Key')).toMatch(/^helm-|^[0-9a-f-]{20,}$/);

    const moveInit = fetchMock.mock.calls[1][1] as RequestInit;
    expect(String(fetchMock.mock.calls[1][0])).toContain('/api/v1/tasks/task-1/move');
    expect(JSON.parse(String(moveInit.body))).toMatchObject({
      destination_column_id: 'ready-column',
      expected_source_column_id: 'backlog-column',
      source: 'board_audit'
    });
    expect((moveInit.headers as Headers).get('If-Match')).toBe('"v7"');
    expect((moveInit.headers as Headers).get('Idempotency-Key')).toMatch(/^helm-|^[0-9a-f-]{20,}$/);
  });

  it('finalizes an audit run as an explicit lifecycle mutation', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(response({ id: 'audit-1', status: 'partial' }));
    await api.finalizeAudit('audit-1', 'partial');
    const [url, init] = fetchMock.mock.calls[0];
    expect(String(url)).toContain('/api/v1/audits/audit-1/finalize');
    expect(init?.method).toBe('POST');
    expect(JSON.parse(String((init as RequestInit).body))).toEqual({ status: 'partial' });
    expect((init?.headers as Headers).get('Idempotency-Key')).toMatch(/^helm-|^[0-9a-f-]{20,}$/);
  });

  it('uses the versioned portable archive and board navigation endpoints', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(response({ format: 'helm.portable', version: 1, projects: [] }))
      .mockResolvedValueOnce(response({ dry_run: true, format: 'helm.portable', version: 1, counts: {}, remaps: [], warnings: [], errors: [] }))
      .mockResolvedValueOnce(response({ data: [{ id: 'project-1', project_id: 'project-1', name: 'Board', slug: 'board', default: true, enabled: true }] }));

    const archive = { format: 'helm.portable', version: 1, projects: [] };
    await api.exportProject('project/one');
    await api.importPortable(archive, { targetProject: 'project/one', conflict: 'fail', dryRun: true });
    await api.listBoards('project/one');

    const exportCall = fetchMock.mock.calls[0];
    expect(String(exportCall[0])).toContain('/api/v1/projects/project%2Fone/export');
    expect((exportCall[1] as RequestInit).method).toBeUndefined();

    const importCall = fetchMock.mock.calls[1];
    expect(String(importCall[0])).toBe('/api/v1/import');
    const importInit = importCall[1] as RequestInit;
    expect(importInit.method).toBe('POST');
    expect(JSON.parse(String(importInit.body))).toEqual({
      archive,
      target_project: 'project/one',
      conflict: 'fail',
      dry_run: true
    });
    expect((importInit.headers as Headers).get('Idempotency-Key')).toBeNull();

    expect(String(fetchMock.mock.calls[2][0])).toContain('/api/v1/projects/project%2Fone/boards');
  });

  it('sends Trello imports through the isolated adapter route with guards', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(response({
      dry_run: true,
      format: 'helm.portable',
      version: 1,
      counts: {},
      remaps: [],
      warnings: ['Trello memberships and permissions are unsupported'],
      errors: []
    }));
    const board = { id: 'trello-1', name: 'Trello board' };
    await api.importTrello(board, { targetProject: 'project/one', dryRun: true, conflict: 'remap' });

    const [url, init] = fetchMock.mock.calls[0];
    expect(String(url)).toContain('/api/v1/import/trello?');
    expect(new URL(String(url), 'http://localhost').searchParams.get('target_project')).toBe('project/one');
    expect(new URL(String(url), 'http://localhost').searchParams.get('dry_run')).toBe('true');
    expect(new URL(String(url), 'http://localhost').searchParams.get('conflict')).toBe('remap');
    expect((init as RequestInit).method).toBe('POST');
    expect(JSON.parse(String((init as RequestInit).body))).toEqual(board);
    expect(((init as RequestInit).headers as Headers).get('Idempotency-Key')).toBeNull();
  });

  it('turns the standard error envelope into ApiError', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(response({ error: { code: 'stale_task', message: 'Task is stale', details: { current: { version: 4 } } } }, 409));
    await expect(request('/tasks/task-1', { method: 'PATCH', body: { title: 'x' } })).rejects.toMatchObject({
      name: 'ApiError',
      status: 409,
      code: 'stale_task',
      message: 'Task is stale'
    });
  });

  it('preserves every portable validation issue and remap for the import review UI', async () => {
    const report = portableImportReportFromError(new ApiError('Portable archive is invalid', 400, 'invalid_request', {
      format: 'helm.portable',
      version: 1,
      dry_run: true,
      conflict: 'remap',
      counts: { tasks_created: 0 },
      remaps: [{ entity: 'task', source: 'task-1', target: 'task-2', field: 'number', reason: 'number conflict' }],
      warnings: ['A warning'],
      errors: [
        { entity: 'task', id: 'task-1', field: 'column_id', message: 'column is missing' },
        { entity: 'task_link', id: 'task-1', field: 'target_task_id', message: 'target conflicts' }
      ]
    }));
    expect(report?.errors).toHaveLength(2);
    expect(report?.errors[1]).toMatchObject({ entity: 'task_link', field: 'target_task_id', message: 'target conflicts' });
    expect(report?.remaps).toEqual([{ entity: 'task', source: 'task-1', target: 'task-2', field: 'number', reason: 'number conflict' }]);
  });
});
