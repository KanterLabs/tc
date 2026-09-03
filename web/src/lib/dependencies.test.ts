import { describe, expect, it } from 'vitest';
import {
  dependencyActionExplanation,
  dependencyBlocked,
  dependencyEventTaskIds,
  dependencyMatches,
  dependencyMoveExplanation,
  dependencyReadiness
} from './dependencies';
import type { Column, Task } from './types';

function task(prerequisites: number, unmet: number): Pick<Task, 'dependency_summary'> {
  return {
    dependency_summary: {
      prerequisite_count: prerequisites,
      unmet_prerequisite_count: unmet,
      dependent_count: 0,
      blocked: unmet > 0
    }
  };
}

function column(semantic_state: Column['semantic_state']): Pick<Column, 'semantic_state'> {
  return { semantic_state };
}

describe('dependency readiness UI helpers', () => {
  it('distinguishes blocked, dependency-ready, and unrelated tasks', () => {
    expect(dependencyReadiness(task(2, 1))).toBe('blocked');
    expect(dependencyReadiness(task(2, 0))).toBe('ready');
    expect(dependencyReadiness(task(0, 0))).toBe('none');
    expect(dependencyReadiness({})).toBe('none');
    expect(dependencyBlocked(task(2, 1))).toBe(true);
  });

  it('matches server filter semantics: ready requires at least one prerequisite', () => {
    expect(dependencyMatches(task(2, 1), 'blocked')).toBe(true);
    expect(dependencyMatches(task(2, 0), 'ready')).toBe(true);
    expect(dependencyMatches(task(0, 0), 'ready')).toBe(false);
    expect(dependencyMatches(task(2, 1), 'all')).toBe(true);
  });

  it('explains blocked actions with a count and guards only lifecycle moves', () => {
    expect(dependencyActionExplanation(task(1, 1), 'claim this task')).toBe(
      'Waiting on 1 unfinished prerequisite. Finish it before you claim this task.'
    );
    expect(dependencyActionExplanation(task(3, 2))).toContain('2 unfinished prerequisites');
    expect(dependencyMoveExplanation(task(2, 1), column('active'))).toContain('start this task');
    expect(dependencyMoveExplanation(task(2, 1), column('completed'))).toContain('complete this task');
    expect(dependencyMoveExplanation(task(2, 1), column('blocked'))).toBe('');
    expect(dependencyMoveExplanation(task(2, 0), column('active'))).toBe('');
  });

  it('invalidates both sides of dependency events without duplicating IDs', () => {
    expect(dependencyEventTaskIds({
      type: 'task.dependency_added',
      task_id: 'dependent',
      payload: { dependent_id: 'dependent', prerequisite_id: 'prerequisite' }
    })).toEqual(['dependent', 'prerequisite']);
    expect(dependencyEventTaskIds({ type: 'task.completed', task_id: 'prerequisite' })).toEqual([]);
  });
});
