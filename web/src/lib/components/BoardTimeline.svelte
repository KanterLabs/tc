<script lang="ts">
  import type {
    Task,
    TaskTimelineFilter,
    TaskTimelineItem
  } from '../types';

  export let items: TaskTimelineItem[] = [];
  export let tasks: Task[] = [];
  export let filter: TaskTimelineFilter = 'all';
  export let loading = false;
  export let loadingOlder = false;
  export let error = '';
  export let hasOlder = false;
  export let onFilterChange: (next: TaskTimelineFilter) => void = () => undefined;
  export let onLoadOlder: () => void | Promise<void> = () => undefined;
  export let onRetry: () => void | Promise<void> = () => undefined;
  export let onOpen: (task: Task) => void = () => undefined;

  type TimelineGroup = {
    key: string;
    label: string;
    items: TaskTimelineItem[];
  };

  const filterOptions: Array<{ value: TaskTimelineFilter; label: string }> = [
    { value: 'all', label: 'All' },
    { value: 'agent_progress', label: 'Agent' },
    { value: 'comment', label: 'Comments' },
    { value: 'task_change', label: 'Changes' }
  ];

  let tasksById = new Map<string, Task>();
  let visibleItems: TaskTimelineItem[] = [];
  let groups: TimelineGroup[] = [];
  let touchedTaskCount = 0;

  $: tasksById = new Map(tasks.map((task) => [task.id, task]));
  $: visibleItems = sortItems(filter === 'all' ? items : items.filter((item) => item.kind === filter));
  $: groups = groupItems(visibleItems);
  $: touchedTaskCount = new Set(visibleItems.map((item) => item.task_id)).size;

  function sortItems(source: TaskTimelineItem[]): TaskTimelineItem[] {
    return source
      .map((item, index) => ({ item, index, timestamp: timestampFor(item.created_at) }))
      .sort((left, right) => right.timestamp - left.timestamp || left.index - right.index)
      .map(({ item }) => item);
  }

  function timestampFor(value: string): number {
    const parsed = Date.parse(value);
    return Number.isFinite(parsed) ? parsed : 0;
  }

  function groupItems(source: TaskTimelineItem[]): TimelineGroup[] {
    const grouped = new Map<string, TimelineGroup>();
    source.forEach((item) => {
      const date = dateFor(item.created_at);
      const key = date ? dayKey(date) : 'unknown';
      const existing = grouped.get(key);
      if (existing) {
        existing.items.push(item);
        return;
      }
      grouped.set(key, {
        key,
        label: date ? dayLabel(date) : 'Date unavailable',
        items: [item]
      });
    });
    return [...grouped.values()];
  }

  function dateFor(value: string): Date | null {
    const timestamp = timestampFor(value);
    return timestamp ? new Date(timestamp) : null;
  }

  function dayKey(value: Date): string {
    return `${value.getFullYear()}-${value.getMonth()}-${value.getDate()}`;
  }

  function dayLabel(value: Date): string {
    const now = new Date();
    const today = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
    const day = new Date(value.getFullYear(), value.getMonth(), value.getDate()).getTime();
    const difference = Math.round((today - day) / 86400000);
    if (difference === 0) return 'Today';
    if (difference === 1) return 'Yesterday';
    return new Intl.DateTimeFormat(undefined, {
      weekday: 'long',
      month: 'short',
      day: 'numeric',
      year: value.getFullYear() === now.getFullYear() ? undefined : 'numeric'
    }).format(value);
  }

  function taskFor(item: TaskTimelineItem): Task | undefined {
    return tasksById.get(item.task_id);
  }

  function taskKey(item: TaskTimelineItem, task = taskFor(item)): string {
    return task?.key || (item.task_id ? `Task ${item.task_id}` : 'Task');
  }

  function taskTitle(item: TaskTimelineItem, task = taskFor(item)): string {
    return task?.title || 'Task details unavailable';
  }

  function actorName(item: TaskTimelineItem): string {
    if (item.actor?.name) return item.actor.name;
    if (item.progress?.actor_id) return item.progress.actor_id;
    if (item.comment?.actor_id) return item.comment.actor_id;
    return 'System';
  }

  function actorKind(item: TaskTimelineItem): 'agent' | 'human' | 'system' {
    if (item.actor?.kind === 'agent') return 'agent';
    if (item.actor?.kind === 'human') return 'human';
    return 'system';
  }

  function actorKindLabel(item: TaskTimelineItem): string {
    const kind = actorKind(item);
    return kind === 'agent' ? 'Agent' : kind === 'human' ? 'Human' : 'System';
  }

  function actorInitial(item: TaskTimelineItem): string {
    return (actorName(item).trim().slice(0, 1) || '?').toUpperCase();
  }

  function kindLabel(kind: TaskTimelineItem['kind']): string {
    if (kind === 'agent_progress') return 'Agent update';
    if (kind === 'comment') return 'Comment';
    return 'Task change';
  }

  function stateLabel(value: string): string {
    if (!value) return 'State not reported';
    return value.charAt(0).toUpperCase() + value.slice(1);
  }

  function checkpointLabel(item: TaskTimelineItem): string {
    const progress = item.progress;
    if (!progress) return 'Not reported';
    const completed = progress.checkpoint_completed;
    const total = progress.checkpoint_total;
    if (typeof completed === 'number' && typeof total === 'number') return `${completed} of ${total} complete`;
    if (progress.checkpoint_refs.length) return `${progress.checkpoint_refs.length} checkpoint${progress.checkpoint_refs.length === 1 ? '' : 's'}`;
    return 'Not reported';
  }

  function checkpointPreview(item: TaskTimelineItem): string {
    const refs = item.progress?.checkpoint_refs || [];
    if (!refs.length) return '';
    const visible = refs.slice(0, 3).join(' · ');
    return refs.length > 3 ? `${visible} · +${refs.length - 3}` : visible;
  }

  function changeLabel(item: TaskTimelineItem): string {
    const type = item.change?.event_type || 'task.updated';
    const labels: Record<string, string> = {
      'task.created': 'created the task',
      'task.updated': 'updated the task',
      'task.moved': 'moved the task',
      'task.completed': 'completed the task',
      'task.blocked': 'blocked the task',
      'task.claimed': 'claimed the task',
      'task.released': 'released the task',
      'task.renewed': 'renewed the claim',
      'task.claim_renewed': 'renewed the claim',
      'task.unblocked': 'unblocked the task',
      'bug.created': 'created the bug',
      'bug.updated': 'updated the bug',
      'bug.triaged': 'triaged the bug',
      'bug.resolved': 'resolved the bug',
      'bug.reopened': 'reopened the bug',
      'bug.duplicated': 'marked the bug as a duplicate'
    };
    return labels[type] || type.replace(/[._-]+/g, ' ').replace(/\b\w/g, (letter) => letter.toUpperCase());
  }

  function textValue(value: unknown): string {
    if (typeof value === 'string') return value.trim();
    if (typeof value === 'number' || typeof value === 'boolean') return String(value);
    if (Array.isArray(value)) return value.filter((entry) => typeof entry === 'string' || typeof entry === 'number').map(String).join(', ');
    return '';
  }

  function humanizeField(value: string): string {
    return value.replace(/[_-]+/g, ' ').replace(/\b\w/g, (letter) => letter.toUpperCase());
  }

  function changeContext(item: TaskTimelineItem): string {
    const payload = item.change?.payload || {};
    const from = textValue(payload.from_column || payload.from_state || payload.previous_value);
    const to = textValue(payload.to_column || payload.to_state || payload.new_value);
    if (from && to) return `${from} → ${to}`;
    const changedFields = textValue(payload.changed_fields || payload.fields);
    if (changedFields) return `Changed ${changedFields.split(', ').map(humanizeField).join(', ')}`;
    const field = textValue(payload.field);
    if (field) return `${humanizeField(field)} changed`;
    const values = ['summary', 'reason', 'note', 'column', 'column_name', 'state', 'resolution', 'phase', 'expires_at']
      .map((key) => textValue(payload[key]))
      .filter(Boolean);
    return values.join(' · ');
  }

  function formatRelative(value: string): string {
    const timestamp = timestampFor(value);
    if (!timestamp) return 'Unknown time';
    const minutes = Math.round((timestamp - Date.now()) / 60000);
    const absolute = Math.abs(minutes);
    if (absolute < 1) return 'just now';
    if (absolute < 60) return `${absolute}m ${minutes < 0 ? 'ago' : 'from now'}`;
    const hours = Math.round(absolute / 60);
    if (hours < 24) return `${hours}h ${minutes < 0 ? 'ago' : 'from now'}`;
    const days = Math.round(hours / 24);
    return `${days}d ${minutes < 0 ? 'ago' : 'from now'}`;
  }

  function formatExact(value: string): string {
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return value || 'Unknown time';
    return new Intl.DateTimeFormat(undefined, {
      year: 'numeric',
      month: 'short',
      day: 'numeric',
      hour: 'numeric',
      minute: '2-digit',
      timeZoneName: 'short'
    }).format(date);
  }

  function rowLabel(item: TaskTimelineItem, task: Task | undefined): string {
    return `Open ${taskKey(item, task)} ${taskTitle(item, task)} activity. ${actorName(item)} posted ${kindLabel(item.kind)} ${formatRelative(item.created_at)}.`;
  }

  function openTask(item: TaskTimelineItem, task: Task | undefined): void {
    if (task) onOpen(task);
  }
</script>

<section class="board-timeline" aria-labelledby="board-timeline-heading" aria-busy={loading}>
  <header class="board-timeline-header">
    <div class="board-timeline-heading-copy">
      <span class="eyebrow">Board timeline</span>
      <h2 id="board-timeline-heading">Recent work</h2>
      <p>Updates across every task, grouped newest-first by day.</p>
    </div>
    <div class="board-timeline-stats" aria-label={`${touchedTaskCount} tasks touched, ${visibleItems.length} updates`}>
      <span><strong>{touchedTaskCount}</strong> tasks touched</span>
      <span><strong>{visibleItems.length}</strong> updates</span>
    </div>
  </header>

  <div class="board-timeline-toolbar">
    <div class="board-timeline-filters" role="group" aria-label="Filter board timeline">
      {#each filterOptions as option}
        <button
          class:active={filter === option.value}
          class="board-timeline-filter"
          type="button"
          aria-pressed={filter === option.value}
          on:click={() => onFilterChange(option.value)}
        >{option.label}</button>
      {/each}
    </div>
    {#if loading && items.length}
      <span class="board-timeline-refresh" role="status" aria-live="polite"><span class="spinner" aria-hidden="true"></span>Refreshing…</span>
    {/if}
  </div>

  {#if error}
    <div class="board-timeline-alert" role="alert">
      <span class="board-timeline-alert-icon" aria-hidden="true">!</span>
      <span>{error}</span>
      <button class="text-button" type="button" on:click={onRetry}>Retry</button>
    </div>
  {/if}

  {#if loading && !items.length}
    <div class="board-timeline-loading" role="status" aria-live="polite" aria-label="Loading board timeline">
      <span class="spinner" aria-hidden="true"></span>
      <span>Loading recent work…</span>
    </div>
  {:else if !visibleItems.length}
    <div class="board-timeline-empty" role="status">
      <span class="board-timeline-empty-icon" aria-hidden="true">◌</span>
      <div>
        <strong>{items.length ? 'No matching updates' : 'No recent work yet'}</strong>
        <p>{items.length ? 'Try another timeline filter.' : 'Task changes, comments, and agent check-ins will appear here.'}</p>
      </div>
    </div>
  {:else}
    <div class="board-timeline-groups" aria-label="Board timeline updates">
      {#each groups as group (group.key)}
        <section class="board-timeline-day" aria-labelledby={`board-timeline-day-${group.key}`}>
          <div class="board-timeline-day-heading">
            <h3 id={`board-timeline-day-${group.key}`}>{group.label}</h3>
            <span>{group.items.length} {group.items.length === 1 ? 'update' : 'updates'}</span>
          </div>
          <ol class="board-timeline-list" aria-label={`${group.label} updates`}>
            {#each group.items as item (item.id)}
              {@const task = taskFor(item)}
              {@const actor = actorKind(item)}
              <li class="board-timeline-item" data-kind={item.kind}>
                <button
                  class="board-timeline-row"
                  type="button"
                  disabled={!task}
                  aria-label={rowLabel(item, task)}
                  on:click={() => openTask(item, task)}
                >
                  <span class:agent={actor === 'agent'} class:system={actor === 'system'} class="board-timeline-avatar" aria-hidden="true">{actorInitial(item)}</span>
                  <span class="board-timeline-row-body">
                    <span class="board-timeline-row-top">
                      <span class="board-timeline-task">
                        <span class="board-timeline-task-key">{taskKey(item, task)}</span>
                        <strong>{taskTitle(item, task)}</strong>
                      </span>
                      <span class={`board-timeline-kind ${item.kind}`}>{kindLabel(item.kind)}</span>
                    </span>
                    <span class="board-timeline-actor-line">
                      <strong>{actorName(item)}</strong>
                      <span class={`board-timeline-actor ${actor}`}>{actorKindLabel(item)}</span>
                    </span>

                    {#if item.kind === 'agent_progress' && item.progress}
                      <span class="board-timeline-progress-state">
                        <span class={`board-timeline-state ${item.progress.state}`}>{stateLabel(item.progress.state)}</span>
                        {#if item.progress.phase}<span><b>Phase</b> {item.progress.phase}</span>{/if}
                      </span>
                      {#if item.progress.summary}<span class="board-timeline-summary">{item.progress.summary}</span>{/if}
                      {#if item.progress.next_action}<span class="board-timeline-detail"><b>Next action</b> {item.progress.next_action}</span>{/if}
                      <span class="board-timeline-detail board-timeline-checkpoints" title={checkpointPreview(item)}>
                        <b>Checkpoints</b> {checkpointLabel(item)}{#if checkpointPreview(item)}<span> · {checkpointPreview(item)}</span>{/if}
                      </span>
                    {:else if item.kind === 'comment' && item.comment}
                      <span class="board-timeline-comment">{item.comment.body || 'Comment added.'}</span>
                    {:else if item.kind === 'task_change'}
                      <span class="board-timeline-change"><strong>{changeLabel(item)}</strong>{#if changeContext(item)}<span> · {changeContext(item)}</span>{/if}</span>
                    {/if}

                    <span class="board-timeline-row-time">
                      <time datetime={item.created_at} title={formatExact(item.created_at)} aria-label={formatExact(item.created_at)}>{formatRelative(item.created_at)}</time>
                      <span aria-hidden="true">·</span>
                      <span class="board-timeline-exact-time">{formatExact(item.created_at)}</span>
                    </span>
                  </span>
                  <span class="board-timeline-arrow" aria-hidden="true">→</span>
                </button>
              </li>
            {/each}
          </ol>
        </section>
      {/each}
    </div>
  {/if}

  {#if hasOlder}
    <div class="board-timeline-load-more">
      {#if loadingOlder}<span class="board-timeline-load-status" role="status" aria-live="polite"><span class="spinner" aria-hidden="true"></span>Loading older updates…</span>{/if}
      <button class="button quiet-button" type="button" disabled={loadingOlder} on:click={onLoadOlder}>
        {#if loadingOlder}<span class="button-spinner" aria-hidden="true"></span>{/if}
        {loadingOlder ? 'Loading older…' : 'Load older updates'}
      </button>
    </div>
  {/if}
</section>
