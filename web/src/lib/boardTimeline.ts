/**
 * Add one board-timeline load behind the prior load. Keeping the queue state
 * outside the component makes the older-page/poll ordering deterministic and
 * prevents a newer request from orphaning the older lane's cleanup.
 */
export function queueBoardTimelineLoad(
  queue: Promise<boolean>,
  load: () => Promise<boolean>
): { promise: Promise<boolean>; queue: Promise<boolean> } {
  const promise = queue.then(load, load);
  return { promise, queue: promise.catch(() => false) };
}
