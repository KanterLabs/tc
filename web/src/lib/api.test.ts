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
