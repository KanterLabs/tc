import type { Column, Project, Task } from './types';

export interface BoardFilters {
  query: string;
  priority: string;
  label: string;
  assignee: string;
  state: string;
}

export function actorId(value: Task['assignee'] | Task['claimed_by']): string {
  if (!value) return '';
  return typeof value === 'string' ? value : value.id;
}

export function actorName(value: Task['assignee'] | Task['claimed_by']): string {
  if (!value) return '';
  return typeof value === 'string' ? value : value.name;
}

export function filterTasks(tasks: Task[], columns: Column[], filters: BoardFilters): Task[] {
  const query = filters.query.trim().toLowerCase();
  const columnById = new Map(columns.map((column) => [column.id, column]));
  return tasks.filter((task) => {
    const column = columnById.get(task.column_id);
    if (query && !`${task.key} ${task.title} ${task.description ?? ''}`.toLowerCase().includes(query)) return false;
    if (filters.priority !== 'all' && task.priority !== filters.priority) return false;
    if (filters.state !== 'all' && column?.semantic_state !== filters.state) return false;
    if (filters.assignee !== 'all' && actorId(task.assignee) !== filters.assignee) return false;
    if (filters.label !== 'all' && !(task.labels ?? []).some((label) => label.id === filters.label || label.name === filters.label)) {
      return false;
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
