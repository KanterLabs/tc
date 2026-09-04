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
  type IssueMetrics,
  type Label,
  type Project,
  type BugInput,
  type BugSeverity,
  type BugResolution,
  type BoardDescriptor,
  type RoadmapSummary,
  type SidebarCounts,
  type SavedView,
  type SearchResponse,
  type SearchSort,
  type Task,
  type TaskDraftSuggestion,
  type TaskDependencies,
  type TaskChecklistCollection,
  type TaskChecklistItemInput,
  type TaskChecklistItemPatch,
  type TaskHierarchy,
  type TaskHierarchyReference,
  type TaskTimelineCollection,
  type TaskTimelineKind,
  type TaskMoveInput,
  type TaskReorderInput,
  type TaskPatch,
  type TriageInput,
  type ResolveInput,
  type ReopenInput,
  type PortableArchive,
  type PortableImportReport
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
  sort?: 'board' | 'position' | 'number' | 'created_at' | 'updated_at' | 'priority' | 'title' | string;
  order?: 'asc' | 'desc';
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

export interface SearchParams {
  q?: string;
  key?: string;
  title?: string;
  description?: string;
  label?: string;
  state?: string;
  priority?: string;
  assignee?: string;
  claim_owner?: string;
  project?: string;
  due_from?: string;
  due_to?: string;
  sort?: string;
  view?: string;
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

/**
 * Validation failures are returned by the server in the structured error
 * details alongside the regular message. Keeping the report intact lets the
 * import review dialog show every failing field and remap, not just the first
 * toast string.
 */
export function portableImportReportFromError(error: unknown): PortableImportReport | null {
  if (!(error instanceof ApiError) || !error.details || typeof error.details !== 'object') return null;
  const details = error.details as Partial<PortableImportReport>;
  if (!Array.isArray(details.errors)) return null;
  return {
    format: typeof details.format === 'string' ? details.format : 'helm.portable',
    version: typeof details.version === 'number' ? details.version : 1,
    dry_run: typeof details.dry_run === 'boolean' ? details.dry_run : true,
    conflict: typeof details.conflict === 'string' ? details.conflict : 'remap',
    counts: details.counts && typeof details.counts === 'object' ? details.counts : {},
    remaps: Array.isArray(details.remaps) ? details.remaps : [],
    warnings: Array.isArray(details.warnings) ? details.warnings : [],
    errors: details.errors
  };
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
  listAllProjects: (params: { includeArchived?: boolean } = {}) => collectPages((cursor) =>
    request<Collection<Project>>(pathWithQuery('/projects', { cursor, limit: 200, archived: params.includeArchived ? true : undefined })).then(collectionFrom)
  ),
  createProject: (input: { key: string; name: string; description?: string; color?: string; favorite?: boolean; checklist_completion_policy?: Project['checklist_completion_policy'] }) =>
    request<Project>('/projects', { method: 'POST', body: input, idempotencyKey: key() }),
  getProject: (project: string) => request<Project>(`/projects/${encodeURIComponent(project)}`),
  patchProject: (project: string, input: Partial<Pick<Project, 'name' | 'description' | 'color' | 'favorite' | 'checklist_completion_policy'>> & { archived?: boolean }, version?: number) =>
    request<Project>(`/projects/${encodeURIComponent(project)}`, { method: 'PATCH', body: input, ifMatch: version, idempotencyKey: key() }),
  exportProject: (project?: string) =>
    request<PortableArchive>(project ? `/projects/${encodeURIComponent(project)}/export` : '/export'),
  importPortable: (archive: PortableArchive | unknown, options: { targetProject?: string; conflict?: 'remap' | 'fail'; dryRun?: boolean } = {}) =>
    request<PortableImportReport>('/import', {
      method: 'POST',
      body: {
        archive,
        target_project: options.targetProject,
        conflict: options.conflict,
        dry_run: options.dryRun
      },
      idempotencyKey: options.dryRun ? undefined : key()
    }),
  importTrello: (board: unknown, options: { targetProject?: string; conflict?: 'remap' | 'fail'; dryRun?: boolean } = {}) =>
    request<PortableImportReport>(pathWithQuery('/import/trello', { target_project: options.targetProject, conflict: options.conflict, dry_run: options.dryRun }), {
      method: 'POST',
      body: board,
      idempotencyKey: options.dryRun ? undefined : key()
    }),
  listBoards: (project: string) =>
    request<{ data: BoardDescriptor[]; next_cursor?: string | null; multi_board?: Record<string, unknown> }>(`/projects/${encodeURIComponent(project)}/boards`),

  listColumns: (project: string, params: { cursor?: string; limit?: number } = {}) =>
    request<Collection<Column> | Column[]>(
      pathWithQuery(`/projects/${encodeURIComponent(project)}/columns`, { cursor: params.cursor, limit: params.limit })
    ).then(collectionFrom),
  listAllColumns: (project: string, params: { includeArchived?: boolean } = {}) => collectPages((cursor) =>
    request<Collection<Column>>(
      pathWithQuery(`/projects/${encodeURIComponent(project)}/columns`, { cursor, limit: 200, archived: params.includeArchived ? true : undefined })
    ).then(collectionFrom)
  ),
  createColumn: (project: string, input: { name: string; semantic_state: Column['semantic_state']; position?: number }) =>
    request<Column>(`/projects/${encodeURIComponent(project)}/columns`, {
      method: 'POST',
      body: input,
      idempotencyKey: key()
    }),
  patchColumn: (column: string, input: Partial<Pick<Column, 'name' | 'semantic_state' | 'position'>> & { archived?: boolean }, version?: number) =>
    request<Column>(`/columns/${encodeURIComponent(column)}`, {
      method: 'PATCH',
      body: input,
      ifMatch: version,
      idempotencyKey: key()
    }),

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
        sort: params.sort,
        order: params.order,
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
      parent?: string | null;
      parent_id?: string | null;
      parent_task_id?: string | null;
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
  getTaskChecklist: (task: string) =>
    request<TaskChecklistCollection>(`/tasks/${encodeURIComponent(task)}/checklist`),
  addTaskChecklistItem: (task: string, input: TaskChecklistItemInput, version: number) =>
    request<Task>(`/tasks/${encodeURIComponent(task)}/checklist`, {
      method: 'POST',
      body: input,
      ifMatch: version,
      idempotencyKey: key()
    }),
  updateTaskChecklistItem: (task: string, item: string, input: TaskChecklistItemPatch, version: number) =>
    request<Task>(`/tasks/${encodeURIComponent(task)}/checklist/${encodeURIComponent(item)}`, {
      method: 'PATCH',
      body: input,
      ifMatch: version,
      idempotencyKey: key()
    }),
  patchTaskChecklistItem: (task: string, item: string, input: TaskChecklistItemPatch, version: number) =>
    api.updateTaskChecklistItem(task, item, input, version),
  deleteTaskChecklistItem: (task: string, item: string, version: number) =>
    request<Task>(`/tasks/${encodeURIComponent(task)}/checklist/${encodeURIComponent(item)}`, {
      method: 'DELETE',
      ifMatch: version,
      idempotencyKey: key()
    }),
  reorderTaskChecklist: (task: string, itemIds: string[], version: number) =>
    request<Task>(`/tasks/${encodeURIComponent(task)}/checklist`, {
      method: 'PATCH',
      body: { item_ids: itemIds },
      ifMatch: version,
      idempotencyKey: key()
    }),
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
  getTaskHierarchy: (task: string) =>
    request<TaskHierarchy>(`/tasks/${encodeURIComponent(task)}/hierarchy`),
  listTaskChildren: (task: string) =>
    request<Collection<TaskHierarchyReference> | TaskHierarchyReference[]>(`/tasks/${encodeURIComponent(task)}/children`).then(collectionFrom),
  listTaskAncestors: (task: string) =>
    request<Collection<TaskHierarchyReference> | TaskHierarchyReference[]>(`/tasks/${encodeURIComponent(task)}/ancestors`).then(collectionFrom),
  listTaskDescendants: (task: string) =>
    request<Collection<TaskHierarchyReference> | TaskHierarchyReference[]>(`/tasks/${encodeURIComponent(task)}/descendants`).then(collectionFrom),
  setTaskParent: (task: string, parent: string, version: number) =>
    request<Task>(`/tasks/${encodeURIComponent(task)}/parent`, {
      method: 'POST',
      body: { parent },
      ifMatch: version,
      idempotencyKey: key()
    }),
  clearTaskParent: (task: string, version: number) =>
    request<Task>(`/tasks/${encodeURIComponent(task)}/parent`, {
      method: 'DELETE',
      ifMatch: version,
      idempotencyKey: key()
    }),
  removeTaskChild: (parent: string, child: string, version: number) =>
    request<Task>(`/tasks/${encodeURIComponent(parent)}/children/${encodeURIComponent(child)}`, {
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
  restoreTask: (task: string, version: number) =>
    request<Task>(`/tasks/${encodeURIComponent(task)}/restore`, {
      method: 'POST',
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
  getComment: (task: string, comment: string) =>
    request<Comment>(`/tasks/${encodeURIComponent(task)}/comments/${encodeURIComponent(comment)}`),
  postComment: (task: string, body: string) =>
    request<Comment>(`/tasks/${encodeURIComponent(task)}/comments`, {
      method: 'POST',
      body: { body },
      idempotencyKey: key()
    }),
  patchComment: (task: string, comment: string, body: string, version: number) =>
    request<Comment>(`/tasks/${encodeURIComponent(task)}/comments/${encodeURIComponent(comment)}`, {
      method: 'PATCH',
      body: { body },
      ifMatch: version,
      idempotencyKey: key()
    }),
  deleteComment: (task: string, comment: string, version: number) =>
    request<void>(`/tasks/${encodeURIComponent(task)}/comments/${encodeURIComponent(comment)}`, {
      method: 'DELETE',
      ifMatch: version,
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
  issueMetrics: (params: { project?: string } = {}) =>
    request<IssueMetrics>(pathWithQuery('/issues/metrics', { project: params.project })),
  sidebarCounts: (params: { project?: string; view?: WorkView } = {}) =>
    request<SidebarCounts>(pathWithQuery('/sidebar-counts', { project: params.project, view: params.view })),
  search: (params: SearchParams = {}) =>
    request<SearchResponse>(
      pathWithQuery('/search', {
        q: params.q,
        key: params.key,
        title: params.title,
        description: params.description,
        label: params.label,
        state: params.state,
        priority: params.priority,
        assignee: params.assignee,
        claim_owner: params.claim_owner,
        project: params.project,
        due_from: params.due_from,
        due_to: params.due_to,
        sort: params.sort,
        view: params.view,
        cursor: params.cursor,
        limit: params.limit
      })
    ).then((result) => ({ ...result, data: result?.data ?? [], next_cursor: result?.next_cursor ?? null })),
  searchSavedView: (view: string, params: Omit<SearchParams, 'view'> = {}) =>
    api.search({ ...params, view }),
  listSavedViews: (params: { cursor?: string; limit?: number } = {}) =>
    request<Collection<SavedView> | SavedView[]>(pathWithQuery('/views', { cursor: params.cursor, limit: params.limit })).then(collectionFrom),
  listAllSavedViews: () => collectPages((cursor) =>
    request<Collection<SavedView> | SavedView[]>(pathWithQuery('/views', { cursor, limit: 200 })).then(collectionFrom)
  ),
  getSavedView: (view: string) => request<SavedView>(`/views/${encodeURIComponent(view)}`),
  createSavedView: (input: { name: string; description?: string; filters: Record<string, unknown>; sort?: SearchSort[]; shared?: boolean }) =>
    request<SavedView>('/views', { method: 'POST', body: input, idempotencyKey: key() }),
  patchSavedView: (view: string, input: Partial<{ name: string; description: string; filters: Record<string, unknown>; sort: SearchSort[]; shared: boolean }>) =>
    request<SavedView>(`/views/${encodeURIComponent(view)}`, { method: 'PATCH', body: input, idempotencyKey: key() }),
  deleteSavedView: (view: string) =>
    request<void>(`/views/${encodeURIComponent(view)}`, { method: 'DELETE', idempotencyKey: key() }),
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
  reorderTask: (task: string, input: TaskReorderInput, version: number) =>
    request<Task>(`/tasks/${encodeURIComponent(task)}/reorder`, {
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
