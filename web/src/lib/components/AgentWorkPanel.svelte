<script lang="ts">
  import type { Task } from '../types';
  import AgentPulse, { type AgentWorkLike } from './AgentPulse.svelte';

  export let task: Task;
  export let now = Date.now();
  export let actorLabel = '';

  $: work = readWork(task);
  $: state = work?.state || '';
  $: phase = work?.phase || '';
  $: summary = work?.summary || '';
  $: nextAction = work?.next_action || '';
  $: started = work?.started_at || '';
  $: updated = work?.updated_at || '';
  $: progress = readProgress(work);
  $: stale = Boolean(work && (work.stale || staleByAge(updated, now)));
  $: stateLabel = state
    ? stale && state !== 'waiting'
      ? 'Stale'
      : state.charAt(0).toUpperCase() + state.slice(1)
    : 'No live pulse';
  $: panelLabel = work
    ? `${actorLabel || work.actor_id || 'Agent'} work, ${stateLabel}${phase ? `, ${phase}` : ''}`
    : 'No live agent work pulse';

  function readWork(value: Task): AgentWorkLike | null {
    const candidate = (value as Task & { agent_work?: unknown }).agent_work;
    return candidate && typeof candidate === 'object' ? candidate as AgentWorkLike : null;
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

  function staleByAge(value: string, timestamp: number): boolean {
    if (!value) return false;
    const parsed = Date.parse(value);
    return !Number.isNaN(parsed) && timestamp - parsed >= 15 * 60 * 1000;
  }

  function formatDateTime(value: string): string {
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

<section class:missing={!work} class:stale class="agent-work-panel" aria-labelledby="agent-work-heading">
  <div class="agent-work-heading">
    <div>
      <span class="eyebrow">Live agent work</span>
      <h2 id="agent-work-heading">{work ? `${actorLabel || work.actor_id || 'Agent'} · ${stateLabel}` : 'No live pulse yet'}</h2>
    </div>
    <span class="agent-work-state" aria-label={panelLabel}>{work ? stateLabel : '?'}</span>
  </div>
  {#if work}
    <AgentPulse {task} {now} {actorLabel} compact={false} />
    {#if stale}<p class="agent-work-stale">{state === 'waiting' ? 'Waiting update is stale.' : 'This agent update is stale and needs attention.'}</p>{/if}
    <dl class="agent-work-details">
      {#if phase}<div><dt>Phase</dt><dd>{phase}</dd></div>{/if}
      {#if progress}<div><dt>Progress</dt><dd>{progress.completed} of {progress.total} checkpoints ({progress.percent}%)</dd></div>{/if}
      {#if summary}<div><dt>Summary</dt><dd>{summary}</dd></div>{/if}
      {#if nextAction}<div><dt>Next</dt><dd>{nextAction}</dd></div>{/if}
      {#if started}<div><dt>Started</dt><dd><time datetime={started}>{formatDateTime(started)}</time></dd></div>{/if}
      {#if updated}<div><dt>Updated</dt><dd><time datetime={updated}>{formatDateTime(updated)}</time></dd></div>{/if}
    </dl>
  {:else}
    <p class="agent-work-empty">This task has no reported agent progress. A live pulse will appear when an agent checks in.</p>
  {/if}
</section>
