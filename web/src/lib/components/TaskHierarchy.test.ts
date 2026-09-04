// @vitest-environment jsdom
import { mount, unmount } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { api } from '../api';
import type { Task, TaskHierarchy, TaskHierarchyReference } from '../types';
import TaskHierarchyPanel from './TaskHierarchy.svelte';

const mountedComponents: Array<ReturnType<typeof mount>> = [];

afterEach(async () => {
  while (mountedComponents.length) await unmount(mountedComponents.pop()!);
  document.body.replaceChildren();
  vi.restoreAllMocks();
});

function task(id: string, number: number, title: string): Task {
  return {
    id,
    number,
    key: `TC-${number}`,
    project_id: 'project-1',
    column_id: 'column-1',
    title,
    priority: 'normal',
    position: number,
    version: 1
  };
}

function reference(value: Task): TaskHierarchyReference {
  return {
    id: value.id,
    number: value.number,
    key: value.key,
    project_id: value.project_id,
    title: value.title,
    kind: 'task',
    column_id: value.column_id,
    semantic_state: 'backlog',
    version: value.version
  };
}

function graph(overrides: Partial<TaskHierarchy> = {}): TaskHierarchy {
  return {
    parent: null,
    children: [],
    ancestors: [],
    descendants: [],
    summary: {
      child_count: 0,
      completed_child_count: 0,
      completion_percent: 0,
      state_counts: {},
      blocked_child_count: 0,
      live_agent_work_count: 0,
      action_needed_count: 0,
      stale_agent_work_count: 0
    },
    ...overrides
  };
}

describe('TaskHierarchy accessible panel', () => {
  it('renders parent, children, rollups, and navigation actions', async () => {
    const current = task('current', 1, 'Current');
    const parent = task('parent', 2, 'Parent');
    const child = task('child', 3, 'Child');
    const navigate = vi.fn();
    vi.spyOn(api, 'getTaskHierarchy').mockResolvedValue(graph({
      parent: reference(parent),
      children: [reference(child)],
      ancestors: [reference(parent)],
      summary: { child_count: 1, completed_child_count: 0, completion_percent: 0, state_counts: { backlog: 1 }, blocked_child_count: 1, live_agent_work_count: 1, action_needed_count: 1, stale_agent_work_count: 0 }
    }));
    vi.spyOn(api, 'listTasks').mockResolvedValue({ data: [], next_cursor: null });

    mountedComponents.push(mount(TaskHierarchyPanel, { target: document.body, props: { task: current, onNavigate: navigate } }));
    await vi.waitFor(() => expect(document.body.textContent).toContain('Parent'));
    expect(document.querySelector('[role="combobox"]')).toBeTruthy();
    expect(document.querySelector('[aria-label="Child rollup"]')).toBeTruthy();
    expect(document.body.textContent).toContain('Children');
    expect(document.body.textContent).toContain('Blocked');

    const parentButton = [...document.querySelectorAll('button')].find((button) => button.getAttribute('aria-label')?.startsWith('Open TC-2'));
    parentButton?.click();
    expect(navigate).toHaveBeenCalledWith(expect.objectContaining({ id: parent.id }));
  });

  it('links a keyboard-selected parent and announces the mutation', async () => {
    const current = task('current', 1, 'Current');
    const candidate = task('candidate', 2, 'Release candidate');
    let currentGraph = graph();
    const updated = { ...current, version: 2, parent_task_id: candidate.id, parent_id: candidate.id, parent: reference(candidate) };
    vi.spyOn(api, 'getTaskHierarchy').mockImplementation(async () => currentGraph);
    vi.spyOn(api, 'listTasks').mockResolvedValue({ data: [candidate], next_cursor: null });
    const setParent = vi.spyOn(api, 'setTaskParent').mockImplementation(async () => {
      currentGraph = graph({ parent: reference(candidate), ancestors: [reference(candidate)] });
      return updated;
    });
    const onTaskUpdated = vi.fn();

    mountedComponents.push(mount(TaskHierarchyPanel, { target: document.body, props: { task: current, onTaskUpdated } }));
    await vi.waitFor(() => expect(document.querySelector('[role="combobox"]')).toBeTruthy());
    const input = document.querySelector('[role="combobox"]') as HTMLInputElement;
    await vi.waitFor(() => expect(input.disabled).toBe(false));
    input.value = 'release';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    await vi.waitFor(() => expect(document.querySelector('[role="option"]')).toBeTruthy());
    input.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }));
    input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
    await vi.waitFor(() => expect(setParent).toHaveBeenCalledWith(current.id, candidate.id, current.version));
    await vi.waitFor(() => expect(onTaskUpdated).toHaveBeenCalledWith(updated));
    expect(document.querySelector('[aria-live="polite"]')?.textContent).toContain('now this task’s parent');
  });

  it('uses the hierarchy version and restores focus after removing a child', async () => {
    const current = task('current', 1, 'Current');
    const child = task('child', 2, 'Child');
    const childReference = { ...reference(child), version: 2 };
    let currentGraph = graph({ children: [childReference], summary: {
      child_count: 1,
      completed_child_count: 0,
      completion_percent: 0,
      state_counts: { backlog: 1 },
      blocked_child_count: 0,
      live_agent_work_count: 0,
      action_needed_count: 0,
      stale_agent_work_count: 0
    } });
    vi.spyOn(api, 'getTaskHierarchy').mockImplementation(async () => currentGraph);
    vi.spyOn(api, 'listTasks').mockResolvedValue({ data: [child], next_cursor: null });
    const removeTaskChild = vi.spyOn(api, 'removeTaskChild').mockImplementation(async () => {
      currentGraph = graph();
      return { ...child, version: 3, parent_task_id: null, parent_id: null, parent: null };
    });

    mountedComponents.push(mount(TaskHierarchyPanel, { target: document.body, props: { task: current } }));
    const removeButton = await vi.waitFor(() => {
      const button = document.querySelector('[aria-label="Remove TC-2 as a child"]');
      expect(button).toBeTruthy();
      return button as HTMLButtonElement;
    });
    removeButton.click();

    await vi.waitFor(() => expect(removeTaskChild).toHaveBeenCalledWith(current.id, child.id, childReference.version));
    await vi.waitFor(() => expect(document.body.textContent).toContain('No children yet.'));
    expect(document.activeElement).toBe(document.getElementById('task-children-heading'));
  });
});
