import { afterEach, describe, expect, it, vi } from 'vitest';
import { api } from './api';

function response(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' }
  });
}

afterEach(() => vi.restoreAllMocks());

describe('hierarchy API client', () => {
  it('uses encoded references and guarded parent-edge headers', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(response({ parent: null, children: [], ancestors: [], descendants: [], summary: {} }))
      .mockResolvedValueOnce(response({ data: [], next_cursor: null }))
      .mockResolvedValueOnce(response({ id: 'child', version: 2 }))
      .mockResolvedValueOnce(response({ id: 'child', version: 3 }))
      .mockResolvedValueOnce(response({ id: 'child', version: 4 }));

    await api.getTaskHierarchy('OPS/1');
    await api.listTaskChildren('OPS/1');
    await api.setTaskParent('OPS/1', 'OPS/2', 1);
    await api.clearTaskParent('OPS/1', 2);
    await api.removeTaskChild('OPS/1', 'OPS/2', 3);

    expect(String(fetchMock.mock.calls[0][0])).toContain('/api/v1/tasks/OPS%2F1/hierarchy');
    expect(String(fetchMock.mock.calls[1][0])).toContain('/api/v1/tasks/OPS%2F1/children');
    const set = fetchMock.mock.calls[2];
    expect(String(set[0])).toContain('/api/v1/tasks/OPS%2F1/parent');
    expect((set[1] as RequestInit).method).toBe('POST');
    expect(JSON.parse(String((set[1] as RequestInit).body))).toEqual({ parent: 'OPS/2' });
    expect(((set[1] as RequestInit).headers as Headers).get('If-Match')).toBe('"v1"');
    expect(((set[1] as RequestInit).headers as Headers).get('Idempotency-Key')).toBeTruthy();

    const clear = fetchMock.mock.calls[3];
    expect((clear[1] as RequestInit).method).toBe('DELETE');
    expect(((clear[1] as RequestInit).headers as Headers).get('If-Match')).toBe('"v2"');
    const remove = fetchMock.mock.calls[4];
    expect(String(remove[0])).toContain('/api/v1/tasks/OPS%2F1/children/OPS%2F2');
    expect(((remove[1] as RequestInit).headers as Headers).get('If-Match')).toBe('"v3"');
  });
});
