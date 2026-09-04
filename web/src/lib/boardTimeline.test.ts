import { describe, expect, it } from 'vitest';
import { queueBoardTimelineLoad } from './boardTimeline';

describe('board timeline load queue', () => {
  it('waits for an older-page load before starting a poll refresh', async () => {
    const calls: string[] = [];
    let releaseOlder!: (value: boolean) => void;
    const olderResult = new Promise<boolean>((resolve) => { releaseOlder = resolve; });
    let queue = Promise.resolve(true);

    const older = queueBoardTimelineLoad(queue, async () => {
      calls.push('older');
      const result = await olderResult;
      return result;
    });
    queue = older.queue;
    const refresh = queueBoardTimelineLoad(queue, async () => {
      calls.push('refresh');
      return true;
    });

    await Promise.resolve();
    expect(calls).toEqual(['older']);
    releaseOlder(true);
    await expect(older.promise).resolves.toBe(true);
    await expect(refresh.promise).resolves.toBe(true);
    expect(calls).toEqual(['older', 'refresh']);
  });

  it('lets the older lane clean up before a queued refresh can observe it', async () => {
    const calls: string[] = [];
    let releaseOlder!: () => void;
    const olderResult = new Promise<void>((resolve) => { releaseOlder = resolve; });
    let loadingOlder = false;
    let queue = Promise.resolve(true);

    const older = queueBoardTimelineLoad(queue, async () => {
      loadingOlder = true;
      try {
        await olderResult;
        calls.push(`older:${loadingOlder}`);
        return true;
      } finally {
        loadingOlder = false;
      }
    });
    queue = older.queue;
    const refresh = queueBoardTimelineLoad(queue, async () => {
      calls.push(`refresh:${loadingOlder}`);
      return true;
    });

    await Promise.resolve();
    expect(calls).toEqual([]);
    releaseOlder();
    await expect(older.promise).resolves.toBe(true);
    await expect(refresh.promise).resolves.toBe(true);
    expect(calls).toEqual(['older:true', 'refresh:false']);
    expect(loadingOlder).toBe(false);
  });

  it('continues the queue after a failed load so loading state cleanup can run', async () => {
    let queue = Promise.resolve(true);
    const failed = queueBoardTimelineLoad(queue, async () => {
      throw new Error('transient timeline failure');
    });
    queue = failed.queue;
    const following = queueBoardTimelineLoad(queue, async () => true);

    await expect(failed.promise).rejects.toThrow('transient timeline failure');
    await expect(following.promise).resolves.toBe(true);
  });
});
