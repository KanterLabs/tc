// @vitest-environment jsdom
import { mount, tick, unmount } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { api } from '../api';
import { ApiError, type Task, type TaskDependencies, type TaskReference } from '../types';
import TaskDependenciesPanel, {
  dependencyCandidates,
  dependencyMutationMessage,
  dependencyStateLabel,
  nextDependencyOptionIndex
} from './TaskDependencies.svelte';

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

function reference(value: Task, satisfied = false): TaskReference {
  return { id: value.id, key: value.key, title: value.title, completed_at: satisfied ? '2026-09-03T00:00:00Z' : null, satisfied };
}

describe('TaskDependencies combobox helpers', () => {
  it('searches key/title while excluding self and edges in either direction', () => {
    const current = task('current', 9, 'Current card');
    const prerequisite = task('prerequisite', 10, 'Existing prerequisite');
    const dependent = task('dependent', 11, 'Existing dependent');
    const candidate = task('candidate', 12, 'Release candidate');
    const later = task('later', 20, 'Candidate follow-up');
    const relations: TaskDependencies = {
      prerequisites: [reference(prerequisite)],
      dependents: [reference(dependent)]
    };

    expect(dependencyCandidates([later, dependent, candidate, prerequisite, current], current.id, relations, 'candidate'))
      .toEqual([candidate, later]);
    expect(dependencyCandidates([candidate], current.id, relations, 'tc-12')).toEqual([candidate]);
    expect(dependencyCandidates([candidate], current.id, relations, '   ')).toEqual([]);
  });

  it('wraps arrow navigation and supports Home/End', () => {
    expect(nextDependencyOptionIndex(-1, 'ArrowDown', 3)).toBe(0);
    expect(nextDependencyOptionIndex(-1, 'ArrowUp', 3)).toBe(2);
    expect(nextDependencyOptionIndex(2, 'ArrowDown', 3)).toBe(0);
    expect(nextDependencyOptionIndex(0, 'ArrowUp', 3)).toBe(2);
    expect(nextDependencyOptionIndex(1, 'Home', 3)).toBe(0);
    expect(nextDependencyOptionIndex(1, 'End', 3)).toBe(2);
    expect(nextDependencyOptionIndex(0, 'ArrowDown', 0)).toBe(-1);
  });
});

describe('TaskDependencies status and error helpers', () => {
  it('keeps completion state concise and readable', () => {
    const value = task('one', 1, 'One');
    expect(dependencyStateLabel(reference(value))).toBe('Open');
    expect(dependencyStateLabel(reference(value, true))).toBe('Done');
  });

  it('gives stale versions and graph conflicts actionable copy without retrying', () => {
    expect(dependencyMutationMessage({ code: 'stale_task' }, 'fallback')).toContain('Relationships were refreshed');
    expect(dependencyMutationMessage({ code: 'dependency_cycle' }, 'fallback')).toContain('cycle');
    expect(dependencyMutationMessage({ code: 'unmet_dependencies' }, 'fallback')).toContain('after work has started');
    expect(dependencyMutationMessage({ message: 'Server detail' }, 'fallback')).toBe('Server detail');
    expect(dependencyMutationMessage(null, 'fallback')).toBe('fallback');
  });
});

describe('TaskDependencies component', () => {
  it('renders loading, authorization failure, retry, and the empty graph', async () => {
    let rejectLoad!: (reason: unknown) => void;
    const initialLoad = new Promise<TaskDependencies>((_resolve, reject) => { rejectLoad = reject; });
    vi.spyOn(api, 'getTaskDependencies')
      .mockReturnValueOnce(initialLoad)
      .mockResolvedValueOnce({ prerequisites: [], dependents: [] });
    vi.spyOn(api, 'listTasks').mockResolvedValue({ data: [], next_cursor: null });

    mountedComponents.push(mount(TaskDependenciesPanel, { target: document.body, props: { task: task('current', 9, 'Current card') } }));
    expect(document.body.textContent).toContain('Loading relationships');

    rejectLoad(new Error('Dependency access was denied.'));
    await vi.waitFor(() => expect(document.body.textContent).toContain('Dependencies could not be loaded.'));
    const retry = [...document.querySelectorAll('button')].find((button) => button.textContent === 'Retry');
    retry?.click();

    await vi.waitFor(() => {
      expect(document.body.textContent).toContain('No prerequisites yet.');
      expect(document.body.textContent).toContain('No tasks depend on this one.');
    });
  });

  it('adds a prerequisite, announces success, and restores combobox focus', async () => {
    const current = task('current', 9, 'Current card');
    const candidate = task('candidate', 12, 'Release candidate');
    let relations: TaskDependencies = { prerequisites: [], dependents: [] };
    vi.spyOn(api, 'getTaskDependencies').mockImplementation(async () => relations);
    vi.spyOn(api, 'listTasks').mockResolvedValue({ data: [candidate], next_cursor: null });
    const add = vi.spyOn(api, 'addTaskDependency').mockImplementation(async () => {
      relations = { prerequisites: [reference(candidate)], dependents: [] };
      return { ...current, version: 2, dependency_summary: { prerequisite_count: 1, unmet_prerequisite_count: 1, dependent_count: 0, blocked: true } };
    });
    const onTaskUpdated = vi.fn();

    mountedComponents.push(mount(TaskDependenciesPanel, {
      target: document.body,
      props: { task: current, onTaskUpdated }
    }));
    const input = await vi.waitFor(() => {
      const element = document.querySelector<HTMLInputElement>('#dependency-search');
      expect(element?.disabled).toBe(false);
      return element!;
    });
    input.value = 'release';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    await tick();
    document.querySelector<HTMLElement>('[role="option"]')?.click();

    await vi.waitFor(() => expect(onTaskUpdated).toHaveBeenCalledWith(expect.objectContaining({ version: 2 })));
    expect(add).toHaveBeenCalledTimes(1);
    expect(add).toHaveBeenCalledWith('current', 'candidate', 1);
    expect(document.body.textContent).toContain('TC-12 added as a prerequisite.');
    await vi.waitFor(() => expect(document.activeElement).toBe(input));
  });

  it('removes one edge with isolated pending state and restores combobox focus', async () => {
    const current = task('current', 9, 'Current card');
    const prerequisite = task('prerequisite', 10, 'Existing prerequisite');
    let relations: TaskDependencies = { prerequisites: [reference(prerequisite)], dependents: [] };
    vi.spyOn(api, 'getTaskDependencies').mockImplementation(async () => relations);
    vi.spyOn(api, 'listTasks').mockResolvedValue({ data: [], next_cursor: null });
    const remove = vi.spyOn(api, 'removeTaskDependency').mockImplementation(async () => {
      relations = { prerequisites: [], dependents: [] };
      return { ...current, version: 2, dependency_summary: { prerequisite_count: 0, unmet_prerequisite_count: 0, dependent_count: 0, blocked: false } };
    });

    mountedComponents.push(mount(TaskDependenciesPanel, { target: document.body, props: { task: current } }));
    const input = await vi.waitFor(() => {
      const element = document.querySelector<HTMLInputElement>('#dependency-search');
      expect(element).not.toBeNull();
      return element!;
    });
    const removeButton = await vi.waitFor(() => {
      const element = document.querySelector<HTMLButtonElement>('[aria-label="Remove TC-10 as a prerequisite"]');
      expect(element).not.toBeNull();
      return element!;
    });
    removeButton.click();

    await vi.waitFor(() => expect(remove).toHaveBeenCalledWith('current', 'prerequisite', 1));
    await vi.waitFor(() => expect(document.body.textContent).toContain('No prerequisites yet.'));
    await vi.waitFor(() => expect(document.activeElement).toBe(input));
  });

  it('recovers a stale ETag without retrying and exposes polling refresh', async () => {
    const current = task('current', 9, 'Current card');
    const candidate = task('candidate', 12, 'Release candidate');
    const dependent = task('dependent', 13, 'Blocked follow-up');
    let relations: TaskDependencies = { prerequisites: [], dependents: [] };
    vi.spyOn(api, 'getTaskDependencies').mockImplementation(async () => relations);
    vi.spyOn(api, 'listTasks').mockResolvedValue({ data: [candidate], next_cursor: null });
    const stale = new ApiError('Task changed.', 409, 'stale_task', { current: { ...current, version: 2 } });
    const add = vi.spyOn(api, 'addTaskDependency').mockRejectedValue(stale);
    const onTaskUpdated = vi.fn();
    const onRefreshTask = vi.fn();

    const mounted = mount(TaskDependenciesPanel, {
      target: document.body,
      props: { task: current, onTaskUpdated, onRefreshTask }
    });
    mountedComponents.push(mounted);
    const input = await vi.waitFor(() => {
      const element = document.querySelector<HTMLInputElement>('#dependency-search');
      expect(element).not.toBeNull();
      return element!;
    });
    input.value = 'release';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    await tick();
    document.querySelector<HTMLElement>('[role="option"]')?.click();

    await vi.waitFor(() => expect(document.body.textContent).toContain('Relationships were refreshed'));
    expect(add).toHaveBeenCalledTimes(1);
    expect(onRefreshTask).toHaveBeenCalledTimes(1);

    relations = { prerequisites: [], dependents: [reference(dependent)] };
    await mounted.refreshRelationships();
    await vi.waitFor(() => expect(document.body.textContent).toContain('Blocked follow-up'));
  });
});
