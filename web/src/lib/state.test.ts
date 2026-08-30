import { describe, expect, it } from 'vitest';
import {
  agentWorkBucket,
  agentWorkActionNeeded,
  agentWorkProgressLabel,
  agentWorkStatusCounts,
  bugReporterId,
  bugResolution,
  bugSeverity,
  columnLookupByProject,
  dateToIso,
  displayAgentWorkStatus,
  filterTasks,
  formatDate,
  groupLiveWork,
  isAgentWorkStale,
  isMissingAgentWorkCandidate,
  liveWorkGroup,
  matchesAgentWorkFilter,
  moveTaskLocal,
  nextPosition,
  projectInitials,
  roadmapActivityKind,
  roadmapActivityLabel,
  roadmapLiveWorkCounts,
  matchesRoadmapActivity,
  parseTaskRoute,
  sortRoadmapLiveWork,
  sortLiveWork,
  shouldShowAgentPulse,
  taskDeepLink,
  toInputDate
} from './state';
import type { AgentWork, Column, Task } from './types';

const columns: Column[] = [
  { id: 'backlog', project_id: 'p', name: 'Backlog', semantic_state: 'backlog', position: 0 },
  { id: 'active', project_id: 'p', name: 'In progress', semantic_state: 'active', position: 1 }
];

const tasks: Task[] = [
  { id: '1', number: 1, key: 'OPS-1', project_id: 'p', column_id: 'backlog', title: 'Write launch copy', priority: 'high', position: 0, version: 1, labels: [{ id: 'design', project_id: 'p', name: 'Design' }] },
  { id: '2', number: 2, key: 'OPS-2', project_id: 'p', column_id: 'active', title: 'Ship API client', priority: 'normal', position: 0, version: 2, assignee: { id: 'alex', kind: 'human', name: 'Alex' } }
];

const bugTask: Task = {
  id: 'bug-1', number: 3, key: 'OPS-3', project_id: 'p', column_id: 'active', title: 'Save button fails', kind: 'bug',
  bug: {
    reporter_id: 'sam', severity: 's1', actual_behavior: 'The button does nothing', expected_behavior: 'The form saves',
    reproduction_steps: 'Click Save', environment: 'Firefox', affected_version: '1.2.0', resolution: 'fixed'
  },
  priority: 'urgent', position: 1, version: 1
};

const workNow = Date.parse('2026-08-28T12:00:00Z');

function liveTask(id: string, work: Partial<AgentWork> | null, updatedAt?: string): Task {
  return {
    id,
    number: Number(id.replace(/\D/g, '')) || 1,
    key: `OPS-${id}`,
    project_id: 'p',
    column_id: 'active',
    title: id,
    priority: 'normal',
    position: 0,
    version: 1,
    updated_at: updatedAt,
    agent_work: work
      ? {
          operation_id: 'operation-1',
          actor_id: 'agent-1',
          state: 'working',
          summary: 'Update',
          checkpoint_refs: [],
          started_at: '2026-08-28T10:00:00Z',
          updated_at: '2026-08-28T11:59:00Z',
          stale: false,
          action_needed: false,
          ...work
        }
      : null
  };
}

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

  it('filters and reads nested bug metadata', () => {
    expect(bugReporterId(bugTask)).toBe('sam');
    expect(bugSeverity(bugTask)).toBe('s1');
    expect(bugResolution(bugTask)).toBe('fixed');
    expect(filterTasks([...tasks, bugTask], columns, { query: '', priority: 'all', label: 'all', assignee: 'all', state: 'all', kind: 'bug' })).toEqual([bugTask]);
    expect(filterTasks([bugTask], columns, { query: '', priority: 'all', label: 'all', assignee: 'all', state: 'all', severity: 's1', reporter: 'sam', resolution: 'fixed' })).toEqual([bugTask]);
    expect(filterTasks([bugTask], columns, { query: '', priority: 'all', label: 'all', assignee: 'all', state: 'all', resolution: 'open' })).toEqual([]);
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

  it('marks agent work stale at exactly fifteen minutes and handles bad timestamps', () => {
    expect(isAgentWorkStale('2026-08-28T11:45:00Z', workNow)).toBe(true);
    expect(isAgentWorkStale('2026-08-28T11:45:01Z', workNow)).toBe(false);
    expect(isAgentWorkStale('2026-08-28T12:01:00Z', workNow)).toBe(false);
    expect(isAgentWorkStale('not-a-timestamp', workNow)).toBe(false);
    expect(isAgentWorkStale('2026-08-28T11:45:00Z', 'not-a-timestamp')).toBe(false);
  });

  it('classifies missing, fresh, stale, and waiting work without losing waiting context', () => {
    const fresh = liveTask('fresh', { state: 'working', updated_at: '2026-08-28T11:46:00Z' });
    const stale = liveTask('stale', { state: 'working', updated_at: '2026-08-28T11:45:00Z' });
    const waiting = liveTask('waiting', { state: 'waiting', updated_at: '2026-08-28T10:00:00Z' });
    const handoff = liveTask('handoff', { state: 'handoff', updated_at: '2026-08-28T11:59:00Z' });
    const missing = liveTask('missing', null);

    expect(displayAgentWorkStatus(fresh, workNow)).toBe('working');
    expect(displayAgentWorkStatus(stale, workNow)).toBe('stale');
    expect(displayAgentWorkStatus(waiting, workNow)).toBe('waiting');
    expect(agentWorkBucket(waiting, workNow)).toBe('stale');
    expect(agentWorkActionNeeded(stale, workNow)).toBe(true);
    expect(agentWorkActionNeeded(waiting, workNow)).toBe(true);
    expect(agentWorkActionNeeded(handoff, workNow)).toBe(true);
    expect(displayAgentWorkStatus(missing, workNow)).toBe('missing');
    expect(agentWorkActionNeeded(missing, workNow)).toBe(false);
    expect(displayAgentWorkStatus(liveTask('bad', { updated_at: 'bad' }), workNow)).toBe('working');
  });

  it('keeps retained completed snapshots out of every live-work surface', () => {
    const completed = {
      ...liveTask('completed', {
        state: 'waiting',
        updated_at: '2026-08-28T10:00:00Z',
        stale: true,
        action_needed: true
      }),
      completed_at: '2026-08-28T11:00:00Z'
    };

    expect(isAgentWorkStale(completed, workNow)).toBe(false);
    expect(agentWorkActionNeeded(completed, workNow, 'completed')).toBe(false);
    expect(agentWorkBucket(completed, workNow)).toBe('completed');
    expect(shouldShowAgentPulse(completed, 'completed')).toBe(false);
    expect(isMissingAgentWorkCandidate({ ...completed, agent_work: null }, 'completed')).toBe(false);
    for (const filter of ['action-needed', 'working', 'waiting', 'verifying', 'stale', 'handoff', 'missing'] as const) {
      expect(matchesAgentWorkFilter(completed, filter, workNow, 'completed')).toBe(false);
    }
    expect(matchesAgentWorkFilter(completed, 'all', workNow, 'completed')).toBe(true);
    expect(agentWorkStatusCounts([completed], workNow, () => 'completed')).toEqual({
      actionNeeded: 0,
      working: 0,
      waiting: 0,
      verifying: 0,
      stale: 0,
      handoff: 0,
      missing: 0
    });
    expect(sortLiveWork([completed], workNow)).toEqual([]);
    expect(completed.agent_work?.summary).toBe('Update');
  });

  it('uses completed semantic state when a legacy payload omits completed_at', () => {
    const legacyCompleted = liveTask('legacy-completed', { state: 'working' });
    expect(shouldShowAgentPulse(legacyCompleted, 'completed')).toBe(false);
    expect(matchesAgentWorkFilter(legacyCompleted, 'working', workNow, 'completed')).toBe(false);
  });

  it('formats checkpoint progress and refuses malformed or partial counts', () => {
    expect(agentWorkProgressLabel(liveTask('full', { checkpoint_completed: 2, checkpoint_total: 4 }))).toBe('2/4');
    expect(agentWorkProgressLabel(liveTask('refs', { checkpoint_completed: 1, checkpoint_refs: ['a', 'b', 'c'] }))).toBe('1/3');
    expect(agentWorkProgressLabel(liveTask('partial', { checkpoint_completed: 1 }))).toBe('');
    expect(agentWorkProgressLabel(liveTask('invalid', { checkpoint_completed: 4, checkpoint_total: 2 }))).toBe('');
    expect(agentWorkProgressLabel(liveTask('none', null))).toBe('');
  });

  it('sorts live work by attention bucket then newest update and groups it', () => {
    const tasks = [
      liveTask('working', { state: 'working', updated_at: '2026-08-28T11:59:00Z' }),
      liveTask('missing', null, '2026-08-28T11:58:00Z'),
      liveTask('verifying', { state: 'verifying', updated_at: '2026-08-28T11:59:30Z' }),
      liveTask('handoff', { state: 'handoff', updated_at: '2026-08-28T11:57:00Z' }),
      liveTask('waiting', { state: 'waiting', updated_at: '2026-08-28T11:56:00Z' }),
      liveTask('stale', { state: 'working', updated_at: '2026-08-28T11:00:00Z' })
    ];

    expect(sortLiveWork(tasks, workNow).map((task) => task.id)).toEqual([
      'handoff',
      'waiting',
      'stale',
      'verifying',
      'working',
      'missing'
    ]);
    expect(tasks.map((task) => task.id)).toEqual(['working', 'missing', 'verifying', 'handoff', 'waiting', 'stale']);
    expect(liveWorkGroup(tasks[5], workNow)).toBe('stale');
    expect(groupLiveWork(tasks, workNow).waiting.map((task) => task.id)).toEqual(['waiting']);
    expect(groupLiveWork(tasks, workNow).missing.map((task) => task.id)).toEqual(['missing']);
  });

  it('builds independent per-project column lookups', () => {
    const lookup = columnLookupByProject([
      ...columns,
      { id: 'ready-2', project_id: 'p-2', name: 'Ready', semantic_state: 'ready', position: 0 }
    ]);
    expect(lookup.get('p')?.get('active')?.name).toBe('In progress');
    expect(lookup.get('p-2')?.get('ready-2')?.semantic_state).toBe('ready');
    expect(lookup.get('p')?.get('ready-2')).toBeUndefined();
  });

  it('prioritizes Roadmap live work and exposes scoped counts', () => {
    const stale = liveTask('stale', { state: 'working', updated_at: '2026-08-28T11:00:00Z' });
    const waiting = liveTask('waiting', { state: 'waiting', updated_at: '2026-08-28T11:56:00Z' });
    const working = liveTask('working', { state: 'working', updated_at: '2026-08-28T11:59:00Z' });
    expect(sortRoadmapLiveWork([working, stale, waiting], workNow).map((task) => task.id)).toEqual(['waiting', 'stale', 'working']);
    expect(roadmapLiveWorkCounts([working, stale, waiting], workNow)).toEqual({ working: 1, needsYou: 2, stale: 1 });
  });

  it('classifies recent activity without inventing timeline records', () => {
    expect(roadmapActivityKind({ type: 'task.progressed' })).toBe('agent-updates');
    expect(roadmapActivityKind({ type: 'comment.created' })).toBe('comments');
    expect(roadmapActivityKind({ type: 'task.moved' })).toBe('task-changes');
    expect(matchesRoadmapActivity({ type: 'task.progressed' }, 'agent-updates')).toBe(true);
    expect(matchesRoadmapActivity({ type: 'task.progressed' }, 'comments')).toBe(false);
    expect(roadmapActivityLabel({ type: 'task.progressed' })).toBe('updated agent progress');
    expect(roadmapActivityLabel({ type: 'task.moved' })).toBe('moved the task');
  });

  it('round-trips stable task links and activity intent aliases', () => {
    expect(taskDeepLink('Product Ops', 'OPS-7', 'activity')).toBe('/p/Product%20Ops/tasks/OPS-7?view=activity');
    expect(parseTaskRoute('/p/Product%20Ops/tasks/OPS-7', '?view=activity')).toEqual({
      projectSlug: 'Product Ops',
      taskReference: 'OPS-7',
      intent: 'activity'
    });
    expect(parseTaskRoute('/p/Product%20Ops/tasks/OPS-7', '?intent=activity')).toMatchObject({ intent: 'activity' });
    expect(parseTaskRoute('/p/Product%20Ops/tasks/OPS-7', '', '#activity')).toMatchObject({ intent: 'activity' });
    expect(parseTaskRoute('/p/Product%20Ops/roadmap')).toBeNull();
  });
});
