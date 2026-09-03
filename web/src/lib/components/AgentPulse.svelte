<script context="module" lang="ts">
  import type { AgentWork } from '../types';
  export { agentPulseAccessibleLabel } from './agentPulseText';

  export type AgentWorkState = AgentWork['state'];
  export type AgentWorkLike = Partial<AgentWork>;
</script>

<script lang="ts">
  import type { Task } from '../types';
  import { agentPulseAccessibleLabel } from './agentPulseText';

  export let task: Task;
  export let now = Date.now();
  export let actorLabel = '';
  export let compact = true;

  const stateLabels: Record<string, string> = {
    working: 'Working',
    waiting: 'Waiting',
    verifying: 'Verifying',
    handoff: 'Handoff'
  };
  const stateIcons: Record<string, string> = {
    working: '▶',
    waiting: 'Ⅱ',
    verifying: '✓',
    handoff: '↗'
  };

  $: work = readWork(task);
  $: state = work?.state || '';
  $: missing = !work;
  $: baseStateLabel = stateLabels[state] || (missing ? 'No live pulse' : 'Agent update');
  // A task's generic update timestamp is not an agent check-in. Keep missing
  // pulses visibly missing instead of implying a live update from edits.
  $: updatedAt = work?.updated_at || '';
  $: progress = readProgress(work);
  $: claimExpiry = task.claim_expires_at || '';
  $: claimExpired = Boolean(claimExpiry && parseDate(claimExpiry) <= now);
  $: claimCountdown = claimExpiry ? formatCountdown(claimExpiry, now) : '';
  $: claimOwner = actorFromTask(task) || '';
  $: stale = Boolean(work && (work.stale || staleByAge(state, updatedAt, now)));
  $: actionNeeded = Boolean(work && (work.action_needed || stale || state === 'waiting' || state === 'handoff'));
  // "live" marks a fresh, actively-working pulse; the CSS layer breathes its
  // icon so a busy board reads as moving without flashing attention states.
  $: live = Boolean(work && !stale && !actionNeeded && state === 'working');
  $: stateLabel = stale && state !== 'waiting' ? 'Stale' : baseStateLabel;
  $: label = agentPulseAccessibleLabel(task, now, actorLabel || work?.actor_id || '');

  function readWork(value: Task): AgentWorkLike | null {
    const candidate = (value as Task & { agent_work?: unknown }).agent_work;
    return candidate && typeof candidate === 'object' ? candidate as AgentWorkLike : null;
  }

  function actorFromTask(value: Task): string {
    const owner = value.claimed_by;
    if (!owner) return '';
    return typeof owner === 'string' ? owner : owner.name || owner.id;
  }

  function parseDate(value: string): number {
    const parsed = Date.parse(value);
    return Number.isNaN(parsed) ? Number.POSITIVE_INFINITY : parsed;
  }

  function readProgress(value: AgentWorkLike | null): { completed: number; total: number; percent: number } | null {
    if (!value) return null;
    const completed = firstNumber(value.checkpoint_completed);
    const total = firstNumber(
      value.checkpoint_total,
      Array.isArray(value.checkpoint_refs) && value.checkpoint_refs.length ? value.checkpoint_refs.length : undefined
    );
    if (completed === undefined || total === undefined || total <= 0) return null;
    const bounded = Math.max(0, Math.min(total, completed));
    return { completed: bounded, total, percent: Math.round((bounded / total) * 100) };
  }

  function firstNumber(...values: Array<number | null | undefined>): number | undefined {
    return values.find((value): value is number => typeof value === 'number' && Number.isFinite(value));
  }

  function formatCountdown(value: string, timestamp: number): string {
    const difference = parseDate(value) - timestamp;
    if (!Number.isFinite(difference)) return '';
    if (difference <= 0) return 'expired';
    const minutes = Math.ceil(difference / 60000);
    if (minutes < 60) return `${minutes}m left`;
    const hours = Math.floor(minutes / 60);
    const remainder = minutes % 60;
    if (hours < 24) return `${hours}h${remainder ? ` ${remainder}m` : ''} left`;
    const days = Math.floor(hours / 24);
    return `${days}d${hours % 24 ? ` ${hours % 24}h` : ''} left`;
  }

  function formatRelative(value: string, timestamp: number): string {
    const parsed = parseDate(value);
    if (!Number.isFinite(parsed)) return 'Unknown update time';
    const difference = Math.round((parsed - timestamp) / 60000);
    const absolute = Math.abs(difference);
    if (absolute < 1) return 'just now';
    if (absolute < 60) return `${absolute}m ${difference < 0 ? 'ago' : 'from now'}`;
    const hours = Math.round(absolute / 60);
    if (hours < 24) return `${hours}h ${difference < 0 ? 'ago' : 'from now'}`;
    const days = Math.round(hours / 24);
    return `${days}d ${difference < 0 ? 'ago' : 'from now'}`;
  }

  function staleByAge(_currentState: string, value: string, timestamp: number): boolean {
    if (!value) return false;
    const age = timestamp - parseDate(value);
    return Number.isFinite(age) && age >= 15 * 60 * 1000;
  }

  function formatExact(value: string): string {
    const parsed = new Date(value);
    if (Number.isNaN(parsed.getTime())) return value;
    return new Intl.DateTimeFormat(undefined, {
      year: 'numeric',
      month: 'short',
      day: 'numeric',
      hour: 'numeric',
      minute: '2-digit',
      timeZoneName: 'short'
    }).format(parsed);
  }
</script>

<div class:compact class:missing class:stale class:live class:needsAction={actionNeeded} class="agent-pulse" role="img" aria-label={label}>
  <span class="agent-pulse-icon" aria-hidden="true">{missing ? '?' : stateIcons[state] || '•'}</span>
  <span class="agent-pulse-copy">
    <strong>{stateLabel}</strong>
    {#if stale && state === 'waiting'}<span class="agent-pulse-secondary">Stale update</span>{:else if stale && baseStateLabel !== stateLabel}<span class="agent-pulse-secondary">{baseStateLabel} update is stale</span>{:else if actionNeeded}<span class="agent-pulse-secondary">Action needed</span>{/if}
    {#if work?.actor_id || actorLabel}<span class="agent-pulse-agent">Agent {actorLabel || work?.actor_id}</span>{/if}
    {#if work?.phase}<span class="agent-pulse-phase">{work.phase}</span>{/if}
    {#if missing}<span class="agent-pulse-missing-copy">{task.claimed_by ? 'Claimed work has no live update' : 'No live agent update yet'}</span>{/if}
    {#if progress}
      <span class="agent-pulse-progress" aria-label={`${progress.completed} of ${progress.total} checkpoints complete`}>
        <span class="agent-pulse-track"><span style={`width: ${progress.percent}%`}></span></span>
        <small>{progress.completed}/{progress.total}</small>
      </span>
    {/if}
    {#if updatedAt}<time datetime={updatedAt}>Updated {formatRelative(updatedAt, now)}</time>{/if}
    {#if claimExpiry}
      <span class:expired={claimExpired} class="agent-pulse-claim">
        <span>{claimOwner ? `Claimed by ${claimOwner}` : 'Claimed'}</span>
        <time datetime={claimExpiry}>· {claimExpired ? 'Expired' : claimCountdown} · {formatExact(claimExpiry)}</time>
      </span>
    {/if}
  </span>
</div>
