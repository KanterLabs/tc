<script lang="ts">
  import type {
    ActivityEvent,
    ActorSummary,
    Project,
    RoadmapActivityFilter,
    Task
  } from '../types';
  import {
    roadmapActivityKind,
    roadmapActivityLabel,
    matchesRoadmapActivity,
    formatDate
  } from '../state';

  export let events: ActivityEvent[] = [];
  export let tasksById: Record<string, Task> = {};
  export let projects: Project[] = [];
  export let actors: Record<string, ActorSummary> = {};
  export let filter: RoadmapActivityFilter = 'all';
  export let loading = false;
  export let error = '';
  export let onFilterChange: (next: RoadmapActivityFilter) => void = () => undefined;
  export let onOpen: (event: ActivityEvent) => void = () => undefined;

  const filters: Array<{ value: RoadmapActivityFilter; label: string }> = [
    { value: 'all', label: 'All' },
    { value: 'agent-updates', label: 'Agent updates' },
    { value: 'comments', label: 'Comments' },
    { value: 'task-changes', label: 'Task changes' }
  ];

  $: visibleEvents = events
    .filter((event) => matchesRoadmapActivity(event, filter))
    .sort((a, b) => b.cursor - a.cursor);

  function taskForEvent(event: ActivityEvent): Task | undefined {
    return event.task_id ? tasksById[event.task_id] : undefined;
  }

  function projectForEvent(event: ActivityEvent, task: Task | undefined): Project | undefined {
    return projects.find((project) => project.id === (task?.project_id || event.project_id));
  }

  function actorLabel(event: ActivityEvent): string {
    const id = event.actor_id || '';
    return actors[id]?.name || id || 'Unknown actor';
  }

  function actorKind(event: ActivityEvent): string {
    const id = event.actor_id || '';
    return actors[id]?.kind === 'agent' ? 'Agent' : 'Human';
  }

  function eventTaskLabel(event: ActivityEvent, task: Task | undefined, project: Project | undefined): string {
    if (task) return `${task.key} · ${task.title}`;
    const number = event.payload?.number;
    if (typeof number === 'number' && project) return `${project.key}-${number}`;
    return event.task_id ? `Task ${event.task_id}` : project?.name || 'Workspace';
  }

  function eventContext(event: ActivityEvent, task: Task | undefined): string {
    const payload = event.payload || {};
    if (roadmapActivityKind(event) === 'agent-updates') {
      const state = typeof payload.state === 'string' ? payload.state : '';
      const phase = typeof payload.phase === 'string' ? payload.phase : '';
      const summary = typeof payload.summary === 'string' ? payload.summary : '';
      return [state ? state.charAt(0).toUpperCase() + state.slice(1) : '', phase, summary].filter(Boolean).join(' · ') || 'Agent progress update';
    }
    if (roadmapActivityKind(event) === 'comments') return 'Comment added to the task.';
    if (event.type === 'task.moved') {
      const destination = typeof payload.to_column === 'string' ? payload.to_column : typeof payload.to_column_state === 'string' ? payload.to_column_state : '';
      return destination ? `Moved to ${destination}.` : 'Task location changed.';
    }
    if (event.type === 'task.claimed' || event.type === 'task.claim_renewed') {
      const expiresAt = typeof payload.expires_at === 'string' ? payload.expires_at : '';
      return expiresAt ? `Claim active through ${formatDate(expiresAt)}.` : 'Claim status changed.';
    }
    if (event.type === 'bug.resolved' && typeof payload.resolution === 'string') return `Resolution: ${payload.resolution.replace(/_/g, ' ')}.`;
    if (task?.assignee) return 'Task ownership changed.';
    return 'Task details changed.';
  }

  function eventTime(event: ActivityEvent): string {
    return event.created_at ? new Date(event.created_at).toLocaleString(undefined, {
      dateStyle: 'medium',
      timeStyle: 'short'
    }) : 'Unknown time';
  }

  function relative(event: ActivityEvent): string {
    const parsed = Date.parse(event.created_at);
    if (Number.isNaN(parsed)) return 'Unknown time';
    const minutes = Math.round((parsed - Date.now()) / 60000);
    const absolute = Math.abs(minutes);
    if (absolute < 1) return 'just now';
    if (absolute < 60) return `${absolute}m ${minutes < 0 ? 'ago' : 'from now'}`;
    const hours = Math.round(absolute / 60);
    if (hours < 24) return `${hours}h ${minutes < 0 ? 'ago' : 'from now'}`;
    return `${Math.round(hours / 24)}d ${minutes < 0 ? 'ago' : 'from now'}`;
  }

  function rowLabel(event: ActivityEvent, task: Task | undefined): string {
    return `Open ${task?.key || 'task'} activity: ${roadmapActivityLabel(event)}`;
  }
</script>

<section class="roadmap-activity" aria-labelledby="roadmap-activity-heading">
  <div class="roadmap-panel-heading roadmap-activity-heading">
    <div>
      <span class="eyebrow">What changed</span>
      <h2 id="roadmap-activity-heading">Recent activity</h2>
      <p>The latest updates for the projects in this view.</p>
    </div>
    <div class="roadmap-activity-filters" role="group" aria-label="Filter recent activity">
      {#each filters as option}
        <button class:active={filter === option.value} class="roadmap-activity-filter" type="button" aria-pressed={filter === option.value} on:click={() => onFilterChange(option.value)}>{option.label}</button>
      {/each}
    </div>
  </div>

  {#if error}
    <div class="inline-alert error" role="alert"><span>!</span><span>{error}</span></div>
  {:else if loading}
    <div class="roadmap-activity-loading" aria-label="Loading recent activity"><span class="spinner"></span><span>Loading recent activity…</span></div>
  {:else if !visibleEvents.length}
    <div class="roadmap-activity-empty"><span class="empty-icon" aria-hidden="true">✦</span><p>No recent activity in this view.</p></div>
  {:else}
    <div class="roadmap-activity-list" aria-label="Recent activity updates">
      {#each visibleEvents as event (event.id || event.cursor)}
        {@const task = taskForEvent(event)}
        {@const project = projectForEvent(event, task)}
        <button class="roadmap-activity-row" type="button" aria-label={rowLabel(event, task)} on:click={() => onOpen(event)}>
          <span class:agent={actors[event.actor_id || '']?.kind === 'agent'} class="roadmap-activity-avatar" aria-hidden="true">{(actorLabel(event).slice(0, 1) || '?').toUpperCase()}</span>
          <span class="roadmap-activity-main">
            <span class="roadmap-activity-top"><span class={`roadmap-activity-kind ${roadmapActivityKind(event)}`}>{roadmapActivityKind(event) === 'agent-updates' ? 'Agent update' : roadmapActivityKind(event) === 'comments' ? 'Comment' : 'Task change'}</span><span class="roadmap-activity-task">{eventTaskLabel(event, task, project)}</span></span>
            <strong><span>{actorLabel(event)}</span> <span class="roadmap-activity-verb">{roadmapActivityLabel(event)}</span></strong>
            <span class="roadmap-activity-context">{eventContext(event, task)}</span>
            <span class="roadmap-activity-meta"><span>{actorKind(event)}</span>{#if project}<span>{project.name}</span>{/if}<time datetime={event.created_at} title={eventTime(event)}>{relative(event)}</time></span>
          </span>
          <span class="row-arrow" aria-hidden="true">→</span>
        </button>
      {/each}
    </div>
    <p class="roadmap-activity-note">Showing the latest {events.length} updates. Open an item for the full durable task history.</p>
  {/if}
</section>
