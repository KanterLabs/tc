import type { Task } from './types';

export type TaskMutationKind = 'upsert' | 'remove';

export type TaskMutationScope = 'board' | 'issues' | 'my-work-live' | 'my-work-assigned';

export type TaskMutationRecord = {
  revision: number;
  kind: TaskMutationKind;
};

/**
 * Keep a newer local task when a background read returns an older version.
 * Agent-work heartbeats do not increment task.version, so equal-version reads
 * also compare their pulse timestamps. This prevents a slower board/list read
 * from overwriting a fresher drawer heartbeat that completed first.
 */
export function mergeAuthoritativeTask(local: Task | undefined, fetched: Task): Task {
  if (!local || local.version < fetched.version) return fetched;
  if (local.version > fetched.version) return local;

  // Dependency readiness is a derived read model and can change without a
  // task version bump. Preserve it only when a retained server omits it;
  // otherwise the latest authoritative read owns the summary.
  const authoritative = fetched.dependency_summary === undefined && local.dependency_summary !== undefined
    ? { ...fetched, dependency_summary: local.dependency_summary }
    : fetched;
  const localPulse = Date.parse(local.agent_work?.updated_at || '');
  const fetchedPulse = Date.parse(fetched.agent_work?.updated_at || '');
  if (Number.isFinite(localPulse) && (!Number.isFinite(fetchedPulse) || localPulse > fetchedPulse)) {
    return fetched.dependency_summary === undefined
      ? local
      : { ...local, dependency_summary: fetched.dependency_summary };
  }
  return authoritative;
}

/**
 * Return only the mutations that completed after a list request started.
 *
 * A list response is still authoritative for every other task ID, including
 * IDs omitted by a filtered query. Callers can pass the resulting map to
 * mergeAuthoritativeTaskList to protect the small set of local mutations that
 * raced the request without retaining every local omission forever.
 */
export function taskMutationsAfter(
  records: ReadonlyMap<string, TaskMutationRecord>,
  requestRevision: number
): Map<string, TaskMutationKind> {
  const mutations = new Map<string, TaskMutationKind>();
  records.forEach((record, taskId) => {
    if (record.revision > requestRevision) mutations.set(taskId, record.kind);
  });
  return mutations;
}

/**
 * Apply an authoritative collection while retaining newer local versions for
 * tasks present in both collections. Tasks omitted by the fetched collection
 * stay omitted unless an explicitly protected local upsert completed after
 * this request started. A protected remove suppresses a stale task that a
 * response begun before the delete may still contain.
 */
export function mergeAuthoritativeTaskList(
  local: Task[],
  fetched: Task[],
  protectedMutations: ReadonlyMap<string, TaskMutationKind> = new Map()
): Task[] {
  const localById = new Map(local.map((task) => [task.id, task]));
  const fetchedIds = new Set(fetched.map((task) => task.id));
  const merged = fetched
    .filter((task) => protectedMutations.get(task.id) !== 'remove')
    .map((task) => {
      const localTask = localById.get(task.id);
      const mutation = protectedMutations.get(task.id);
      // A successful local mutation is newer than a response that started
      // before it, even when the endpoint leaves task.version unchanged
      // (label/task-work metadata can follow that path).
      if (mutation === 'upsert' && localTask && task.version <= localTask.version) {
        // Equal-version dependency invalidations are authoritative even while
        // an unrelated local metadata/heartbeat mutation is protected.
        if (task.version === localTask.version && task.dependency_summary !== undefined) {
          return { ...localTask, dependency_summary: task.dependency_summary };
        }
        return localTask;
      }
      return mergeAuthoritativeTask(localTask, task);
    });

  // New local creates are absent from a request that started before the POST
  // completed. Preserve only those explicitly protected IDs; all ordinary
  // omissions remain authoritative filtered removals.
  protectedMutations.forEach((kind, taskId) => {
    if (kind !== 'upsert' || fetchedIds.has(taskId)) return;
    const localTask = localById.get(taskId);
    if (localTask) merged.push(localTask);
  });

  return merged;
}
