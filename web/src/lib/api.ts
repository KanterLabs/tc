import {
  ApiError,
  type ActivityEvent,
  type Actor,
  type Agent,
  type AgentWorkInput,
  type AgentWorkStateFilter,
  type ApiErrorShape,
  type ApiToken,
  type AuthStatus,
  type CodexAccountStatus,
  type CodexDeviceLogin,
  type AuditDetail,
  type AuditFinding,
  type AuditFindingPatch,
  type AuditRun,
  type AuditTerminalStatus,
  type Collection,
  type Column,
  type Comment,
  type Label,
  type Project,
  type BugInput,
  type BugSeverity,
  type BugResolution,
  type RoadmapSummary,
  type Task,
  type TaskDraftSuggestion,
  type TaskDependencies,
  type TaskTimelineCollection,
  type TaskTimelineKind,
  type TaskMoveInput,
  type TaskPatch,
  type TriageInput,
  type ResolveInput,
  type ReopenInput
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
  kind?: Task['kind'];
  severity?: BugSeverity | 'untriaged' | 'none';
  reporter?: string;
  resolution?: BugResolution | 'unresolved' | 'none';
  agent_state?: AgentWorkStateFilter;
  action_needed?: boolean | string;
  dependency?: 'blocked' | 'ready';
  q?: string;
  updated_after?: string;
  cursor?: string;
  limit?: number;
}

export interface IssueListParams extends Omit<TaskListParams, 'kind'> {
  project?: string;
}

export type WorkView = 'assigned' | 'live';

export interface MyWorkParams {
  project?: string;
  state?: string;
  priority?: string;
  label?: string;
  q?: string;
  updated_after?: string;
  view?: WorkView;
  agent_state?: AgentWorkStateFilter;
  action_needed?: boolean | string;
  dependency?: 'blocked' | 'ready';
  cursor?: string;
  limit?: number;
}

function pathWithQuery(path: string, query?: Record<string, string | number | boolean | undefined | null>): string {
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
  return `helm-${Date.now()}-${Math.random().toString(36).slice(2)}`;
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
  codexAccount: (refresh = false) => request<CodexAccountStatus>(pathWithQuery('/codex/account', { refresh })),
  startCodexLogin: () => request<CodexDeviceLogin>('/codex/login', { method: 'POST' }),
  cancelCodexLogin: (loginId: string) => request<{ status: string }>('/codex/login/cancel', { method: 'POST', body: { login_id: loginId } }),
  logoutCodex: () => request<{ ok: boolean }>('/codex/logout', { method: 'POST' }),

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
        kind: params.kind,
        severity: params.severity,
        reporter: params.reporter,
        resolution: params.resolution,
        agent_state: params.agent_state,
        action_needed: params.action_needed,
        dependency: params.dependency,
        q: params.q,
        updated_after: params.updated_after,
        cursor: params.cursor,
        limit: params.limit ?? 200
      })
    ).then(collectionFrom),
  draftTask: (project: string, query: string, signal?: AbortSignal) => request<TaskDraftSuggestion>(`/projects/${encodeURIComponent(project)}/task-draft`, { method: 'POST', body: { query }, signal }),
  listIssues: (params: IssueListParams = {}) =>
    request<Collection<Task> | Task[]>(
      pathWithQuery('/issues', {
        project: params.project,
        state: params.state,
        column: params.column,
        priority: params.priority,
        label: params.label,
        assignee: params.assignee,
        severity: params.severity,
        reporter: params.reporter,
        resolution: params.resolution,
        agent_state: params.agent_state,
        action_needed: params.action_needed,
        dependency: params.dependency,
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
      kind?: Task['kind'];
      bug?: BugInput;
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
  getTaskDependencies: (task: string) =>
    request<TaskDependencies>(`/tasks/${encodeURIComponent(task)}/dependencies`),
  addTaskDependency: (task: string, prerequisite: string, version: number) =>
    request<Task>(`/tasks/${encodeURIComponent(task)}/dependencies`, {
      method: 'POST',
      body: { prerequisite },
      ifMatch: version,
      idempotencyKey: key()
    }),
  removeTaskDependency: (task: string, prerequisite: string, version: number) =>
    request<Task>(`/tasks/${encodeURIComponent(task)}/dependencies/${encodeURIComponent(prerequisite)}`, {
      method: 'DELETE',
      ifMatch: version,
      idempotencyKey: key()
    }),

  triageTask: (task: string, version: number, input: TriageInput) =>
    request<Task>(`/tasks/${encodeURIComponent(task)}/triage`, {
      method: 'POST',
      body: input,
      ifMatch: version,
      idempotencyKey: key()
    }),
  resolveTask: (task: string, version: number, input: ResolveInput) =>
    request<Task>(`/tasks/${encodeURIComponent(task)}/resolve`, {
      method: 'POST',
      body: input,
      ifMatch: version,
      idempotencyKey: key()
    }),
  reopenTask: (task: string, version: number, input: ReopenInput) =>
    request<Task>(`/tasks/${encodeURIComponent(task)}/reopen`, {
      method: 'POST',
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
  listTaskTimeline: (task: string, params: { before?: string; limit?: number; kind?: TaskTimelineKind } = {}) =>
    request<TaskTimelineCollection>(
      pathWithQuery(`/tasks/${encodeURIComponent(task)}/timeline`, {
        before: params.before,
        limit: params.limit ?? 50,
        kind: params.kind
      })
    ),
  listProjectTimeline: (project: string, params: { before?: string; limit?: number; kind?: TaskTimelineKind } = {}) =>
    request<TaskTimelineCollection>(
      pathWithQuery(`/projects/${encodeURIComponent(project)}/timeline`, {
        before: params.before,
        limit: params.limit ?? 50,
        kind: params.kind
      })
    ),

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
  blockTask: (task: string, version: number, reason?: string) =>
    request<Task>(`/tasks/${encodeURIComponent(task)}/block`, {
      method: 'POST',
      ...(reason === undefined ? {} : { body: { reason } }),
      ifMatch: version,
      idempotencyKey: key()
    }),
  publishAgentWork: (task: string, version: number, input: AgentWorkInput) =>
    request<Task>(`/tasks/${encodeURIComponent(task)}/progress`, {
      method: 'POST',
      body: input,
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

  myWork: (params: MyWorkParams = {}) =>
    request<Collection<Task> | Task[]>(
      pathWithQuery('/my-work', {
        project: params.project,
        state: params.state,
        priority: params.priority,
        label: params.label,
        q: params.q,
        updated_after: params.updated_after,
        view: params.view,
        agent_state: params.agent_state,
        action_needed: params.action_needed,
        dependency: params.dependency,
        cursor: params.cursor,
        limit: params.limit
      })
    ).then(collectionFrom),
  allMyWork: (params: MyWorkParams = {}) => collectPages((cursor) =>
    request<Collection<Task> | Task[]>(
      pathWithQuery('/my-work', {
        project: params.project,
        state: params.state,
        priority: params.priority,
        label: params.label,
        q: params.q,
        updated_after: params.updated_after,
        view: params.view,
        agent_state: params.agent_state,
        action_needed: params.action_needed,
        dependency: params.dependency,
        cursor,
        limit: params.limit ?? 200
      })
    ).then(collectionFrom), params.cursor
  ),
  roadmap: (project?: string) =>
    request<RoadmapSummary>(project ? `/projects/${encodeURIComponent(project)}/roadmap` : '/roadmap'),

  /** List audit runs for one project. Runs are read-only until a finding is explicitly reviewed. */
  listAudits: (project: string, params: { cursor?: string; limit?: number } = {}) =>
    request<Collection<AuditRun> | AuditRun[]>(
      pathWithQuery(`/projects/${encodeURIComponent(project)}/audits`, {
        cursor: params.cursor,
        limit: params.limit ?? 100
      })
    ).then(collectionFrom),
  listAllAudits: (project: string) => collectPages((cursor) =>
    request<Collection<AuditRun> | AuditRun[]>(
      pathWithQuery(`/projects/${encodeURIComponent(project)}/audits`, { cursor, limit: 200 })
    ).then(collectionFrom)
  ),
  /** Start an audit run. Starting a run never applies a proposed move. */
  createAudit: (project: string, input: { scope?: string; status?: 'queued' | 'running' } = {}) =>
    request<AuditRun>(`/projects/${encodeURIComponent(project)}/audits`, {
      method: 'POST',
      body: input,
      idempotencyKey: key()
    }),
  getAudit: async (audit: string): Promise<AuditDetail> => {
    const run = await request<AuditRun>(`/audits/${encodeURIComponent(audit)}`);
    const findings = await collectPages((cursor) =>
      request<Collection<AuditFinding> | AuditFinding[]>(
        pathWithQuery(`/audits/${encodeURIComponent(audit)}/findings`, { cursor, limit: 200 })
      ).then(collectionFrom)
    );
    return { ...run, findings: findings.data };
  },
  finalizeAudit: (audit: string, status: AuditTerminalStatus = 'complete') =>
    request<AuditRun>(`/audits/${encodeURIComponent(audit)}/finalize`, {
      method: 'POST',
      body: { status },
      idempotencyKey: key()
    }),
  patchAuditFinding: (finding: string, input: AuditFindingPatch, version: number) =>
    request<AuditFinding>(`/audit-findings/${encodeURIComponent(finding)}`, {
      method: 'PATCH',
      body: input,
      ifMatch: version,
      idempotencyKey: key()
    }),
  /**
   * Apply one approved audit recommendation through the guarded task move
   * endpoint. The server owns destination positioning and validates the
   * source column, task version, and claim state atomically.
   */
  moveTask: (task: string, input: TaskMoveInput, version: number) =>
    request<Task>(`/tasks/${encodeURIComponent(task)}/move`, {
      method: 'POST',
      body: input,
      ifMatch: version,
      idempotencyKey: key()
    }),
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

export async function listAllIssues(params: IssueListParams = {}): Promise<Collection<Task>> {
  return collectPages((cursor) => api.listIssues({ ...params, cursor, limit: params.limit ?? 200 }), params.cursor);
}

export function unwrapActor(value: Actor | { user: Actor }): Actor {
  return 'user' in value ? value.user : value;
}
