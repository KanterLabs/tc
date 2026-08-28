<script lang="ts">
  import type { Column, Project, Task } from '../types';
  import AgentPulse from './AgentPulse.svelte';

  const priorityLabels: Record<Task['priority'], string> = {
    low: 'Low',
    normal: 'Normal',
    high: 'High',
    urgent: 'Urgent'
  };

  export let task: Task;
  export let project: Project | undefined = undefined;
  export let column: Column | undefined = undefined;
  export let now = Date.now();
  export let actorLabel = '';
  export let onOpen: (task: Task) => void;

  $: projectLabel = project?.name || 'Project';
  $: columnLabel = column?.name || 'In progress';
</script>

<button class="work-row live-work-row" type="button" aria-label={`Open ${task.key}: ${task.title}`} on:click={() => onOpen(task)}>
  <span class="work-project-dot" style={`--project-color: ${project?.color || '#6d5efc'}`}></span>
  <span class="work-main">
    <span class="work-row-top">
      <span class="task-key">{task.key}</span>
      <span class={`priority-pill priority-${task.priority}`}>{priorityLabels[task.priority]}</span>
    </span>
    <strong>{task.title}</strong>
    <span class="work-project-name">{projectLabel} · {columnLabel}</span>
  </span>
  <span class="live-work-state"><AgentPulse {task} {now} {actorLabel} /></span>
  <span class="row-arrow" aria-hidden="true">→</span>
</button>
