import {
  ApiError,
  type ActivityEvent,
  type Actor,
  type Agent,
  type ApiErrorShape,
  type ApiToken,
  type AuthStatus,
  type Collection,
  type Column,
  type Comment,
  type Label,
  type Project,
  type RoadmapSummary,
  type Task,
  type TaskPatch
} from './types';

export const API_PREFIX = '/api/v1';

export type RequestOptions = Omit<RequestInit, 'body'> & {
  body?: unknown;
  idempotencyKey?: string;
  ifMatch?: string | number;
};

export interface TaskListParams {
  state?: string;
  column?: string;
  priority?: string;
  label?: string;
  assignee?: string;
  q?: string;
  updated_after?: string;
  cursor?: string;
  limit?: number;
}

function pathWithQuery(path: string, query?: Record<string, string | number | undefined | null>): string {
  const url = new URL(`${API_PREFIX}${path}`, window.location.origin);
  Object.entries(query ?? {}).forEach(([key, value]) => {
    if (value !== undefined && value !== null && String(value).length > 0) {
      url.searchParams.set(key, String(value));
    }
  });
  return `${url.pathname.slice(API_PREFIX.length)}${url.search}`;
}

function asBody(body: unknown): BodyInit | undefined {
  if (body === undefined) return undefined;
  if (typeof body === 'string' || body instanceof FormData || body instanceof Blob) return body;
  return JSON.stringify(body);
}

/**
 * The one low-level request helper used by the UI. Keeping it exported makes
 * the API boundary easy to test without mounting the application.
 */
export async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { body, idempotencyKey, ifMatch, ...init } = options;
  const headers = new Headers(init.headers);
  if (body !== undefined && !(body instanceof FormData) && !(body instanceof Blob)) {
    headers.set('Content-Type', 'application/json');
  }
  headers.set('Accept', 'application/json');
  if (idempotencyKey) headers.set('Idempotency-Key', idempotencyKey);
  if (ifMatch !== undefined) {
    headers.set('If-Match', typeof ifMatch === 'number' ? etagForVersion(ifMatch) : ifMatch);
  }

  const response = await fetch(`${API_PREFIX}${path}`, {
    ...init,
    body: asBody(body),
    credentials: 'include',
    headers
  });

  const text = await response.text();
  let parsed: unknown = undefined;
  if (text) {
    try {
      parsed = JSON.parse(text);
    } catch {
      parsed = text;
    }
  }

  if (!response.ok) {
    const envelope = parsed as Partial<ApiErrorShape> | undefined;
    const error = envelope?.error;
    throw new ApiError(
      error?.message || (typeof parsed === 'string' ? parsed : response.statusText) || 'Request failed',
      response.status,
      error?.code || 'request_failed',
      error?.details || {}
    );
  }

  return parsed as T;
}

export function etagForVersion(version: number): string {
  return `"v${version}"`;
}

export function collectionFrom<T>(payload: Collection<T> | T[]): Collection<T> {
  if (Array.isArray(payload)) return { data: payload };
  return { data: payload?.data ?? [], next_cursor: payload?.next_cursor ?? null };
}

function key(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') return crypto.randomUUID();
  return `roadmap-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

async function collectPages<T>(load: (cursor?: string) => Promise<Collection<T>>, startCursor?: string): Promise<Collection<T>> {
  const data: T[] = [];
  let cursor = startCursor;
  // The database and agent budgets bound practical collection size. This
  // guard still prevents a malformed server cursor from trapping the UI.
  for (let page = 0; page < 100; page += 1) {
    const result = await load(cursor);
    data.push(...result.data);
    if (!result.next_cursor) return { data, next_cursor: null };
    if (result.next_cursor === cursor) {
      throw new ApiError('The server repeated a pagination cursor.', 500, 'invalid_pagination_cursor');
    }
    cursor = result.next_cursor;
  }
  throw new ApiError('The collection exceeded the browser pagination safety limit.', 500, 'pagination_limit');
}

export const api = {
  authStatus: () => request<AuthStatus>('/auth/status'),
  authSetup: (input: { name: string; email: string; password: string }) =>
    request<Actor | { user: Actor }>('/auth/setup', { method: 'POST', body: input }),
  authLogin: (input: { email: string; password: string }) =>
    request<Actor | { user: Actor }>('/auth/login', { method: 'POST', body: input }),
  authLogout: () => request<{ ok: boolean }>('/auth/logout', { method: 'POST' }),
  authMe: () => request<Actor>('/auth/me'),

  listProjects: (params: { cursor?: string; limit?: number } = {}) =>
    request<Collection<Project>>(
      pathWithQuery('/projects', { cursor: params.cursor, limit: params.limit ?? 100 })
    ).then(collectionFrom),
  listAllProjects: () => collectPages((cursor) =>
    request<Collection<Project>>(pathWithQuery('/projects', { cursor, limit: 200 })).then(collectionFrom)
  ),
  createProject: (input: { key: string; name: string; description?: string; color?: string; favorite?: boolean }) =>
    request<Project>('/projects', { method: 'POST', body: input, idempotencyKey: key() }),
  patchProject: (project: string, input: Partial<Pick<Project, 'name' | 'description' | 'color' | 'favorite'>>) =>
    request<Project>(`/projects/${encodeURIComponent(project)}`, { method: 'PATCH', body: input, idempotencyKey: key() }),

  listColumns: (project: string, params: { cursor?: string; limit?: number } = {}) =>
    request<Collection<Column> | Column[]>(
      pathWithQuery(`/projects/${encodeURIComponent(project)}/columns`, { cursor: params.cursor, limit: params.limit })
    ).then(collectionFrom),
  listAllColumns: (project: string) => collectPages((cursor) =>
    request<Collection<Column>>(
      pathWithQuery(`/projects/${encodeURIComponent(project)}/columns`, { cursor, limit: 200 })
    ).then(collectionFrom)
  ),

  listTasks: (project: string, params: TaskListParams = {}) =>
    request<Collection<Task> | Task[]>(
      pathWithQuery(`/projects/${encodeURIComponent(project)}/tasks`, {
        state: params.state,
        column: params.column,
        priority: params.priority,
        label: params.label,
        assignee: params.assignee,
        q: params.q,
        updated_after: params.updated_after,
        cursor: params.cursor,
        limit: params.limit ?? 200
      })
    ).then(collectionFrom),
  createTask: (
    project: string,
    input: {
      title: string;
      description?: string;
      priority?: Task['priority'];
      column_id?: string;
      position?: number;
      due_at?: string | null;
      assignee?: string | null;
      labels?: string[];
      label_ids?: string[];
    }
  ) =>
    request<Task>(`/projects/${encodeURIComponent(project)}/tasks`, {
      method: 'POST',
      body: input,
      idempotencyKey: key()
    }),
  getTask: (task: string) => request<Task>(`/tasks/${encodeURIComponent(task)}`),
  patchTask: (task: string, input: TaskPatch, version: number) =>
    request<Task>(`/tasks/${encodeURIComponent(task)}`, {
      method: 'PATCH',
      body: input,
      ifMatch: version,
      idempotencyKey: key()
    }),
  deleteTask: (task: string, version: number) =>
    request<void>(`/tasks/${encodeURIComponent(task)}`, {
      method: 'DELETE',
      ifMatch: version,
      idempotencyKey: key()
    }),

  listComments: (task: string, params: { cursor?: string; limit?: number } = {}) =>
    request<Collection<Comment> | Comment[]>(
      pathWithQuery(`/tasks/${encodeURIComponent(task)}/comments`, { cursor: params.cursor, limit: params.limit })
    ).then(collectionFrom),
  listAllComments: (task: string) => collectPages((cursor) =>
    request<Collection<Comment>>(
      pathWithQuery(`/tasks/${encodeURIComponent(task)}/comments`, { cursor, limit: 200 })
    ).then(collectionFrom)
  ),
  postComment: (task: string, body: string) =>
    request<Comment>(`/tasks/${encodeURIComponent(task)}/comments`, {
      method: 'POST',
      body: { body },
      idempotencyKey: key()
    }),

  claimTask: (task: string, version: number, leaseSeconds = 3600) =>
    request<Task>(`/tasks/${encodeURIComponent(task)}/claim`, {
      method: 'POST',
      body: { lease_seconds: leaseSeconds },
      ifMatch: version,
      idempotencyKey: key()
    }),
  renewTask: (task: string, version: number, leaseSeconds = 3600) =>
    request<Task>(`/tasks/${encodeURIComponent(task)}/renew`, {
      method: 'POST',
      body: { lease_seconds: leaseSeconds },
      ifMatch: version,
      idempotencyKey: key()
    }),
  releaseTask: (task: string, version: number) =>
    request<Task>(`/tasks/${encodeURIComponent(task)}/release`, {
      method: 'POST',
      ifMatch: version,
      idempotencyKey: key()
    }),
  completeTask: (task: string, version: number) =>
    request<Task>(`/tasks/${encodeURIComponent(task)}/complete`, {
      method: 'POST',
      ifMatch: version,
      idempotencyKey: key()
    }),
  blockTask: (task: string, version: number) =>
    request<Task>(`/tasks/${encodeURIComponent(task)}/block`, {
      method: 'POST',
      ifMatch: version,
      idempotencyKey: key()
    }),

  listLabels: (project: string, params: { cursor?: string; limit?: number } = {}) =>
    request<Collection<Label> | Label[]>(
      pathWithQuery(`/projects/${encodeURIComponent(project)}/labels`, { cursor: params.cursor, limit: params.limit })
    ).then(collectionFrom),
  listAllLabels: (project: string) => collectPages((cursor) =>
    request<Collection<Label>>(
      pathWithQuery(`/projects/${encodeURIComponent(project)}/labels`, { cursor, limit: 200 })
    ).then(collectionFrom)
  ),
  createLabel: (project: string, input: { name: string; color?: string }) =>
    request<Label>(`/projects/${encodeURIComponent(project)}/labels`, {
      method: 'POST',
      body: input,
      idempotencyKey: key()
    }),
  deleteLabel: (label: string) =>
    request<void>(`/labels/${encodeURIComponent(label)}`, { method: 'DELETE', idempotencyKey: key() }),

  myWork: (params: { cursor?: string; limit?: number } = {}) =>
    request<Collection<Task> | Task[]>(pathWithQuery('/my-work', { cursor: params.cursor, limit: params.limit })).then(collectionFrom),
  allMyWork: () => collectPages((cursor) =>
    request<Collection<Task>>(pathWithQuery('/my-work', { cursor, limit: 200 })).then(collectionFrom)
  ),
  roadmap: (project?: string) =>
    request<RoadmapSummary>(project ? `/projects/${encodeURIComponent(project)}/roadmap` : '/roadmap'),
  listEvents: (params: { after?: number | string; project?: string } = {}) =>
    request<Collection<ActivityEvent> | ActivityEvent[]>(
      pathWithQuery('/events', { after: params.after, project: params.project })
    ).then(collectionFrom),

  listAgents: (params: { cursor?: string; limit?: number } = {}) =>
    request<Collection<Agent> | Agent[]>(pathWithQuery('/agents', { cursor: params.cursor, limit: params.limit })).then(collectionFrom),
  listAllAgents: () => collectPages((cursor) =>
    request<Collection<Agent>>(pathWithQuery('/agents', { cursor, limit: 200 })).then(collectionFrom)
  ),
  createAgent: (input: { name: string; description?: string; project_ids?: string[] }) =>
    request<Agent>('/agents', { method: 'POST', body: input, idempotencyKey: key() }),
  createToken: (
    agent: string,
    input: { name: string; scopes: string[]; project_ids?: string[]; expires_at?: string | null }
  ) =>
    request<ApiToken>(`/agents/${encodeURIComponent(agent)}/tokens`, {
      method: 'POST',
      // Token plaintext is deliberately returned once and never persisted in
      // the idempotency cache, so issuance cannot safely be replayed.
      body: input
    }),
  deleteToken: (token: string) =>
    request<void>(`/tokens/${encodeURIComponent(token)}`, { method: 'DELETE', idempotencyKey: key() })
};

/** Fetch every page for a board while retaining the contract's cursor API. */
export async function listAllTasks(project: string, params: TaskListParams = {}): Promise<Collection<Task>> {
  return collectPages((cursor) => api.listTasks(project, { ...params, cursor, limit: params.limit ?? 200 }), params.cursor);
}

export function unwrapActor(value: Actor | { user: Actor }): Actor {
  return 'user' in value ? value.user : value;
}
