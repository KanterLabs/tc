import type { ActivityEvent, Task } from './types';

/** Expand hierarchy events to the child and parent sides for live refreshes. */
export function hierarchyEventTaskIds(
  event: Pick<ActivityEvent, 'type' | 'task_id' | 'payload'>
): string[] {
  if (!['task.parent_linked', 'task.parent_unlinked'].includes(event.type)) return [];
  const ids = [event.task_id, event.payload?.child_id, event.payload?.parent_id, event.payload?.previous_parent_id]
    .filter((value): value is string => typeof value === 'string' && value.length > 0);
  return [...new Set(ids)];
}

/** Compact board label for a task with children or a parent. */
export function hierarchyBadgeLabel(task: Pick<Task, 'parent_id' | 'parent_task_id' | 'hierarchy_summary'>): string {
  const children = task.hierarchy_summary?.child_count || 0;
  if (children > 0) return `${children} child${children === 1 ? '' : 'ren'}`;
  if (task.parent_id || task.parent_task_id) return 'Child task';
  return '';
}
