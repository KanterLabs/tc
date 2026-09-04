<script lang="ts">
  import { renderMarkdown } from '../markdown';
  import type { Comment, TaskTimelineFilter, TaskTimelineItem } from '../types';

  export let items: TaskTimelineItem[] = [];
  export let filter: TaskTimelineFilter = 'all';
  export let loading = false;
  export let loadingOlder = false;
  export let error = '';
  export let hasOlder = false;
  export let onFilterChange: (next: TaskTimelineFilter) => void = () => undefined;
  export let onLoadOlder: () => void | Promise<void> = () => undefined;
  export let onRetry: () => void | Promise<void> = () => undefined;
  export let currentActorId = '';
  export let canManageComments = false;
  export let onEditComment: (comment: Comment, body: string) => void | Promise<void> = () => undefined;
  export let onConfirmDelete: (comment: Comment) => boolean | Promise<boolean> = () => true;
  export let onDeleteComment: (comment: Comment) => void | Promise<void> = () => undefined;

  let editingCommentId: string | null = null;
  let editingBody = '';
  let commentMutationId: string | null = null;
  let commentMutationError = '';
  let commentMutationErrorId: string | null = null;

  const filterOptions: Array<{ value: TaskTimelineFilter; label: string }> = [
    { value: 'all', label: 'All' },
    { value: 'agent_progress', label: 'Agent' },
    { value: 'comment', label: 'Comments' },
    { value: 'task_change', label: 'Changes' }
  ];

  $: visibleItems = filter === 'all' ? items : items.filter((item) => item.kind === filter);

  function actorName(item: TaskTimelineItem): string {
    return item.actor?.name || item.progress?.actor_id || item.comment?.actor_id || 'Unknown actor';
  }

  function actorKind(item: TaskTimelineItem): string {
    return item.actor?.kind === 'agent' ? 'Agent' : item.actor?.kind === 'human' ? 'Human' : 'Actor';
  }

  function actorInitial(item: TaskTimelineItem): string {
    return (actorName(item).trim().slice(0, 1) || '?').toUpperCase();
  }

  function kindLabel(kind: TaskTimelineItem['kind']): string {
    if (kind === 'agent_progress') return 'Agent update';
    if (kind === 'comment') return 'Comment';
    return 'Task change';
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
      'bug.triaged': 'triaged the bug',
      'bug.resolved': 'resolved the bug',
      'bug.reopened': 'reopened the bug',
      'comment.updated': 'edited a comment',
      'comment.deleted': 'deleted a comment'
    };
    return labels[type] || type.replace(/[._-]+/g, ' ').replace(/\b\w/g, (letter) => letter.toUpperCase());
  }

  function changeContext(item: TaskTimelineItem): string {
    const payload = item.change?.payload || {};
    const values = ['summary', 'reason', 'note', 'column', 'column_name', 'state', 'resolution', 'phase']
      .map((key) => payload[key])
      .filter((value): value is string | number => typeof value === 'string' || typeof value === 'number')
      .map(String);
    return values.join(' · ');
  }

  function formatRelative(value: string): string {
    const timestamp = Date.parse(value);
    if (!Number.isFinite(timestamp)) return 'Unknown time';
    const minutes = Math.round((timestamp - Date.now()) / 60000);
    const absolute = Math.abs(minutes);
    if (absolute < 1) return 'just now';
    if (absolute < 60) return `${absolute}m ${minutes < 0 ? 'ago' : 'from now'}`;
    const hours = Math.round(absolute / 60);
    if (hours < 24) return `${hours}h ${minutes < 0 ? 'ago' : 'from now'}`;
    const days = Math.round(hours / 24);
    return `${days}d ${minutes < 0 ? 'ago' : 'from now'}`;
  }

  function formatDateTime(value: string): string {
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return value;
    return new Intl.DateTimeFormat(undefined, {
      year: 'numeric',
      month: 'short',
      day: 'numeric',
      hour: 'numeric',
      minute: '2-digit',
      timeZoneName: 'short'
    }).format(date);
  }

  function canEditComment(comment: Comment): boolean {
    return Boolean(comment.actor_id && (comment.actor_id === currentActorId || canManageComments));
  }

  function startEditing(comment: Comment): void {
    editingCommentId = comment.id;
    editingBody = comment.body;
    commentMutationError = '';
    commentMutationErrorId = null;
  }

  function cancelEditing(): void {
    editingCommentId = null;
    editingBody = '';
    commentMutationError = '';
    commentMutationErrorId = null;
  }

  async function saveComment(comment: Comment): Promise<void> {
    if (!editingBody.trim() || commentMutationId) return;
    commentMutationId = comment.id;
    commentMutationError = '';
    commentMutationErrorId = null;
    try {
      await onEditComment(comment, editingBody);
      cancelEditing();
    } catch (error) {
      commentMutationError = error instanceof Error ? error.message : 'The comment could not be saved.';
      commentMutationErrorId = comment.id;
    } finally {
      commentMutationId = null;
    }
  }

  async function deleteComment(comment: Comment): Promise<void> {
    if (commentMutationId) return;
    if (!(await onConfirmDelete(comment))) return;
    commentMutationId = comment.id;
    commentMutationError = '';
    commentMutationErrorId = null;
    try {
      await onDeleteComment(comment);
      if (editingCommentId === comment.id) cancelEditing();
    } catch (error) {
      commentMutationError = error instanceof Error ? error.message : 'The comment could not be deleted.';
      commentMutationErrorId = comment.id;
    } finally {
      commentMutationId = null;
    }
  }
</script>

<section class="task-timeline" aria-labelledby="task-activity-heading">
  <div class="task-timeline-heading">
    <div>
      <span class="eyebrow">Durable history</span>
      <h2 id="task-activity-heading">Activity</h2>
      <p>Agent updates, comments, and task changes in newest-first order.</p>
    </div>
    <div class="task-timeline-filters" role="group" aria-label="Filter task activity">
      {#each filterOptions as option}
        <button
          class:active={filter === option.value}
          class="task-timeline-filter"
          type="button"
          aria-pressed={filter === option.value}
          on:click={() => onFilterChange(option.value)}
        >{option.label}</button>
      {/each}
    </div>
  </div>

  {#if error}
    <div class="inline-alert error" role="alert"><span>!</span>{error}<button class="text-button" type="button" on:click={onRetry}>Retry</button></div>
  {/if}

  {#if loading && !items.length}
    <div class="task-timeline-loading" role="status" aria-live="polite"><span class="spinner"></span><span>Loading activity…</span></div>
  {:else if !visibleItems.length}
    <div class="task-timeline-empty"><span class="empty-icon" aria-hidden="true">◌</span><p>No activity matches this filter yet.</p></div>
  {:else}
    <div class="task-timeline-list" aria-live="polite">
      {#each visibleItems as item (item.id)}
        <article class="task-timeline-item" data-kind={item.kind}>
          <span class:agent={item.actor?.kind === 'agent'} class="task-timeline-avatar" aria-hidden="true">{actorInitial(item)}</span>
          <div class="task-timeline-content">
            <div class="task-timeline-item-heading">
              <strong>{actorName(item)}</strong>
              <span class="task-timeline-actor-kind">{actorKind(item)}</span>
              <span class={`task-timeline-kind ${item.kind}`}>{kindLabel(item.kind)}</span>
            </div>
            {#if item.kind === 'agent_progress' && item.progress}
              <p class="task-timeline-summary">{item.progress.summary}</p>
              <dl class="task-timeline-details">
                {#if item.progress.phase}<div><dt>Phase</dt><dd>{item.progress.phase}</dd></div>{/if}
                {#if item.progress.next_action}<div><dt>Next</dt><dd>{item.progress.next_action}</dd></div>{/if}
                {#if item.progress.checkpoint_total !== null && item.progress.checkpoint_total !== undefined}<div><dt>Checkpoints</dt><dd>{item.progress.checkpoint_completed ?? 0} of {item.progress.checkpoint_total}</dd></div>{/if}
              </dl>
            {:else if item.kind === 'comment' && item.comment}
              {@const comment = item.comment}
              {#if editingCommentId === comment.id}
                <div class="task-timeline-comment-edit">
                  <textarea rows="4" bind:value={editingBody} aria-label="Edit comment"></textarea>
                  <div class="task-timeline-comment-actions">
                    <button class="button primary" type="button" disabled={!editingBody.trim() || commentMutationId === comment.id} on:click={() => void saveComment(comment)}>
                      {#if commentMutationId === comment.id}<span class="button-spinner"></span>{/if}Save
                    </button>
                    <button class="text-button" type="button" disabled={commentMutationId === comment.id} on:click={cancelEditing}>Cancel</button>
                  </div>
                </div>
              {:else}
                <div class="task-timeline-comment">{@html renderMarkdown(comment.body)}</div>
                {#if canEditComment(comment)}
                  <div class="task-timeline-comment-actions">
                    <button class="text-button" type="button" on:click={() => startEditing(comment)}>Edit</button>
                    <button class="text-button danger-text-button" type="button" disabled={commentMutationId === comment.id} on:click={() => void deleteComment(comment)}>Delete</button>
                  </div>
                {/if}
              {/if}
              {#if commentMutationError && (editingCommentId === comment.id || commentMutationId === comment.id || commentMutationErrorId === comment.id)}
                <div class="task-timeline-comment-error" role="alert">{commentMutationError}</div>
              {/if}
            {:else if item.kind === 'task_change' && item.change}
              <p class="task-timeline-summary">{changeLabel(item)}{#if changeContext(item)}<span> · {changeContext(item)}</span>{/if}</p>
            {/if}
            <time datetime={item.created_at} title={formatDateTime(item.created_at)}>{formatRelative(item.created_at)}</time>
          </div>
        </article>
      {/each}
    </div>
  {/if}

  {#if hasOlder}
    <button class="button quiet-button task-timeline-load-older" type="button" disabled={loadingOlder} on:click={onLoadOlder}>
      {#if loadingOlder}<span class="button-spinner"></span>{/if}Load older activity
    </button>
  {/if}
</section>
