import type { ActivityEvent, Column, Task } from './types';

export type DependencyReadiness = 'blocked' | 'ready' | 'none';

/** Normalize the embedded server summary into the board's three display states. */
export function dependencyReadiness(task: Pick<Task, 'dependency_summary'>): DependencyReadiness {
  const summary = task.dependency_summary;
  if (!summary || summary.prerequisite_count <= 0) return 'none';
  return summary.blocked || summary.unmet_prerequisite_count > 0 ? 'blocked' : 'ready';
}

export function dependencyBlocked(task: Pick<Task, 'dependency_summary'>): boolean {
  return dependencyReadiness(task) === 'blocked';
}

export function dependencyMatches(
  task: Pick<Task, 'dependency_summary'>,
  filter: 'all' | 'blocked' | 'ready' | undefined
): boolean {
  return !filter || filter === 'all' || dependencyReadiness(task) === filter;
}

/** Human-readable explanation shared by visible notices and disabled controls. */
export function dependencyActionExplanation(
  task: Pick<Task, 'dependency_summary'>,
  action = 'start or complete this task'
): string {
  if (!dependencyBlocked(task)) return '';
  const count = Math.max(1, task.dependency_summary?.unmet_prerequisite_count || 0);
  return `Waiting on ${count} unfinished prerequisite${count === 1 ? '' : 's'}. Finish ${count === 1 ? 'it' : 'them'} before you ${action}.`;
}

/** Only active/completed semantic transitions cross a guarded lifecycle boundary. */
export function dependencyMoveExplanation(
  task: Pick<Task, 'dependency_summary'>,
  destination: Pick<Column, 'semantic_state'> | undefined
): string {
  if (!destination || !dependencyBlocked(task)) return '';
  if (destination.semantic_state === 'active') return dependencyActionExplanation(task, 'start this task');
  if (destination.semantic_state === 'completed') return dependencyActionExplanation(task, 'complete this task');
  return '';
}

/** Expand dependency events to both sides of the relationship for polling. */
export function dependencyEventTaskIds(
  event: Pick<ActivityEvent, 'type' | 'task_id' | 'payload'>
): string[] {
  if (!['task.dependency_added', 'task.dependency_removed', 'task.dependency_state_changed'].includes(event.type)) {
    return [];
  }
  const ids = [event.task_id, event.payload?.dependent_id, event.payload?.prerequisite_id]
    .filter((value): value is string => typeof value === 'string' && value.length > 0);
  return [...new Set(ids)];
}
