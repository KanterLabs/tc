import type { ActivityEvent, Comment, TaskTimelineItem } from './types';

export interface TimelineCommentReconciliation {
  /** Canonical comments fetched for rows that are already loaded in the drawer. */
  updatedComments?: ReadonlyMap<string, Comment>;
  /** Comment IDs that no longer have an active timeline row. */
  deletedCommentIds?: Iterable<string>;
}

export type TimelineCommentChange = 'updated' | 'deleted';

export interface TimelineCommentReconciliationResult {
  ok: boolean;
  reconciliation: TimelineCommentReconciliation;
}

/** Keep only the newest lifecycle event for each comment in a poll batch. */
export function commentChangesForTask(events: readonly ActivityEvent[], taskId = ''): Map<string, TimelineCommentChange> {
  const changes = new Map<string, { change: TimelineCommentChange; cursor: number }>();
  [...events].sort((a, b) => a.cursor - b.cursor).forEach((event) => {
    if ((taskId && event.task_id !== taskId) || (event.type !== 'comment.updated' && event.type !== 'comment.deleted')) return;
    const commentID = event.payload?.comment_id;
    if (typeof commentID !== 'string' || !commentID.trim()) return;
    const previous = changes.get(commentID);
    if (!previous || event.cursor >= previous.cursor) changes.set(commentID, {
      change: event.type === 'comment.deleted' ? 'deleted' : 'updated',
      cursor: event.cursor
    });
  });
  return new Map([...changes].map(([commentID, value]) => [commentID, value.change]));
}

/** Fetch canonical rows for loaded comments while retaining tombstone IDs. */
export async function reconcileTimelineComments(
  events: readonly ActivityEvent[],
  loadedCommentTasks: ReadonlyMap<string, string>,
  getComment: (taskId: string, commentID: string) => Promise<Comment>,
  isNotFound: (error: unknown) => boolean,
  taskId = ''
): Promise<TimelineCommentReconciliationResult> {
  const changes = commentChangesForTask(events, taskId);
  if (!changes.size) return { ok: true, reconciliation: {} };
  const deletedCommentIDs = new Set<string>();
  const updatedComments = new Map<string, Comment>();
  const updateIDs = [...changes]
    .filter(([commentID, change]) => loadedCommentTasks.has(commentID) && change === 'updated')
    .map(([commentID]) => commentID);
  for (const [commentID, change] of changes) {
    if (loadedCommentTasks.has(commentID) && change === 'deleted') deletedCommentIDs.add(commentID);
  }
  const fetched = await Promise.all(updateIDs.map(async (commentID) => {
    const taskID = loadedCommentTasks.get(commentID) as string;
    try {
      return { commentID, comment: await getComment(taskID, commentID), error: undefined };
    } catch (error) {
      return { commentID, comment: undefined, error };
    }
  }));
  let ok = true;
  fetched.forEach(({ commentID, comment, error }) => {
    if (comment) {
      updatedComments.set(commentID, comment);
    } else if (isNotFound(error)) {
      deletedCommentIDs.add(commentID);
    } else {
      ok = false;
    }
  });
  return { ok, reconciliation: { updatedComments, deletedCommentIds: deletedCommentIDs } };
}

/**
 * Merge a refreshed timeline page into the drawer's loaded pages.
 *
 * A normal refresh is additive because the server returns only the newest
 * page. Lifecycle reconciliation is deliberately explicit: a deleted comment
 * is removed by its immutable event ID, while an edited comment is replaced
 * with the canonical resource fetched for that ID. Other loaded rows and any
 * component-local edit draft remain untouched.
 */
export function mergeTimelineItems(
  existing: TaskTimelineItem[],
  incoming: TaskTimelineItem[],
  reconciliation: TimelineCommentReconciliation = {}
): TaskTimelineItem[] {
  const merged = new Map<string, TaskTimelineItem>();
  [...existing, ...incoming].forEach((item) => merged.set(item.id, item));

  for (const [commentID, comment] of reconciliation.updatedComments ?? []) {
    const current = merged.get(commentID);
    if (current?.kind === 'comment' && current.comment?.id === commentID) {
      merged.set(commentID, { ...current, comment });
      continue;
    }
    for (const [itemID, item] of merged) {
      if (item.kind === 'comment' && item.comment?.id === commentID) {
        merged.set(itemID, { ...item, comment });
        break;
      }
    }
  }

  const deletedCommentIDs = new Set(reconciliation.deletedCommentIds ?? []);
  if (deletedCommentIDs.size > 0) {
    for (const [itemID, item] of merged) {
      if (item.kind === 'comment' && item.comment && deletedCommentIDs.has(item.comment.id)) {
        merged.delete(itemID);
      }
    }
  }

  return sortTimelineItems([...merged.values()]);
}

/**
 * Sort timeline rows using the same key order as the API cursor comparator:
 * timestamp, event sequence, kind rank, kind, then item ID. In particular,
 * the opaque cursor's encoded text is never used as a lexical sort key.
 */
export function sortTimelineItems(items: TaskTimelineItem[]): TaskTimelineItem[] {
  return [...items].sort(compareTimelineItems);
}

function compareTimelineItems(a: TaskTimelineItem, b: TaskTimelineItem): number {
  const timestampOrder = compareTimelineTimestamps(a.created_at, b.created_at);
  if (timestampOrder) return timestampOrder;
  const aCursor = decodeTimelineCursor(a.cursor);
  const bCursor = decodeTimelineCursor(b.cursor);
  if (aCursor.eventCursor !== bCursor.eventCursor) return bCursor.eventCursor - aCursor.eventCursor;
  const rank = (kind: TaskTimelineItem['kind']): number => kind === 'agent_progress' ? 3 : kind === 'comment' ? 2 : 1;
  return rank(b.kind) - rank(a.kind) || descendingString(a.kind, b.kind) || descendingString(a.id, b.id);
}

/** Preserve RFC3339Nano ordering below JavaScript Date's millisecond precision. */
function compareTimelineTimestamps(a: string, b: string): number {
  const aTime = Date.parse(a);
  const bTime = Date.parse(b);
  const bothValid = Number.isFinite(aTime) && Number.isFinite(bTime);
  if (!bothValid) return a === b ? 0 : descendingString(a, b);
  if (aTime !== bTime) return bTime - aTime;
  const aNanoseconds = subMillisecondNanoseconds(a);
  const bNanoseconds = subMillisecondNanoseconds(b);
  return bNanoseconds - aNanoseconds;
}

function subMillisecondNanoseconds(value: string): number {
  const match = value.match(/\.(\d{1,9})(?:Z|[+-]\d{2}:\d{2})$/);
  if (!match) return 0;
  return Number(match[1].padEnd(9, '0').slice(3, 9));
}

/** Match Go's bytewise string tie-break rather than locale-dependent ordering. */
function descendingString(a: string, b: string): number {
  return a === b ? 0 : a > b ? -1 : 1;
}

function decodeTimelineCursor(cursor: string): { eventCursor: number } {
  try {
    const encoded = cursor.replace(/-/g, '+').replace(/_/g, '/').padEnd(Math.ceil(cursor.length / 4) * 4, '=');
    const decoded = JSON.parse(atob(encoded)) as { ec?: unknown };
    return { eventCursor: typeof decoded.ec === 'number' && Number.isFinite(decoded.ec) ? decoded.ec : 0 };
  } catch {
    return { eventCursor: 0 };
  }
}
