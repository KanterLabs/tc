<script lang="ts">
  import type { ActorSummary, Column, Project, Task } from '../types';
  import {
    actorId,
    actorName,
    agentWorkActionNeeded,
    agentWorkForTask,
    agentWorkProgressLabel,
    agentWorkUpdatedAt,
    displayAgentWorkStatus,
    roadmapLiveWorkCounts,
    sortRoadmapLiveWork
  } from '../state';
  import AgentPulse from './AgentPulse.svelte';

  export let tasks: Task[] = [];
  export let projects: Project[] = [];
  export let columnsByProject: Record<string, Column[]> = {};
  export let actors: Record<string, ActorSummary> = {};
  export let now = Date.now();
  export let loading = false;
  export let error = '';
  export let onOpen: (task: Task) => void = () => undefined;
  export let onViewAll: () => void = () => undefined;

  $: sortedTasks = sortRoadmapLiveWork(tasks, now, semanticStateForTask);
  $: counts = roadmapLiveWorkCounts(tasks, now, semanticStateForTask);

  function semanticStateForTask(task: Task): string {
    return columnsByProject[task.project_id]?.find((column) => column.id === task.column_id)?.semantic_state || '';
  }

  function projectForTask(task: Task): Project | undefined {
    return projects.find((project) => project.id === task.project_id);
  }

  function columnForTask(task: Task): Column | undefined {
    return columnsByProject[task.project_id]?.find((column) => column.id === task.column_id);
  }

  function actorDisplay(value: Task['assignee'] | Task['claimed_by']): string {
    const id = actorId(value);
    return actorName(value) || actors[id]?.name || id || 'Unassigned';
  }

  function agentDisplay(task: Task): string {
    const actor = agentWorkForTask(task)?.actor_id || '';
    return actors[actor]?.name || actor || 'Unknown agent';
  }

  function progressLabel(task: Task): string {
    const progress = agentWorkProgressLabel(task);
    return progress ? `${progress} checkpoints` : '';
  }

  function stateLabel(task: Task): string {
    const state = displayAgentWorkStatus(task, now);
    return state === 'stale' ? 'Stale' : state.charAt(0).toUpperCase() + state.slice(1);
  }

  function relative(value: string): string {
    const parsed = Date.parse(value);
    if (Number.isNaN(parsed)) return 'Unknown update time';
    const minutes = Math.round((parsed - now) / 60000);
    const absolute = Math.abs(minutes);
    if (absolute < 1) return 'just now';
    if (absolute < 60) return `${absolute}m ${minutes < 0 ? 'ago' : 'from now'}`;
    const hours = Math.round(absolute / 60);
    if (hours < 24) return `${hours}h ${minutes < 0 ? 'ago' : 'from now'}`;
    return `${Math.round(hours / 24)}d ${minutes < 0 ? 'ago' : 'from now'}`;
  }

  function sortSummary(task: Task): string {
    const work = agentWorkForTask(task);
    return work?.summary || 'No summary was published for this update.';
  }

  function rowLabel(task: Task): string {
    return `Open ${task.key}: ${task.title} activity`;
  }
</script>

<section class="roadmap-live-work" aria-labelledby="roadmap-live-work-heading">
  <div class="roadmap-panel-heading">
    <div>
      <span class="eyebrow">Follow along</span>
      <h2 id="roadmap-live-work-heading">Agent work</h2>
      <p>Active pulses across the projects in this view.</p>
    </div>
    <button class="button quiet-button compact-button" type="button" on:click={onViewAll}>View all live work</button>
  </div>

  <div class="roadmap-live-counts" role="list" aria-label="Live agent work counts">
    <article role="listitem" class="roadmap-live-count working">
      <span class="roadmap-live-count-dot" aria-hidden="true"></span>
      <span><strong>{counts.working}</strong><small>Working</small></span>
    </article>
    <article role="listitem" class="roadmap-live-count needs-you">
      <span class="roadmap-live-count-dot" aria-hidden="true"></span>
      <span><strong>{counts.needsYou}</strong><small>Needs you</small></span>
    </article>
    <article role="listitem" class="roadmap-live-count stale">
      <span class="roadmap-live-count-dot" aria-hidden="true"></span>
      <span><strong>{counts.stale}</strong><small>Stale</small></span>
    </article>
  </div>

  {#if error}
    <div class="inline-alert error" role="alert"><span>!</span><span>{error}</span></div>
  {:else if loading}
    <div class="roadmap-live-loading" aria-label="Loading agent work"><span class="spinner"></span><span>Loading live work…</span></div>
  {:else if !sortedTasks.length}
    <div class="roadmap-live-empty"><span class="empty-icon" aria-hidden="true">◌</span><p>No agents are reporting work right now.</p></div>
  {:else}
    <div class="roadmap-live-list" aria-label="Prioritized live agent work">
      {#each sortedTasks.slice(0, 5) as task (task.id)}
        {@const project = projectForTask(task)}
        {@const work = agentWorkForTask(task)}
        <button class="roadmap-live-row" type="button" aria-label={rowLabel(task)} on:click={() => onOpen(task)}>
          <span class="work-project-dot" style={`--project-color: ${project?.color || '#6d5efc'}`} aria-hidden="true"></span>
          <span class="roadmap-live-row-main">
            <span class="roadmap-live-row-top">
              <span class="task-key">{task.key}</span>
              <span class={`priority-pill priority-${task.priority}`}>{task.priority}</span>
              {#if agentWorkActionNeeded(task, now, semanticStateForTask(task))}<span class="roadmap-live-attention">Needs you</span>{/if}
            </span>
            <strong>{task.title}</strong>
            <span class="roadmap-live-context">{project?.name || 'Project'} · {columnForTask(task)?.name || 'In progress'}</span>
            <span class="roadmap-live-summary">{sortSummary(task)}</span>
            <span class="roadmap-live-actors">
              <span><b>Agent</b> {agentDisplay(task)}</span>
              {#if task.assignee}<span><b>Assignee</b> {actorDisplay(task.assignee)}</span>{/if}
              {#if task.claimed_by}<span><b>Claimant</b> {actorDisplay(task.claimed_by)}</span>{/if}
            </span>
          </span>
          <span class="roadmap-live-row-status">
            <AgentPulse {task} {now} actorLabel={agentDisplay(task)} />
            <span class="roadmap-live-status-line"><strong>{stateLabel(task)}</strong>{#if work?.phase}<span>{work.phase}</span>{/if}{#if progressLabel(task)}<span>{progressLabel(task)}</span>{/if}</span>
            {#if agentWorkUpdatedAt(task)}<time datetime={agentWorkUpdatedAt(task)}>Updated {relative(agentWorkUpdatedAt(task))}</time>{/if}
          </span>
          <span class="row-arrow" aria-hidden="true">→</span>
        </button>
      {/each}
    </div>
    {#if sortedTasks.length > 5}<p class="roadmap-live-more">Showing the five highest-priority updates. View all live work for the rest.</p>{/if}
  {/if}
</section>
