import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, collectionFrom, etagForVersion, request } from './api';
import { ApiError } from './types';

function response(body: unknown, status = 200, headers: Record<string, string> = {}): Response {
  return new Response(body === undefined ? '' : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers }
  });
}

afterEach(() => vi.restoreAllMocks());

describe('public API client', () => {
  it('uses the version ETag format required by task mutations', () => {
    expect(etagForVersion(14)).toBe('"v14"');
  });

  it('normalizes collection envelopes and arrays', () => {
    expect(collectionFrom([{ id: 'one' }])).toEqual({ data: [{ id: 'one' }] });
    expect(collectionFrom({ data: [{ id: 'two' }], next_cursor: '3' })).toEqual({ data: [{ id: 'two' }], next_cursor: '3' });
  });

  it('sends credentials and query filters through the documented route', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(response({ data: [], next_cursor: null }));
    await api.listTasks('project/one', { q: 'ship', priority: 'high', limit: 25 });
    expect(fetchMock).toHaveBeenCalledOnce();
    const [url, init] = fetchMock.mock.calls[0];
    expect(String(url)).toContain('/api/v1/projects/project%2Fone/tasks?');
    expect(String(url)).toContain('q=ship');
    expect(String(url)).toContain('priority=high');
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

  it('supports the global issue collection route', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(response({ data: [], next_cursor: null }));
    await api.listIssues({ project: 'project-1', severity: 'untriaged', resolution: 'unresolved', limit: 25 });
    expect(String(fetchMock.mock.calls[0][0])).toContain('/api/v1/issues?');
    expect(String(fetchMock.mock.calls[0][0])).toContain('project=project-1');
    expect(String(fetchMock.mock.calls[0][0])).toContain('severity=untriaged');
    expect(String(fetchMock.mock.calls[0][0])).toContain('resolution=unresolved');
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
    expect(headers.get('Idempotency-Key')).toMatch(/^roadmap-|^[0-9a-f-]{20,}$/);
    expect(JSON.parse(String(init.body))).toEqual({ title: 'Updated' });
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
    expect((progressInit.headers as Headers).get('Idempotency-Key')).toMatch(/^roadmap-|^[0-9a-f-]{20,}$/);

    const blockCall = fetchMock.mock.calls[1];
    const blockInit = blockCall[1] as RequestInit;
    expect(String(blockCall[0])).toContain('/api/v1/tasks/task-1/block');
    expect(JSON.parse(String(blockInit.body))).toEqual({ reason: 'Waiting for credentials' });
    expect((blockInit.headers as Headers).get('If-Match')).toBe('"v8"');
    expect((blockInit.headers as Headers).get('Idempotency-Key')).toMatch(/^roadmap-|^[0-9a-f-]{20,}$/);
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

  it('turns the standard error envelope into ApiError', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(response({ error: { code: 'stale_task', message: 'Task is stale', details: { current: { version: 4 } } } }, 409));
    await expect(request('/tasks/task-1', { method: 'PATCH', body: { title: 'x' } })).rejects.toMatchObject({
      name: 'ApiError',
      status: 409,
      code: 'stale_task',
      message: 'Task is stale'
    });
  });
});
