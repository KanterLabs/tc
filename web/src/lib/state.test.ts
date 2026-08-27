import { describe, expect, it } from 'vitest';
import { dateToIso, filterTasks, formatDate, moveTaskLocal, nextPosition, projectInitials, toInputDate } from './state';
import type { Column, Task } from './types';

const columns: Column[] = [
  { id: 'backlog', project_id: 'p', name: 'Backlog', semantic_state: 'backlog', position: 0 },
  { id: 'active', project_id: 'p', name: 'In progress', semantic_state: 'active', position: 1 }
];

const tasks: Task[] = [
  { id: '1', number: 1, key: 'OPS-1', project_id: 'p', column_id: 'backlog', title: 'Write launch copy', priority: 'high', position: 0, version: 1, labels: [{ id: 'design', project_id: 'p', name: 'Design' }] },
  { id: '2', number: 2, key: 'OPS-2', project_id: 'p', column_id: 'active', title: 'Ship API client', priority: 'normal', position: 0, version: 2, assignee: { id: 'alex', kind: 'human', name: 'Alex' } }
];

describe('board state helpers', () => {
  it('filters by text, priority, label, and column semantic state', () => {
    expect(filterTasks(tasks, columns, { query: 'launch', priority: 'all', label: 'all', assignee: 'all', state: 'all' }).map((task) => task.id)).toEqual(['1']);
    expect(filterTasks(tasks, columns, { query: '', priority: 'high', label: 'all', assignee: 'all', state: 'all' }).map((task) => task.id)).toEqual(['1']);
    expect(filterTasks(tasks, columns, { query: '', priority: 'all', label: 'design', assignee: 'all', state: 'all' }).map((task) => task.id)).toEqual(['1']);
    expect(filterTasks(tasks, columns, { query: '', priority: 'all', label: 'all', assignee: 'all', state: 'active' }).map((task) => task.id)).toEqual(['2']);
  });

  it('calculates an append position and moves a task without mutating input', () => {
    expect(nextPosition(tasks, 'active')).toBe(1);
    const moved = moveTaskLocal(tasks, '1', 'active');
    expect(moved).not.toBe(tasks);
    expect(moved.find((task) => task.id === '1')).toMatchObject({ column_id: 'active', position: 1, version: 2 });
    expect(tasks[0].column_id).toBe('backlog');
  });

  it('creates readable project initials', () => {
    expect(projectInitials({ name: 'Product Operations', key: 'OPS' })).toBe('PO');
    expect(projectInitials({ name: 'Roadmap', key: 'RM' })).toBe('RM');
  });

  it('keeps due dates stable as calendar days across conversions and formatting', () => {
    expect(dateToIso(' 2026-08-27 ')).toBe('2026-08-27T23:59:59Z');
    expect(toInputDate('2026-08-27')).toBe('2026-08-27');
    expect(toInputDate('2026-08-27T23:59:59Z')).toBe('2026-08-27');
    expect(formatDate('2026-08-27')).toBe(formatDate('2026-08-27T23:59:59Z'));
  });
});
