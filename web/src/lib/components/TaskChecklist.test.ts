// @vitest-environment jsdom
import { mount, tick, unmount } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { api } from '../api';
import type { Task, TaskChecklistItem } from '../types';
import TaskChecklistPanel, { checklistMove, checklistPercent } from './TaskChecklist.svelte';

const mountedComponents: Array<ReturnType<typeof mount>> = [];

afterEach(async () => {
  while (mountedComponents.length) await unmount(mountedComponents.pop()!);
  document.body.replaceChildren();
  vi.restoreAllMocks();
});

function item(id: string, text: string, position: number, completed = false): TaskChecklistItem {
  return { id, task_id: 'task-1', text, position, completed };
}

function task(items: TaskChecklistItem[] = []): Task {
  const completed = items.filter((entry) => entry.completed).length;
  return {
    id: 'task-1',
    number: 1,
    key: 'TC-1',
    project_id: 'project-1',
    column_id: 'column-1',
    title: 'Checklist task',
    priority: 'normal',
    position: 1,
    version: 1,
    checklist: items,
    checklist_summary: { total: items.length, completed, open: items.length - completed, percent: checklistPercent(completed, items.length), completion_policy: 'warn', warning: false }
  };
}

describe('TaskChecklist helpers', () => {
  it('calculates bounded progress and keyboard-friendly order moves', () => {
    expect(checklistPercent(0, 0)).toBe(0);
    expect(checklistPercent(1, 3)).toBe(33);
    const items = [item('one', 'One', 0), item('two', 'Two', 1), item('three', 'Three', 2)];
    expect(checklistMove(items, 'two', -1)).toEqual(['two', 'one', 'three']);
    expect(checklistMove(items, 'two', 1)).toEqual(['one', 'three', 'two']);
    expect(checklistMove(items, 'one', -1)).toEqual(['one', 'two', 'three']);
  });
});

describe('TaskChecklist component', () => {
  it('adds criteria with the form keyboard contract and reports progress', async () => {
    const current = task();
    const onTaskUpdated = vi.fn();
    const add = vi.spyOn(api, 'addTaskChecklistItem').mockResolvedValue({
      ...task([item('one', 'Ship API', 0)]),
      version: 2,
      checklist_summary: { total: 1, completed: 0, open: 1, percent: 0, completion_policy: 'warn', warning: false }
    });
    mountedComponents.push(mount(TaskChecklistPanel, { target: document.body, props: { task: current, onTaskUpdated } }));
    const input = document.querySelector<HTMLInputElement>('#checklist-new-item');
    expect(input).not.toBeNull();
    input!.value = 'Ship API';
    input!.dispatchEvent(new Event('input', { bubbles: true }));
    document.querySelector<HTMLFormElement>('.checklist-add')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    await vi.waitFor(() => expect(add).toHaveBeenCalledWith('task-1', { text: 'Ship API' }, 1));
    await vi.waitFor(() => expect(onTaskUpdated).toHaveBeenCalledWith(expect.objectContaining({ version: 2 })));
    expect(document.body.textContent).toContain('0/0');
  });

  it('checks, edits, reorders, and removes items through labeled controls', async () => {
    const current = task([item('one', 'One', 0), item('two', 'Two', 1)]);
    const onTaskUpdated = vi.fn();
    vi.spyOn(api, 'updateTaskChecklistItem').mockResolvedValue(task([item('one', 'Updated one', 0, true), item('two', 'Two', 1)]));
    vi.spyOn(api, 'reorderTaskChecklist').mockResolvedValue(task([item('two', 'Two', 0), item('one', 'One', 1, true)]));
    vi.spyOn(api, 'deleteTaskChecklistItem').mockResolvedValue(task([item('two', 'Two', 0)]));
    mountedComponents.push(mount(TaskChecklistPanel, { target: document.body, props: { task: current, onTaskUpdated } }));

    document.querySelector<HTMLInputElement>('[aria-label="Complete checklist item: One"]')!.click();
    await vi.waitFor(() => expect(api.updateTaskChecklistItem).toHaveBeenCalledWith('task-1', 'one', { completed: true }, 1));
    await tick();

    const textInput = document.querySelector<HTMLInputElement>('[aria-label="Edit checklist item: One"]')!;
    textInput.value = 'Updated one';
    textInput.dispatchEvent(new Event('input', { bubbles: true }));
    textInput.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
    await vi.waitFor(() => expect(api.updateTaskChecklistItem).toHaveBeenCalledWith('task-1', 'one', { text: 'Updated one' }, 1));

    const moveUp = document.querySelector<HTMLButtonElement>('[aria-label="Move checklist item up: Two"]')!;
    await vi.waitFor(() => expect(moveUp.disabled).toBe(false));
    moveUp.click();
    await vi.waitFor(() => expect(api.reorderTaskChecklist).toHaveBeenCalledWith('task-1', ['two', 'one'], 1));
    const remove = document.querySelector<HTMLButtonElement>('[aria-label="Remove checklist item: One"]')!;
    await vi.waitFor(() => expect(remove.disabled).toBe(false));
    remove.click();
    await vi.waitFor(() => expect(api.deleteTaskChecklistItem).toHaveBeenCalledWith('task-1', 'one', 1));
    expect(onTaskUpdated).toHaveBeenCalled();
  });
});
