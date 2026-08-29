import type { AgentWork, AgentWorkState, BugSeverity, Column, Project, Task } from './types';

export interface BoardFilters {
  query: string;
  priority: string;
  label: string;
  assignee: string;
  state: string;
  /** Issue filters remain optional so existing board callers stay source-compatible. */
  kind?: string;
  severity?: string;
  reporter?: string;
  resolution?: string;
}

export function actorId(value: Task['assignee'] | Task['claimed_by']): string {
  if (!value) return '';
  return typeof value === 'string' ? value : value.id;
}

export function actorName(value: Task['assignee'] | Task['claimed_by']): string {
  if (!value) return '';
  return typeof value === 'string' ? value : value.name;
}

/** Runtime agent-work payloads are intentionally read defensively. */
export type AgentWorkLike = Partial<AgentWork> & { updated_at?: string };

/** Filters exposed by the board and cross-project live-work views. */
export type AgentWorkFilter = 'all' | 'action-needed' | AgentWorkState | 'stale' | 'missing';

export type AgentWorkDisplayStatus = AgentWorkState | 'stale' | 'missing' | 'completed';
export type LiveWorkGroup = 'stale' | 'waiting' | 'handoff' | 'verifying' | 'working' | 'missing';
export type AgentWorkBucket = LiveWorkGroup | 'completed';

export interface AgentWorkStatusCounts {
  actionNeeded: number;
  working: number;
  waiting: number;
  verifying: number;
  stale: number;
  handoff: number;
  missing: number;
}

type TimeValue = Date | number | string;
type AgentWorkSource = AgentWork | AgentWorkLike | Task | null | undefined;

const AGENT_WORK_STALE_AFTER_MS = 15 * 60 * 1000;

/** Completion is deliberately independent from agent_work. A task may retain
 * its last published work snapshot after completion so reopening can restore
 * that context, while all live-work presentation stays suppressed meanwhile.
 */
export function isTaskCompleted(task: Pick<Task, 'completed_at'> | null | undefined, semanticState = ''): boolean {
  return Boolean(task?.completed_at) || semanticState === 'completed';
}

/** Return the last published agent snapshot without mutating or clearing it. */
export function agentWorkForTask(task: Pick<Task, 'agent_work'> | null | undefined): AgentWorkLike | null {
  const candidate = task?.agent_work;
  return candidate && typeof candidate === 'object' ? candidate as AgentWorkLike : null;
}

function taskSource(value: AgentWorkSource): Task | null {
  if (!value || typeof value !== 'object') return null;
  // `completed_at` is optional in older task payloads, so project/column IDs
  // provide a second runtime discriminator for tasks that omit it.
  if ('completed_at' in value || ('project_id' in value && 'column_id' in value)) return value as Task;
  return null;
}

function sourceIsCompleted(value: AgentWorkSource): boolean {
  return isTaskCompleted(taskSource(value));
}

function milliseconds(value: TimeValue | undefined | null): number | null {
  if (value instanceof Date) {
    const timestamp = value.getTime();
    return Number.isFinite(timestamp) ? timestamp : null;
  }
  if (typeof value === 'number') return Number.isFinite(value) ? value : null;
  if (typeof value !== 'string' || !value.trim()) return null;
  const timestamp = Date.parse(value);
  return Number.isFinite(timestamp) ? timestamp : null;
}

function workValue(value: AgentWorkSource): AgentWorkLike | null {
  if (!value) return null;
  if (typeof value !== 'object') return null;
  // Tasks can omit `agent_work` when no pulse has ever been published. Do not
  // mistake the task object itself for a partial work snapshot in that case.
  if ('agent_work' in value || ('project_id' in value && 'column_id' in value)) {
    const candidate = 'agent_work' in value ? (value as { agent_work?: unknown }).agent_work : null;
    return candidate && typeof candidate === 'object' ? candidate as AgentWorkLike : null;
  }
  return value as AgentWorkLike;
}

function workUpdatedAt(value: AgentWorkSource): string | undefined {
  const work = workValue(value);
  return work?.updated_at;
}

function hasStaleWork(value: AgentWorkSource, now?: TimeValue): boolean {
  if (sourceIsCompleted(value)) return false;
  const work = workValue(value);
  if (!work) return false;
  // The API's flag is useful when a response was produced on the server
  // shortly before it reached the browser. Recompute as well so a response
  // can become stale while it remains mounted.
  return work.stale === true || isAgentWorkStale(work.updated_at, now);
}

/**
 * Return whether an agent update is at least fifteen minutes old.
 *
 * Invalid timestamps are treated as unknown (and therefore not stale) rather
 * than allowing NaN to leak into sorting or status decisions. `now` accepts a
 * Date, epoch milliseconds, or an ISO timestamp so callers can test the exact
 * boundary without changing the process clock.
 */
export function isAgentWorkStale(value: AgentWorkSource | string, now: TimeValue = Date.now()): boolean {
  if (typeof value !== 'string' && sourceIsCompleted(value)) return false;
  const updatedAt = typeof value === 'string' ? value : workUpdatedAt(value);
  const updated = milliseconds(updatedAt);
  const current = milliseconds(now);
  if (updated === null || current === null) return false;
  return current - updated >= AGENT_WORK_STALE_AFTER_MS;
}

/** Descriptive alias for callers that prefer the computation wording. */
export const computeAgentWorkStale = isAgentWorkStale;

/** Backwards-friendly alias for callers that read the predicate as a noun. */
export const agentWorkIsStale = isAgentWorkStale;

/**
 * Classify the compact status shown for a task's agent work.
 *
 * Waiting is intentionally preserved even after its update goes stale: the
 * waiting state tells a human why work needs attention, while the separate
 * action-needed helper supplies the attention signal.
 */
export function displayAgentWorkStatus(value: AgentWorkSource, now: TimeValue = Date.now()): AgentWorkDisplayStatus {
  if (sourceIsCompleted(value)) return 'completed';
  const work = workValue(value);
  if (!work) return 'missing';
  if (work.state === 'waiting') return 'waiting';
  if (hasStaleWork(value, now)) return 'stale';
  if (work.state === 'working' || work.state === 'verifying' || work.state === 'handoff') return work.state;
  // Runtime JSON can be malformed even though the TypeScript contract is
  // narrow. Unknown states are safer to surface as stale than as fresh work.
  return 'stale';
}

export const classifyAgentWorkStatus = displayAgentWorkStatus;
export const classifyDisplayedAgentWork = displayAgentWorkStatus;
export const classifyAgentWork = displayAgentWorkStatus;
export const agentWorkDisplayStatus = displayAgentWorkStatus;

/** Return whether a human should inspect or respond to the published work. */
export function agentWorkActionNeeded(
  value: AgentWorkSource,
  now: TimeValue = Date.now(),
  semanticState = ''
): boolean {
  if (sourceIsCompleted(value) || semanticState === 'completed') return false;
  const work = workValue(value);
  if (!work) {
    const task = taskSource(value);
    return Boolean(task?.claimed_by && ['active', 'blocked'].includes(semanticState));
  }
  return work.action_needed === true || hasStaleWork(value, now) || work.state === 'waiting' || work.state === 'handoff';
}

export const isAgentWorkActionNeeded = agentWorkActionNeeded;
export const actionNeededForAgentWork = agentWorkActionNeeded;
export const agentWorkNeedsAction = agentWorkActionNeeded;

function count(value: unknown): number | null {
  return typeof value === 'number' && Number.isInteger(value) && Number.isFinite(value) && value >= 0 ? value : null;
}

/**
 * Format checkpoint progress for a compact badge. Explicit server counts win;
 * when only refs are available their count is the total. Invalid or partial
 * counts produce an empty label instead of inventing progress.
 */
export function agentWorkProgressLabel(value: AgentWorkSource): string {
  if (sourceIsCompleted(value)) return '';
  const work = workValue(value);
  if (!work) return '';
  const completed = count(work.checkpoint_completed);
  const explicitTotal = count(work.checkpoint_total);
  const refs = Array.isArray(work.checkpoint_refs)
    ? work.checkpoint_refs.filter((reference): reference is string => typeof reference === 'string')
    : [];
  const total = explicitTotal ?? (refs.length ? refs.length : null);
  if (completed !== null && total !== null && completed <= total) return `${completed}/${total}`;
  return '';
}

export const progressLabelForAgentWork = agentWorkProgressLabel;
export const agentWorkProgress = agentWorkProgressLabel;
export const progressLabel = agentWorkProgressLabel;

/** Determine the sort bucket used by the live-work view. */
export function liveWorkGroup(value: AgentWorkSource, now: TimeValue = Date.now()): LiveWorkGroup {
  if (sourceIsCompleted(value)) return 'missing';
  const work = workValue(value);
  if (!work) return 'missing';
  if (work.state === 'waiting') return 'waiting';
  if (hasStaleWork(value, now)) return 'stale';
  if (work.state === 'handoff') return 'handoff';
  if (work.state === 'verifying') return 'verifying';
  if (work.state === 'working') return 'working';
  return 'stale';
}

/** Return the live-work bucket, with completion kept distinct from missing. */
export function agentWorkBucket(value: AgentWorkSource, now: TimeValue = Date.now()): AgentWorkBucket {
  if (sourceIsCompleted(value)) return 'completed';
  const work = workValue(value);
  if (!work) return 'missing';
  // Board filters historically group every aged snapshot as stale, including
  // waiting work. Live Work keeps waiting as a separate presentation group.
  if (hasStaleWork(value, now)) return 'stale';
  if (work.state === 'waiting') return 'waiting';
  if (work.state === 'handoff') return 'handoff';
  if (work.state === 'verifying') return 'verifying';
  if (work.state === 'working') return 'working';
  return 'stale';
}

/** The compact status helpers use the same task-aware work snapshot. */
export function agentWorkState(value: AgentWorkSource): string {
  return workValue(value)?.state || '';
}

export function agentWorkUpdatedAt(value: AgentWorkSource): string {
  return workValue(value)?.updated_at || '';
}

/** A missing pulse is actionable only for unfinished claimed/active work. */
export function isMissingAgentWorkCandidate(task: Task, semanticState = ''): boolean {
  if (isTaskCompleted(task, semanticState)) return false;
  return !agentWorkForTask(task) && Boolean(
    task.claimed_by || ['active', 'blocked'].includes(semanticState)
  );
}

/** Whether the board/drawer should mount any compact agent-work indicator. */
export function shouldShowAgentPulse(task: Task, semanticState = ''): boolean {
  if (isTaskCompleted(task, semanticState)) return false;
  return Boolean(agentWorkForTask(task) || isMissingAgentWorkCandidate(task, semanticState));
}

/** Completed tasks remain visible in the `all` view but never match a live
 * agent-work bucket, including missing/action-needed candidates. */
export function matchesAgentWorkFilter(
  task: Task,
  filter: AgentWorkFilter,
  now: TimeValue = Date.now(),
  semanticState = ''
): boolean {
  if (filter === 'all') return true;
  if (isTaskCompleted(task, semanticState)) return false;
  if (filter === 'action-needed') return agentWorkActionNeeded(task, now, semanticState);
  if (filter === 'missing') return isMissingAgentWorkCandidate(task, semanticState);
  return agentWorkBucket(task, now) === filter;
}

/** Count only unfinished tasks so retained completed snapshots stay inert. */
export function agentWorkStatusCounts(
  tasks: Task[],
  now: TimeValue = Date.now(),
  semanticStateForTask: (task: Task) => string = () => ''
): AgentWorkStatusCounts {
  const unfinished = tasks.filter((task) => !isTaskCompleted(task, semanticStateForTask(task)));
  return {
    actionNeeded: unfinished.filter((task) => agentWorkActionNeeded(task, now, semanticStateForTask(task))).length,
    working: unfinished.filter((task) => agentWorkBucket(task, now) === 'working').length,
    waiting: unfinished.filter((task) => agentWorkBucket(task, now) === 'waiting').length,
    verifying: unfinished.filter((task) => agentWorkBucket(task, now) === 'verifying').length,
    stale: unfinished.filter((task) => agentWorkBucket(task, now) === 'stale').length,
    handoff: unfinished.filter((task) => agentWorkBucket(task, now) === 'handoff').length,
    missing: unfinished.filter((task) => isMissingAgentWorkCandidate(task, semanticStateForTask(task))).length
  };
}

const liveWorkRank: Record<LiveWorkGroup, number> = {
  stale: 0,
  waiting: 0,
  handoff: 0,
  verifying: 1,
  working: 2,
  missing: 3
};

function liveWorkTimestamp(task: Task): number | null {
  return milliseconds(task.agent_work?.updated_at) ?? milliseconds(task.updated_at);
}

/**
 * Sort live work by attention bucket and, within a bucket, newest update.
 * Input order is retained for equal/unknown timestamps to keep rendering
 * stable across refreshes.
 */
export function sortLiveWork(tasks: Task[], now: TimeValue = Date.now()): Task[] {
  return tasks
    .filter((task) => !isTaskCompleted(task))
    .map((task, index) => ({ task, index, group: liveWorkGroup(task, now), updated: liveWorkTimestamp(task) }))
    .sort((a, b) => {
      const groupDelta = liveWorkRank[a.group] - liveWorkRank[b.group];
      if (groupDelta) return groupDelta;
      if (a.updated !== null && b.updated !== null && a.updated !== b.updated) return b.updated - a.updated;
      if (a.updated !== null && b.updated === null) return -1;
      if (a.updated === null && b.updated !== null) return 1;
      return a.index - b.index;
    })
    .map(({ task }) => task);
}

export const sortAgentWork = sortLiveWork;
export const sortLiveWorkTasks = sortLiveWork;

/**
 * Group live work while retaining the same display order as `sortLiveWork`.
 * Empty groups are included so a caller can render predictable sections.
 */
export function groupLiveWork(tasks: Task[], now: TimeValue = Date.now()): Record<LiveWorkGroup, Task[]> {
  const groups: Record<LiveWorkGroup, Task[]> = {
    stale: [],
    waiting: [],
    handoff: [],
    verifying: [],
    working: [],
    missing: []
  };
  sortLiveWork(tasks, now).forEach((task) => groups[liveWorkGroup(task, now)].push(task));
  return groups;
}

export const groupAgentWork = groupLiveWork;

/** Build project -> column ID -> column lookup maps for cross-project views. */
export function columnLookupByProject(columns: Column[]): Map<string, Map<string, Column>> {
  const projects = new Map<string, Map<string, Column>>();
  columns.forEach((column) => {
    let byId = projects.get(column.project_id);
    if (!byId) {
      byId = new Map<string, Column>();
      projects.set(column.project_id, byId);
    }
    byId.set(column.id, column);
  });
  return projects;
}

export const columnsByProject = columnLookupByProject;

export function columnForTask(
  task: Pick<Task, 'project_id' | 'column_id'>,
  columns: Column[] | Map<string, Map<string, Column>>
): Column | undefined {
  if (columns instanceof Map) return columns.get(task.project_id)?.get(task.column_id);
  return columns.find((column) => column.project_id === task.project_id && column.id === task.column_id);
}

export function bugReporterId(task: Task): string {
  return task.bug?.reporter_id || '';
}

export function bugSeverity(task: Task): BugSeverity | '' {
  return task.bug?.severity || '';
}

export function bugResolution(task: Task): string {
  return task.bug?.resolution || '';
}

export function filterTasks(tasks: Task[], columns: Column[], filters: BoardFilters): Task[] {
  const query = filters.query.trim().toLowerCase();
  const columnById = new Map(columns.map((column) => [column.id, column]));
  return tasks.filter((task) => {
    const column = columnById.get(task.column_id);
    if (
      query &&
      !`${task.key} ${task.title} ${task.description ?? ''} ${task.bug?.actual_behavior ?? ''} ${task.bug?.expected_behavior ?? ''} ${task.bug?.reproduction_steps ?? ''} ${task.bug?.environment ?? ''} ${task.bug?.affected_version ?? ''}`
        .toLowerCase()
        .includes(query)
    ) return false;
    if (filters.priority !== 'all' && task.priority !== filters.priority) return false;
    if (filters.state !== 'all' && column?.semantic_state !== filters.state) return false;
    if (filters.assignee !== 'all' && actorId(task.assignee) !== filters.assignee) return false;
    if (filters.label !== 'all' && !(task.labels ?? []).some((label) => label.id === filters.label || label.name === filters.label)) {
      return false;
    }
    if (filters.kind && filters.kind !== 'all' && (task.kind || 'task') !== filters.kind) return false;
    if (filters.severity && filters.severity !== 'all') {
      if ((filters.severity === 'untriaged' || filters.severity === 'none') && bugSeverity(task)) return false;
      if (filters.severity !== 'untriaged' && filters.severity !== 'none' && bugSeverity(task) !== filters.severity) return false;
    }
    if (filters.reporter && filters.reporter !== 'all' && bugReporterId(task) !== filters.reporter) return false;
    if (filters.resolution && filters.resolution !== 'all') {
      if ((filters.resolution === 'open' || filters.resolution === 'unresolved' || filters.resolution === 'none') && bugResolution(task)) return false;
      if (filters.resolution !== 'open' && filters.resolution !== 'unresolved' && filters.resolution !== 'none' && bugResolution(task) !== filters.resolution) return false;
    }
    return true;
  });
}

export function sortTasks(tasks: Task[]): Task[] {
  return [...tasks].sort((a, b) => a.position - b.position || a.number - b.number);
}

export function nextPosition(tasks: Task[], columnId: string): number {
  const inColumn = tasks.filter((task) => task.column_id === columnId);
  return inColumn.length ? Math.max(...inColumn.map((task) => task.position)) + 1 : 0;
}

export function moveTaskLocal(tasks: Task[], taskId: string, destinationColumnId: string): Task[] {
  const position = nextPosition(tasks, destinationColumnId);
  return tasks.map((task) =>
    task.id === taskId ? { ...task, column_id: destinationColumnId, position, version: task.version + 1 } : task
  );
}

export function projectInitials(project: Pick<Project, 'name' | 'key'>): string {
  const words = project.name.trim().split(/\s+/).filter(Boolean);
  if (words.length > 1) return words.slice(0, 2).map((word) => word[0]).join('').toUpperCase();
  return (project.key || project.name).slice(0, 2).toUpperCase();
}

export function toInputDate(value?: string | null): string {
  if (!value) return '';
  const normalized = value.trim();
  const datePart = normalized.match(/^(\d{4}-\d{2}-\d{2})(?:$|[T ])/);
  if (datePart) return datePart[1];
  const date = new Date(normalized);
  if (Number.isNaN(date.getTime())) return value.slice(0, 10);
  return date.toISOString().slice(0, 10);
}

export function dateToIso(value: string): string | null {
  const normalized = value.trim();
  if (!normalized) return null;
  // Due dates are day-oriented in the UI; use the end of the UTC day so a
  // task remains due throughout the date selected by the user.
  return `${normalized}T23:59:59Z`;
}

export function isOverdue(value?: string | null): boolean {
  if (!value) return false;
  const due = new Date(value);
  return !Number.isNaN(due.getTime()) && due.getTime() < Date.now();
}

export function isDueSoon(value?: string | null): boolean {
  if (!value) return false;
  const due = new Date(value);
  if (Number.isNaN(due.getTime())) return false;
  const now = Date.now();
  return due.getTime() >= now && due.getTime() <= now + 1000 * 60 * 60 * 24 * 7;
}

export function formatDate(value?: string | null): string {
  if (!value) return '';
  const normalized = value.trim();
  const datePart = normalized.match(/^(\d{4}-\d{2}-\d{2})(?:$|[T ])/);
  const date = datePart ? new Date(`${datePart[1]}T00:00:00Z`) : new Date(normalized);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric', year: 'numeric', timeZone: 'UTC' }).format(date);
}

export function formatRelative(value?: string | null): string {
  if (!value) return '';
  const time = new Date(value).getTime();
  if (Number.isNaN(time)) return '';
  const minutes = Math.round((time - Date.now()) / 60000);
  const absolute = Math.abs(minutes);
  if (absolute < 1) return 'just now';
  if (absolute < 60) return `${absolute}m ${minutes < 0 ? 'ago' : 'from now'}`;
  const hours = Math.round(absolute / 60);
  if (hours < 24) return `${hours}h ${minutes < 0 ? 'ago' : 'from now'}`;
  const days = Math.round(hours / 24);
  if (days < 7) return `${days}d ${minutes < 0 ? 'ago' : 'from now'}`;
  return formatDate(value);
}

export function displayEvent(event: { action?: string; type?: string; kind?: string; message?: string | null }): string {
  if (event.message) return event.message;
  const raw = event.action || event.type || event.kind || 'updated task';
  return raw.replace(/[_-]+/g, ' ').replace(/\b\w/g, (letter) => letter.toUpperCase());
}

export function loadRecentProjects(storage: Storage | undefined, key = 'roadmap.recent-projects'): string[] {
  if (!storage) return [];
  try {
    const parsed = JSON.parse(storage.getItem(key) || '[]');
    return Array.isArray(parsed) ? parsed.filter((value): value is string => typeof value === 'string') : [];
  } catch {
    return [];
  }
}

export function rememberProject(
  projectId: string,
  storage: Storage | undefined,
  key = 'roadmap.recent-projects'
): string[] {
  const next = [projectId, ...loadRecentProjects(storage, key).filter((id) => id !== projectId)].slice(0, 6);
  storage?.setItem(key, JSON.stringify(next));
  return next;
}
