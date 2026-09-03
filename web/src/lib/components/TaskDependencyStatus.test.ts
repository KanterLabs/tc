import { describe, expect, it } from 'vitest';
import type { DependencySummary } from '../types';
import { prerequisiteAccessibleLabel, prerequisiteBadgeLabel } from './TaskDependencyStatus.svelte';

function summary(prerequisites: number, unmet: number): DependencySummary {
  return { prerequisite_count: prerequisites, unmet_prerequisite_count: unmet, dependent_count: 0, blocked: unmet > 0 };
}

describe('task dependency status labels', () => {
  it('keeps blocked and ready badges readable without relying on color', () => {
    expect(prerequisiteBadgeLabel(summary(3, 2))).toBe('2 waiting');
    expect(prerequisiteBadgeLabel(summary(3, 0))).toBe('3 ready');
    expect(prerequisiteAccessibleLabel(summary(3, 2))).toBe('1 of 3 prerequisites finished; 2 unfinished');
  });
});
