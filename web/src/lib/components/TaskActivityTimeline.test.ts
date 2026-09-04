// @vitest-environment jsdom
import { flushSync, mount, tick, unmount } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { mergeTimelineItems } from '../timeline';
import type { Comment, TaskTimelineItem } from '../types';
import TaskActivityTimeline from './TaskActivityTimeline.svelte';
import TimelineTestHarness from './TimelineTestHarness.svelte';

const mountedComponents: Array<ReturnType<typeof mount>> = [];

afterEach(async () => {
  while (mountedComponents.length) await unmount(mountedComponents.pop()!);
  document.body.replaceChildren();
  vi.restoreAllMocks();
});

function cursor(eventCursor: number, kind: TaskTimelineItem['kind'], id: string): string {
  return btoa(JSON.stringify({ v: 1, at: '2026-01-01T00:00:00Z', ec: eventCursor, k: kind, id }))
    .replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

function comment(body = 'Original comment', version = 1): Comment {
  return {
    id: 'comment-1',
    task_id: 'task-1',
    actor_id: 'actor-1',
    body,
    version,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z'
  };
}

function commentItem(value = comment()): TaskTimelineItem {
  return {
    id: value.id,
    cursor: cursor(1, 'comment', value.id),
    kind: 'comment',
    task_id: value.task_id,
    actor: { id: value.actor_id, kind: 'human', name: 'Author' },
    created_at: value.created_at,
    progress: null,
    comment: value,
    change: null
  };
}

function deletedEvent(): TaskTimelineItem {
  return {
    id: 'delete-event',
    cursor: cursor(2, 'task_change', 'delete-event'),
    kind: 'task_change',
    task_id: 'task-1',
    actor: { id: 'actor-1', kind: 'human', name: 'Author' },
    created_at: '2026-01-02T00:00:00Z',
    progress: null,
    comment: null,
    change: { event_id: 'delete-event', event_type: 'comment.deleted', payload: { comment_id: 'comment-1' } }
  };
}

describe('TaskActivityTimeline component', () => {
  it('sends edited Markdown through the comment callback', async () => {
    const onEditComment = vi.fn().mockResolvedValue(undefined);
    const mounted = mount(TaskActivityTimeline, {
      target: document.body,
      props: { items: [commentItem()], currentActorId: 'actor-1', onEditComment }
    });
    mountedComponents.push(mounted);

    document.querySelector<HTMLButtonElement>('.task-timeline-comment-actions .text-button')?.click();
    await tick();
    const textarea = document.querySelector<HTMLTextAreaElement>('.task-timeline-comment-edit textarea');
    expect(textarea).not.toBeNull();
    textarea!.value = 'Edited with **Markdown**';
    textarea!.dispatchEvent(new Event('input', { bubbles: true }));
    document.querySelector<HTMLButtonElement>('.task-timeline-comment-edit .button.primary')?.click();

    await vi.waitFor(() => expect(onEditComment).toHaveBeenCalledWith(expect.objectContaining({ id: 'comment-1' }), 'Edited with **Markdown**'));
  });

  it('sends a confirmed deletion through the comment callback', async () => {
    const onConfirmDelete = vi.fn().mockResolvedValue(true);
    const onDeleteComment = vi.fn().mockResolvedValue(undefined);
    const mounted = mount(TaskActivityTimeline, {
      target: document.body,
      props: { items: [commentItem()], currentActorId: 'actor-1', onConfirmDelete, onDeleteComment }
    });
    mountedComponents.push(mounted);

    const deleteButton = [...document.querySelectorAll<HTMLButtonElement>('.task-timeline-comment-actions .text-button')]
      .find((button) => button.textContent?.trim() === 'Delete');
    deleteButton?.click();
    await vi.waitFor(() => expect(onDeleteComment).toHaveBeenCalledWith(expect.objectContaining({ id: 'comment-1' })));
    expect(onConfirmDelete).toHaveBeenCalledWith(expect.objectContaining({ id: 'comment-1' }));
  });

  it('does not delete a comment when confirmation is canceled', async () => {
    const onConfirmDelete = vi.fn().mockResolvedValue(false);
    const onDeleteComment = vi.fn().mockResolvedValue(undefined);
    const mounted = mount(TaskActivityTimeline, {
      target: document.body,
      props: { items: [commentItem()], currentActorId: 'actor-1', onConfirmDelete, onDeleteComment }
    });
    mountedComponents.push(mounted);

    const deleteButton = [...document.querySelectorAll<HTMLButtonElement>('.task-timeline-comment-actions .text-button')]
      .find((button) => button.textContent?.trim() === 'Delete');
    deleteButton?.click();
    await vi.waitFor(() => expect(onConfirmDelete).toHaveBeenCalledOnce());
    expect(onDeleteComment).not.toHaveBeenCalled();
  });

  it('keeps a local edit draft while replacing stale content and retains the delete event', async () => {
    const original = commentItem();
    const mounted = mount(TimelineTestHarness, {
      target: document.body,
      props: { items: [original], currentActorId: 'actor-1' }
    });
    mountedComponents.push(mounted);

    document.querySelector<HTMLButtonElement>('.task-timeline-comment-actions .text-button')?.click();
    await tick();
    const textarea = document.querySelector<HTMLTextAreaElement>('.task-timeline-comment-edit textarea');
    textarea!.value = 'Local draft not yet saved';
    textarea!.dispatchEvent(new Event('input', { bubbles: true }));

    const edited = mergeTimelineItems(
      [original],
      [],
      { updatedComments: new Map([['comment-1', comment('Remote canonical edit', 2)]]) }
    );
    mounted.updateItems(edited);
    flushSync();
    expect(textarea!.value).toBe('Local draft not yet saved');
    expect(edited[0]?.comment?.body).toBe('Remote canonical edit');

    document.querySelector<HTMLButtonElement>('.task-timeline-comment-edit .text-button')?.click();
    await tick();
    expect(document.body.textContent).toContain('Remote canonical edit');

    mounted.updateItems([deletedEvent()]);
    flushSync();
    expect(document.body.textContent).not.toContain('Original comment');
    expect(document.body.textContent).toContain('deleted a comment');
  });
});
