import { describe, expect, it } from 'vitest';
import {
  mergeAuthoritativeTask,
  mergeAuthoritativeTaskList,
  taskMutationsAfter,
  type TaskMutationRecord
} from './liveness';
import type { Task } from './types';

function task(id: string, version: number, updatedAt: string): Task {
  return {
    id,
    number: Number(id),
    key: `TASK-${id}`,
    project_id: 'project-1',
    column_id: 'column-1',
    title: `Task ${id}`,
    description: '',
    priority: 'normal',
    position: 1,
    version,
    updated_at: updatedAt
  };
}

describe('liveness task merges', () => {
  it('keeps a newer local version when a background read is stale', () => {
    const local = task('1', 2, '2026-08-28T12:02:00Z');
    const fetched = task('1', 1, '2026-08-28T12:03:00Z');

    expect(mergeAuthoritativeTask(local, fetched)).toBe(local);
  });

  it('accepts equal-version heartbeat payloads', () => {
    const local = task('1', 2, '2026-08-28T12:02:00Z');
    const fetched = {
      ...task('1', 2, '2026-08-28T12:03:00Z'),
      agent_work: {
        operation_id: 'op-1',
        actor_id: 'agent-1',
        state: 'working' as const,
        summary: 'Still working',
        checkpoint_refs: [],
        started_at: '2026-08-28T12:00:00Z',
        updated_at: '2026-08-28T12:03:00Z',
        stale: false,
        action_needed: false
      }
    };

    expect(mergeAuthoritativeTask(local, fetched)).toBe(fetched);
  });

  it('does not regress an equal-version heartbeat when reads finish out of order', () => {
    const local = {
      ...task('1', 2, '2026-08-28T12:03:00Z'),
      agent_work: {
        operation_id: 'op-1',
        actor_id: 'agent-1',
        state: 'working' as const,
        summary: 'Still working',
        checkpoint_refs: [],
        started_at: '2026-08-28T12:00:00Z',
        updated_at: '2026-08-28T12:03:00Z',
        stale: false,
        action_needed: false
      }
    };
    const fetched = {
      ...local,
      agent_work: { ...local.agent_work, updated_at: '2026-08-28T12:02:00Z' }
    };

    expect(mergeAuthoritativeTask(local, fetched)).toBe(local);
  });

  it('accepts equal-version dependency readiness while preserving a newer local pulse', () => {
    const local = {
      ...task('1', 2, '2026-08-28T12:03:00Z'),
      dependency_summary: { prerequisite_count: 1, unmet_prerequisite_count: 1, dependent_count: 0, blocked: true },
      agent_work: {
        operation_id: 'op-1',
        actor_id: 'agent-1',
        state: 'working' as const,
        summary: 'Fresh local pulse',
        checkpoint_refs: [],
        started_at: '2026-08-28T12:00:00Z',
        updated_at: '2026-08-28T12:03:00Z',
        stale: false,
        action_needed: false
      }
    };
    const fetched = {
      ...local,
      dependency_summary: { prerequisite_count: 1, unmet_prerequisite_count: 0, dependent_count: 0, blocked: false },
      agent_work: { ...local.agent_work, summary: 'Older response pulse', updated_at: '2026-08-28T12:02:00Z' }
    };

    const merged = mergeAuthoritativeTask(local, fetched);
    expect(merged.agent_work).toBe(local.agent_work);
    expect(merged.dependency_summary).toEqual(fetched.dependency_summary);
  });

  it('preserves fetched collection removals', () => {
    const local = [task('1', 2, '2026-08-28T12:02:00Z'), task('2', 1, '2026-08-28T12:02:00Z')];
    const fetched = [task('1', 1, '2026-08-28T12:03:00Z')];

    expect(mergeAuthoritativeTaskList(local, fetched).map((item) => item.id)).toEqual(['1']);
    expect(mergeAuthoritativeTaskList(local, fetched)[0]).toBe(local[0]);
  });

  it('protects only mutations completed after the request snapshot', () => {
    const records = new Map<string, TaskMutationRecord>([
      ['1', { revision: 2, kind: 'upsert' }],
      ['2', { revision: 4, kind: 'remove' }]
    ]);

    expect([...taskMutationsAfter(records, 2).entries()]).toEqual([['2', 'remove']]);
  });

  it('retains a locally added task while keeping unrelated filtered removals authoritative', () => {
    const local = [task('1', 1, '2026-08-28T12:00:00Z'), task('2', 1, '2026-08-28T12:01:00Z')];
    const fetched = [task('1', 1, '2026-08-28T12:00:00Z')];

    expect(mergeAuthoritativeTaskList(local, fetched, new Map([['2', 'upsert']])).map((item) => item.id))
      .toEqual(['1', '2']);
  });

  it('prefers a protected local mutation at an unchanged task version', () => {
    const local = [{ ...task('1', 2, '2026-08-28T12:03:00Z'), title: 'Saved locally' }];
    const fetched = [{ ...task('1', 2, '2026-08-28T12:04:00Z'), title: 'Stale response' }];

    expect(mergeAuthoritativeTaskList(local, fetched, new Map([['1', 'upsert']]))[0]).toBe(local[0]);
  });

  it('merges equal-version dependency readiness into a protected local mutation', () => {
    const local = [{
      ...task('1', 2, '2026-08-28T12:03:00Z'),
      title: 'Saved locally',
      dependency_summary: { prerequisite_count: 1, unmet_prerequisite_count: 1, dependent_count: 0, blocked: true }
    }];
    const fetched = [{
      ...task('1', 2, '2026-08-28T12:04:00Z'),
      title: 'Stale response',
      dependency_summary: { prerequisite_count: 1, unmet_prerequisite_count: 0, dependent_count: 0, blocked: false }
    }];

    const merged = mergeAuthoritativeTaskList(local, fetched, new Map([['1', 'upsert']]))[0];
    expect(merged.title).toBe('Saved locally');
    expect(merged.dependency_summary).toEqual(fetched[0].dependency_summary);
  });

  it('suppresses a stale deleted task without retaining other omissions', () => {
    const local = [task('1', 1, '2026-08-28T12:00:00Z'), task('2', 1, '2026-08-28T12:01:00Z')];
    const fetched = [task('1', 1, '2026-08-28T12:00:00Z'), task('2', 2, '2026-08-28T12:02:00Z')];

    expect(mergeAuthoritativeTaskList(local, fetched, new Map([['2', 'remove']])).map((item) => item.id))
      .toEqual(['1']);
  });
});
