import { describe, expect, it, vi } from 'vitest';
import { mergeTimelineItems, reconcileTimelineComments, sortTimelineItems } from './timeline';
import type { ActivityEvent, Comment, TaskTimelineItem } from './types';

function comment(id: string, body: string, version = 1): Comment {
  return {
    id,
    task_id: 'task-1',
    actor_id: 'actor-1',
    body,
    version,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z'
  };
}

function commentItem(value: Comment): TaskTimelineItem {
  return {
    id: value.id,
    cursor: 'e30',
    kind: 'comment',
    task_id: value.task_id,
    actor: { id: value.actor_id, kind: 'human', name: 'Author' },
    created_at: value.created_at,
    progress: null,
    comment: value,
    change: null
  };
}

function changeItem(id: string, createdAt: string, eventType: string): TaskTimelineItem {
  return {
    id,
    cursor: 'e30',
    kind: 'task_change',
    task_id: 'task-1',
    actor: null,
    created_at: createdAt,
    progress: null,
    comment: null,
    change: { event_id: id, event_type: eventType, payload: {} }
  };
}

function timelineCursor(eventCursor: number, kind: TaskTimelineItem['kind'], id: string): string {
  return btoa(JSON.stringify({
    v: 1,
    at: '2026-01-01T00:00:00Z',
    ec: eventCursor,
    k: kind,
    id
  })).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

function commentEvent(cursor: number, type: 'comment.updated' | 'comment.deleted', commentID: string, taskID = 'task-1'): ActivityEvent {
  return {
    cursor,
    id: `${type}-${commentID}-${cursor}`,
    type,
    task_id: taskID,
    payload: { comment_id: commentID },
    created_at: '2026-01-01T00:00:00Z'
  };
}

describe('mergeTimelineItems', () => {
  it('replaces a remotely edited comment without discarding older loaded history', () => {
    const original = comment('comment-old', 'before edit');
    const unrelated = changeItem('change-old', '2025-12-01T00:00:00Z', 'task.updated');
    const merged = mergeTimelineItems(
      [commentItem(original), unrelated],
      [changeItem('change-new', '2026-02-01T00:00:00Z', 'comment.updated')],
      { updatedComments: new Map([[original.id, comment(original.id, 'after edit', 2)]]) }
    );

    expect(merged.find((item) => item.id === original.id)?.comment?.body).toBe('after edit');
    expect(merged.find((item) => item.id === original.id)?.comment?.version).toBe(2);
    expect(merged.some((item) => item.id === unrelated.id)).toBe(true);
    expect(merged.some((item) => item.id === 'change-new')).toBe(true);
  });

  it('removes a remotely deleted comment while retaining its immutable delete event', () => {
    const original = commentItem(comment('comment-deleted', 'retained body'));
    const deletedEvent = changeItem('delete-event', '2026-02-01T00:00:00Z', 'comment.deleted');
    const merged = mergeTimelineItems([original], [deletedEvent], { deletedCommentIds: ['comment-deleted'] });

    expect(merged.some((item) => item.kind === 'comment' && item.id === 'comment-deleted')).toBe(false);
    expect(merged.find((item) => item.id === deletedEvent.id)?.change?.event_type).toBe('comment.deleted');
  });
});

describe('sortTimelineItems', () => {
  it('preserves server ordering for timestamps that differ below one millisecond', () => {
    const older = {
      ...changeItem('older-nanoseconds', '2026-01-01T00:00:00.123456700Z', 'task.updated'),
      cursor: timelineCursor(100, 'task_change', 'older-nanoseconds')
    };
    const newer = {
      ...changeItem('newer-nanoseconds', '2026-01-01T00:00:00.123456789Z', 'task.updated'),
      cursor: timelineCursor(1, 'task_change', 'newer-nanoseconds')
    };

    expect(sortTimelineItems([older, newer]).map((item) => item.id)).toEqual([
      'newer-nanoseconds',
      'older-nanoseconds'
    ]);
  });

  it('uses numeric event cursors and server kind/id tie-breaks for equal timestamps', () => {
    const createdAt = '2026-01-01T00:00:00Z';
    const olderEncodedCursor = timelineCursor(2, 'task_change', 'event-2');
    const newerEncodedCursor = timelineCursor(10, 'task_change', 'event-10');
    const lowerRank = {
      ...changeItem('change-rank', createdAt, 'task.updated'),
      cursor: timelineCursor(5, 'task_change', 'change-rank')
    };
    const progress = {
      ...changeItem('progress-rank', createdAt, 'task.updated'),
      kind: 'agent_progress' as const,
      cursor: timelineCursor(5, 'agent_progress', 'progress-rank'),
      progress: {
        operation_id: 'operation-1',
        actor_id: 'actor-1',
        state: 'working' as const,
        phase: '',
        summary: 'Progress',
        next_action: '',
        checkpoint_refs: [],
        checkpoint_completed: null,
        checkpoint_total: null,
        started_at: createdAt
      },
      change: null
    };
    const zID = {
      ...changeItem('z-id', createdAt, 'task.updated'),
      cursor: timelineCursor(5, 'task_change', 'z-id')
    };
    const aID = {
      ...changeItem('a-id', createdAt, 'task.updated'),
      cursor: timelineCursor(5, 'task_change', 'a-id')
    };

    const ordered = sortTimelineItems([
      { ...changeItem('event-2', createdAt, 'task.updated'), cursor: olderEncodedCursor },
      { ...changeItem('event-10', createdAt, 'task.updated'), cursor: newerEncodedCursor },
      lowerRank,
      progress,
      zID,
      aID
    ]);

    expect(ordered.map((item) => item.id)).toEqual(['event-10', 'progress-rank', 'z-id', 'change-rank', 'a-id', 'event-2']);
  });
});

describe('reconcileTimelineComments', () => {
  it('fetches only loaded comments and resolves latest updates/deletions by event cursor', async () => {
    const canonical = comment('comment-loaded', 'remote edit', 2);
    const getComment = vi.fn().mockResolvedValue(canonical);
    const notFound = new Error('comment no longer exists');
    getComment.mockImplementation(async (_taskID: string, commentID: string) => {
      if (commentID === 'comment-gone') throw notFound;
      return canonical;
    });

    const result = await reconcileTimelineComments(
      [
        commentEvent(5, 'comment.updated', 'comment-loaded'),
        commentEvent(7, 'comment.deleted', 'comment-gone'),
        commentEvent(8, 'comment.updated', 'comment-loaded'),
        commentEvent(10, 'comment.updated', 'comment-gone'),
        commentEvent(12, 'comment.updated', 'comment-not-loaded', 'task-2')
      ],
      new Map([
        ['comment-loaded', 'task-1'],
        ['comment-gone', 'task-1'],
        ['comment-not-loaded', 'task-2']
      ]),
      getComment,
      (error) => error === notFound,
      'task-1'
    );

    expect(getComment).toHaveBeenCalledWith('task-1', 'comment-loaded');
    expect(getComment).toHaveBeenCalledWith('task-1', 'comment-gone');
    expect(getComment).not.toHaveBeenCalledWith('task-2', 'comment-not-loaded');
    expect(result.ok).toBe(true);
    expect(result.reconciliation.updatedComments?.get('comment-loaded')?.body).toBe('remote edit');
    expect([...result.reconciliation.deletedCommentIds ?? []]).toEqual(['comment-gone']);
  });

  it('reports a canonical fetch failure without discarding the loaded timeline', async () => {
    const failure = new Error('temporary failure');
    const getComment = vi.fn().mockRejectedValue(failure);
    const result = await reconcileTimelineComments(
      [commentEvent(1, 'comment.updated', 'comment-loaded')],
      new Map([['comment-loaded', 'task-1']]),
      getComment,
      () => false,
      'task-1'
    );

    expect(result.ok).toBe(false);
    expect(result.reconciliation.deletedCommentIds).toEqual(new Set());
  });
});
