import { mount, unmount } from 'svelte';
import { afterEach, describe, expect, it } from 'vitest';
import { agentPulseAccessibleLabel } from './AgentPulse.svelte';
import AgentPulse from './AgentPulse.svelte';
import type { AgentWork, Task } from '../types';

const now = Date.parse('2026-08-28T12:00:00Z');
let taskSequence = 0;
const mountedComponents: Array<ReturnType<typeof mount>> = [];

afterEach(async () => {
  while (mountedComponents.length) await unmount(mountedComponents.pop()!);
  document.body.replaceChildren();
});

function task(work: Partial<AgentWork> | null, claimed = false): Task {
  const sequence = ++taskSequence;
  return {
    id: `task-${sequence}`,
    number: sequence,
    key: `OPS-${sequence}`,
    project_id: 'project-1',
    column_id: 'active',
    title: 'Agent task',
    priority: 'normal',
    position: 0,
    version: 1,
    claimed_by: claimed ? { id: 'agent-1', kind: 'agent', name: 'Build bot' } : undefined,
    agent_work: work
      ? {
          operation_id: 'operation-1',
          actor_id: 'agent-1',
          state: 'working',
          phase: 'Implement',
          summary: 'Working on the task',
          next_action: 'Continue',
          checkpoint_refs: ['one', 'two'],
          checkpoint_completed: 1,
          checkpoint_total: 2,
          started_at: '2026-08-28T10:00:00Z',
          updated_at: '2026-08-28T11:59:00Z',
          stale: false,
          action_needed: false,
          ...work
        }
      : null
  };
}

function render(value: Task) {
  return agentPulseAccessibleLabel(value, now);
}

describe('AgentPulse accessible state text', () => {
  it('identifies a missing pulse on claimed work', () => {
    const pulse = render(task(null, true));
    expect(pulse).toContain('No live pulse');
    expect(pulse).toContain('no live update reported');
    expect(pulse).toContain('claimed by Build bot');
    expect(pulse).toContain('claim expiry unavailable');
  });

  it('shows claim countdown, exact expiry, version, and an imminent-expiry warning', () => {
    const claimed = { ...task(null, true), claim_expires_at: '2026-08-28T12:04:00Z' };
    const pulse = render(claimed);
    expect(pulse).toContain('claimed by Build bot');
    expect(pulse).toContain('claim 4m left');
    expect(pulse).toContain('claim expires');
    expect(pulse).toContain('claim expiring soon');
    expect(pulse).toContain('task version v1');
  });

  it('identifies an expired claim in accessible state text', () => {
    const expired = { ...task(null, true), claim_expires_at: '2026-08-28T11:59:00Z' };
    const pulse = render(expired);
    expect(pulse).toContain('claim expired');
    expect(pulse).toContain('claim expires');
  });

  it('identifies fresh working work and checkpoint progress', () => {
    const pulse = render(task({}));
    expect(pulse).toContain('Working');
    expect(pulse).toContain('1 of 2 checkpoints');
    expect(pulse).toContain('agent agent-1');
    expect(pulse).toContain('updated 1m ago');
    expect(pulse).toContain('2026-08-28T11:59:00.000Z');
  });

  it('keeps waiting visible while calling out a stale update', () => {
    const pulse = render(task({ state: 'waiting', updated_at: '2026-08-28T11:00:00Z', stale: false }));
    expect(pulse).toContain('Waiting');
    expect(pulse).toContain('Waiting update is stale');
  });

  it('identifies verifying work', () => {
    const pulse = render(task({ state: 'verifying' }));
    expect(pulse).toContain('Verifying');
    expect(pulse).toContain('Verifying');
  });

  it('identifies stale working work separately from fresh work', () => {
    const pulse = render(task({ updated_at: '2026-08-28T11:44:00Z', stale: false }));
    expect(pulse).toContain('Stale');
    expect(pulse).toContain('Working update is stale');
    expect(pulse).toContain('Stale');
  });

  it('keeps relative and exact claim expiry available to assistive technology', () => {
    const pulse = render({
      ...task({}),
      claim_expires_at: '2026-08-28T12:30:00Z'
    });
    expect(pulse).toContain('claim 30m left');
    expect(pulse).toContain('2026-08-28T12:30:00.000Z');
  });

  it('renders structured pulse text instead of a role=img label blob', () => {
    const value = task({});
    mountedComponents.push(mount(AgentPulse, { target: document.body, props: { task: value, now } }));
    const pulse = document.querySelector<HTMLElement>('[data-agent-pulse]');
    expect(pulse?.getAttribute('role')).toBe('group');
    expect(pulse?.getAttribute('role')).not.toBe('img');
    expect(pulse?.getAttribute('aria-label')).toBeNull();
    expect(pulse?.textContent).toContain('Progress: 1 of 2 checkpoints (50%)');
    expect(pulse?.textContent).toContain('Updated 1m ago');
    expect(pulse?.textContent).toContain('2026-08-28T11:59:00.000Z');
  });

  it('never describes a retained completed snapshot as stale or actionable', () => {
    const completed = {
      ...task({ updated_at: '2026-08-28T10:00:00Z', stale: true, action_needed: true }),
      completed_at: '2026-08-28T11:00:00Z'
    };
    const pulse = render(completed);
    expect(pulse).toContain('Completed');
    expect(pulse).not.toContain('Stale');
    expect(pulse).not.toContain('action needed');
  });
});
