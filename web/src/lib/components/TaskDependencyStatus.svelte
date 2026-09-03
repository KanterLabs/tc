<script context="module" lang="ts">
  import type { DependencySummary } from '../types';

  export function prerequisiteBadgeLabel(summary: DependencySummary): string {
    if (summary.blocked || summary.unmet_prerequisite_count > 0) {
      return `${summary.unmet_prerequisite_count} waiting`;
    }
    return `${summary.prerequisite_count} ready`;
  }

  export function prerequisiteAccessibleLabel(summary: DependencySummary): string {
    const finished = Math.max(0, summary.prerequisite_count - summary.unmet_prerequisite_count);
    return `${finished} of ${summary.prerequisite_count} prerequisites finished; ${summary.unmet_prerequisite_count} unfinished`;
  }
</script>

<script lang="ts">
  import { dependencyActionExplanation } from '../dependencies';
  import type { Task } from '../types';

  export let task: Task;
  export let mode: 'badges' | 'notice' = 'badges';
  $: summary = task.dependency_summary;
</script>

{#if mode === 'notice'}
  {#if summary?.blocked}
    <div class="dependency-notice" role="note" aria-label="Dependency-blocked actions">
      <span class="notice-icon" aria-hidden="true">⌛</span>
      <span>
        <strong>{summary.unmet_prerequisite_count} unfinished prerequisite{summary.unmet_prerequisite_count === 1 ? '' : 's'}</strong>
        <small>{dependencyActionExplanation(task)}</small>
      </span>
    </div>
  {/if}
{:else if summary && (summary.prerequisite_count > 0 || summary.dependent_count > 0)}
  <span class="dependency-badges" aria-label="Task dependencies">
    {#if summary.prerequisite_count > 0}
      <span
        class:blocked={summary.blocked}
        class:ready={!summary.blocked}
        class="dependency-badge"
        title={prerequisiteAccessibleLabel(summary)}
        aria-label={prerequisiteAccessibleLabel(summary)}
      ><span aria-hidden="true">{summary.blocked ? '⌛' : '✓'}</span>{prerequisiteBadgeLabel(summary)}</span>
    {/if}
    {#if summary.dependent_count > 0}
      <span class="dependency-badge dependents" title={`Blocking ${summary.dependent_count} dependent tasks`}>
        <span aria-hidden="true">↗</span>{summary.dependent_count} dependent{summary.dependent_count === 1 ? '' : 's'}
      </span>
    {/if}
  </span>
{/if}

<style>
  .dependency-badges { display: flex; flex-wrap: wrap; gap: 4px; margin-top: 8px; }
  .dependency-badge { display: inline-flex; align-items: center; gap: 4px; padding: 3px 6px; border: 1px solid transparent; border-radius: 5px; font-size: 8px; font-weight: 800; line-height: 1.2; }
  .dependency-badge.blocked { color: var(--red); border-color: color-mix(in srgb, var(--red), transparent 72%); background: var(--red-soft); }
  .dependency-badge.ready { color: var(--green); border-color: color-mix(in srgb, var(--green), transparent 72%); background: var(--green-soft); }
  .dependency-badge.dependents { color: var(--purple); border-color: color-mix(in srgb, var(--purple), transparent 76%); background: var(--purple-soft); }
  .dependency-notice { display: flex; align-items: flex-start; gap: 9px; margin: 12px 22px 0; padding: 10px 11px; border: 1px solid color-mix(in srgb, var(--red), transparent 68%); border-radius: 9px; color: var(--red); background: var(--red-soft); }
  .notice-icon { flex: 0 0 auto; padding-top: 1px; font-size: 13px; }
  .dependency-notice > span:last-child { min-width: 0; display: grid; gap: 2px; }
  .dependency-notice strong { font-size: 10px; }
  .dependency-notice small { color: var(--ink-soft); font-size: 9px; line-height: 1.4; }
  :global(.task-card.dependency-blocked) { border-color: color-mix(in srgb, var(--red), var(--border) 52%); box-shadow: 0 2px 9px color-mix(in srgb, var(--red), transparent 88%); }
  :global(.task-card.dependency-blocked:hover) { border-color: color-mix(in srgb, var(--red), var(--border) 28%); }
  @media (max-width: 520px) {
    .dependency-notice { margin-right: 16px; margin-left: 16px; }
  }
</style>
