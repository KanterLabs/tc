import { describe, expect, it } from 'vitest';
import { hierarchyBadgeLabel, hierarchyEventTaskIds } from './hierarchy';
import type { Task, TaskHierarchy, TaskHierarchyReference } from './types';
import {
  hierarchyCandidates,
  hierarchyMutationMessage,
  nextHierarchyOptionIndex
} from './components/TaskHierarchy.svelte';

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

function hierarchy(overrides: Partial<TaskHierarchy> = {}): TaskHierarchy {
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

describe('task hierarchy UI helpers', () => {
  it('searches project tasks while excluding self, ancestors, and descendants', () => {
    const current = task('current', 1, 'Current card');
    const ancestor = task('ancestor', 2, 'Existing ancestor');
    const descendant = task('descendant', 3, 'Existing descendant');
    const candidate = task('candidate', 4, 'Release candidate');
    const later = task('later', 5, 'Candidate follow-up');
    const graph = hierarchy({ ancestors: [reference(ancestor)], descendants: [reference(descendant)] });

    expect(hierarchyCandidates([later, descendant, candidate, ancestor, current], current.id, graph, 'candidate'))
      .toEqual([candidate, later]);
    expect(hierarchyCandidates([candidate], current.id, graph, '   ')).toEqual([]);
  });

  it('wraps keyboard navigation and keeps graph events scoped to both sides', () => {
    expect(nextHierarchyOptionIndex(-1, 'ArrowDown', 3)).toBe(0);
    expect(nextHierarchyOptionIndex(-1, 'ArrowUp', 3)).toBe(2);
    expect(nextHierarchyOptionIndex(2, 'ArrowDown', 3)).toBe(0);
    expect(nextHierarchyOptionIndex(1, 'Home', 3)).toBe(0);
    expect(nextHierarchyOptionIndex(1, 'End', 3)).toBe(2);
    expect(nextHierarchyOptionIndex(0, 'ArrowDown', 0)).toBe(-1);
    expect(hierarchyEventTaskIds({
      type: 'task.parent_linked',
      task_id: 'child',
      payload: { child_id: 'child', parent_id: 'parent' }
    })).toEqual(['child', 'parent']);
    expect(hierarchyEventTaskIds({
      type: 'task.parent_unlinked',
      task_id: 'child',
      payload: { child_id: 'child', previous_parent_id: 'old-parent' }
    })).toEqual(['child', 'old-parent']);
  });

  it('labels rollups accessibly and explains graph conflicts', () => {
    const parent = task('parent', 10, 'Parent');
    parent.hierarchy_summary = {
      child_count: 2,
      completed_child_count: 1,
      completion_percent: 50,
      state_counts: { completed: 1, active: 1 },
      blocked_child_count: 0,
      live_agent_work_count: 1,
      action_needed_count: 0,
      stale_agent_work_count: 0
    };
    expect(hierarchyBadgeLabel(parent)).toBe('2 children');
    expect(hierarchyBadgeLabel({ parent_id: 'parent' })).toBe('Child task');
    expect(hierarchyMutationMessage({ code: 'hierarchy_cycle' }, 'fallback')).toContain('cycle');
    expect(hierarchyMutationMessage({ code: 'hierarchy_depth_exceeded' }, 'fallback')).toContain('depth');
    expect(hierarchyMutationMessage(null, 'fallback')).toBe('fallback');
  });
});
